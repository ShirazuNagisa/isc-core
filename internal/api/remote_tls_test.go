package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/remote"
)

// 本文件是远程面的**端到端**验证：真的起一个 TLS 监听，真的走一次
// HTTPS，真的比对公钥指纹。
//
// # 为什么必须有这一层
//
// 上面那些测试全部走的是 `httptest.NewRequest` —— 它们绕过了 TLS 与
// 网络，因此证明不了"手机连得上"。而远程面最可能出问题的地方恰好就在
// 那一段：证书的 SAN 没覆盖地址、TLS 版本协商不上、监听绑错了接口、
// 状态里报的端口与真正监听的端口不是一个。这些在进程内测试里
// **一条都测不出来**。

// startTestRemote 起一个真实的远程监听并返回它的端口。
func startTestRemote(t *testing.T, srv *Server, svc *remote.Service) int {
	t.Helper()

	svc.SetHandler(srv.RemoteRoutes())
	// 端口 0：让操作系统挑，避免与真正在跑的内核（8788）或并行测试撞车。
	if err := svc.Apply(context.Background(), true, 0); err != nil {
		t.Fatalf("启动远程监听失败: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.Stop(context.Background())
	})

	port := svc.Status(context.Background()).Port
	if port == 0 {
		t.Fatal("监听起来了但端口是 0 —— 状态里的端口没有回读")
	}
	return port
}

// pinnedClient 构造一个**按公钥指纹固定**的 HTTPS 客户端。
//
// 这正是客户端（ISC Mizar）要做的事：不去验证书链（自签证书没有链），
// 而是比对公钥的 SHA-256。这段代码在测试里出现，是为了让"指纹"这个
// 概念在两边（服务端算出来的、客户端算出来的）被验证一次是同一个东西。
func pinnedClient(t *testing.T, wantSPKI string) *http.Client {
	t.Helper()

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				// 自签证书：链校验必然失败，信任来自下面的指纹比对。
				InsecureSkipVerify: true, //nolint:gosec // 见上面的说明
				VerifyConnection: func(cs tls.ConnectionState) error {
					if len(cs.PeerCertificates) == 0 {
						return errNoPeerCert
					}
					got := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
					if base64.RawURLEncoding.EncodeToString(got[:]) != wantSPKI {
						return errSPKIMismatch
					}
					return nil
				},
			},
		},
	}
}

var (
	errNoPeerCert   = &spkiError{"对端没有提供证书"}
	errSPKIMismatch = &spkiError{"公钥指纹不匹配"}
)

type spkiError struct{ msg string }

func (e *spkiError) Error() string { return e.msg }

// TestRemoteListenerServesOverTLS 覆盖整条链路。
func TestRemoteListenerServesOverTLS(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	port := startTestRemote(t, srv, svc)

	device, token := issueDevice(t, svc, remote.RoleOperator)

	client := pinnedClient(t, svc.Certificate().SPKIBase64())
	base := "https://127.0.0.1:" + strconv.Itoa(port)

	// 1. 带令牌的只读请求必须成功。
	resp, err := doGet(t, client, base+"/v1/health", token)
	if err != nil {
		t.Fatalf("带令牌的请求失败: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("带令牌的 /v1/health 得到 %d，期望 200", resp.Status)
	}

	// 2. 不带令牌必须 401。
	resp, err = doGet(t, client, base+"/v1/metrics", "")
	if err != nil {
		t.Fatalf("不带令牌的请求失败: %v", err)
	}
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("不带令牌得到 %d，期望 401", resp.Status)
	}

	// 3. 白名单外的路径必须 403 —— 而且是在**真的链路上**。
	resp, err = doGet(t, client, base+"/v1/settings", token)
	if err != nil {
		t.Fatalf("请求 /v1/settings 失败: %v", err)
	}
	if resp.Status != http.StatusForbidden {
		t.Fatalf("白名单外的路径得到 %d，期望 403", resp.Status)
	}

	// 4. 控制台引导端点必须完全不存在。
	//
	// 它是整个设计里最关键的一条：那条路径**免鉴权就会交出本地令牌**，
	// 而本地令牌等价于内核的完整管理权限。
	resp, err = doGet(t, client, base+"/v1/console/bootstrap", "")
	if err != nil {
		t.Fatalf("请求引导端点失败: %v", err)
	}
	if resp.Status == http.StatusOK {
		t.Fatal("远程面上竟然能拿到控制台引导令牌 —— 这是最严重的一类缺陷")
	}
	if strings.Contains(resp.Body, `"token"`) {
		t.Fatal("引导端点的响应里出现了 token 字段")
	}

	// 5. 吊销之后立刻失效（走真链路）。
	if _, err := svc.RevokeDevice(context.Background(), device.ID, time.Now()); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}
	resp, err = doGet(t, client, base+"/v1/health", token)
	if err != nil {
		t.Fatalf("吊销后的请求失败: %v", err)
	}
	if resp.Status != http.StatusUnauthorized {
		t.Fatalf("吊销之后得到 %d，期望 401", resp.Status)
	}
}

