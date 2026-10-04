package remote

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// APNs 的失败现场全在 Apple 那一侧，而这里能验证的只有"我们发出去的东西
// 长什么样"。因此这些测试的重点是**形状**：JWT 的签名格式、请求头、
// 载荷结构，以及对那些必须区别对待的状态码（410 / 429 / 400）的处理。

// testKey 生成一把 P-256 私钥及其 .p8 文本。
func testKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成私钥失败: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("编码私钥失败: %v", err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

// TestAPNSJWTHasTheShapeAppleExpects 覆盖这一块最容易踩的坑。
//
// `ecdsa.SignASN1` 会产出一个长度不定的 DER 序列，而 Apple 要的是
// **raw R||S 各 32 字节**。用错时 Apple 回 403 InvalidProviderToken ——
// 那个错误看起来像"key id 或 team id 填错了"，用户会去反复核对
// 那两串完全正确的字符串。
func TestAPNSJWTHasTheShapeAppleExpects(t *testing.T) {
	t.Parallel()

	key, pemText := testKey(t)
	now := time.Unix(1_800_000_000, 0)
	token, err := signAPNSJWT(Credentials{
		TeamID: "TEAM123456", KeyID: "KEY7890", BundleID: "app.isc.mizar", PrivateKey: pemText,
	}, now)
	if err != nil {
		t.Fatalf("签发失败: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT 应当是三段，得到 %d 段", len(parts))
	}

	header := decodeSegment(t, parts[0])
	if header["alg"] != "ES256" || header["kid"] != "KEY7890" {
		t.Fatalf("头部不对：%v", header)
	}
	claims := decodeSegment(t, parts[1])
	if claims["iss"] != "TEAM123456" || claims["iat"].(float64) != float64(now.Unix()) {
		t.Fatalf("声明不对：%v", claims)
	}

	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("签名不是合法的 base64url: %v", err)
	}
	// 这一条就是那个坑：DER 编码的签名长度不固定（通常 70~72 字节），
	// 而 raw R||S 恒为 64。
	if len(signature) != 64 {
		t.Fatalf("签名长度 = %d，期望 64（raw R||S）。\n"+
			"用 DER 的话 Apple 会回 403 InvalidProviderToken，而那个错误"+
			"看起来像 team id / key id 填错了。", len(signature))
	}

	// 用公钥验一遍：长度对了但拆错了位置同样签不过。
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("签名验不过 —— R/S 的位置或填充不对")
	}
}

func TestAPNSRejectsBadCredentials(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)

	cases := []struct {
		name string
		c    Credentials
	}{
		{"缺 team id", Credentials{KeyID: "K", BundleID: "B", PrivateKey: pemText}},
		{"缺私钥", Credentials{TeamID: "T", KeyID: "K", BundleID: "B"}},
		{"私钥不是 PEM", Credentials{TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: "not pem"}},
		{"私钥是别的类型", Credentials{TeamID: "T", KeyID: "K", BundleID: "B",
			PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("x")}))}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if err := c.c.Validate(); err == nil {
				t.Fatal("应当被拒绝")
			}
		})
	}

	// 合法的应当通过。
	good := Credentials{TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText}
	if err := good.Validate(); err != nil {
		t.Fatalf("合法凭据被拒: %v", err)
	}
}

// fakeAPNS 是一个假的 APNs 服务器。
type fakeAPNS struct {
	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	statuses []int    // 依次返回；用完之后返回 200
	reasons  []string // 与 statuses 对应
	server   *httptest.Server
}

func newFakeAPNS(t *testing.T, statuses []int, reasons []string) *fakeAPNS {
	t.Helper()
	f := &fakeAPNS{statuses: statuses, reasons: reasons}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)

		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		f.bodies = append(f.bodies, body[:n])
		index := len(f.requests) - 1
		status := http.StatusOK
		if index < len(f.statuses) {
			status = f.statuses[index]
		}
		reason := ""
		if index < len(f.reasons) {
			reason = f.reasons[index]
		}
		f.mu.Unlock()

		w.Header().Set("apns-id", fmt.Sprintf("apns-%d", index))
		w.WriteHeader(status)
		if reason != "" {
			_ = json.NewEncoder(w).Encode(map[string]string{"reason": reason})
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPNS) snapshot() ([]*http.Request, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...), append([][]byte(nil), f.bodies...)
}

