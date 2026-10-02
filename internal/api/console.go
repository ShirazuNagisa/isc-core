package api

import (
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/console"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// 本文件把验证控制台挂到本机管理接口上。
//
// # 鉴权模型（重要）
//
// 控制台的**静态资源**不需要令牌：浏览器加载一个 HTML 页面时无法携带
// Authorization 头，要求令牌会让控制台根本无法打开。
//
// 令牌通过 GET /v1/console/bootstrap 交给页面上的 JS。这个端点受两道
// 限制保护：
//
//  1. 只允许回环来源（见 LoopbackGuard）；
//  2. 只允许 Host 头是回环地址的请求。
//
// 第 2 条不是多余的：网页可以发起跨站请求，正常情况下浏览器会因缺少
// CORS 头而**阻止页面读取响应**，因此拿不到令牌。但 DNS rebinding 攻击
// 可以绕过这一层 —— 攻击者先让自己的域名解析到 127.0.0.1，浏览器就认为
// 请求是同源的，从而允许读取响应。而那种情况下 Host 头是攻击者的域名，
// 因此校验 Host 能把这条路堵死。
//
// 这与 docs/DECISIONS.md D09 的已知限制一致：同一台机器上的其它进程
// 本来就能读到 runtime.json 里的令牌，因此"把令牌交给本机浏览器"没有
// 扩大暴露面。真正的修复（会话级授权）见 R13。

// consoleAssets 是控制台的静态资源前缀。
const consoleAssets = "/console/"

// MountConsole 把控制台的路由挂到 mux 上。
//
// 刻意放在 Routes() 里显式调用而不是 init()：路由表应当能在一个地方读完，
// 而不是散落在多个文件的 init 里。
func (s *Server) MountConsole(mux *http.ServeMux) {
	assets := console.FS()

	// 根路径重定向到控制台。
	//
	// 用户拿到的是 http://127.0.0.1:PORT/ ，直接给他一个 404 会让人以为
	// 内核没起来。API 在 /v1/ 下，根路径本来也没有别的用途。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, consoleAssets, http.StatusFound)
	})

	mux.Handle("GET "+consoleAssets, s.consoleFileServer(assets))
	mux.HandleFunc("GET /v1/console/bootstrap", s.handleConsoleBootstrap)
}

// consoleFileServer 提供静态资源。
func (s *Server) consoleFileServer(assets fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(assets))

	return http.StripPrefix(consoleAssets, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 不走缓存。
		//
		// 控制台是开发与验证工具，改了资源之后用户按 F5 就该看到新版。
		// 让浏览器缓存一个 304 会制造"我明明改了却没生效"的困惑，
		// 而那是最浪费时间的一类问题。
		w.Header().Set("Cache-Control", "no-store")
		// 资源里没有任何需要被外部页面嵌入的东西。
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// 目录请求显式返回 index.html。
		//
		// **不能**把路径改写成 "/index.html" 再交给 FileServer：
		// FileServer 会把 /index.html 规范化重定向回 "./"，
		// 而 "./" 又被这里改写成 /index.html —— 形成无限重定向，
		// 浏览器只会报"重定向次数过多"。
		if r.URL.Path == "" || r.URL.Path == "/" {
			s.serveConsoleIndex(w, assets)
			return
		}
		fileServer.ServeHTTP(w, r)
	}))
}

// serveConsoleIndex 直接返回 index.html 的内容。
func (s *Server) serveConsoleIndex(w http.ResponseWriter, assets fs.FS) {
	body, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		// 内嵌资源在编译期就已确定，读不到说明二进制被破坏了。
		s.Log.Error(i18n.T("api.console_index"), "err", err)
		writeProblem(w, nil, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleConsoleBootstrap 把令牌交给控制台页面。
//
// 它要求已经通过 LoopbackGuard —— 那是在中间件链上做的，
// 这里只做 Host 头的二次确认，因为路由可能被单独挂载。
func (s *Server) handleConsoleBootstrap(w http.ResponseWriter, r *http.Request) {
	if !isLoopbackHost(r.Host) {
		// 用 403 而不是 404：这是一个明确的安全拒绝，
		// 而不是"资源不存在"。日志里能据此看出有人在尝试。
		s.Log.Warn(i18n.T("api.console_host"),
			"host", r.Host, "remote", r.RemoteAddr)
		writeProblem(w, r, s.Log, http.StatusForbidden,
			CodeForbidden, "console.host_not_allowed", r.Host)
		return
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", map[string]any{
		"token":        s.Token,
		"version":      versionString(),
		"api_base":     "/v1",
		"ws_path":      "/v1/events",
		"ws_protocol":  "isc.token.",
		"capabilities": toGenCapabilities(s.Platform.Capabilities()),
	})
}

// ---------------------------------------------------------------------------
// Host 校验
// ---------------------------------------------------------------------------

// LoopbackGuard 拒绝 Host 头不是本机地址的请求。
//
// # 为什么本地接口也需要这个
//
// DNS rebinding：攻击者让自己的域名先解析到攻击者的服务器（页面同源加载
// 成功），再把这个域名重新绑定到 127.0.0.1。此时浏览器认为目标仍是同源，
// 于是允许页面读取响应 —— 令牌就这样被读走。
//
// 整个攻击的前提是 Host 头是攻击者的域名。校验 Host 是最直接的堵法。
//
// 放行 `isc.local`：命名管道与 Unix 套接字没有真实的 host，客户端统一用
// 这个占位值（见 platform.Endpoint.HTTPBaseURL）。它不可能是公开可解析的
// 域名，因此放行它是安全的。
func LoopbackGuard(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			log.Warn(i18n.T("api.console_rebind"),
				"host", r.Host, "remote", r.RemoteAddr, "path", r.URL.Path)
			writeProblem(w, r, log, http.StatusForbidden,
				CodeForbidden, "console.host_not_allowed", r.Host)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isLoopbackHost 报告 Host 头是否指向本机。
//
// 接受的形态：
//
//	127.0.0.1        127.0.0.1:8080
//	localhost        localhost:8080
//	[::1]            [::1]:8080
//	isc.local        （管道 / Unix 套接字的占位 host）
//
// 刻意**不接受** `0.0.0.0`、主机名、以及任何其它地址：内核的管理接口
// 只监听回环，因此那些 Host 只可能来自被伪装的请求。
func isLoopbackHost(host string) bool {
	if host == "" {
		// HTTP/1.0 的请求可以没有 Host。Go 的 http 服务器在 HTTP/1.1 下
		// 会补上，因此空值意味着请求不是正常客户端发出的。
		return false
	}

	// 去掉端口。用 net.SplitHostPort 而不是手工切冒号 ——
	// IPv6 字面量里的冒号会让手工切分切错位置。
	name := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		name = h
	}
	name = strings.ToLower(strings.Trim(name, "[]"))

	switch name {
	case "127.0.0.1", "localhost", "::1", "isc.local":
		return true
	}

	// 整个 127.0.0.0/8 都是回环。
	if ip := net.ParseIP(name); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// versionString 返回给控制台展示的版本串。
func versionString() string { return version.Version }