// TestRemoteListenerPinsToTheAdvertisedKey 证明"二维码里的指纹"是客户端
// 真的会算出来的那一个。
//
// 这一条如果错了，症状是每一台手机都在配对时拒绝连接，而两边的日志
// 都只会说"指纹不匹配"，没人知道是哪一边算错了。
func TestRemoteListenerPinsToTheAdvertisedKey(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	port := startTestRemote(t, srv, svc)

	// 直接从 TLS 握手里取证书，按客户端的方式算一遍指纹。
	conn, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // 这里就是要手工比对
	})
	if err != nil {
		t.Fatalf("TLS 握手失败: %v", err)
	}
	defer conn.Close() //nolint:errcheck // 测试收尾

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("握手没有拿到对端证书")
	}
	leaf := certs[0]

	sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != svc.Certificate().SPKIBase64() {
		t.Fatalf("客户端算出的指纹 %s 与服务端声明的 %s 不一致", got, svc.Certificate().SPKIBase64())
	}

	// 短码必须是同一个指纹的前四位十六进制 —— 它是给人核对用的，
	// 与完整指纹指向不同的东西就等于没有核对价值。
	short := strings.ReplaceAll(svc.Certificate().FingerprintShort(), "-", "")
	if !strings.HasPrefix(strings.ToUpper(short), strings.ToUpper(hexPrefix(sum[:], 4))) {
		t.Fatalf("短码 %s 与指纹不一致", short)
	}

	// SAN 必须覆盖客户端会用到的地址（这里用回环，因为它一定在本机）。
	// 真正的候选地址由 Candidates 提供，它们必须**都在**证书里，
	// 否则客户端会看到名字不匹配。
	for _, addr := range remote.Candidates(port) {
		host, _, err := splitAddr(addr)
		if err != nil {
			t.Fatalf("候选地址 %q 无法解析: %v", addr, err)
		}
		// `<hostname>.local` 会出现在 DNSNames 里，IP 会出现在 IPAddresses 里。
		if !leafHasName(leaf, host) && !isLocalHostname(host) {
			t.Errorf("候选地址 %q 不在证书的 SAN 里", host)
		}
	}
}

// TestRemoteStatusReportsTheRealPort 钉住"显示的端口 = 监听的端口"。
func TestRemoteStatusReportsTheRealPort(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	port := startTestRemote(t, srv, svc)

	req := newRequest(http.MethodGet, "/v1/remote/status")
	rec := serve(t, srv, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态接口得到 %d", rec.Code)
	}
	var status struct {
		Port      int      `json:"port"`
		Listening bool     `json:"listening"`
		Enabled   bool     `json:"enabled"`
		Addresses []string `json:"addresses"`
	}
	if err := json.Unmarshal([]byte(rec.Body()), &status); err != nil {
		t.Fatalf("状态响应无法解析: %v", err)
	}
	if status.Port != port {
		t.Fatalf("状态里的端口是 %d，实际监听的是 %d", status.Port, port)
	}
	if !status.Listening || !status.Enabled {
		t.Fatal("监听已经起来，状态里却不是 running/enabled")
	}
	// 候选地址必须带上真实的端口，否则二维码里会写一个连不上的地址。
	want := ":" + strconv.Itoa(port)
	for _, addr := range status.Addresses {
		if !strings.HasSuffix(addr, want) {
			t.Fatalf("候选地址 %q 没有带上真实端口 %d", addr, port)
		}
	}
}

// ---------------------------------------------------------------------------
// 测试用的小工具
// ---------------------------------------------------------------------------

type httpResult struct {
	Status int
	Body   string
}

func doGet(t *testing.T, client *http.Client, url, token string) (httpResult, error) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return httpResult{}, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return httpResult{}, err
	}
	defer resp.Body.Close() //nolint:errcheck // 测试收尾
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpResult{}, err
	}
	return httpResult{Status: resp.StatusCode, Body: string(body)}, nil
}

