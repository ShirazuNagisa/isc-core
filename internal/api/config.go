package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/configio"
)

// maxImportBytes 是导入请求体的上限。
//
// 一份配置最多几百 KB，1 MB 已经有充足余量。设上限是必要的：
// 没有它，一个畸形的请求就能让内核把内存吃光，而内核崩掉意味着
// 用户的域名解析静默停摆。
const maxImportBytes = 1 << 20

// ExportConfig 实现 GET /v1/config/export。
func (s *Server) ExportConfig(w http.ResponseWriter, r *http.Request, params gen.ExportConfigParams) {
	includeSecrets := params.IncludeSecrets != nil && *params.IncludeSecrets

	body, err := s.Config.Export(r.Context(), includeSecrets)
	if err != nil {
		s.internalError(w, r, "导出配置失败", err)
		return
	}

	// 记审计时**不记"是否含明文"以外的任何内容**：导出动作本身
	// 值得留痕（它可能把密钥写到别处去了），但导出内容绝不进审计表。
	detail := "已脱敏"
	if includeSecrets {
		detail = "包含明文凭据"
	}
	s.auditSuccess(r, audit.ActionConfigExport, "config", detail)

	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 明确告知浏览器这是一个下载，而不是可以内联渲染的文档。
	if includeSecrets {
		w.Header().Set("Content-Disposition", `attachment; filename="isc-config-with-secrets.yaml"`)
	} else {
		w.Header().Set("Content-Disposition", `attachment; filename="isc-config.yaml"`)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// DescribeImport 实现 GET /v1/config/import。
//
// 它不做任何事，只描述导入会怎样表现。客户端（尤其是界面）靠它
// 决定要不要给用户弹一个"这是预览，不会有改动"的提示。
func (s *Server) DescribeImport(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.Log, http.StatusOK, "application/json", struct {
		DryRunSupported bool   `json:"dry_run_supported"`
		Notes           string `json:"notes"`
	}{
		DryRunSupported: true,
		Notes: "dry_run 默认为 true：先返回将会发生什么，确认后再以 " +
			"dry_run=false 提交。凭据按 (服务商, 标签) 匹配：已存在则更新，" +
			"否则新建。",
	})
}

// ImportConfig 实现 POST /v1/config/import。
func (s *Server) ImportConfig(w http.ResponseWriter, r *http.Request, params gen.ImportConfigParams) {
	body, ok := readImportBody(w, r, s)
	if !ok {
		return
	}
	dryRun := params.DryRun == nil || *params.DryRun

	res, err := s.Config.Import(r.Context(), body, dryRun)
	if err != nil {
		s.auditFailure(r, audit.ActionConfigImport, "config", err)
		s.importError(w, r, err)
		return
	}
	s.auditImport(r, audit.ActionConfigImport, res)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenImportResult(res))
}

// ImportDdnsGoConfig 实现 POST /v1/config/import/ddns-go。
func (s *Server) ImportDdnsGoConfig(w http.ResponseWriter, r *http.Request, params gen.ImportDdnsGoConfigParams) {
	body, ok := readImportBody(w, r, s)
	if !ok {
		return
	}
	dryRun := params.DryRun == nil || *params.DryRun

	res, err := s.Config.ImportDdnsGo(r.Context(), body, dryRun)
	if err != nil {
		s.auditFailure(r, audit.ActionConfigImportDdnsGo, "config", err)
		s.importError(w, r, err)
		return
	}
	s.auditImport(r, audit.ActionConfigImportDdnsGo, res)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenImportResult(res))
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func readImportBody(w http.ResponseWriter, r *http.Request, s *Server) ([]byte, bool) {
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "请求体为空")
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxImportBytes+1))
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return nil, false
	}
	if len(body) > maxImportBytes {
		writeProblem(w, r, s.Log, http.StatusRequestEntityTooLarge,
			CodeInvalidRequest, "error.invalid_request",
			"导入内容超过 1 MB 上限")
		return nil, false
	}
	return body, true
}

// importError 把导入错误翻译成 HTTP 响应。
func (s *Server) importError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, configio.ErrInvalidDocument),
		errors.Is(err, configio.ErrNotDdnsGo):
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "config.import.invalid", err.Error())
	default:
		s.internalError(w, r, "导入配置失败", err)
	}
}

// auditImport 记录一次导入的统计结果。
//
// 统计数字是有价值的审计内容（"这次导入建了 3 条凭据"），
// 而凭据内容不是 —— 因此这里只记计数。
func (s *Server) auditImport(r *http.Request, action string, res configio.Result) {
	detail := "预览"
	if !res.DryRun {
		detail = "已应用"
	}
	detail += "；凭据 +" +
		itoa(res.Summary.CredentialsCreated) + " ~" +
		itoa(res.Summary.CredentialsUpdated) + " 跳过 " +
		itoa(res.Summary.CredentialsSkipped)
	s.auditSuccess(r, action, "config", detail)
}

func toGenImportResult(res configio.Result) gen.ImportResult {
	out := gen.ImportResult{
		DryRun: res.DryRun,
		Summary: gen.ImportSummary{
			CredentialsCreated: &res.Summary.CredentialsCreated,
			CredentialsUpdated: &res.Summary.CredentialsUpdated,
			CredentialsSkipped: &res.Summary.CredentialsSkipped,
			TasksCreated:       &res.Summary.TasksCreated,
			TasksUpdated:       &res.Summary.TasksUpdated,
			TasksSkipped:       &res.Summary.TasksSkipped,
			SettingsUpdated:    &res.Summary.SettingsUpdated,
		},
	}
	if len(res.Warnings) > 0 {
		out.Warnings = &res.Warnings
	}
	if len(res.Errors) > 0 {
		out.Errors = &res.Errors
	}
	return out
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