func testDevice() Device {
	return Device{
		ID: "dev-1", Label: "iPhone", Role: RoleViewer,
		APNSToken: "abc123", APNSEnvironment: PushEnvSandbox,
		APNSTopic: "app.isc.mizar", NotificationsEnabled: true,
	}
}

func TestAPNSPushSendsTheRightRequest(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)
	fake := newFakeAPNS(t, nil, nil)

	pusher, err := NewAPNSPusher(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "app.isc.mizar", PrivateKey: pemText,
	}, fake.server.URL)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	result := pusher.Push(context.Background(), testDevice(), Notification{
		Title: "站点挂了", Body: "它重启了三次", ThreadID: "isc-Mac-mini",
		Route:      map[string]string{"kind": "app", "id": "app-1"},
		CollapseID: "app-app-1", TimeSensitive: true,
	})
	if result.Status != PushStatusSent {
		t.Fatalf("发送失败：%+v", result)
	}
	if result.APNSID != "apns-0" {
		t.Fatalf("没有回读 apns-id：%q", result.APNSID)
	}

	requests, bodies := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("请求数 = %d，期望 1", len(requests))
	}
	req := requests[0]

	if !strings.HasSuffix(req.URL.Path, "/3/device/abc123") {
		t.Errorf("路径不对：%s", req.URL.Path)
	}
	if got := req.Header.Get("apns-topic"); got != "app.isc.mizar" {
		t.Errorf("apns-topic = %q", got)
	}
	// alert 而不是 background：后台推送的投递时机由系统决定，
	// 而"站点挂了"用户希望现在知道。
	if got := req.Header.Get("apns-push-type"); got != "alert" {
		t.Errorf("apns-push-type = %q", got)
	}
	if got := req.Header.Get("apns-priority"); got != "10" {
		t.Errorf("apns-priority = %q", got)
	}
	if got := req.Header.Get("apns-collapse-id"); got != "app-app-1" {
		t.Errorf("apns-collapse-id = %q", got)
	}
	if auth := req.Header.Get("Authorization"); !strings.HasPrefix(auth, "bearer ") {
		t.Errorf("Authorization 不对：%q", auth)
	}

	var payload map[string]any
	if err := json.Unmarshal(bodies[0], &payload); err != nil {
		t.Fatalf("载荷不是 JSON: %v", err)
	}
	aps, _ := payload["aps"].(map[string]any)
	if aps == nil {
		t.Fatal("载荷里没有 aps")
	}
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "站点挂了" || alert["body"] != "它重启了三次" {
		t.Fatalf("alert 不对：%v", alert)
	}
	if aps["thread-id"] != "isc-Mac-mini" {
		t.Errorf("thread-id 不对：%v", aps["thread-id"])
	}
	if aps["interruption-level"] != "time-sensitive" {
		t.Errorf("时间敏感级别没带上：%v", aps["interruption-level"])
	}
	// 深链是结构化的（kind + id），不是 URL —— URL 的格式会随界面结构变。
	route, _ := payload["route"].(map[string]any)
	if route["kind"] != "app" || route["id"] != "app-1" {
		t.Errorf("route 不对：%v", route)
	}
}

