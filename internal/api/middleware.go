package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

// Middleware 是标准的 http 中间件。
type Middleware func(http.Handler) http.Handler

// Chain 按声明顺序包裹 handler。
//
// 顺序语义：Chain(h, A, B) 的请求路径是 A → B → h，
// 即**先声明的在外层**。这与阅读直觉一致。
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ctxKey 是本包私有的 context key 类型。
//
// 用私有类型而不是字符串，避免与其他包的 key 相撞。
type ctxKey int

const ctxKeyRequestID ctxKey = iota

// RequestIDHeader 是请求 ID 使用的头名。
const RequestIDHeader = "X-Request-Id"

// RequestID 为每个请求分配一个 ID，写入响应头与 context。
//
// 价值：客户端报错时能凭这个 ID 在内核日志里精确定位到那一次请求。
// 对于"手机从 4G 访问失败"这类难以复现的问题，这是最有效的排查入口。
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(RequestIDHeader)
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set(RequestIDHeader, id)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFrom 从 context 取出请求 ID。
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// newRequestID 生成 8 字节随机 ID（16 个十六进制字符）。
//
// 不用完整 UUID：它只需要在日志里唯一，短一点更易读。
// 随机生成失败时退化为时间戳，绝不返回空串 —— 空 ID 会让日志失去关联能力。
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return time.Now().UTC().Format("150405.000000")
	}
	return hex.EncodeToString(buf[:])
}

// Recover 捕获 handler 中的 panic，转成 500 而不是让整个进程退出。
//
// 对于一个以系统服务身份常驻的内核，一次 panic 就意味着所有定时任务停摆、
// 用户的域名解析静默失效 —— 那是比"这一个请求失败"严重得多的后果。
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if log != nil {
						log.Error("请求处理 panic",
							"panic", rec,
							"method", r.Method,
							"path", r.URL.Path,
							"request_id", RequestIDFrom(r.Context()))
					}
					writeProblem(w, r, log, http.StatusInternalServerError,
						CodeInternal, "error.internal", "")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder 记录响应状态码与字节数，供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush 透传，保证事件流的 WebSocket 升级不被中间件破坏。
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack 透传，同上：WebSocket 升级依赖 Hijacker。
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// LogRequests 记录访问日志。
//
// 刻意**不记录查询串**：事件流的令牌可能出现在查询串里（虽然我们
// 推荐用子协议），把它写进日志等于把凭据写进日志。
func LogRequests(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			// WebSocket 升级后状态码是 101，且连接长期存活；
			// 用 Info 级别记下来便于确认控制台是否真的连上了。
			if log != nil {
				log.Debug("http 请求",
					"method", r.Method,
					"path", r.URL.Path,
					"status", rec.status,
					"bytes", rec.bytes,
					"duration_ms", time.Since(start).Milliseconds(),
					"request_id", RequestIDFrom(r.Context()))
			}
		})
	}
}
