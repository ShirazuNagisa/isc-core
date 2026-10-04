package sysproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// failoverCooldown 是一条路径被判为"暂时不可用"之后被跳过的时长。
//
// 取 30 秒的理由与系统代理设置的缓存时长一致：代理软件切节点、掉线、
// 重连都在这个量级上。太短会让每个请求都去撞一次坏路径，太长则会在
// 代理恢复之后仍然不肯用它。
const failoverCooldown = 30 * time.Second

// Transport 让外发请求在"走代理"和"直连"之间自动兜底。
//
// 需要它，是因为两条路都可能不通，而且会**互相镜像**地出问题：
//
//	开着代理时直连被本地网络拦截 → TLS 证书不匹配 / connection reset
//	代理软件没选到节点、或节点掉线 → 走代理的请求全部失败
//
// 这两种情况在同一台机器上会先后发生。只挑一边站队，就必然在其中一种
// 情况下全军覆没 —— 实测过：同一个内核，上午必须走代理才能访问
// Cloudflare，傍晚代理节点掉了之后，连运行时都下载不下来。
//
// 策略分两层：
//
//  1. **选路**：优先走当前"健康"的那条。一条路失败后冷却 30 秒，
//     期间后续请求直接走另一条。这一层不改请求语义，对任何方法都安全。
//  2. **重放**：当前请求本身失败了，就换另一条路重发一次 —— 但只在
//     重发不会造成重复副作用时才做（见 replayable）。
//
// 第 1 层是关键：它让"创建一条 DNS 记录"这种不可重放的请求也能在
// 代理坏掉之后正常走直连，而不需要冒险重发。
//
// base 的 Proxy 字段会被强制设为 Func()（跟随系统代理）。
func Transport(base *http.Transport) *Failover {
	if base == nil {
		base = &http.Transport{}
	}
	return newFailover(base, Func())
}

// newFailover 允许注入"这个请求该走哪个代理"，测试用它替掉系统设置。
func newFailover(base *http.Transport, proxy func(*http.Request) (*url.URL, error)) *Failover {
	if base == nil {
		base = &http.Transport{}
	}
	base.Proxy = proxy

	// 直连那条路是 base 的副本，但**不能直接用 Clone 的结果**。
	//
	// http.Transport.Clone 会给副本一个非空但为空的 TLSNextProto
	// （等于"声明不支持 h2"），却仍然让 ALPN 通告 h2。服务端于是选 h2，
	// 而客户端这边没有对应的处理器，把收到的 h2 帧按 HTTP/1.x 解析：
	//
	//	malformed HTTP response "\x00\x00\x12\x04..."
	//
	// 实测过四种构造，只有下面这种能通：
	//
	//	裸 Clone + Proxy=nil            ✗ malformed HTTP response
	//	Clone + 清空 TLSNextProto       ✗ 同上
	//	Clone + 清空 + ForceAttemptHTTP2 ✓
	//	全新 Transport（丢掉调用方设置） ✗ 不可取，会丢 DialContext/TLS
	//
	// 这一点特别隐蔽：兜底**看起来执行了**（确实发起了第二次请求），
	// 只是永远失败，于是错误信息仍然是第一条路径的。
	direct := base.Clone()
	direct.Proxy = nil
	direct.TLSNextProto = nil
	direct.ForceAttemptHTTP2 = true

	return &Failover{base: base, direct: direct}
}

// Failover 是 Transport 返回的传输层。
//
// 之所以是具体类型而不是 http.RoundTripper：调用方还要能调整底层
// 传输层（DDNS 的"跳过 TLS 校验"就是直接改 TLSClientConfig）。
type Failover struct {
	base   *http.Transport
	direct *http.Transport

	// 两条路径各自"暂时不可用"的截止时间（Unix 纳秒，0 表示可用）。
	proxyDownUntil  atomic.Int64
	directDownUntil atomic.Int64
}

// SetTLSClientConfig 同时作用于两条路径。
//
// 直连那条路是 base 的**副本**，只改 base 的话，"跳过 TLS 校验"会
// 变成"走代理时跳过、直连时不跳过" —— 一条只在特定路径上生效的安全
// 设置，比不生效更难查。
func (f *Failover) SetTLSClientConfig(cfg *tls.Config) {
	f.base.TLSClientConfig = cfg
	f.direct.TLSClientConfig = cfg
}

