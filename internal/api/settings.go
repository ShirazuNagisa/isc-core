package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// GetSettings 实现 GET /v1/settings。
func (s *Server) GetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenSettings(s.Settings.Get()))
}

// UpdateSettings 实现 PATCH /v1/settings。
func (s *Server) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lang             *string `json:"lang"`
		LogLevel         *string `json:"log_level"`
		EventBufferSize  *int    `json:"event_buffer_size"`
		NotifyOnIPChange *bool   `json:"notify_on_ip_change"`
		ProxyEnabled     *bool   `json:"proxy_enabled"`
		ProxyPort        *int    `json:"proxy_port"`
	}
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "请求体为空")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	next, err := s.Settings.Update(r.Context(), settings.Patch{
		Lang:             body.Lang,
		LogLevel:         body.LogLevel,
		EventBufferSize:  body.EventBufferSize,
		NotifyOnIPChange: body.NotifyOnIPChange,
		ProxyEnabled:     body.ProxyEnabled,
		ProxyPort:        body.ProxyPort,
	})
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
		if err := s.Proxy.Start(r.Context(), next.ProxyPort); err != nil {
			s.Log.Error("按设置启动反向代理失败",
				"port", next.ProxyPort, "err", err)
		}

	case next.ProxyEnabled && status.Running && status.Port != next.ProxyPort:
		// 端口变了：Start 内部会先停掉旧的再按新端口监听。
		if err := s.Proxy.Start(r.Context(), next.ProxyPort); err != nil {
			s.Log.Error("按新端口重启反向代理失败",
				"port", next.ProxyPort, "err", err)
		}
	}
}

func toGenSettings(s settings.Settings) gen.Settings {
	return gen.Settings{
		Lang:             gen.SettingsLang(s.Lang),
		LogLevel:         gen.SettingsLogLevel(s.LogLevel),
		EventBufferSize:  &s.EventBufferSize,
		NotifyOnIpChange: &s.NotifyOnIPChange,
		ProxyEnabled:     &s.ProxyEnabled,
		ProxyPort:        &s.ProxyPort,
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
	if params.Result != nil {
		f.Result = string(*params.Result)
	}

	items, next, err := s.AuditWriter.ListAudit(r.Context(), f)
	if err != nil {
		s.internalError(w, r, "查询审计失败", err)
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
