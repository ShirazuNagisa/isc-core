package proxy

import (
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Route 是一条转发规则。
type Route struct {
	// ID 是规则标识，用于日志与界面。
	ID string `json:"id"`
	// Label 是用户可读的名称。
	Label string `json:"label"`
	// Hosts 是要匹配的域名。
	//
	// 支持两种写法：
	//
	//	home.example.com    精确匹配
	//	*.example.com       匹配一级子域名
	//
	// `*.example.com` **不**匹配 `example.com` 本身，也**不**匹配
	// `a.b.example.com`。这与 TLS 证书的通配规则一致 —— 若这里放宽，
	// 用户会遇到"路由匹配上了但证书不匹配"的困惑，
	// 而那个问题的表现是浏览器报证书错误，很难联想到路由。
	Hosts []string `json:"hosts"`

	// Upstream 是转发目标，形如 http://127.0.0.1:8096。
	//
	// 必须是本机或内网地址 —— 见 upstream.go 里的说明。
	Upstream string `json:"upstream"`

	// TLS 表示这个域名是否需要 HTTPS。
	//
	// 为 true 时请求必须走 TLS；内核会用为该域名签发的证书终止 TLS。
	TLS bool `json:"tls"`
}

// Validate 检查一条路由是否可用。
func (r Route) Validate(selfPort int) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New(i18n.T("proxy.err.no_route_id"))
	}
	if len(r.Hosts) == 0 {
		return errors.New(i18n.T("proxy.err.no_domain"))
	}
	for _, h := range r.Hosts {
		if err := validateHostPattern(h); err != nil {
			return err
		}
	}

	normalized, err := ValidateUpstream(r.Upstream)
	if err != nil {
		return err
	}

	// 拒绝把自己作为上游。
	//
	// upstream 的校验允许回环地址，因此 127.0.0.1:<本代理的端口>
	// 会通过校验，然后请求进入一个无限循环 —— 直到耗尽文件描述符。
	// 那个症状（内核卡死、无法连接）与根因（配置写错了一个端口）
	// 之间没有任何提示。
	if selfPort > 0 && pointsToSelf(normalized, selfPort) {
		return fmt.Errorf(
			i18n.T("proxy.err.self_loop"), selfPort)
	}
	return nil
}

func pointsToSelf(upstream string, selfPort int) bool {
	u, err := url.Parse(upstream)
	if err != nil {
		return false
	}
	if u.Port() != fmt.Sprint(selfPort) {
		return false
	}
	host := u.Hostname()
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// validateHostPattern 校验域名模式。
func validateHostPattern(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New(i18n.T("proxy.err.empty_domain"))
	}
	if strings.ContainsAny(host, " /\\") {
		return fmt.Errorf(i18n.T("proxy.err.bad_chars"), host)
	}

	// 通配只允许出现在最前面，且只允许一个。
	if strings.Contains(host, "*") {
		if !strings.HasPrefix(host, "*.") || strings.Count(host, "*") != 1 {
			return fmt.Errorf(
				i18n.T("proxy.err.wildcard_pos"), host)
		}
		rest := strings.TrimPrefix(host, "*.")
		if rest == "" || strings.Contains(rest, "*") {
			return fmt.Errorf(i18n.T("proxy.err.bad_pattern"), host)
		}
	}
	return nil
}

// Server 按 Host 把请求转发到本机的服务。
type Server struct {
	log *slog.Logger

	// selfPort 是代理自己监听的端口，用于拒绝"转发给自己"。
	selfPort int

	mu       sync.RWMutex
	routes   []Route
	proxies  map[string]*httputil.ReverseProxy
	byHost   map[string]Route // 精确匹配表
	wildcard []wildcardRoute  // 通配匹配表（按域名长度倒序）

	// sharedTransport 是所有转发器共用的传输层。
	//
	// 共用而不是每个上游一个：它持有连接池，分开会让空闲连接数量
	// 随路由数增长，而家用场景下路由通常只有几条、连接数也不多。
	sharedTransport *http.Transport
}

