package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖反向代理的**安全边界**。
//
// 反代跑在公网上，因此它天然是攻击面。三条硬约束各有专门的测试：
//
//	不能变成开放代理     上游只允许本机/内网
//	不信客户端给的身份头  X-Forwarded-* / X-Real-IP 必须被替换
//	不转发 CONNECT       那等于把洞开在 TLS 层
//
// 除此之外还覆盖路由匹配的细节 —— 那些是"看起来能用、边界上出错"
// 的地方（通配匹配几级、是否匹配裸域名）。

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------------------------------------------------------------------------
// 上游校验
// ---------------------------------------------------------------------------

// TestValidateUpstreamAllowsLocalOnly 是防开放代理的第一道关。
//
// 若允许任意上游，任何人都能拿这台机器当跳板 —— 请求进来、出去打
// 内网或公网上的第三方，而所有流量都记在用户头上。
func TestValidateUpstreamAllowsLocalOnly(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"127.0.0.1:8096",
		"http://127.0.0.1:8096",
		"localhost:8096",
		"http://localhost:8096",
		"[::1]:8096",
		"10.0.0.5:80",
		"192.168.1.10:8080",
		"172.16.0.1:3000",
		"[fd00::1]:8096",
		"127.0.0.1:8096/",           // 带斜杠
		"http://127.0.0.1:8096/api", // 带路径
	}
	for _, raw := range allowed {
		if _, err := ValidateUpstream(raw); err != nil {
			t.Errorf("%q 应当被接受，实际报错: %v", raw, err)
		}
	}

	rejected := []struct {
		raw string
		why string
	}{
		{"8.8.8.8:53", "公网地址 —— 会变成开放代理"},
		{"http://example.com:80", "公网域名"},
		{"1.1.1.1:443", "公网地址"},
		{"[2001:4860:4860::8888]:53", "公网 IPv6"},
		{"100.64.0.1:80", "运营商级 NAT 网段，不可能是用户的服务"},
		{"0.0.0.0:8096", "表示'监听所有接口'，作为目标是歧义的"},
		{"169.254.1.1:80", "链路本地允许，但这条是允许的（见下）"},
	}
	for _, tc := range rejected {
		if tc.raw == "169.254.1.1:80" {
			continue // 这一条实际是允许的，列在这里只是为了说明
		}
		if _, err := ValidateUpstream(tc.raw); err == nil {
			t.Errorf("%q 应当被拒绝（%s）", tc.raw, tc.why)
		}
	}
}

func TestValidateUpstreamRejectsMalformed(t *testing.T) {
	t.Parallel()

	bad := []struct {
		raw string
		why string
	}{
		{"", "空"},
		{"127.0.0.1", "缺少端口 —— 猜端口会转发到意想不到的服务上"},
		{"http://127.0.0.1", "缺少端口"},
		{"https://127.0.0.1:8096", "上游不支持 https"},
		{"ftp://127.0.0.1:8096", "不支持的 scheme"},
		{"127.0.0.1:0", "端口 0"},
		{"127.0.0.1:99999", "端口超范围"},
		{"127.0.0.1:abc", "端口不是数字"},
	}
	for _, tc := range bad {
		if _, err := ValidateUpstream(tc.raw); err == nil {
			t.Errorf("%q 应当被拒绝（%s）", tc.raw, tc.why)
		}
	}
}

func TestValidateUpstreamErrorMessageIsActionable(t *testing.T) {
	t.Parallel()

	// 缺少端口是最常见的配置错误，错误信息必须说清楚该填什么。
	_, err := ValidateUpstream("127.0.0.1")
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "端口") {
		t.Errorf("错误信息应当提到端口: %v", err)
	}
	// 至少要给一个正确写法的例子。
	if !strings.Contains(err.Error(), "127.0.0.1:8096") {
		t.Errorf("错误信息应当给出正确写法的例子: %v", err)
	}
}

