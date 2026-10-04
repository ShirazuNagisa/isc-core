package remote

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件实现 APNs 的发送侧。
//
// # 为什么推送由内核发出，而不是手机自己轮询
//
// 手机在后台会被系统挂起 —— 它没有能力"定时醒来看看服务器怎么样"。
// 真正需要即时知道的三件事（站点挂了、证书快到期了、解析更新失败了）
// 恰好都是**服务端先知道**的，因此由服务端推。
//
// # 为什么单独一层 Pusher 接口
//
// APNs 的失败现场全在 Apple 那一侧，而这里能做的验证只有"请求长什么样"。
// 把它抽成接口之后，测试可以注入一个假的实现去断言 JWT 的形状、
// 重试的次数、以及对 410 的处理 —— 那些是这个文件里唯二会出错的逻辑。

// Credentials 是 APNs 的 Token-based 鉴权凭据。
//
// 用 token（.p8）而不是证书：证书一年一换，而 token 不会过期；
// 同一个 token 还能给多个 App 用。
type Credentials struct {
	TeamID     string `json:"team_id"`
	KeyID      string `json:"key_id"`
	BundleID   string `json:"bundle_id"`
	PrivateKey string `json:"private_key"`
}

// Validate 报告凭据是否完整。
func (c Credentials) Validate() error {
	if c.TeamID == "" || c.KeyID == "" || c.BundleID == "" || c.PrivateKey == "" {
		return errors.New(i18n.T("remote.err.apns_incomplete"))
	}
	if _, err := c.parseKey(); err != nil {
		return err
	}
	return nil
}

// parseKey 解析 .p8 里的 P-256 私钥。
func (c Credentials) parseKey() (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(c.PrivateKey))
	if block == nil {
		return nil, errors.New(i18n.T("remote.err.apns_key_pem"))
	}
	// .p8 是 PKCS#8（`BEGIN PRIVATE KEY`）。也容忍 SEC1 的
	// `BEGIN EC PRIVATE KEY` —— 有些工具导出的就是那一种，
	// 而拒绝它只会让用户对着一个"格式不对"发呆。
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ecdsaKey, ok := key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New(i18n.T("remote.err.apns_key_type"))
		}
		return ecdsaKey, nil
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New(i18n.T("remote.err.apns_key_parse"))
}

// Notification 是一条要发出去的通知。
type Notification struct {
	Title string
	Body  string
	// ThreadID 把同一台服务端的通知归到一组，iOS 会把它们摞在一起。
	ThreadID string
	// Route 告诉客户端点开之后该去哪儿。
	//
	// 它是**结构化**的（kind + id）而不是一个 URL：URL 的格式会随
	// 界面结构变化，而 kind/id 表达的是"哪台服务器的哪个东西"，
	// 那是稳定的。
	Route map[string]string
	// CollapseID 让后一条同 id 的通知替换掉前一条。
	CollapseID string
	// DedupeKey 用于服务端的去重（每设备每 key 五分钟一条）。
	DedupeKey string
	// TimeSensitive 标记"用户现在就该知道"的一类（站点挂了）。
	TimeSensitive bool
}

// Delivery 是一次投递的结果。
type Delivery struct {
	Status     string
	HTTPStatus int
	Reason     string
	APNSID     string
	// Unregistered 表示这个设备令牌已经失效，调用方应当把它清掉。
	//
	// 这是 APNs 里**唯一**必须区别对待的错误：继续给一个失效的令牌发
	// 通知不会有任何效果，而设备端也不会知道。
	Unregistered bool
}

// Pusher 把通知送到一台设备。
type Pusher interface {
	Push(ctx context.Context, device Device, n Notification) Delivery
}

// APNSHost 是 APNs 的两个接入点。
//
// 用错环境的症状是 `BadDeviceToken` —— 它看起来像"令牌不对"，
// 实际上是"你把开发版装到了真机上"。
const (
	APNSHostProduction = "https://api.push.apple.com"
	APNSHostSandbox    = "https://api.sandbox.push.apple.com"
)

// APNSPusher 是真实的 APNs 客户端。
type APNSPusher struct {
	credentials Credentials
	host        string
	client      *http.Client
	now         func() time.Time

	mu       sync.Mutex
	jwt      string
	jwtUntil time.Time
}

// NewAPNSPusher 构造一个 APNs 客户端。
//
// host 由调用方给（生产 / 沙箱，测试时是一个假服务器）——
// 把它硬编码进去的话，"给沙箱设备发生产"这个最常见的配置错误
// 就只能靠用户自己发现。
func NewAPNSPusher(credentials Credentials, host string) (*APNSPusher, error) {
	if err := credentials.Validate(); err != nil {
		return nil, err
	}
	if host == "" {
		host = APNSHostProduction
	}
	return &APNSPusher{
		credentials: credentials,
		host:        strings.TrimSuffix(host, "/"),
		now:         time.Now,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				// APNs **只**接受 HTTP/2。Go 的 Transport 在配置了
				// TLSClientConfig 之后默认不再自动升级，因此这一行是必需的 ——
				// 少了它的症状是 Apple 回一个含糊的 400，而错误信息里
				// 完全看不出"你用的是 HTTP/1.1"。
				ForceAttemptHTTP2: true,
			},
		},
	}, nil
}