// splitAddr 拆开 host:port（IPv6 带方括号）。
func splitAddr(addr string) (host, port string, err error) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", "", errNoPeerCert
	}
	host = strings.TrimSuffix(strings.TrimPrefix(addr[:i], "["), "]")
	return host, addr[i+1:], nil
}

// leafHasName 报告证书的 SAN 是否覆盖某个名字（IP 或 DNS）。
func leafHasName(leaf *x509.Certificate, name string) bool {
	for _, dns := range leaf.DNSNames {
		if dns == name {
			return true
		}
	}
	for _, ip := range leaf.IPAddresses {
		if ip.String() == name {
			return true
		}
	}
	return false
}

// isLocalHostname 报告某个名字是不是 `<hostname>.local`。
//
// 它按设计**不在**证书的 IP 列表里（那是一个 mDNS 名字），
// 因此单独判断。
func isLocalHostname(name string) bool { return strings.HasSuffix(name, ".local") }

// hexPrefix 取字节切片前 n 个字节的大写十六进制。
func hexPrefix(raw []byte, n int) string {
	const digits = "0123456789ABCDEF"
	if n > len(raw) {
		n = len(raw)
	}
	out := make([]byte, 0, n*2)
	for _, b := range raw[:n] {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

// newRequest 构造一个本地面请求。
//
// Host 必须是本机地址：本地面有 LoopbackGuard（DNS rebinding 防护），
// 而它只看 Host 头。令牌同理 —— 本地管理面永远强制鉴权。
func newRequest(method, path string) *http.Request {
	req, _ := http.NewRequest(method, path, nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Authorization", "Bearer "+testLocalToken)
	return req
}

// serve 把一个请求交给本地面处理器。
func serve(t *testing.T, srv *Server, req *http.Request) *recorder {
	t.Helper()
	rec := &recorder{}
	srv.Routes().ServeHTTP(rec, req)
	return rec
}

// recorder 是 httptest.ResponseRecorder 的最小替代。
//
// 不用 httptest 里的那个是为了让 decodeBody 的路径与真实服务端一致：
// 真实服务端总是有 Body 的（可能是空的），而 httptest.NewRequest 会
// 在没有正文时把 Body 设成 http.NoBody —— 那会让解码分支与
// 真实请求不同。这里保持默认值即可。
type recorder struct {
	header http.Header
	Code   int
	body   []byte
}

func (r *recorder) Header() http.Header {
	if r.header == nil {
		r.header = http.Header{}
	}
	return r.header
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.Code == 0 {
		r.Code = http.StatusOK
	}
	r.body = append(r.body, b...)
	return len(b), nil
}

func (r *recorder) WriteHeader(status int) { r.Code = status }

func (r *recorder) Body() string { return string(r.body) }

// 远程监听按 SNI 选证书，而**回落必须安静**。
//
// # 这条门禁测的是真握手
//
// 单元测试只覆盖了选择逻辑；这里走一次真正的 TLS 握手，因为最容易
// 出问题的地方恰好是"TLS 栈怎么看待我们返回的那张证书" ——
// 一个非 nil 但空的 `tls.Certificate` 会被接受、然后在握手时报
// "no certificates"，而那是一个只在运行时才出现的失败。
//
// # 两条路必须并存
//
//   - 局域网（IP / `.local`，没有 SNI 名字）→ 自签那张，它的公钥指纹
//     是手机配对时固定的。换了会让所有已配对的手机要求重新配对。
//   - 公网（域名）→ Let's Encrypt 那张。它不能沿用固定指纹，因为
//     续期会换密钥。
//
// 而"公网那张还没有"（首次配置时必然如此）必须**回落**，不能报错：
// 报错会把"公网还没配好"变成"手机完全连不上"。
func TestRemoteListenerFallsBackToSelfSignedWithoutSNI(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	port := startTestRemote(t, srv, svc)

	// 不带 ServerName 的握手 —— 手机用 IP 连时就是这样。
	conn, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port), &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // 这里就是要手工比对指纹
	})
	if err != nil {
		t.Fatalf("没有 SNI 名字时握手不该失败: %v", err)
	}
	defer conn.Close() //nolint:errcheck // 测试收尾

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("握手没有拿到对端证书")
	}
	sum := sha256.Sum256(certs[0].RawSubjectPublicKeyInfo)
	if got := base64.RawURLEncoding.EncodeToString(sum[:]); got != svc.Certificate().SPKIBase64() {
		t.Fatal("没有 SNI 名字时给出的应当是自签那张（局域网路径靠它的指纹）")
	}
}