// TestAPNSReusesTheAuthorisationToken 钉住 JWT 的缓存。
//
// APNs 明确要求不要每次请求都重新签发（它会回 TooManyProviderTokenUpdates），
// 而那个故障的表现是"偶尔漏几条通知" —— 极难查。
func TestAPNSReusesTheAuthorisationToken(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)
	fake := newFakeAPNS(t, nil, nil)
	pusher, err := NewAPNSPusher(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText,
	}, fake.server.URL)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	for i := 0; i < 3; i++ {
		pusher.Push(context.Background(), testDevice(), Notification{Title: "x"})
	}
	requests, _ := fake.snapshot()
	if len(requests) != 3 {
		t.Fatalf("请求数 = %d", len(requests))
	}
	first := requests[0].Header.Get("Authorization")
	for i, req := range requests {
		if req.Header.Get("Authorization") != first {
			t.Fatalf("第 %d 次请求换了 token —— APNs 会因为频繁换 token 而拒绝连接", i)
		}
	}
}

// TestAPNSHandlesUnregistered 覆盖唯一必须区别对待的错误。
//
// 继续给一个失效的令牌发通知不会有任何效果，而设备端也不会知道 ——
// 用户只会觉得"推送坏了"。
func TestAPNSHandlesUnregistered(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)
	cases := []struct {
		name    string
		status  int
		reason  string
		expects bool
	}{
		{"410 Gone", http.StatusGone, "Unregistered", true},
		{"400 BadDeviceToken", http.StatusBadRequest, "BadDeviceToken", true},
		{"400 别的错误", http.StatusBadRequest, "TopicDisallowed", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeAPNS(t, []int{c.status}, []string{c.reason})
			pusher, err := NewAPNSPusher(Credentials{
				TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText,
			}, fake.server.URL)
			if err != nil {
				t.Fatalf("构造失败: %v", err)
			}

			result := pusher.Push(context.Background(), testDevice(), Notification{Title: "x"})
			if result.Status != PushStatusFailed {
				t.Fatalf("状态应当是 failed，得到 %s", result.Status)
			}
			if result.Unregistered != c.expects {
				t.Fatalf("Unregistered = %v，期望 %v（reason=%q）", result.Unregistered, c.expects, c.reason)
			}
			if result.Reason != c.reason {
				t.Errorf("Reason = %q，期望 %q", result.Reason, c.reason)
			}
			// 4xx（除了 429）不该重试：再来一次是同样的结果，
			// 而每多试一次就多浪费一秒告诉用户"它失败了"。
			if requests, _ := fake.snapshot(); len(requests) != 1 {
				t.Errorf("请求数 = %d，期望 1（4xx 不该重试）", len(requests))
			}
		})
	}
}

func TestAPNSRetriesOnRetryableStatuses(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)
	// 两次可重试，第三次成功。
	fake := newFakeAPNS(t,
		[]int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusOK},
		[]string{"TooManyRequests", "ServiceUnavailable", ""})
	pusher, err := NewAPNSPusher(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText,
	}, fake.server.URL)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	result := pusher.Push(context.Background(), testDevice(), Notification{Title: "x"})
	if result.Status != PushStatusSent {
		t.Fatalf("重试之后应当成功，得到 %+v", result)
	}
	if requests, _ := fake.snapshot(); len(requests) != 3 {
		t.Fatalf("请求数 = %d，期望 3", len(requests))
	}
}

func TestAPNSSkipsDevicesWithoutAToken(t *testing.T) {
	t.Parallel()

	_, pemText := testKey(t)
	fake := newFakeAPNS(t, nil, nil)
	pusher, err := NewAPNSPusher(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText,
	}, fake.server.URL)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	device := testDevice()
	device.APNSToken = ""
	result := pusher.Push(context.Background(), device, Notification{Title: "x"})
	if result.Status != PushStatusSkipped {
		t.Fatalf("没有令牌的设备应当被跳过，得到 %s", result.Status)
	}
	if requests, _ := fake.snapshot(); len(requests) != 0 {
		t.Fatal("不该发出任何请求")
	}
}

// ---------------------------------------------------------------------------
// 凭据存储
// ---------------------------------------------------------------------------

// identityCipher 是一个恒等"加密"（仅供测试）。
type identityCipher struct{}

func (identityCipher) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (identityCipher) Decrypt(p []byte) ([]byte, error) { return p, nil }