type wildcardRoute struct {
	suffix string // ".example.com"
	route  Route
}

// NewServer 构造代理。
func NewServer(log *slog.Logger, selfPort int) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:      log,
		selfPort: selfPort,
		proxies:  make(map[string]*httputil.ReverseProxy),
		byHost:   make(map[string]Route),
	}
}

// SetRoutes 替换全部路由。
//
// 整体替换而不是增量修改：路由表是用户在界面上一份一份编出来的，
// 增量修改的语义（"删掉第三条"）在并发下没有意义，而且会让
// "界面显示的路由"与"实际生效的路由"出现差异。
func (s *Server) SetRoutes(routes []Route) error {
	compiled := make([]Route, 0, len(routes))
	byHost := make(map[string]Route, len(routes))
	var wildcards []wildcardRoute

	for _, r := range routes {
		if err := r.Validate(s.selfPort); err != nil {
			return err
		}

		normalized, err := ValidateUpstream(r.Upstream)
		if err != nil {
			return err
		}
		r.Upstream = normalized

		// 规范化域名：小写、去空白。
		hosts := make([]string, 0, len(r.Hosts))
		for _, h := range r.Hosts {
			h = strings.ToLower(strings.TrimSpace(h))
			hosts = append(hosts, h)

			if strings.HasPrefix(h, "*.") {
				wildcards = append(wildcards, wildcardRoute{
					suffix: h[1:], // ".example.com"
					route:  r,
				})
				continue
			}
			byHost[h] = r
		}
		r.Hosts = hosts
		compiled = append(compiled, r)
	}

	// 通配按后缀长度倒序：更具体的模式先匹配。
	//
	// 例如同时配了 *.example.com 与 *.a.example.com，
	// 请求 b.a.example.com 应当命中后者。不倒序的话结果取决于
	// 用户输入的顺序 —— 那是一个"改一下顺序就好了"的隐蔽 bug。
	sort.SliceStable(wildcards, func(i, j int) bool {
		return len(wildcards[i].suffix) > len(wildcards[j].suffix)
	})

	s.mu.Lock()
	s.routes = compiled
	s.byHost = byHost
	s.wildcard = wildcards
	s.mu.Unlock()

	s.log.Info("代理路由已更新", "count", len(compiled))
	return nil
}

// Routes 返回当前路由的副本。
func (s *Server) Routes() []Route {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Route(nil), s.routes...)
}

// Lookup 找出某个 Host 对应的路由。
func (s *Server) Lookup(host string) (Route, bool) {
	host = strings.ToLower(stripPort(host))

	s.mu.RLock()
	defer s.mu.RUnlock()

	if r, ok := s.byHost[host]; ok {
		return r, true
	}
	for _, w := range s.wildcard {
		// 通配只匹配**一级**：b.a.example.com 不该被 *.example.com 命中。
		//
		// 这与 TLS 证书的通配规则一致。放宽它的后果是用户拿到一个
		// 证书不匹配的域名，而浏览器只会说"证书无效"。
		if !strings.HasSuffix(host, w.suffix) {
			continue
		}
		prefix := strings.TrimSuffix(host, w.suffix)
		if prefix == "" || strings.Contains(prefix, ".") {
			continue
		}
		return w.route, true
	}
	return Route{}, false
}

// stripPort 去掉 Host 里的端口。
func stripPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.Trim(host, "[]")
}

// ServeHTTP 实现 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CONNECT 一律拒绝。
	//
	// `httputil.ReverseProxy` 本身不处理 CONNECT，但显式拒绝更清楚：
	// 转发 CONNECT 等于把开放代理的洞开在 TLS 层，而那是扫描器
	// 最想找到的东西。
	if r.Method == http.MethodConnect {
		s.log.Warn("拒绝 CONNECT 请求（防开放代理）",
			"remote", r.RemoteAddr, "host", r.Host)
		http.Error(w, i18n.T("proxy.err.no_connect"), http.StatusMethodNotAllowed)
		return
	}

	route, ok := s.Lookup(r.Host)
	if !ok {
		// 找不到路由时**不**回退到任何默认上游。
		//
		// 回退会让"随便一个域名指向这台机器"都能打到某个本地服务上，
		// 而用户完全不知道自己的服务被谁访问了。
		s.log.Debug("没有匹配的代理路由", "host", r.Host, "path", r.URL.Path)
		http.Error(w, i18n.T("proxy.err.no_rule"), http.StatusNotFound)
		return
	}

	rp, err := s.proxyFor(route)
	if err != nil {
		s.log.Error("构造转发器失败", "route", route.ID, "err", err)
		http.Error(w, i18n.T("proxy.err.bad_forward"), http.StatusBadGateway)
		return
	}
	rp.ServeHTTP(w, r)
}

