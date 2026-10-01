package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	apispec "github.com/ShirazuNagisa/isc-core/api"
	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/configio"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/ddns"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/provider"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// Deps 是 API 层的依赖。
//
// 全部通过构造注入，不使用包级全局变量 —— 这是从 ddns-go 移植代码时
// 必须坚持的改造方向（见 docs/PLAN.md R11）。
type Deps struct {
	Bus      *event.Bus
	Jobs     *job.Engine
	Platform *platform.Bundle
	Log      *slog.Logger

	// StartedAt 是进程启动时间，用于 /v1/health 的 uptime。
	StartedAt time.Time

	// AllowedOrigins 是 WebSocket 允许的 Origin 模式。
	//
	// 留空表示只允许同源连接（浏览器场景）与无 Origin 的连接（CLI / 原生 GUI）。
	// 仅在用 Vite 开发服务器调试控制台时才需要放宽。
	AllowedOrigins []string

	// Token 是本地管理接口的访问令牌。
	//
	// 由守护进程在启动时生成并写入 runtime.json；为空是配置错误，
	// 中间件会拒绝所有请求而不是放行。
	Token string

	// Providers 是 DNS 服务商注册表。
	//
	// 它是 /v1/providers 的数据来源，也是凭据字段校验的依据 ——
	// 下游 GUI 靠它渲染表单，因此这个依赖不能省。
	Providers *provider.Registry

	// Credentials 是凭据领域服务。
	Credentials *credential.Service

	// Settings 是运行时设置。
	Settings *settings.Service

	// Audit 写审计记录。
	Audit *audit.Recorder

	// AuditWriter 读审计记录。
	//
	// 与 Audit 分开是因为它们的生命周期不同：写入从第一个请求就要可用，
	// 而读取在数据库不可用时可以优雅地返回空列表。
	AuditWriter audit.Writer

	// Config 提供配置导入导出与 ddns-go 迁移。
	Config *configio.Service

	// Tasks 是动态解析任务的领域服务。
	Tasks *ddns.Service
}

// Server 实现 gen.ServerInterface。
type Server struct {
	Deps
}

// 编译期断言：接口实现必须完整。
//
// 这条断言的价值在于：以后往 openapi.yaml 里加接口、重新生成代码之后，
// 如果忘了实现，**编译就会失败**，而不是等到运行时才 404。
var _ gen.ServerInterface = (*Server)(nil)

// New 构造 API 服务。
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	if d.Bus == nil {
		d.Bus = event.NewBus(0)
	}
	return &Server{Deps: d}
}

// Routes 返回挂载了全部路由与中间件的 http.Handler。
//
// 中间件顺序（由外到内）：
//
//	RequestID    为每个请求分配可追踪的 ID
//	Recover      把 panic 转成 500，避免一次请求打挂整个内核
//	LogRequests  访问日志
//	AuthMiddleware  强制令牌（/v1/health 与契约原文除外）
//
// Recover 必须在内层：它要把 panic 写成一个合法的 problem+json 响应，
// 因此需要 RequestID 已经在 context 里，且不能先被 Auth 拦掉。
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// 生成的 handler 把自己的路由注册到 mux 上。
	// 注意：它注册的是**路径**模式，方法校验在生成的 wrapper 内部完成；
	// 因此下面追加带方法的模式（Go 1.22+ 语法）不会与之冲突。
	gen.HandlerFromMux(s, mux)

	// 契约原文：供验证控制台与 Scalar UI 使用。
	mux.HandleFunc("GET /v1/openapi.yaml", s.handleOpenAPISpec)

	return Chain(mux,
		RequestID(),
		Recover(s.Log),
		LogRequests(s.Log),
		func(next http.Handler) http.Handler {
			return AuthMiddleware(s.Token, s.Log, next)
		},
	)
}

// handleOpenAPISpec 返回 OpenAPI 契约原文。
//
// 返回 YAML 而不是 JSON：这份文件同时供人阅读与代码生成使用，
// YAML 的可读性明显更好，而 Scalar 与 Swagger UI 都原生支持。
func (s *Server) handleOpenAPISpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(apispec.OpenAPISpec)
}

// ---------------------------------------------------------------------------
// Meta
// ---------------------------------------------------------------------------

