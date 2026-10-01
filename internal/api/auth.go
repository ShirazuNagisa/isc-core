package api

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
)

// tokenSubprotocolPrefix 是承载访问令牌的 WebSocket 子协议前缀。
//
// 为什么需要它：浏览器的 WebSocket API **无法设置自定义请求头**，
// 因此验证控制台（浏览器页面）没有别的办法携带 `Authorization`。
// 常见的替代做法是把令牌放进查询串，但那会让令牌出现在 URL 里，
// 进而可能落进访问日志、Referer、书签历史 —— 子协议不会。
//
// 子协议值必须符合 RFC 6455 的 token 语法，base64url 字符集恰好满足。
const tokenSubprotocolPrefix = "isc.token."

// authHeader 是标准鉴权头。
const authHeader = "Authorization"

// bearerPrefix 是 Bearer 方案前缀。
const bearerPrefix = "Bearer "

// AuthMiddleware 强制校验访问令牌。
//
// 设计依据见 docs/DECISIONS.md D09：
//
//   - **即使是回环连接也必须带令牌**。回环并不等于可信 ——
//     本机上的任何进程、以及浏览器里的任意页面，都能向回环端口发请求。
//     令牌是唯一把"能连上"和"能操作"分开的东西。
//   - 令牌比较使用常数时间算法，避免通过响应耗时逐字节猜测。
//   - 唯一的例外是 /v1/health：它只暴露"内核是否在运行"，
//     且必须能被尚未拿到令牌的客户端（例如刚装好还没启动过）用来探测。
func AuthMiddleware(token string, log *slog.Logger, next http.Handler) http.Handler {
	if token == "" {
		// 没有配置令牌意味着调用方写错了。宁可让所有请求失败，
		// 也不要静默放行 —— 后者会把管理接口变成人人可用的后门。
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if log != nil {
				log.Error("访问令牌为空，拒绝所有请求（这是配置错误）", "path", r.URL.Path)
			}
			writeProblem(w, r, log, http.StatusUnauthorized,
				CodeUnauthorized, "error.unauthorized", "")
		})
	}

	want := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		got := tokenFromRequest(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			writeProblem(w, r, log, http.StatusUnauthorized,
				CodeUnauthorized, "error.unauthorized", "")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isPublicPath 报告该路径是否无需鉴权。
func isPublicPath(path string) bool {
	switch path {
	case "/v1/health", "/v1/openapi.yaml":
		// 契约原文公开是安全的：它只描述接口形状，不含任何数据或密钥，
		// 而验证控制台需要在拿到令牌之前就能渲染接口文档。
		return true
	default:
		return false
	}
}

// tokenFromRequest 从请求中提取访问令牌。
//
// 支持两种载体：
//
//	Authorization: Bearer <token>                CLI、原生 GUI
//	Sec-WebSocket-Protocol: isc.token.<token>    浏览器 WebSocket
func tokenFromRequest(r *http.Request) string {
	if h := r.Header.Get(authHeader); h != "" {
		// 大小写不敏感地匹配 "Bearer"：HTTP 头值里的方案名按规范不区分大小写。
		if len(h) >= len(bearerPrefix) && strings.EqualFold(h[:len(bearerPrefix)], bearerPrefix) {
			if tok := strings.TrimSpace(h[len(bearerPrefix):]); tok != "" {
				return tok
			}
		}
	}

	for _, p := range offeredSubprotocols(r) {
		if tok, ok := strings.CutPrefix(p, tokenSubprotocolPrefix); ok && tok != "" {
			return tok
		}
	}
	return ""
}

// offeredSubprotocols 解析客户端在 Sec-WebSocket-Protocol 中提供的子协议。
func offeredSubprotocols(r *http.Request) []string {
	raw := r.Header.Get("Sec-WebSocket-Protocol")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// selectedTokenSubprotocol 返回应当回显给客户端的子协议。
//
// 客户端（浏览器）在握手中提供子协议后，服务端**必须**回显其中之一，
// 否则浏览器会判定握手失败并直接关闭连接。
func selectedTokenSubprotocol(r *http.Request) []string {
	for _, p := range offeredSubprotocols(r) {
		if strings.HasPrefix(p, tokenSubprotocolPrefix) {
			return []string{p}
		}
	}
	return nil
}
