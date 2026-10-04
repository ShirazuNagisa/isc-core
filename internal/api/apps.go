package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/acme"
	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 任务类型。必须在 daemon 装配时登记（Engine.RegisterKinds）。
const (
	JobKindAppDeploy = "app.deploy"
	JobKindAppStart  = "app.start"
)

// ListApps 实现 GET /v1/apps。
func (s *Server) ListApps(w http.ResponseWriter, r *http.Request) {
	if s.Apps == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.AppList{Items: []gen.App{}})
		return
	}
	list, err := s.Apps.List(r.Context())
	if err != nil {
		s.internalError(w, r, "failed to list apps", err)
		return
	}
	items := make([]gen.App, 0, len(list))
	for _, app := range list {
		items = append(items, s.toGenApp(r, app))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.AppList{Items: items})
}

// CreateApp 实现 POST /v1/apps。
func (s *Server) CreateApp(w http.ResponseWriter, r *http.Request) {
	if s.Apps == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "app hosting is unavailable")
		return
	}
	var req gen.AppCreateRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil ||
		strings.TrimSpace(req.Name) == "" || strings.TrimSpace(req.SourcePath) == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "name, preset_id and source_path are required")
		return
	}

	spec := apps.CreateSpec{
		Name:        req.Name,
		PresetID:    req.PresetId,
		SourcePath:  req.SourcePath,
		AutoStart:   req.AutoStart,
		MaxRestarts: req.MaxRestarts,
	}
	if req.Port != nil {
		spec.Port = *req.Port
	}
	if req.Domains != nil {
		spec.Domains = normalizeDomains(*req.Domains)
	}
	if req.CustomExecutable != nil && *req.CustomExecutable != "" {
		step := presets.Step{Executable: *req.CustomExecutable}
		if req.CustomArgs != nil {
			step.Args = *req.CustomArgs
		}
		spec.CustomRun = &step
	}

	app, err := s.Apps.Create(r.Context(), spec)
	if err != nil {
		// 识别不出入口、路径不对、预设未知 —— 都是用户的输入问题。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}
	s.auditSuccess(r, audit.ActionAppCreate, app.ID, app.Name)
	writeJSON(w, s.Log, http.StatusCreated, "application/json", s.toGenApp(r, app))
}

// GetApp 实现 GET /v1/apps/{id}。
func (s *Server) GetApp(w http.ResponseWriter, r *http.Request, id string) {
	app, ok, err := s.lookupApp(r, id)
	if err != nil {
		s.internalError(w, r, "failed to read the app", err)
		return
	}
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "app not found")
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", s.toGenApp(r, app))
}

// DeleteApp 实现 DELETE /v1/apps/{id}。
func (s *Server) DeleteApp(w http.ResponseWriter, r *http.Request, id string) {
	if s.Apps == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "app hosting is unavailable")
		return
	}
	if err := s.Apps.Delete(r.Context(), id); err != nil {
		if errors.Is(err, apps.ErrNotFound) {
			writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "app not found")
			return
		}
		s.internalError(w, r, "failed to delete the app", err)
		return
	}
	s.auditSuccess(r, audit.ActionAppDelete, id, "")
	w.WriteHeader(http.StatusNoContent)
}

// DeployApp 实现 POST /v1/apps/{id}/deploy。
func (s *Server) DeployApp(w http.ResponseWriter, r *http.Request, id string) {
	s.submitAppJob(w, r, id, JobKindAppDeploy, func(ctx context.Context, appID string, report func(float64, string)) error {
		return s.Apps.Deploy(ctx, appID, report)
	})
}

// StartApp 实现 POST /v1/apps/{id}/start。
func (s *Server) StartApp(w http.ResponseWriter, r *http.Request, id string) {
	s.submitAppJob(w, r, id, JobKindAppStart, func(ctx context.Context, appID string, report func(float64, string)) error {
		if err := s.Apps.Start(ctx, appID); err != nil {
			return err
		}
		report(1, "")
		return nil
	})
}

// RestartApp 实现 POST /v1/apps/{id}/restart。
func (s *Server) RestartApp(w http.ResponseWriter, r *http.Request, id string) {
	s.submitAppJob(w, r, id, JobKindAppStart, func(ctx context.Context, appID string, report func(float64, string)) error {
		if err := s.Apps.Stop(ctx, appID); err != nil && !errors.Is(err, apps.ErrNotFound) {
			return err
		}
		if err := s.Apps.Start(ctx, appID); err != nil {
			return err
		}
		report(1, "")
		return nil
	})
}

// StopApp 实现 POST /v1/apps/{id}/stop。
//
// 它是同步的：停止由 stopGrace 界定（先优雅退出、超时强杀），
// 不需要借任务引擎，也不需要用户去任务中心看结果。
func (s *Server) StopApp(w http.ResponseWriter, r *http.Request, id string) {
	if s.Apps == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "app hosting is unavailable")
		return
	}
	if err := s.Apps.Stop(r.Context(), id); err != nil {
		if errors.Is(err, apps.ErrNotFound) {
			writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "app not found")
			return
		}
		s.internalError(w, r, "failed to stop the app", err)
		return
	}
	s.auditSuccess(r, audit.ActionAppStop, id, "")
	w.WriteHeader(http.StatusNoContent)
}

