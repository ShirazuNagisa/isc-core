package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件覆盖 M2-e：验证控制台。
//
// 控制台的特殊之处在于它是**唯一一个会被浏览器加载**的界面，
// 因此它带来两个别处不存在的问题：
//
//  1. 浏览器无法为页面加载携带 Authorization 头 —— 令牌必须另想办法交付；
//  2. 网页可以发起跨站请求，因此需要防 DNS rebinding。
//
// 下面两条测试分别守着这两件事。

// consoleClient 返回一个走回环 TCP 的客户端。
//
// 必须用 TCP 而不是命名管道：控制台的唯一消费者是浏览器，
// 而浏览器无法访问命名管道。用管道测控制台等于没测。
func (h *harness) consoleClient(t *testing.T) (*http.Client, string) {
	t.Helper()

	if h.info.FallbackEndpoint == "" {
		t.Fatal("内核没有启动回环 TCP 通道 —— 浏览器将无法访问控制台")
	}
	ep := platform.Endpoint(h.info.FallbackEndpoint)
	if ep.Scheme() != platform.SchemeTCP {
		t.Fatalf("备用通道不是 TCP：%s", h.info.FallbackEndpoint)
	}

	hc, err := ep.HTTPClient(10 * time.Second)
	if err != nil {
		t.Fatalf("构造回环 TCP 客户端失败: %v", err)
	}
	return hc, ep.HTTPBaseURL()
}

// TestConsoleIsServedOverLoopbackTCP 验证控制台真的能被浏览器打开。
func TestConsoleIsServedOverLoopbackTCP(t *testing.T) {
	h := startDaemon(t)
	hc, base := h.consoleClient(t)
	ctx := context.Background()

	// --- 1. 首页可以**不带令牌**打开 ---
	//
	// 浏览器加载页面时无法携带 Authorization 头。若这里要求鉴权，
	// 控制台就永远打不开 —— 而错误表现只是一个 401 空白页。
	resp := doGet(t, ctx, hc, base+"/console/", "")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck // 测试清理
		t.Fatalf("控制台首页返回 %d: %s", resp.StatusCode, body)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close() //nolint:errcheck // 测试清理

	html := string(body)
	if !strings.Contains(html, "<title>") {
		t.Error("首页不像是一个 HTML 页面")
	}
	// 必须引用本地的样式与脚本 —— 引用外部 CDN 会把本机管理界面的
	// 加载行为暴露给第三方。
	if strings.Contains(html, "http://") && strings.Contains(html, "cdn") {
		t.Error("控制台不应引用外部 CDN 资源")
	}
	// 页面里不该直接内嵌令牌：静态资源是明文交付的，
	// 令牌必须通过受保护的引导端点获取。
	if strings.Contains(html, h.info.Token) {
		t.Error("令牌被直接写进了 HTML —— 应当通过 /v1/console/bootstrap 获取")
	}

	// --- 2. 静态资源可读 ---
	for _, path := range []string{"/console/app.js", "/console/style.css"} {
		r := doGet(t, ctx, hc, base+path, "")
		if r.StatusCode != http.StatusOK {
			t.Errorf("%s 返回 %d", path, r.StatusCode)
		}
		r.Body.Close() //nolint:errcheck // 测试清理
	}

	// --- 3. 根路径重定向到控制台 ---
	//
	// 用户拿到的是 http://127.0.0.1:PORT/ ，直接给 404 会让人以为内核没起来。
	noRedirect := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/", nil)
	rootResp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatalf("请求根路径失败: %v", err)
	}
	rootResp.Body.Close() //nolint:errcheck // 测试清理
	if rootResp.StatusCode != http.StatusFound {
		t.Errorf("根路径应当 302 到控制台，得到 %d", rootResp.StatusCode)
	}
	if loc := rootResp.Header.Get("Location"); loc != "/console/" {
		t.Errorf("重定向目标 = %q, 期望 /console/", loc)
	}
}

