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
	})
	if err != nil {
		s.auditFailure(r, audit.ActionSettingsUpdate, "settings", err)
		// settings 的校验错误都是用户输入问题，返回 400 而不是 500。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	s.auditSuccess(r, audit.ActionSettingsUpdate, "settings", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenSettings(next))
}

func toGenSettings(s settings.Settings) gen.Settings {
	return gen.Settings{
		Lang:             gen.SettingsLang(s.Lang),
		LogLevel:         gen.SettingsLogLevel(s.LogLevel),
		EventBufferSize:  &s.EventBufferSize,
		NotifyOnIpChange: &s.NotifyOnIPChange,
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
