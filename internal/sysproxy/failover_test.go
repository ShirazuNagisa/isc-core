package sysproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deadProxy 起一个"连得上但立刻断开"的代理。
//
// 这正是代理软件没选到节点时的表现：TCP 是通的（代理进程活着），
// 但隧道建立之后立刻被关掉，客户端看到的是 EOF 而不是 connection refused。
// 实测中 dl.static-php.dev 就是这么失败的。
type deadProxy struct {
	ln    net.Listener
	conns atomic.Int64
}

func newDeadProxy(t *testing.T) *deadProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &deadProxy{ln: ln}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			d.conns.Add(1)
			conn.Close()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return d
}

func (d *deadProxy) url() *url.URL {
	u, _ := url.Parse("http://" + d.ln.Addr().String())
	return u
}

// proxyTo 返回一个"永远指向某个代理"的选路函数。
func proxyTo(u *url.URL) func(*http.Request) (*url.URL, error) {
	return func(*http.Request) (*url.URL, error) { return u, nil }
}

func noProxy(*http.Request) (*url.URL, error) { return nil, nil }

// GET 在代理坏掉时必须自己改成直连 —— 这就是运行时下载失败的那个场景。
func TestFailoverFallsBackToDirectForGET(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "来自源站")
	}))
	defer origin.Close()

	dead := newDeadProxy(t)
	tr := newFailover(&http.Transport{}, proxyTo(dead.url()))
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("本该兜底成功，却失败了：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "来自源站" {
		t.Fatalf("body = %q", body)
	}
	if dead.conns.Load() == 0 {
		t.Fatal("代理根本没被尝试过，测的就不是兜底")
	}
}

// 坏掉的代理必须被记住一小段时间：否则每个请求都要先去撞一次墙。
func TestFailoverRemembersBadProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ok")
	}))
	defer origin.Close()

	dead := newDeadProxy(t)
	calls := atomic.Int64{}
	proxyFn := func(*http.Request) (*url.URL, error) {
		calls.Add(1)
		return dead.url(), nil
	}
	tr := newFailover(&http.Transport{}, proxyFn)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	for i := 0; i < 3; i++ {
		resp, err := client.Get(origin.URL)
		if err != nil {
			t.Fatalf("第 %d 次请求失败：%v", i+1, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	// 首次要试一次代理，之后 30 秒冷却期内不该再试。
	if got := dead.conns.Load(); got != 1 {
		t.Fatalf("代理被连接了 %d 次，期望 1 次（冷却没生效）", got)
	}
}

// 不可重放的请求不换路重发：响应读到一半才断时，服务端可能已经处理过了。
//
// 这类请求靠"选路"兜底 —— 见 TestFailoverRemembersBadProxy：代理一旦
// 被判坏，后续 POST 会直接走直连。
func TestFailoverDoesNotReplayPOST(t *testing.T) {
	var hits atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
	}))
	defer origin.Close()

	dead := newDeadProxy(t)
	tr := newFailover(&http.Transport{}, proxyTo(dead.url()))
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	// 刻意不用 bytes.Reader：那样 http.NewRequest 会填上 GetBody，
	// 请求就变成"可重放"的了，测不出区别。
	body := io.NopCloser(strings.NewReader("payload"))
	resp, err := client.Post(origin.URL, "text/plain", body)
	if err == nil {
		resp.Body.Close()
		t.Fatal("本该失败（不许重放），却成功了")
	}
	if hits.Load() != 0 {
		t.Fatalf("源站被调用了 %d 次，不可重放的请求不该被发出去", hits.Load())
	}
}

// 代理返回 502 时，幂等请求换直连重试（网关错误是代理自己生成的）。
func TestFailoverRetriesOnGatewayStatus(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "源站回答")
	}))
	defer origin.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer bad.Close()
	badURL, _ := url.Parse(bad.URL)

	tr := newFailover(&http.Transport{}, proxyTo(badURL))
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "源站回答" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

// 调用方主动取消时不许换路重发 —— 那违背它的意图。
func TestFailoverHonoursCancellation(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "不该被调用")
	}))
	defer origin.Close()

	dead := newDeadProxy(t)
	f := newFailover(&http.Transport{}, proxyTo(dead.url()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, origin.URL, nil)
	if _, err := f.RoundTrip(req); err == nil {
		t.Fatal("已取消的请求应当报错")
	}
	if dead.conns.Load() != 0 {
		t.Fatal("已取消的请求不该真的发出去")
	}
}

// 两条路径都要拿到同一份 TLS 设置，否则"跳过校验"只在一半情况下生效。
func TestFailoverAppliesTLSToBothRoutes(t *testing.T) {
	f := newFailover(&http.Transport{}, noProxy)
	cfg := &tls.Config{InsecureSkipVerify: true}
	f.SetTLSClientConfig(cfg)

	if f.base.TLSClientConfig != cfg {
		t.Fatal("基准传输层没拿到 TLS 配置")
	}
	if f.directTransport().TLSClientConfig != cfg {
		t.Fatal("直连传输层没拿到 TLS 配置")
	}
}

