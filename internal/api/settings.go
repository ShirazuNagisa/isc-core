package api

import (
	"context"
	"encoding/json"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// GetSettings 实现 GET /v1/settings。
func (s *Server) GetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenSettings(s.Settings.Get()))
}

// UpdateSettings 实现 PATCH /v1/settings。
func (s *Server) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	// **直接解码进 settings.Patch**，不在这里再声明一个匿名结构体。
	//
	// # 为什么这一点很重要
	//
	// 早先这里有一份与 settings.Patch 逐字段重复的匿名结构体，而它
	// 已经被漏掉过两次：给 Settings 与 Patch 都加了新字段，却忘了
	// 往这里也加一个 —— 于是 JSON 里的未知字段被**静默忽略**，
	// 接口返回 200 而值根本没变。用户看到的是"点开开关什么都没发生"，
	// 而编译、测试、日志都不会有任何提示。
	//
	// 复用一个结构体就从根本上消除了这类疏漏：没有第二份需要同步的
	// 字段列表。
	var patch settings.Patch
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.T("api.empty_body"))
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	next, err := s.Settings.Update(r.Context(), patch)
	if err != nil {
		s.auditFailure(r, audit.ActionSettingsUpdate, "settings", err)
		// settings 的校验错误都是用户输入问题，返回 400 而不是 500。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	// 代理相关设置变化后立即调整监听。
	//
	// 不做这一步的话，用户在界面上开启代理之后什么都看不到变化 ——
	// 他得重启内核才行，而界面完全没提示这一点。
	s.applyProxySettings(r, next)

	s.auditSuccess(r, audit.ActionSettingsUpdate, "settings", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenSettings(next))
}

// applyProxySettings 按最新设置调整代理监听。
//
// 失败**不**让整个设置更新失败：其它设置（语言、日志级别）已经生效了，
// 把它们一起回滚是更糟的选择。错误会留在代理状态里，用户能在界面上
// 看到"为什么没起来"。
func (s *Server) applyProxySettings(r *http.Request, next settings.Settings) {
	if s.Proxy == nil {
		return
	}

	status := s.Proxy.Status()

	switch {
	case !next.ProxyEnabled && status.Running:
		_ = s.Proxy.Stop(r.Context())

	case next.ProxyEnabled && !status.Running:
		s.startProxyWithSettings(r.Context(), next)

	case next.ProxyEnabled && status.Running &&
		(status.Port != next.ProxyPort || status.TLS != next.ProxyTLS):
		// 端口或 TLS 模式变了：需要重启监听。
		//
		// **TLS 这一项很容易漏**：早先只比较端口，于是用户在已经
		// 运行的代理上打开 HTTPS 开关时"什么都没发生" —— 代理还在
		// 用明文跑，而界面上开关明明是打开的。
		s.startProxyWithSettings(r.Context(), next)
	}
}

// startProxyWithSettings 按设置启动或重启代理监听。
func (s *Server) startProxyWithSettings(ctx context.Context, next settings.Settings) {
	var err error

	if next.ProxyTLS {
		err = s.Proxy.ServeTLS(ctx, next.ProxyPort, proxy.TLSOptions{
			Provider: s.CertProvider,
		})
	} else {
		err = s.Proxy.Start(ctx, next.ProxyPort)
	}

	if err != nil {
		// 启动失败**不让整个设置更新失败**：其它设置（语言、日志级别）
		// 已经生效了，把它们一起回滚是更糟的选择。错误留在代理状态里，
		// 用户能在界面上看到"为什么没起来"。
		s.Log.Error(i18n.T("api.settings.proxy_failed"),
			"port", next.ProxyPort, "tls", next.ProxyTLS, "err", err)
	}
}

func toGenSettings(s settings.Settings) gen.Settings {
	return gen.Settings{
		Lang:                gen.SettingsLang(s.Lang),
		LogLevel:            gen.SettingsLogLevel(s.LogLevel),
		EventBufferSize:     &s.EventBufferSize,
		NotifyOnIpChange:    &s.NotifyOnIPChange,
		ProxyEnabled:        &s.ProxyEnabled,
		ProxyPort:           &s.ProxyPort,
		ProxyTls:            &s.ProxyTLS,
		AcmeEmail:           &s.ACMEEmail,
		AcmeDirectory:       &s.ACMEDirectory,
		AcmeDnsCredentialId: &s.ACMEDNSCredentialID,
	}
}

// ListAudit 实现 GET /v1/audit。
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, params gen.ListAuditParams) {
	if s.AuditWriter == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.AuditList{
			Items: []gen.AuditEntry{},
		})
		return
	}

	f := audit.Filter{}
	if params.Cursor != nil {
		f.Cursor = string(*params.Cursor)
	}
	if params.Limit != nil {
		f.Limit = int(*params.Limit)
	}
	if params.Action != nil {
		f.Action = *params.Action
	}
	if params.ActionPrefix != nil {
		f.ActionPrefix = *params.ActionPrefix
	}
	if params.Result != nil {
		f.Result = string(*params.Result)
	}

	items, next, err := s.AuditWriter.ListAudit(r.Context(), f)
	if err != nil {
		s.internalError(w, r, i18n.T("api.audit.query_failed"), err)
		return
	}

	resp := gen.AuditList{Items: make([]gen.AuditEntry, 0, len(items))}
	for _, rec := range items {
		resp.Items = append(resp.Items, gen.AuditEntry{
			Id:        rec.ID,
			Ts:        parseAuditTime(rec.TS),
			Action:    rec.Action,
			Target:    optionalString(rec.Target),
			Result:    gen.AuditResult(rec.Result),
			Detail:    optionalString(rec.Detail),
			RequestId: optionalString(rec.RequestID),
			Remote:    optionalString(rec.Remote),
		})
	}
	if next != "" {
		resp.NextCursor = &next
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// parseAuditTime 把存储中的 RFC 3339 字符串还原为时间。
//
// 解析失败时返回零值而不是报错：一条时间戳损坏的审计记录不该让
// 整个审计列表打不开 —— 那恰好会让用户失去排查问题的唯一线索。
func parseAuditTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