func TestCredentialStoreRoundTrips(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewCredentialStore(dir, identityCipher{})
	_, pemText := testKey(t)

	// 没有配置时返回 nil 而不是错误。
	credentials, err := store.Get()
	if err != nil || credentials != nil {
		t.Fatalf("未配置时应当是 (nil, nil)，得到 (%v, %v)", credentials, err)
	}

	want := Credentials{TeamID: "TEAM", KeyID: "ABCDEF1234", BundleID: "app.isc.mizar", PrivateKey: pemText}
	if err := store.Set(want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 换一个实例读：证明它真的落盘了。
	reopened := NewCredentialStore(dir, identityCipher{})
	got, err := reopened.Get()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got == nil || got.TeamID != want.TeamID || got.PrivateKey != want.PrivateKey {
		t.Fatalf("读回来的不是存进去的：%+v", got)
	}

	// 状态里不能有完整私钥，key id 也要打码。
	configured, team, key, bundle, err := reopened.Status()
	if err != nil || !configured {
		t.Fatalf("状态不对: %v %v", configured, err)
	}
	if team != "TEAM" || bundle != "app.isc.mizar" {
		t.Fatalf("team/bundle 不对：%q %q", team, bundle)
	}
	if key == "ABCDEF1234" {
		t.Fatal("key id 不该原样返回")
	}
	if !strings.HasSuffix(key, "1234") {
		t.Fatalf("key id 的末四位应当保留（用户靠它区分多套凭据），得到 %q", key)
	}

	if err := reopened.Delete(); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if after, err := reopened.Get(); err != nil || after != nil {
		t.Fatalf("删除之后应当是 (nil, nil)，得到 (%v, %v)", after, err)
	}
}

// 没有加密器时必须**拒绝写入** —— 明文落盘比功能不可用糟得多。
func TestCredentialStoreRefusesToWriteInClear(t *testing.T) {
	t.Parallel()

	store := NewCredentialStore(t.TempDir(), nil)
	_, pemText := testKey(t)
	err := store.Set(Credentials{TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText})
	if err == nil {
		t.Fatal("没有加密器时应当拒绝写入")
	}
}

func TestCredentialStoreRejectsUnparsableKeys(t *testing.T) {
	t.Parallel()

	store := NewCredentialStore(t.TempDir(), identityCipher{})
	// 校验发生在写盘**之前**：一份解析不出私钥的凭据存进去之后，
	// 症状是"推送发不出去"，而那时用户已经看不到自己填错了什么。
	err := store.Set(Credentials{TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: "garbage"})
	if err == nil {
		t.Fatal("无法解析的私钥应当被拒绝")
	}
}

// 解不开的凭据文件必须**保留**并报错，而不是当成"没配过"。
//
// 当成没配过会让用户重新填一遍，而旧文件还在那里 —— 下次启动仍然解不开，
// 于是形成一个静默的循环。
func TestCredentialStoreKeepsUndecryptableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewCredentialStore(dir, identityCipher{})
	_, pemText := testKey(t)
	if err := store.Set(Credentials{TeamID: "T", KeyID: "K", BundleID: "B", PrivateKey: pemText}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// 换一个会失败的解密器（模拟主密钥被换掉）。
	failing := NewCredentialStore(dir, failingCipher{})
	credentials, err := failing.Get()
	if err == nil {
		t.Fatal("解不开时应当报错")
	}
	if credentials != nil {
		t.Fatal("解不开时不该返回凭据")
	}
	statusConfigured, _, _, _, statusErr := failing.Status()
	if statusErr == nil || statusConfigured {
		t.Fatal("解不开时状态应当是未配置且带错误")
	}
}

type failingCipher struct{}

func (failingCipher) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (failingCipher) Decrypt([]byte) ([]byte, error) {
	return nil, fmt.Errorf("bad key")
}

func decodeSegment(t *testing.T, segment string) map[string]any {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		t.Fatalf("段不是合法的 base64url: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("段不是 JSON: %v", err)
	}
	return out
}