// 没有代理时就是直连，不该有任何多余动作。
func TestFailoverWithoutProxyGoesDirect(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "直连")
	}))
	defer origin.Close()

	tr := newFailover(&http.Transport{}, noProxy)
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "直连" {
		t.Fatalf("body=%q", body)
	}
}

// 直连那条路必须是 base 的副本：调用方绑定的 DialContext（DDNS 要绑定
// 本机地址和网卡）不能因为换路就丢掉。
func TestFailoverKeepsDialContextOnBothRoutes(t *testing.T) {
	base := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, address)
		},
	}
	dead := newDeadProxy(t)
	f := newFailover(base, proxyTo(dead.url()))
	if f.directTransport().DialContext == nil {
		t.Fatal("副本丢了 DialContext")
	}
	if f.directTransport().Proxy != nil {
		t.Fatal("副本必须是不走代理的")
	}
}

// connectProxy 是一个最小的 CONNECT 代理：只把字节双向转发。
//
// 造它出来是为了复现真实时序 —— 先让基准传输层**成功地**发一次 https
// 请求（这一步会初始化 HTTP/2 状态），再让它坏掉，看兜底那条路还行不行。
type connectProxy struct {
	ln net.Listener
}

func newConnectProxy(t *testing.T) *connectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{ln: ln}
	go p.serve()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *connectProxy) url() *url.URL {
	u, _ := url.Parse("http://" + p.ln.Addr().String())
	return u
}

func (p *connectProxy) serve() {
	for {
		conn, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(conn)
	}
}

func (p *connectProxy) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		conn.Write([]byte("HTTP/1.1 405 Method Not Allowed\r\n\r\n"))
		return
	}
	upstream, err := net.Dial("tcp", req.Host)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer upstream.Close()
	conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, br); done <- struct{}{} }()
	go func() { io.Copy(conn, upstream); done <- struct{}{} }()
	<-done
}

// 直连副本必须显式启用 HTTP/2，而不是照抄 Clone 的默认。
//
// 背景是一个花了很久才定位的 Go 坑。`http.Transport.Clone()` 出来的副本
// 和"新建一个同样字段的 Transport"并不等价：基准传输层只要真实发过一次
// https，惰性初始化过的 HTTP/2 状态就会以某种不一致的形式留在副本里，
// 结果是 ALPN 通告了 h2、客户端却没有对应的处理器，把收到的 h2 帧按
// HTTP/1.x 解析：
//
//	malformed HTTP response "\x00\x00\x12\x04..."
//
// 实测四种构造，只有下面这种能通（对同一时刻的同一个地址）：
//
//	裸 Clone + Proxy=nil            ✗ malformed HTTP response
//	Clone + 清空 TLSNextProto       ✗ 同上
//	Clone + 清空 + ForceAttemptHTTP2 ✓
//	全新 Transport                   ✓ 但会丢掉调用方的 DialContext / TLS 设置
//
// 它极难发现，因为兜底**看起来执行了**（第二次请求确实发出去了），
// 只是必然失败，而错误信息里留下的还是第一条路径的错误。
//
// 这里只钉住构造结果，不造真实场景：要触发它，基准传输层不能有自定义
// TLSClientConfig（Go 在这种情况下才安装 h2），而测试访问自签名服务器
// 又必须设 TLSClientConfig —— 两头凑不齐。真实证据是
// failover_live_test.go 里的联网用例：加这两行之前三个运行时源站全挂，
// 加上之后全部 206。
func TestFailoverDirectRouteEnablesHTTP2Explicitly(t *testing.T) {
	f := newFailover(&http.Transport{}, noProxy)
	d := f.directTransport()

	if !d.ForceAttemptHTTP2 {
		t.Error("直连副本没有显式启用 HTTP/2：Clone 的默认会让 ALPN 与处理器不一致")
	}
	if d.TLSNextProto != nil {
		t.Error("直连副本带着 TLSNextProto，应当清空让传输层自己安装 h2 支持")
	}
	if d.Proxy != nil {
		t.Error("直连副本不该走代理")
	}
}

// 兜底那条路必须真的能用，而不是"发了但永远失败"。
//
// 这条走的是完整链路：先用一个能用的 CONNECT 代理成功发一程，
// 再把代理关掉，逼出兜底。它保证"换路"这件事在真实请求上成立。
func TestFailoverDirectRouteWorksAgainstHTTP2Origin(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "h2 源站")
	}))
	ts.EnableHTTP2 = true
	ts.StartTLS()
	defer ts.Close()

	proxy := newConnectProxy(t)
	f := newFailover(&http.Transport{}, proxyTo(proxy.url()))
	f.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: true})
	client := &http.Client{Transport: f, Timeout: 10 * time.Second}

	// 第一程：经代理成功，这一步会把 HTTP/2 状态初始化起来。
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("经代理的首个请求就失败了：%v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// 第二程：代理死掉，必须自动改走直连并且真的成功。
	proxy.ln.Close()
	resp, err = client.Get(ts.URL)
	if err != nil {
		t.Fatalf("代理坏掉后兜底失败：%v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "h2 源站" {
		t.Fatalf("body = %q", body)
	}
}
