package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 错误码常量。
//
// 这些值会出现在客户端的判断分支里，因此**一旦发布不可更改**。
// 与 title / detail 不同，它们不随语言变化。
const (
	CodeUnauthorized     = "unauthorized"
	CodeNotFound         = "not_found"
	CodeMethodNotAllowed = "method_not_allowed"
	CodeInvalidRequest   = "invalid_request"
	CodeInternal         = "internal"
	CodeJobNotFound      = "job_not_found"
	CodeJobNotCancelable = "job_not_cancelable"
	CodeConflict         = "conflict"
	// CodeUnsupported 表示该服务商不支持此操作。
	//
	// 与 CodeInvalidRequest 分开：后者是"你提交的内容有问题"，
	// 前者是"这家服务商做不到这件事"。用户对二者的处置完全不同 ——
	// 一个要改输入，一个要换服务商或换个做法。
	CodeUnsupported = "unsupported"
	// CodeUpstreamError 表示服务商拒绝了这次操作。
	//
	// detail 里带服务商的原始说明 —— 那恰恰是用户能据此行动的信息。
	CodeUpstreamError = "upstream_error"
	// CodeForbidden 表示请求被安全策略拒绝。
	//
	// 目前只有一种情形：Host 头不是本机地址（DNS rebinding 防护）。
	CodeForbidden = "forbidden"
)

// problemTypeBase 是错误类型 URI 的前缀。
//
// 用 URI 而不是裸字符串，是为了让客户端可以按类型做精确匹配；
// 使用保留域名 .invalid 表明它不指向真实网页。
const problemTypeBase = "https://isc.invalid/problems/"

// problem 构造一个 RFC 9457 错误对象。
//
// titleKey 是 i18n 消息 key；detail 已经是本地化后的文本（可为空）。
func problem(cat *i18n.Catalog, status int, code, titleKey, detail string) gen.Problem {
	typ := problemTypeBase + code
	// 用**请求自己的**目录，而不是全局默认值 —— 见 i18n/context.go 的说明。
	// cat 由 writeProblem 从 context 里取，永不返回 nil。
	title := cat.T(titleKey)
	p := gen.Problem{
		Type:   typ,
		Title:  title,
		Status: status,
		Code:   &code,
	}
	if detail != "" {
		p.Detail = &detail
	}
	return p
}

// writeProblem 写出一个 problem+json 响应。
func writeProblem(w http.ResponseWriter, r *http.Request, log *slog.Logger,
	status int, code, titleKey, detail string) {

	p := problem(i18n.FromContext(r.Context()), status, code, titleKey, detail)
	if r != nil && r.URL != nil {
		instance := r.URL.Path
		p.Instance = &instance
	}

	writeJSON(w, log, status, "application/problem+json", p)
}

// writeJSON 写出 JSON 响应。
//
// 序列化在内存中完成后才写状态码：一旦 WriteHeader 被调用，
// 再出错就只能截断响应体，客户端会收到一个语法不完整的 JSON ——
// 那比一个干净的 500 更难排查。
func writeJSON(w http.ResponseWriter, log *slog.Logger, status int, contentType string, body any) {
	byt, err := json.Marshal(body)
	if err != nil {
		if log != nil {
			log.Error(i18n.T("api.encode_failed"), "err", err)
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		// 这里只能手写字面量：再走一次 problem() 可能再次失败。
		_, _ = w.Write([]byte(`{"type":"https://isc.invalid/problems/internal",` +
			`"title":"internal error","status":500,"code":"internal"}`))
		return
	}

	w.Header().Set("Content-Type", contentType+"; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(byt)
}

// strPtr 返回字符串的指针；空串返回 nil，便于配合 omitempty 语义。
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