// GetAppLogs 实现 GET /v1/apps/{id}/logs。
func (s *Server) GetAppLogs(w http.ResponseWriter, r *http.Request, id string, params gen.GetAppLogsParams) {
	_, ok, err := s.lookupApp(r, id)
	if err != nil {
		s.internalError(w, r, "failed to read the app", err)
		return
	}
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "app not found")
		return
	}
	tail := apps.LogTailDefault
	if params.Tail != nil {
		tail = *params.Tail
	}
	lines := s.Apps.Logs(id, tail)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.AppLogs{Lines: lines})
}

// submitAppJob 是启动/部署/重启共用的提交路径。
func (s *Server) submitAppJob(w http.ResponseWriter, r *http.Request, id, kind string,
	run func(ctx context.Context, appID string, report func(float64, string)) error) {

	if s.Apps == nil || s.Jobs == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "app hosting is unavailable")
		return
	}
	_, ok, err := s.lookupApp(r, id)
	if err != nil {
		s.internalError(w, r, "failed to read the app", err)
		return
	}
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound, CodeNotFound, "error.not_found", "app not found")
		return
	}

	manager := s.Apps
	jobRecord, err := s.Jobs.Submit(r.Context(), kind, func(ctx context.Context, reporter job.Reporter) (any, error) {
		// 把任务进度透给应用层：它按阶段上报，最后一段是"等健康"。
		return nil, run(ctx, id, func(fraction float64, message string) {
			reporter.Progress(fraction, message)
		})
	})
	if err != nil {
		// 并发状态切换是冲突（409），不是内部错误。
		if errors.Is(err, apps.ErrBusy) {
			writeProblem(w, r, s.Log, http.StatusConflict, CodeConflict, "error.conflict", err.Error())
			return
		}
		s.internalError(w, r, "failed to submit the app job", err)
		return
	}
	_ = manager
	s.auditSuccess(r, audit.ActionAppDeploy, id, kind)
	writeJSON(w, s.Log, http.StatusAccepted, "application/json", gen.JobAccepted{JobId: jobRecord.ID})
}

func (s *Server) lookupApp(r *http.Request, id string) (apps.App, bool, error) {
	if s.Apps == nil {
		return apps.App{}, false, nil
	}
	return s.Apps.Get(r.Context(), id)
}

// toGenApp 把领域对象转成契约对象，并补上**读的时候**才知道的公网状态。
func (s *Server) toGenApp(r *http.Request, app apps.App) gen.App {
	item := gen.App{
		Id:           app.ID,
		Name:         app.Name,
		PresetId:     app.PresetID,
		Kind:         string(app.Kind),
		SourcePath:   app.SourcePath,
		LocalPort:    app.LocalPort,
		State:        gen.AppState(app.State),
		Health:       gen.AppHealth(app.Health),
		AutoStart:    &app.AutoStart,
		MaxRestarts:  &app.MaxRestarts,
		RestartCount: &app.RestartCount,
	}
	if app.HealthDetail != "" {
		item.HealthDetail = &app.HealthDetail
	}
	if app.LastError != "" {
		item.LastError = &app.LastError
	}
	if !app.CreatedAt.IsZero() {
		created := app.CreatedAt
		item.CreatedAt = &created
	}
	if !app.UpdatedAt.IsZero() {
		updated := app.UpdatedAt
		item.UpdatedAt = &updated
	}

	domains := s.domainStatuses(r, app)
	if len(domains) > 0 {
		item.Domains = &domains
	}
	if s.Runtimes != nil && app.Kind != runtime.KindNone {
		if found, ok, err := s.Runtimes.Resolve(r.Context(), app.Kind, ""); err == nil && ok {
			info := toGenRuntime(found)
			item.Runtime = &info
		}
	}
	return item
}

// domainStatuses 查出每个域名的公网侧真实状态。
//
// 这些不是应用自己的字段：反代规则与证书由内核其余部分持有，用户也可能
// 在别处改过。读的时候查一次，才不会显示一个过期的副本。
func (s *Server) domainStatuses(r *http.Request, app apps.App) []gen.AppDomain {
	if len(app.Domains) == 0 {
		return nil
	}

	hosts := map[string]bool{}
	if s.ProxyRoutes != nil {
		if routes, err := s.ProxyRoutes.List(r.Context()); err == nil {
			for _, route := range routes {
				for _, host := range route.Hosts {
					hosts[host] = true
				}
			}
		}
	}

	// 证书按域名建索引：一张证书可以覆盖多个域名。
	certs := map[string]acme.CertStatus{}
	if s.Certs != nil {
		if statuses, err := s.Certs.Status(s.certWants(r)); err == nil {
			for _, status := range statuses {
				for _, domain := range status.Domains {
					certs[domain] = status
				}
			}
		}
	}

	out := make([]gen.AppDomain, 0, len(app.Domains))
	for _, domain := range app.Domains {
		entry := gen.AppDomain{Name: domain}
		ready := hosts[domain]
		entry.RouteReady = &ready
		if status, ok := certs[domain]; ok {
			needsRenew := status.NeedsRenew
			staging := status.Staging
			entry.CertNeedsRenew = &needsRenew
			entry.CertStaging = &staging
			if !status.ExpiresAt.IsZero() {
				expires := status.ExpiresAt
				entry.CertExpiresAt = &expires
			}
			if status.Reason != "" {
				reason := status.Reason
				entry.CertReason = &reason
			}
			if status.Error != "" {
				failure := status.Error
				entry.CertError = &failure
			}
		}
		out = append(out, entry)
	}
	return out
}

// normalizeDomains 清理域名列表：去空白、去重、保序。
func normalizeDomains(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		domain := strings.ToLower(strings.TrimSpace(value))
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		out = append(out, domain)
	}
	return out
}