func TestIsLocalAddr(t *testing.T) {
	t.Parallel()

	local := []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.5.5", "192.168.0.1",
		"fd00::1", "169.254.1.1", "fe80::1",
	}
	for _, s := range local {
		if !IsLocalAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s 应当是允许的上游地址", s)
		}
	}

	remote := []string{
		"8.8.8.8", "1.1.1.1", "100.64.0.1", "0.0.0.0",
		"2001:4860:4860::8888", "2409:8a50:6a1:7450::50b",
	}
	for _, s := range remote {
		if IsLocalAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s 不该被当作本机地址", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 路由匹配
// ---------------------------------------------------------------------------

func newTestServer(t *testing.T, routes ...Route) *Server {
	t.Helper()
	s := NewServer(testLogger(), 0)
	if err := s.SetRoutes(routes); err != nil {
		t.Fatalf("设置路由失败: %v", err)
	}
	return s
}

func TestLookupExactMatch(t *testing.T) {
	t.Parallel()

	s := newTestServer(t, Route{
		ID: "r1", Label: "媒体", Hosts: []string{"home.example.com"},
		Upstream: "127.0.0.1:8096",
	})

	r, ok := s.Lookup("home.example.com")
	if !ok || r.ID != "r1" {
		t.Errorf("精确匹配失败: %+v", r)
	}

	// 大小写不敏感（HTTP 的 Host 大小写不敏感）。
	if _, ok := s.Lookup("HOME.Example.COM"); !ok {
		t.Error("域名匹配应当大小写不敏感")
	}

	// 带端口也要能匹配（浏览器在非标端口上会带上端口）。
	if _, ok := s.Lookup("home.example.com:8443"); !ok {
		t.Error("Host 带端口时应当仍能匹配")
	}

	// 没配过的域名不匹配。
	if _, ok := s.Lookup("other.example.com"); ok {
		t.Error("未配置的域名不该匹配")
	}
}

// TestWildcardMatchesOneLevelOnly 钉住通配的匹配范围。
//
// 与 TLS 证书的通配规则一致：`*.example.com` 匹配 a.example.com，
// 但**不**匹配 example.com 本身，也**不**匹配 a.b.example.com。
//
// 放宽它的后果是用户拿到一个证书不匹配的域名，而浏览器只会说
// "证书无效" —— 很难联想到是路由配错了。
func TestWildcardMatchesOneLevelOnly(t *testing.T) {
	t.Parallel()

	s := newTestServer(t, Route{
		ID: "wild", Label: "通配", Hosts: []string{"*.example.com"},
		Upstream: "127.0.0.1:8096",
	})

	if _, ok := s.Lookup("a.example.com"); !ok {
		t.Error("*.example.com 应当匹配 a.example.com")
	}
	if _, ok := s.Lookup("media.example.com"); !ok {
		t.Error("*.example.com 应当匹配 media.example.com")
	}

	if _, ok := s.Lookup("example.com"); ok {
		t.Error("*.example.com **不**该匹配裸域名 example.com —— " +
			"这与 TLS 证书的规则一致")
	}
	if _, ok := s.Lookup("a.b.example.com"); ok {
		t.Error("*.example.com **不**该匹配多级子域名 a.b.example.com")
	}
	if _, ok := s.Lookup("notexample.com"); ok {
		t.Error("后缀匹配必须按标签边界，notexample.com 不该被 *.example.com 命中")
	}
}

// TestMoreSpecificWildcardWins 验证更具体的通配优先。
//
// 不倒序的话结果取决于用户输入顺序 —— 那是一个"改一下顺序就好了"
// 的隐蔽 bug。
func TestMoreSpecificWildcardWins(t *testing.T) {
	t.Parallel()

	s := newTestServer(t,
		Route{ID: "broad", Hosts: []string{"*.example.com"}, Upstream: "127.0.0.1:8001"},
		Route{ID: "narrow", Hosts: []string{"*.a.example.com"}, Upstream: "127.0.0.1:8002"},
	)

	r, ok := s.Lookup("b.a.example.com")
	if !ok {
		t.Fatal("应当匹配到某条路由")
	}
	if r.ID != "narrow" {
		t.Errorf("b.a.example.com 应当命中更具体的 narrow，实际是 %s", r.ID)
	}
}

func TestSetRoutesRejectsSelfLoop(t *testing.T) {
	t.Parallel()

	// 上游指向代理自己的端口会造成无限循环 —— 直到耗尽文件描述符。
	// 那个症状（内核卡死、无法连接）与根因（端口填错）之间没有任何提示。
	s := NewServer(testLogger(), 8443)

	err := s.SetRoutes([]Route{{
		ID: "loop", Hosts: []string{"a.example.com"},
		Upstream: "127.0.0.1:8443",
	}})
	if err == nil {
		t.Fatal("上游指向代理自己时应当被拒绝")
	}
	if !strings.Contains(err.Error(), "无限循环") {
		t.Errorf("错误信息应当说明后果: %v", err)
	}

	// 换个端口就可以了。
	err = s.SetRoutes([]Route{{
		ID: "ok", Hosts: []string{"a.example.com"},
		Upstream: "127.0.0.1:8096",
	}})
	if err != nil {
		t.Errorf("指向别的端口应当被接受: %v", err)
	}
}

func TestValidateHostPattern(t *testing.T) {
	t.Parallel()

	good := []string{"example.com", "a.example.com", "*.example.com", "localhost"}
	for _, h := range good {
		if err := validateHostPattern(h); err != nil {
			t.Errorf("%q 应当是合法的域名模式: %v", h, err)
		}
	}

	bad := []string{
		"", " ", "a b.com", "a/b.com", "a\\b.com",
		"example.*", "*.", "*.*.example.com", "a.*.example.com",
	}
	for _, h := range bad {
		if err := validateHostPattern(h); err == nil {
			t.Errorf("%q 不该被接受", h)
		}
	}
}

// ---------------------------------------------------------------------------
// 请求处理
// ---------------------------------------------------------------------------

// backend 是一个记录收到的请求的假上游服务。
type backend struct {
	*httptest.Server
	mu       chan struct{}
	lastReq  *http.Request
	lastBody string
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	b := &backend{mu: make(chan struct{}, 1)}
	b.mu <- struct{}{}

	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		<-b.mu
		b.lastReq = r.Clone(context.Background())
		b.lastBody = string(body)
		b.mu <- struct{}{}

		w.Header().Set("X-Backend", "yes")
		_, _ = io.WriteString(w, "来自上游的响应")
	}))
	t.Cleanup(b.Server.Close)
	return b
}