// route 是一条候选路径。
type route struct {
	rt      *http.Transport
	proxied bool
}

// directTransport 是 base 的副本，只是不走代理。
//
// 走一次 Clone 而不是另起一个裸 Transport：调用方在 base 上设置的
// DialContext（DDNS 要绑定本机地址和网卡）、TLS 超时等都必须保留，
// 否则"换条路"会悄悄换掉别的语义。
func (f *Failover) directTransport() *http.Transport { return f.direct }

// routes 按优先级给出候选路径。
func (f *Failover) routes(req *http.Request) []route {
	proxied := false
	if f.base.Proxy != nil {
		if u, err := f.base.Proxy(req); err == nil && u != nil {
			proxied = true
		}
	}
	direct := route{rt: f.directTransport()}
	if !proxied {
		return []route{direct}
	}
	viaProxy := route{rt: f.base, proxied: true}
	if f.isDown(&f.proxyDownUntil) && !f.isDown(&f.directDownUntil) {
		return []route{direct, viaProxy}
	}
	return []route{viaProxy, direct}
}

// RoundTrip 先走首选路径，必要时换另一条。
func (f *Failover) RoundTrip(req *http.Request) (*http.Response, error) {
	// 同一条请求上的重试必须显式重放 body，否则第二次会发一个空 body。
	// 先把 body 读出来的成本太高，交给 GetBody（bytes.Reader 等会自动带）。
	candidates := f.routes(req)
	resp, err := candidates[0].rt.RoundTrip(req)
	used := candidates[0]

	if !f.shouldSwitch(req, resp, err, used) {
		return resp, err
	}
	f.markDown(used)
	if len(candidates) < 2 {
		return resp, err
	}
	next := candidates[1]
	if clone, ok := replay(req); ok {
		if r2, err2 := next.rt.RoundTrip(clone); err2 == nil {
			// 换路成功本身就证明首选那条路是坏的，冷却保持有效 ——
			// 立刻撤销的话，后面的请求会继续撞同一堵墙。
			if resp != nil {
				resp.Body.Close()
			}
			return r2, nil
		}
	}
	if resp != nil {
		// 网关错误也算一次失败，但它是有响应的，交给调用方处理。
		return resp, err
	}
	return nil, err
}

// shouldSwitch 判断是否值得换另一条路重发。
func (f *Failover) shouldSwitch(req *http.Request, resp *http.Response, err error, used route) bool {
	if err == nil {
		// 只有"从代理那里拿到网关错误"才当作代理的问题：
		// 502/503/504 几乎都是代理自己生成的（连不上节点），
		// 而源站极少用这几个码回答 API 请求。
		return used.proxied && isGatewayStatus(resp) && idempotent(req.Method)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		// 调用方主动取消：换条路重发违背它的意图。
		return false
	}
	return replayable(req, err)
}

// replayable 判断这个失败的请求换条路重发是否安全。
//
//	连接都没建立起来        → 请求根本没离开这台机器，随便重发
//	幂等方法                → 重发没有额外副作用
//	其它（POST/PUT/PATCH）  → 不重发。响应读了一半就断、或 body 写了一半
//	                          才断，服务端都可能已经处理过了，重发就是
//	                          一次重复的写入。这种请求靠"选路"那一层兜底。
func replayable(req *http.Request, err error) bool {
	if dialFailure(err) {
		return true
	}
	if !idempotent(req.Method) {
		return false
	}
	if req.Body == nil || req.Body == http.NoBody {
		return true
	}
	return req.GetBody != nil
}

// replay 复制一份可以重新发送的请求；不能重放时返回 false。
func replay(req *http.Request) (*http.Request, bool) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	clone.Body = body
	return clone, true
}

func (f *Failover) markDown(r route) {
	if r.proxied {
		f.proxyDownUntil.Store(time.Now().Add(failoverCooldown).UnixNano())
		return
	}
	f.directDownUntil.Store(time.Now().Add(failoverCooldown).UnixNano())
}

func (f *Failover) isDown(until *atomic.Int64) bool {
	v := until.Load()
	return v != 0 && time.Now().UnixNano() < v
}

// dialFailure 判断错误是不是"连接就没建立起来"。
func dialFailure(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "dial"
	}
	return false
}

func idempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

func isGatewayStatus(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