// GetHealth 实现 GET /v1/health。
//
// 说明：该端点不要求鉴权，因为它只暴露"内核是否在运行"。
// 它必须是**最小依赖**的 —— 不碰数据库、不碰平台后端，
// 这样任何子系统故障都不会让它误报内核已死。
func (s *Server) GetHealth(w http.ResponseWriter, _ *http.Request) {
	resp := gen.Health{
		Status:        gen.Ok,
		UptimeSeconds: int64(time.Since(s.StartedAt).Seconds()),
		Version:       strPtr(version.Version),
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// GetMeta 实现 GET /v1/meta。
func (s *Server) GetMeta(w http.ResponseWriter, _ *http.Request) {
	resp := gen.Meta{
		Version:      version.Version,
		ApiVersion:   version.APIVersion,
		Commit:       strPtr(version.Commit),
		BuildTime:    strPtr(version.BuildTime),
		Os:           s.Platform.OS,
		Arch:         s.Platform.Arch,
		StartedAt:    s.StartedAt,
		Capabilities: toGenCapabilities(s.Platform.Capabilities()),
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

func toGenCapabilities(c platform.Capabilities) gen.Capabilities {
	return gen.Capabilities{
		Firewall:       toGenImplState(c.Firewall),
		ServiceManager: toGenImplState(c.ServiceManager),
		IpMonitor:      toGenImplState(c.IPMonitor),
		SecretStore:    toGenImplState(c.SecretStore),
		Transport:      toGenImplState(c.Transport),
		LowPortBinder:  toGenImplState(c.LowPortBinder),
	}
}

func toGenImplState(s platform.ImplState) gen.ImplState {
	return gen.ImplState{
		Available: s.Available,
		Backend:   s.Backend,
		Note:      strPtr(s.Note),
	}
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// ListJobs 实现 GET /v1/jobs。
func (s *Server) ListJobs(w http.ResponseWriter, r *http.Request, params gen.ListJobsParams) {
	f := job.Filter{}
	if params.Cursor != nil {
		f.Cursor = string(*params.Cursor)
	}
	if params.Limit != nil {
		f.Limit = int(*params.Limit)
	}
	if params.Kind != nil {
		f.Kind = *params.Kind
	}
	if params.Status != nil {
		for _, st := range *params.Status {
			f.Statuses = append(f.Statuses, job.Status(st))
		}
	}

	items, next, err := s.Jobs.List(r.Context(), f)
	if err != nil {
		s.Log.Error("查询任务列表失败", "err", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}

	resp := gen.JobList{Items: make([]gen.Job, 0, len(items))}
	for _, j := range items {
		resp.Items = append(resp.Items, toGenJob(j))
	}
	if next != "" {
		resp.NextCursor = &next
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// GetJob 实现 GET /v1/jobs/{id}。
func (s *Server) GetJob(w http.ResponseWriter, r *http.Request, id gen.JobId) {
	j, found, err := s.Jobs.Get(r.Context(), string(id))
	if err != nil {
		s.Log.Error("查询任务失败", "job_id", id, "err", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}
	if !found {
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeJobNotFound, "error.job_not_found", string(id))
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenJob(j))
}

// CancelJob 实现 POST /v1/jobs/{id}/cancel。
func (s *Server) CancelJob(w http.ResponseWriter, r *http.Request, id gen.JobId) {
	j, err := s.Jobs.Cancel(r.Context(), string(id))
	switch {
	case err == nil:
		writeJSON(w, s.Log, http.StatusOK, "application/json", toGenJob(j))
	case errors.Is(err, job.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeJobNotFound, "error.job_not_found", string(id))
	case errors.Is(err, job.ErrNotCancelable):
		writeProblem(w, r, s.Log, http.StatusConflict,
			CodeJobNotCancelable, "error.job_not_cancelable", string(id))
	default:
		s.Log.Error("取消任务失败", "job_id", id, "err", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
	}
}

// RunNoopJob 实现 POST /v1/debug/noop。
//
// 非产品接口：存在的唯一目的是让任务引擎与事件流在没有任何业务功能的
// M0 阶段就能被端到端验证。M1 之后移除。
func (s *Server) RunNoopJob(w http.ResponseWriter, r *http.Request) {
	var req gen.NoopRequest
	if r.Body != nil {
		// 空 body 是合法的：全部字段都取默认值。
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
			writeProblem(w, r, s.Log, http.StatusBadRequest,
				CodeInvalidRequest, "error.invalid_request", err.Error())
			return
		}
	}

	j, err := s.Jobs.Submit(r.Context(), "debug.noop", noopFunc(req))
	if err != nil {
		s.Log.Error("提交空转任务失败", "err", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}
	writeJSON(w, s.Log, http.StatusAccepted, "application/json",
		gen.JobAccepted{JobId: j.ID})
}

// noopFunc 构造空转任务体。
func noopFunc(req gen.NoopRequest) job.Func {
	steps := intOr(req.Steps, 5)
	stepMs := intOr(req.StepMs, 200)
	failAt := intOr(req.FailAtStep, 0)

	return func(ctx context.Context, r job.Reporter) (any, error) {
		for i := 1; i <= steps; i++ {
			r.Progress(float64(i-1)/float64(steps), i18n.T("job.noop.running", i, steps))

			timer := time.NewTimer(time.Duration(stepMs) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}

			if failAt > 0 && i == failAt {
				return nil, &job.Error{
					Code:   "noop_requested_failure",
					Title:  i18n.T("job.noop.failed", i),
					Detail: "fail_at_step 参数要求在第 N 步失败",
				}
			}
		}
		r.Progress(1, i18n.T("job.noop.done"))
		return map[string]any{"steps": steps, "step_ms": stepMs}, nil
	}
}

// ---------------------------------------------------------------------------
// 映射
// ---------------------------------------------------------------------------

func toGenJob(j job.Job) gen.Job {
	out := gen.Job{
		Id:         j.ID,
		Kind:       j.Kind,
		Status:     gen.JobStatus(j.Status),
		Progress:   j.Progress,
		Message:    strPtr(j.Message),
		CreatedAt:  j.CreatedAt,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
	if j.Err != nil {
		detail := j.Err.Detail
		code := j.Err.Code
		out.Error = &gen.Problem{
			Type:   problemTypeBase + code,
			Title:  j.Err.Title,
			Status: http.StatusInternalServerError,
			Code:   &code,
			Detail: strPtr(detail),
		}
	}
	if len(j.Result) > 0 {
		// 直接把原始 JSON 放进 interface{} 会让 encoding/json 二次编码成
		// base64 字符串（json.RawMessage 的 MarshalJSON 只有在静态类型是
		// RawMessage 时才生效）。这里先解码成通用结构，保证输出是真正的 JSON。
		var decoded any
		if err := json.Unmarshal(j.Result, &decoded); err == nil {
			out.Result = decoded
		}
	}
	return out
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}