func (b *backend) upstream(t *testing.T) string {
	t.Helper()
	// httptest.Server 监听在 127.0.0.1 上，因此天然通过上游校验。
	return strings.TrimPrefix(b.Server.URL, "http://")
}

func (b *backend) request() *http.Request {
	<-b.mu
	req := b.lastReq
	b.mu <- struct{}{}
	return req
}

func TestProxyForwardsToUpstream(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: b.upstream(t),
	})

	req := httptest.NewRequest(http.MethodGet, "http://home.example.com/hello", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应体: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "来自上游的响应" {
		t.Errorf("响应体 = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Backend") != "yes" {
		t.Error("上游设置的响应头没有透传")
	}

	got := b.request()
	if got.URL.Path != "/hello" {
		t.Errorf("上游收到的路径 = %q", got.URL.Path)
	}
}

// TestProxyStripsClientSuppliedIdentityHeaders 是最关键的一条安全断言。
//
// 应用常靠 X-Forwarded-* / X-Real-IP 判断"请求来自哪里"，
// 而让它相信客户端伪造的值会让 IP 白名单之类的机制完全失效。
func TestProxyStripsClientSuppliedIdentityHeaders(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: b.upstream(t),
	})

	req := httptest.NewRequest(http.MethodGet, "http://home.example.com/", nil)
	// 客户端伪造了一整套身份头。
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Real-IP", "1.2.3.4")
	req.Header.Set("Forwarded", "for=1.2.3.4;proto=https")

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	got := b.request()

	// 伪造的值必须被替换掉。
	if strings.Contains(got.Header.Get("X-Forwarded-For"), "1.2.3.4") {
		t.Errorf("客户端伪造的 X-Forwarded-For 被透传了: %q",
			got.Header.Get("X-Forwarded-For"))
	}
	if got.Header.Get("X-Forwarded-Host") == "evil.example.com" {
		t.Error("客户端伪造的 X-Forwarded-Host 被透传了")
	}
	if got.Header.Get("X-Real-IP") == "1.2.3.4" {
		t.Error("客户端伪造的 X-Real-IP 被透传了")
	}
	if strings.Contains(got.Header.Get("Forwarded"), "1.2.3.4") {
		t.Errorf("客户端伪造的 Forwarded 被透传了: %q", got.Header.Get("Forwarded"))
	}

	// 必须填上真实值。
	if got.Header.Get("X-Forwarded-For") == "" {
		t.Error("应当填上真实的 X-Forwarded-For")
	}
	if got.Header.Get("X-Real-IP") == "" {
		t.Error("应当填上真实的 X-Real-IP")
	}
	// 便于本地服务区分"来自外网"与"来自本机"。
	if got.Header.Get("X-Forwarded-By") != "isc" {
		t.Error("应当标记请求经过了 ISC 代理")
	}
}