// proxyFor 取出（或构造）某条路由的转发器。
func (s *Server) proxyFor(route Route) (*httputil.ReverseProxy, error) {
	s.mu.RLock()
	if rp, ok := s.proxies[route.Upstream]; ok {
		s.mu.RUnlock()
		return rp, nil
	}
	s.mu.RUnlock()

	target, err := url.Parse(route.Upstream)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("proxy.err.resolve_failed"), err)
	}

	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)

			// SetXForwarded 会填上正确的 X-Forwarded-For / -Host / -Proto。
			//
			// 客户端给的那几个头**已经**被 ReverseProxy 在调用 Rewrite
			// 之前清掉了（Go 1.20+ 的行为），因此这里填的就是真实值。
			// 这一点很关键：应用常靠这些头判断"请求来自哪里"，
			// 而让它相信客户端伪造的值会导致 IP 白名单之类的机制失效。
			pr.SetXForwarded()

			// X-Real-IP 不在 SetXForwarded 的范围内，但很多应用读它。
			// 客户端给的那个必须删掉，否则它就成了一个可以随意伪造身份
			// 的入口。
			pr.Out.Header.Del("X-Real-IP")
			pr.Out.Header.Set("X-Real-IP", clientIP(pr.In))

			// 标记请求经过了代理，便于本地服务区分"来自外网"与"来自本机"。
			pr.Out.Header.Set("X-Forwarded-By", "isc")
		},
		Transport:     s.transport(),
		FlushInterval: 100 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			// 上游不通是最常见的问题（服务没起来、端口填错）。
			// 日志里要说清楚是哪个上游 —— 用户配了好几条路由时，
			// 一句笼统的 502 完全无法定位。
			s.log.Warn("转发到上游失败",
				"host", r.Host, "upstream", route.Upstream, "err", err)

			if errors.Is(err, ErrUnsafeUpstream) {
				http.Error(w, i18n.T("proxy.err.upstream_denied"), http.StatusBadGateway)
				return
			}
			http.Error(w, i18n.T("proxy.err.connect_local"),
				http.StatusBadGateway)
		},
	}

	s.mu.Lock()
	// 双检：并发构造同一个上游时只保留一个。
	if existing, ok := s.proxies[route.Upstream]; ok {
		s.mu.Unlock()
		return existing, nil
	}
	s.proxies[route.Upstream] = rp
	s.mu.Unlock()

	return rp, nil
}

// transport 返回带安全拨号的传输层。
//
// 每次调用都返回**同一个**：它持有连接池，每次新建会让连接无法复用
// （每个请求都重新握手），而且会泄漏空闲连接。
func (s *Server) transport() http.RoundTripper {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sharedTransport != nil {
		return s.sharedTransport
	}
	s.sharedTransport = &http.Transport{
		DialContext:           newSafeDialer().DialContext,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// ResponseHeaderTimeout 不设：有些本地服务的首个响应很慢
		//（例如正在转码的媒体服务器），设超时会误杀正常请求。
	}
	return s.sharedTransport
}

// clientIP 取客户端地址。
//
// 用 RemoteAddr 而**不是** X-Forwarded-For：后者是客户端可伪造的，
// 而这里生成的正是要交给上游信任的那个值。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Close 释放代理持有的连接。
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sharedTransport != nil {
		s.sharedTransport.CloseIdleConnections()
	}
	return nil
}