// TestConsoleBootstrapDeliversToken 验证令牌能交给页面。
func TestConsoleBootstrapDeliversToken(t *testing.T) {
	h := startDaemon(t)
	hc, base := h.consoleClient(t)

	resp := doGet(t, context.Background(), hc, base+"/v1/console/bootstrap", "")
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("引导端点返回 %d: %s", resp.StatusCode, body)
	}

	var info struct {
		Token      string `json:"token"`
		Version    string `json:"version"`
		APIBase    string `json:"api_base"`
		WSProtocol string `json:"ws_protocol"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatalf("解析引导响应失败: %v", err)
	}

	if info.Token != h.info.Token {
		t.Errorf("引导端点给出的令牌与 runtime.json 不一致")
	}
	if info.APIBase != "/v1" {
		t.Errorf("api_base = %q", info.APIBase)
	}
	// 浏览器无法为 WebSocket 设置请求头，因此必须告诉页面子协议前缀。
	if info.WSProtocol == "" {
		t.Error("缺少 ws_protocol —— 控制台将无法建立事件流")
	}

	// 拿到的令牌必须真的能用。
	//
	// 只断言"返回了一个字符串"是不够的：一个格式正确但无效的令牌
	// 会让控制台打开后每个操作都 401，而用户完全不知道为什么。
	authed := doGet(t, context.Background(), hc, base+"/v1/meta", info.Token)
	defer authed.Body.Close() //nolint:errcheck // 测试清理
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("用引导端点给出的令牌访问 /v1/meta 返回 %d", authed.StatusCode)
	}
}

// TestConsoleRejectsForeignHost 是 DNS rebinding 防护的核心断言。
//
// # 攻击场景
//
// 攻击者让自己的域名先解析到自己的服务器（页面成功同源加载），随后把这个
// 域名重新绑定到 127.0.0.1。此时浏览器认为目标仍是同源，于是**允许页面
// 读取响应** —— 控制台引导端点上的令牌就这样被读走，而那个令牌能操作
// 全部 DNS 记录。
//
// 整个攻击的前提是 Host 头是攻击者的域名。因此校验 Host 是最直接的堵法。
func TestConsoleRejectsForeignHost(t *testing.T) {
	h := startDaemon(t)
	hc, base := h.consoleClient(t)
	ctx := context.Background()

	// 用真实令牌 + 伪造 Host：模拟浏览器在 rebinding 之后发出的请求。
	// 请求确实到达了内核（TCP 连的是 127.0.0.1），但 Host 被改成了外部域名。
	for _, path := range []string{
		"/v1/console/bootstrap",
		"/v1/meta",
		"/v1/credentials",
		"/console/",
	} {
		resp := doGetHost(t, ctx, hc, base+path, h.info.Token, "evil.example.com")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck // 测试清理

		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Host=evil.example.com 访问 %s 应当被拒绝（403），得到 %d: %s",
				path, resp.StatusCode, body)
		}
		// 无论如何都不能把令牌交出去。
		if strings.Contains(string(body), h.info.Token) {
			t.Errorf("被拒绝的响应里泄漏了令牌: %s", path)
		}
	}
}

// TestConsoleAcceptsLoopbackHosts 验证防护没有误伤正常访问方式。
//
// 这条同样重要：一个把 localhost 也挡掉的 Host 校验会让用户
// 完全无法打开控制台，而错误信息只会说"被拒绝"。
func TestConsoleAcceptsLoopbackHosts(t *testing.T) {
	h := startDaemon(t)
	hc, base := h.consoleClient(t)
	ctx := context.Background()

	allowed := []string{
		"127.0.0.1",
		"127.0.0.1:12345",
		"localhost",
		"localhost:8080",
		"[::1]",
		"[::1]:9000",
		// 命名管道与 Unix 套接字的占位 host —— CLI 走的就是这条路径。
		"isc.local",
	}

	for _, host := range allowed {
		resp := doGetHost(t, ctx, hc, base+"/v1/health", h.info.Token, host)
		resp.Body.Close() //nolint:errcheck // 测试清理
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Host=%q 应当被接受，得到 %d", host, resp.StatusCode)
		}
	}
}

// TestNamedPipeTransportStillWorks 验证 Host 校验没有打断 CLI 的通道。
//
// 命名管道没有真实的 host，客户端用占位值 isc.local。这条路径要是断了，
// 整个 CLI 都会失效 —— 而 CLI 是内核开发与运维全过程唯一的手和眼睛
// （见 docs/DECISIONS.md D19）。
func TestNamedPipeTransportStillWorks(t *testing.T) {
	h := startDaemon(t)

	// h.get 走的就是 runtime.json 里的**首选通道**（管道 / Unix 套接字）。
	resp, err := h.get("/v1/meta")
	if err != nil {
		t.Fatalf("经首选通道访问失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("经 %s 访问 /v1/meta 返回 %d: %s",
			h.endpoint.TransportName(), resp.StatusCode, body)
	}
}

// TestConsoleNotFoundForMissingAsset 验证缺失资源不会返回首页。
//
// 返回首页（SPA 的常见做法）在这里是错的：它会让一个拼错的脚本路径
// 变成"页面加载了但什么都不工作"，而浏览器控制台里只有一句
// "Unexpected token '<'"。验证工具应当直接给出 404。
func TestConsoleNotFoundForMissingAsset(t *testing.T) {
	h := startDaemon(t)
	hc, base := h.consoleClient(t)

	resp := doGet(t, context.Background(), hc, base+"/console/nope.js", "")
	resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("缺失资源应当 404，得到 %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func doGet(t *testing.T, ctx context.Context, hc *http.Client, url, token string) *http.Response {
	t.Helper()
	return doGetHost(t, ctx, hc, url, token, "")
}

func doGetHost(t *testing.T, ctx context.Context, hc *http.Client, url, token, host string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if host != "" {
		// req.Host 控制实际的 Host 头，而 URL 里的地址仍是 127.0.0.1 ——
		// 这正是模拟"连接到了本机但 Host 头是外部域名"的方式。
		req.Host = host
	}

	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("请求 %s 失败: %v", url, err)
	}
	return resp
}
