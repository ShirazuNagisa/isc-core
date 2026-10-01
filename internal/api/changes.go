package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/change"
	"github.com/ShirazuNagisa/isc-core/internal/reach"
)

// 本文件实现"预览 → 应用 → 撤销"这条闭环的接口。
//
// # 为什么计划要留在内核里
//
// 计划里的步骤持有 apply / revert 闭包（它们捕获了平台后端的句柄），
// 那些东西既不能序列化，也不能通过 HTTP 传一个来回。
//
// 因此流程是：内核生成计划后留在内存里，只把可序列化的预览发给客户端；
// 用户确认时凭 plan ID 回来取。
//
// 更重要的原因是安全：若改成"客户端把计划传回来"，就等于让客户端
// 能构造一份计划让内核以管理员身份执行任意防火墙操作。留在内存里
// 天然没有这个问题。

// PlanReachExpose 实现 POST /v1/reach/providers/{name}/plan。
//
// 它**不产生任何系统变更** —— 只计算差异并登记计划。
func (s *Server) PlanReachExpose(w http.ResponseWriter, r *http.Request,
	name gen.ReachProviderName) {

	provider, ok := s.Reach.Get(string(name))
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "reach.provider_not_found", string(name))
		return
	}

	var in gen.ExposeRequest
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "请求体为空")
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	req := reach.Request{Port: in.Port, Protocol: "tcp", Label: strDeref(in.Label)}
	if in.Protocol != nil {
		req.Protocol = string(*in.Protocol)
	}

	plan, err := provider.Plan(r.Context(), req)
	if err != nil {
		// 计划失败通常是用户能自己处理的问题（端口不合法、后端未实现），
		// 因此用 400 并把原因原样带上 —— 那是他唯一能据此行动的线索。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeUnsupported, "reach.plan_failed", err.Error())
		return
	}

	preview, err := s.Changes.Prepare(r.Context(), plan)
	if err != nil {
		s.internalError(w, r, "登记变更计划失败", err)
		return
	}

	s.auditSuccess(r, audit.ActionChangePlan, preview.ID,
		safeLabel(req))

	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenPreview(preview))
}

// ListPendingChanges 实现 GET /v1/changes/pending。
func (s *Server) ListPendingChanges(w http.ResponseWriter, _ *http.Request) {
	previews := s.Changes.PendingList()

	resp := gen.ChangePreviewList{Items: make([]gen.ChangePreview, 0, len(previews))}
	for _, p := range previews {
		resp.Items = append(resp.Items, toGenPreview(p))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// ApplyChange 实现 POST /v1/changes/{planId}/apply。
//
// 计划在取出时即被消费，因此重复调用会得到 404 ——
// 用户双击"应用"不会执行两次。
func (s *Server) ApplyChange(w http.ResponseWriter, r *http.Request, planId gen.PlanId) {
	id := string(planId)

	res, err := s.Changes.ApplyPending(r.Context(), id)
	if err != nil {
		if errors.Is(err, change.ErrPlanExpired) {
			writeProblem(w, r, s.Log, http.StatusNotFound,
				CodeNotFound, "change.plan_expired", id)
			return
		}
		// 执行失败不是"程序出错"，而是**业务结果**：变更没做成，
		// 且内核已经尽力回滚过。记录已经落库，这里返回它的最终状态。
		s.Log.Warn("变更执行失败", "plan", id, "err", err,
			"failed_step", res.FailedStep, "rollback_err", res.RollbackErr)

		rec, found, getErr := s.Changes.Get(r.Context(), id)
		if getErr != nil || !found {
			s.internalError(w, r, "读取变更结果失败", err)
			return
		}
		s.auditFailure(r, audit.ActionChangeApply, id, err)
		writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeRecord(rec))
		return
	}

	rec, found, getErr := s.Changes.Get(r.Context(), id)
	if getErr != nil || !found {
		s.internalError(w, r, "读取变更结果失败", getErr)
		return
	}

	s.auditSuccess(r, audit.ActionChangeApply, id, rec.Title)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeRecord(rec))
}

// RollbackChange 实现 POST /v1/changes/{planId}/rollback。
func (s *Server) RollbackChange(w http.ResponseWriter, r *http.Request, planId gen.PlanId) {
	id := string(planId)

	res, err := s.Changes.Rollback(r.Context(), id)
	switch {
	case err == nil:
		// 成功，继续读记录返回。

	case errors.Is(err, change.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "change.not_found", id)
		return

	default:
		// 执行中的变更不能撤销：那个进程可能还在写系统状态，
		// 两边同时动手会得到谁也没预料到的结果。用 409 而不是 500。
		if res.Status == change.StatusRollbackFailed {
			rec, found, getErr := s.Changes.Get(r.Context(), id)
			if getErr == nil && found {
				s.auditFailure(r, audit.ActionChangeRollback, id, err)
				writeJSON(w, s.Log, http.StatusOK, "application/json",
					toGenChangeRecord(rec))
				return
			}
		}
		s.auditFailure(r, audit.ActionChangeRollback, id, err)
		writeProblem(w, r, s.Log, http.StatusConflict,
			CodeConflict, "change.rollback_failed", err.Error())
		return
	}

	rec, found, getErr := s.Changes.Get(r.Context(), id)
	if getErr != nil || !found {
		s.internalError(w, r, "读取撤销结果失败", getErr)
		return
	}

	s.auditSuccess(r, audit.ActionChangeRollback, id, rec.Title)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeRecord(rec))
}

// ---------------------------------------------------------------------------
// 映射
// ---------------------------------------------------------------------------

func toGenPreview(p change.Preview) gen.ChangePreview {
	out := gen.ChangePreview{
		Id:        p.ID,
		Kind:      p.Kind,
		Title:     p.Title,
		Risk:      gen.ChangePreviewRisk(p.Risk),
		Diff:      toGenDiffLines(p.Diff),
		Steps:     make([]gen.ChangeStepPreview, 0, len(p.Steps)),
		Empty:     p.Empty,
		CreatedAt: p.CreatedAt,
		ExpiresAt: p.ExpiresAt,
	}
	if len(p.Warnings) > 0 {
		out.Warnings = &p.Warnings
	}
	if len(p.Notes) > 0 {
		out.Notes = &p.Notes
	}
	for _, s := range p.Steps {
		step := gen.ChangeStepPreview{
			Id:         s.ID,
			Title:      s.Title,
			Revertable: s.Revertable,
			Diff:       ptrDiffLines(toGenDiffLines(s.Diff)),
		}
		if s.Details != "" {
			step.Details = &s.Details
		}
		out.Steps = append(out.Steps, step)
	}
	return out
}

func toGenDiffLines(lines []change.DiffLine) []gen.ChangeDiffLine {
	out := make([]gen.ChangeDiffLine, 0, len(lines))
	for _, d := range lines {
		out = append(out, gen.ChangeDiffLine{
			Op:   gen.ChangeDiffLineOp(d.Op),
			Text: d.Text,
		})
	}
	return out
}

// ptrDiffLines 在切片为空时返回 nil，配合 omitempty 语义。
func ptrDiffLines(v []gen.ChangeDiffLine) *[]gen.ChangeDiffLine {
	if len(v) == 0 {
		return nil
	}
	return &v
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// safeLabel 生成审计用的标签。
//
// 用规则名的安全形式而不是用户输入的原样：审计表往往比业务表被更多人
// 看到，而用户输入可能含有换行之类会破坏日志可读性的字符。
func safeLabel(req reach.Request) string {
	return reach.RuleLabel(req.Label, req.Protocol, req.Port)
}
