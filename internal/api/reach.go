package api

import (
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/change"
	"github.com/ShirazuNagisa/isc-core/internal/reach"
)

// 本文件实现可达性与系统变更记录的接口。
//
// 这两块放在一起是有原因的：可达性探测（doctor）回答"现在哪一环断了"，
// 变更记录回答"我们改过什么、能不能退回去"。用户排查问题时
// 几乎总是交替看这两样。

// ListReachProviders 实现 GET /v1/reach/providers。
func (s *Server) ListReachProviders(w http.ResponseWriter, _ *http.Request) {
	providers := s.Reach.List()

	resp := gen.ReachProviderList{Items: make([]gen.ReachProvider, 0, len(providers))}
	for _, p := range providers {
		m := p.Meta()
		resp.Items = append(resp.Items, gen.ReachProvider{
			Name:                m.Name,
			DisplayName:         m.DisplayName,
			Description:         m.Description,
			NeedsExternalServer: m.NeedsExternalServer,
			Tier:                m.Tier,
		})
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// ProbeReachProvider 实现 GET /v1/reach/providers/{name}/probe。
//
// 这是 `isc doctor` 的后端。它是**只读**的 —— 用户点"检查"时
// 不该有任何系统状态被改动，否则他下次就不敢点了。
func (s *Server) ProbeReachProvider(w http.ResponseWriter, r *http.Request,
	name gen.ReachProviderName) {

	p, ok := s.Reach.Get(string(name))
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "reach.provider_not_found", string(name))
		return
	}

	readiness, err := p.Probe(r.Context())
	if err != nil {
		s.Log.Error(i18n.T("api.reach.probe_failed"), "provider", name, "err", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenReadiness(readiness))
}

func toGenReadiness(rd reach.Readiness) gen.ReachReadiness {
	resp := gen.ReachReadiness{
		Viable:  rd.Viable,
		Summary: rd.Summary,
		Checks:  make([]gen.ReachCheck, 0, len(rd.Checks)),
	}
	for _, c := range rd.Checks {
		item := gen.ReachCheck{
			Name:   c.Name,
			Scope:  gen.ReachCheckScope(c.Scope),
			Status: gen.ReachCheckStatus(c.Status),
		}
		if c.Detail != "" {
			item.Detail = &c.Detail
		}
		if c.Hint != "" {
			item.Hint = &c.Hint
		}
		resp.Checks = append(resp.Checks, item)
	}
	return resp
}

// ---------------------------------------------------------------------------
// 变更记录
// ---------------------------------------------------------------------------

// ListChanges 实现 GET /v1/changes。
func (s *Server) ListChanges(w http.ResponseWriter, r *http.Request,
	params gen.ListChangesParams) {

	limit := 50
	if params.Limit != nil {
		limit = int(*params.Limit)
	}

	records, err := s.Changes.List(r.Context(), limit)
	if err != nil {
		s.internalError(w, r, i18n.T("api.change.query_failed"), err)
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeList(records))
}

// GetChange 实现 GET /v1/changes/{planId}。
func (s *Server) GetChange(w http.ResponseWriter, r *http.Request, planId gen.PlanId) {
	rec, found, err := s.Changes.Get(r.Context(), string(planId))
	if err != nil {
		s.internalError(w, r, i18n.T("api.change.read_failed"), err)
		return
	}
	if !found {
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "change.not_found", string(planId))
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeRecord(rec))
}

// ListInterruptedChanges 实现 GET /v1/changes/interrupted。
//
// 刻意只报告、不自动撤销 —— 理由见 openapi.yaml 里的说明与
// change.Runner.RecoverInterrupted 的注释。
func (s *Server) ListInterruptedChanges(w http.ResponseWriter, r *http.Request) {
	interrupted, err := s.Changes.RecoverInterrupted(r.Context())
	if err != nil {
		s.internalError(w, r, i18n.T("api.change.interrupted_failed"), err)
		return
	}

	records := make([]change.Record, 0, len(interrupted))
	for _, it := range interrupted {
		records = append(records, it.Record)
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenChangeList(records))
}

func toGenChangeList(records []change.Record) gen.ChangeList {
	resp := gen.ChangeList{Items: make([]gen.ChangeRecord, 0, len(records))}
	for _, rec := range records {
		resp.Items = append(resp.Items, toGenChangeRecord(rec))
	}
	return resp
}

func toGenChangeRecord(rec change.Record) gen.ChangeRecord {
	out := gen.ChangeRecord{
		PlanId:    rec.PlanID,
		Kind:      rec.Kind,
		Title:     rec.Title,
		Risk:      gen.ChangeRecordRisk(rec.Risk),
		Status:    gen.ChangeRecordStatus(rec.Status),
		Steps:     make([]gen.ChangeStep, 0, len(rec.Steps)),
		CreatedAt: rec.CreatedAt,
		UpdatedAt: rec.UpdatedAt,
	}
	if len(rec.Warnings) > 0 {
		out.Warnings = &rec.Warnings
	}
	if len(rec.Notes) > 0 {
		out.Notes = &rec.Notes
	}

	for _, s := range rec.Steps {
		step := gen.ChangeStep{
			Id:    s.ID,
			Title: s.Title,
			State: gen.ChangeStepState(s.State),
		}
		if len(s.Diff) > 0 {
			lines := make([]gen.ChangeDiffLine, 0, len(s.Diff))
			for _, d := range s.Diff {
				lines = append(lines, gen.ChangeDiffLine{
					Op:   gen.ChangeDiffLineOp(d.Op),
					Text: d.Text,
				})
			}
			step.Diff = &lines
		}
		if s.Error != "" {
			step.Error = &s.Error
		}
		out.Steps = append(out.Steps, step)
	}
	return out
}

// 保留 errors 引用：变更相关的错误分类在接入 Apply 端点时会用到。
var _ = errors.Is