// TestProxyRejectsUnknownHost 验证没有默认上游。
//
// 回退到某个默认上游会让"随便一个域名指向这台机器"都能打到那个服务上，
// 而用户完全不知道自己的服务被谁访问了。
func TestProxyRejectsUnknownHost(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: b.upstream(t),
	})

	for _, host := range []string{"other.example.com", "example.com", "evil.com", ""} {
		req := httptest.NewRequest(http.MethodGet, "http://"+host+"/", nil)
		if host == "" {
			req.Host = ""
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("Host=%q 应当返回 404，得到 %d", host, rec.Code)
		}
	}
}

func TestProxyRejectsConnect(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: b.upstream(t),
	})

	req := httptest.NewRequest(http.MethodConnect, "http://home.example.com:443", nil)
	req.Host = "home.example.com:443"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	// 转发 CONNECT 等于把开放代理的洞开在 TLS 层。
	if rec.Code == http.StatusOK {
		t.Fatal("CONNECT 必须被拒绝")
	}
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("状态码 = %d，期望 405", rec.Code)
	}
}

func TestProxyReportsUpstreamDown(t *testing.T) {
	t.Parallel()

	// 找一个确定没有服务在监听的端口。
	port := freePort(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"},
		Upstream: fmt.Sprintf("127.0.0.1:%d", port),
	})

	req := httptest.NewRequest(http.MethodGet, "http://home.example.com/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d，期望 502", rec.Code)
	}
	// 提示必须指向用户能检查的两件事。
	body := rec.Body.String()
	if !strings.Contains(body, "启动") || !strings.Contains(body, "端口") {
		t.Errorf("502 的提示应当指向「服务没起来」与「端口填错」: %s", body)
	}
}

func TestProxyForwardsBodyAndQuery(t *testing.T) {
	t.Parallel()

	b := newBackend(t)
	s := newTestServer(t, Route{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: b.upstream(t),
	})

	req := httptest.NewRequest(http.MethodPost,
		"http://home.example.com/api?x=1&y=2", strings.NewReader("请求体内容"))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	got := b.request()
	if got.URL.RawQuery != "x=1&y=2" {
		t.Errorf("查询串 = %q", got.URL.RawQuery)
	}
	<-b.mu
	body := b.lastBody
	b.mu <- struct{}{}
	if body != "请求体内容" {
		t.Errorf("请求体 = %q", body)
	}
}