// Push 发送一条通知。
func (p *APNSPusher) Push(ctx context.Context, device Device, n Notification) Delivery {
	if device.APNSToken == "" {
		return Delivery{Status: PushStatusSkipped, Reason: i18n.T("remote.msg.push_no_token")}
	}

	token, err := p.authorization()
	if err != nil {
		return Delivery{Status: PushStatusFailed, Reason: err.Error()}
	}

	payload, err := json.Marshal(apsPayload(device, n))
	if err != nil {
		return Delivery{Status: PushStatusFailed, Reason: err.Error()}
	}

	// 重试只针对"再来一次可能成功"的情况：429 与 5xx。
	// 4xx（令牌错、凭据错、载荷错）重试多少次都是同样的结果，
	// 而每多试一次就多浪费一秒告诉用户"它失败了"。
	var last Delivery
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return last
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		last = p.pushOnce(ctx, device, token, payload, n)
		if last.Status == PushStatusSent || !retryable(last.HTTPStatus) {
			return last
		}
	}
	return last
}

func (p *APNSPusher) pushOnce(ctx context.Context, device Device, token string, payload []byte, n Notification) Delivery {
	url := fmt.Sprintf("%s/3/device/%s", p.host, device.APNSToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Delivery{Status: PushStatusFailed, Reason: err.Error()}
	}

	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	topic := device.APNSTopic
	if topic == "" {
		topic = p.credentials.BundleID
	}
	req.Header.Set("apns-topic", topic)
	// alert 而不是 background：后台推送（content-available）的投递时机
	// 完全由系统决定，而"站点挂了"这件事用户希望**现在**知道。
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	if n.CollapseID != "" {
		req.Header.Set("apns-collapse-id", n.CollapseID)
	}
	if n.TimeSensitive {
		req.Header.Set("apns-push-type", "alert")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return Delivery{Status: PushStatusFailed, Reason: err.Error()}
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应体

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	apnsID := resp.Header.Get("apns-id")

	if resp.StatusCode == http.StatusOK {
		return Delivery{Status: PushStatusSent, HTTPStatus: resp.StatusCode, APNSID: apnsID}
	}

	reason := parseAPNSReason(body)
	delivery := Delivery{
		Status:     PushStatusFailed,
		HTTPStatus: resp.StatusCode,
		Reason:     reason,
		APNSID:     apnsID,
	}
	// 410（Unregistered）与 400 + BadDeviceToken 都表示这个令牌已经无效。
	// 两者都要清掉，否则设备列表里会永远留着一台收不到通知的设备。
	if resp.StatusCode == http.StatusGone || reason == "BadDeviceToken" || reason == "Unregistered" {
		delivery.Unregistered = true
	}
	return delivery
}

// authorization 返回缓存的 JWT。
//
// APNs 明确要求**不要**每次请求都重新签发（它会对频繁换 token 的连接
// 报 TooManyProviderTokenUpdates），而 token 的有效期是 1 小时。
// 50 分钟是一个留了余量的刷新点。
func (p *APNSPusher) authorization() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	if p.jwt != "" && now.Before(p.jwtUntil) {
		return p.jwt, nil
	}

	token, err := signAPNSJWT(p.credentials, now)
	if err != nil {
		return "", err
	}
	p.jwt = token
	p.jwtUntil = now.Add(50 * time.Minute)
	return token, nil
}

// signAPNSJWT 签发 APNs 的鉴权 token。
func signAPNSJWT(c Credentials, now time.Time) (string, error) {
	key, err := c.parseKey()
	if err != nil {
		return "", err
	}

	header, err := json.Marshal(map[string]string{"alg": "ES256", "kid": c.KeyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{"iss": c.TeamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}

	signing := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)

	// ES256 的签名是 **raw R||S**（各 32 字节），不是 ASN.1 DER。
	//
	// 这是这一块最容易踩的坑：`ecdsa.SignASN1` 会产出一个长度不定的
	// DER 序列，Apple 直接回 403 InvalidProviderToken —— 而那个错误
	// 看起来像"key id 或 team id 填错了"，用户会去反复核对那两串
	// 完全正确的字符串。
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", fmt.Errorf(i18n.T("remote.err.apns_jwt"), err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])

	return signing + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// apsPayload 组装 Apple 要求的载荷形状。
func apsPayload(device Device, n Notification) map[string]any {
	alert := map[string]any{"title": n.Title}
	if n.Body != "" {
		alert["body"] = n.Body
	}
	aps := map[string]any{
		"alert": alert,
		"sound": "default",
	}
	if n.ThreadID != "" {
		aps["thread-id"] = n.ThreadID
	}
	if n.TimeSensitive {
		// 站点挂了这类事值得穿透专注模式 —— 而用户可以在系统设置里
		// 单独关掉它，那比我们替他决定要好。
		aps["interruption-level"] = "time-sensitive"
	}

	payload := map[string]any{"aps": aps}
	if len(n.Route) > 0 {
		payload["route"] = n.Route
	}
	// 服务端名字也带上：通知在锁屏上可能来自好几台服务器，
	// 而"哪一台"是用户第一个会问的问题。
	if device.Label != "" {
		payload["device"] = map[string]any{"label": device.Label}
	}
	return payload
}

// parseAPNSReason 从 APNs 的错误体里取出 reason。
func parseAPNSReason(body []byte) string {
	var parsed struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Reason
}

// retryable 报告这个状态码值得再试一次。
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}