// TestProxyPassesWebSocketUpgrade 验证 WebSocket 透传。
//
// 用户跑的服务里有相当一部分是 WebSocket 应用（Home Assistant、
// 各种自建面板），透传断了的表现是"页面能开但一直转圈"。
func TestProxyPassesWebSocketUpgrade(t *testing.T) {
	t.Parallel()

	// 上游是一个会真正完成协议升级的服务。
	//
	// **必须劫持连接**：Go 的 http 服务器要求 101 由处理器劫持后自行
	// 写出，直接 w.WriteHeader(101) 会让服务器异常关闭连接，
	// 而代理那边看到的是一个连接错误（502），看起来像透传坏了。
	var gotUpgrade string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUpgrade = r.Header.Get("Upgrade")

		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "测试服务器不支持劫持", http.StatusInternalServerError)
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // 测试清理

		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: dGVzdA==\r\n\r\n")
		_ = buf.Flush()

		// 保持一小会儿，让代理有机会把响应读完。
		time.Sleep(150 * time.Millisecond)
	}))
	t.Cleanup(upstream.Close)

	s := newTestServer(t, Route{
		ID: "ws", Hosts: []string{"ws.example.com"},
		Upstream: strings.TrimPrefix(upstream.URL, "http://"),
	})

	// **必须**用一个真实的服务器，不能用 httptest.NewRecorder。
	//
	// 101 响应要求代理**劫持**底层连接，而 ResponseRecorder 不实现
	// http.Hijacker —— 用它测会得到一个 502，看起来像"WebSocket
	// 透传坏了"，实际只是测试工具不支持。
	front := httptest.NewServer(s)
	t.Cleanup(front.Close)

	req, err := http.NewRequest(http.MethodGet, front.URL+"/socket", nil)
	if err != nil {
		t.Fatal(err)
	}
	// 真实服务器的地址是 127.0.0.1:port，而路由是按域名匹配的 ——
	// 因此必须显式设置 Host，否则会得到 404（"没有匹配的路由"）。
	req.Host = "ws.example.com"
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	// 不让客户端自动跟随重定向或重试 —— 我们要看到的就是那个 101。
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	// 客户端可能因为响应格式不完整而报错，但状态码已经拿到了。
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("状态码 = %d，期望 101 —— WebSocket 透传断了。响应: %s",
			resp.StatusCode, body)
	}
	if !strings.EqualFold(gotUpgrade, "websocket") {
		t.Errorf("上游没有收到 Upgrade 头，实际 = %q", gotUpgrade)
	}
}

// ---------------------------------------------------------------------------
// 安全拨号
// ---------------------------------------------------------------------------

// TestSafeDialerBlocksRebinding 验证连接时的二次校验。
//
// 配置时校验过不代表连接时还合法：主机名会被 DNS 重新解析，而
// **DNS rebinding** 正是利用这一点 —— 配置时域名解析到 127.0.0.1
// （通过校验），随后改解析到公网地址（变成跳板）。
func TestSafeDialerBlocksRebinding(t *testing.T) {
	t.Parallel()

	d := newSafeDialer()

	// 直接拨一个公网地址：即使上游校验被绕过，这里也必须挡住。
	_, err := d.DialContext(context.Background(), "tcp", "8.8.8.8:53")
	if err == nil {
		t.Fatal("安全拨号器不该允许连接公网地址")
	}
	if !errors.Is(err, ErrUnsafeUpstream) {
		t.Errorf("应当返回 ErrUnsafeUpstream，得到 %v", err)
	}
}

func TestSafeDialerAllowsLocal(t *testing.T) {
	t.Parallel()

	// 起一个真实的本地监听。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	d := newSafeDialer()
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("连接本地地址失败: %v", err)
	}
	_ = conn.Close()
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// freePort 找一个当前空闲的端口。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	// 给内核一点时间释放。
	time.Sleep(20 * time.Millisecond)
	return port
}
