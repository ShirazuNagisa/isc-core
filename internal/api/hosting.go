package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 本文件实现建站侧的三件事：预设目录、只读源码识别、运行时供给。
//
// 三者的共同点是"都要如实告诉用户会发生什么"：会选哪个预设、依据是什么、
// 要下载多少字节。含糊其辞会让用户在一个耗时数分钟的操作上失去判断依据。

// JobKindRuntimeProvision 是运行时供给的任务类型。
//
// 它必须在 daemon 装配时登记（Engine.RegisterKinds），否则会被 Submit 拒绝。
const JobKindRuntimeProvision = "runtime.provision"

// ListPresets 实现 GET /v1/presets。
func (s *Server) ListPresets(w http.ResponseWriter, r *http.Request) {
	cat := i18n.FromContext(r.Context())
	items := make([]gen.Preset, 0, len(presets.WebsitePresets()))
	for _, preset := range presets.WebsitePresets() {
		items = append(items, toGenPreset(preset, cat))
	}
	// 自定义服务器不是目录里的一项（它的命令由用户提供），但界面需要
	// 一个统一的"选项"概念，因此一并返回。
	items = append(items, toGenPreset(presets.CustomPreset(), cat))
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PresetCatalog{Items: items})
}

func toGenPreset(preset presets.Preset, cat *i18n.Catalog) gen.Preset {
	item := gen.Preset{
		Id:               preset.ID,
		Version:          preset.Version,
		Title:            preset.Title,
		Kind:             gen.PresetKind(preset.Kind),
		DefaultPort:      preset.DefaultPort,
		DockerOnly:       &preset.DockerOnly,
		DetectorFiles:    &preset.DetectorFiles,
		DetectorSuffixes: &preset.DetectorSuffixes,
	}
	if preset.MinVersion != "" {
		item.MinVersion = &preset.MinVersion
	}
	if preset.NoteKey != "" {
		note := cat.T(preset.NoteKey)
		item.Note = &note
	}
	return item
}

// InspectSource 实现 POST /v1/sources/inspect。
func (s *Server) InspectSource(w http.ResponseWriter, r *http.Request) {
	var req gen.SourceInspectRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil || req.Path == "" {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "a source path is required")
		return
	}

	result, err := presets.Inspect(req.Path)
	if err != nil {
		// 路径不存在/不是目录是用户的输入问题，不是内核故障。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	cat := i18n.FromContext(r.Context())
	evidence := make([]gen.SourceEvidence, 0, len(result.Evidence))
	for _, item := range result.Evidence {
		evidence = append(evidence, gen.SourceEvidence{
			File: item.File, Signal: item.Signal, Confidence: float32(item.Confidence),
		})
	}
	candidates := make([]gen.Preset, 0, len(result.Candidates))
	for _, preset := range result.Candidates {
		candidates = append(candidates, toGenPreset(preset, cat))
	}
	warnings := presets.MessagesIn(result.Warnings, cat)
	if warnings == nil {
		warnings = []string{}
	}

	s.auditSuccess(r, audit.ActionSourceInspect, result.Root, "")
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.SourceInspection{
		Root:                result.Root,
		Evidence:            evidence,
		Candidates:          candidates,
		RecommendedPresetId: result.Recommended,
		Warnings:            &warnings,
	})
}

// ListRuntimes 实现 GET /v1/runtimes。
func (s *Server) ListRuntimes(w http.ResponseWriter, r *http.Request) {
	if s.Runtimes == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RuntimeList{Items: []gen.RuntimeInfo{}})
		return
	}
	found, err := s.Runtimes.Inventory(r.Context())
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "failed to list runtimes")
		return
	}
	items := make([]gen.RuntimeInfo, 0, len(found))
	for _, item := range found {
		items = append(items, toGenRuntime(item))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.RuntimeList{Items: items})
}

func toGenRuntime(item runtime.Installed) gen.RuntimeInfo {
	info := gen.RuntimeInfo{
		Kind:       string(item.Kind),
		Version:    item.Version,
		Source:     gen.RuntimeInfoSource(item.Source),
		Executable: &item.Executable,
	}
	if item.Root != "" {
		root := item.Root
		info.Path = &root
	}
	return info
}

// ProvisionRuntimes 实现 POST /v1/runtimes/provision。
func (s *Server) ProvisionRuntimes(w http.ResponseWriter, r *http.Request) {
	var req gen.RuntimeProvisionRequest
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Kinds) == 0 {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "at least one runtime kind is required")
		return
	}
	if s.Runtimes == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "runtime provisioning is unavailable")
		return
	}
	if s.Jobs == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "the job engine is unavailable")
		return
	}

	kinds := make([]runtime.Kind, 0, len(req.Kinds))
	for _, raw := range req.Kinds {
		kind := runtime.Kind(raw)
		if kind == runtime.KindNone {
			continue
		}
		if !kind.Provisionable() {
			// Docker 与未知类型都在这里挡掉：让用户看到"这个不归内核装"，
			// 而不是提交一个注定失败的任务。
			writeProblem(w, r, s.Log, http.StatusBadRequest,
				CodeInvalidRequest, "error.invalid_request",
				"runtime "+raw+" cannot be provisioned by the kernel")
			return
		}
		kinds = append(kinds, kind)
	}
	if len(kinds) == 0 {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "no provisionable runtime was requested")
		return
	}

	minVersions := map[string]string{}
	if req.MinVersions != nil {
		for kind, version := range *req.MinVersions {
			minVersions[kind] = version
		}
	}

	manager := s.Runtimes
	job, err := s.Jobs.Submit(r.Context(), JobKindRuntimeProvision, func(ctx context.Context, reporter job.Reporter) (any, error) {
		ready := make([]gen.RuntimeInfo, 0, len(kinds))
		for index, kind := range kinds {
			base := float64(index) / float64(len(kinds))
			span := 1.0 / float64(len(kinds))
			installed, err := manager.Provision(ctx, kind, minVersions[string(kind)], func(fraction float64, message string) {
				reporter.Progress(base+fraction*span, message)
			})
			if err != nil {
				return nil, err
			}
			ready = append(ready, toGenRuntime(installed))
		}
		return gen.RuntimeList{Items: ready}, nil
	})
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}
	s.auditSuccess(r, audit.ActionRuntimeProvision, "runtimes", "")
	writeJSON(w, s.Log, http.StatusAccepted, "application/json", gen.JobAccepted{JobId: job.ID})
}

// RemoveRuntime 实现 DELETE /v1/runtimes/{kind}。
func (s *Server) RemoveRuntime(w http.ResponseWriter, r *http.Request, kind string) {
	if s.Runtimes == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "runtime provisioning is unavailable")
		return
	}
	removed, err := s.Runtimes.Remove(runtime.Kind(kind))
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "failed to remove the runtime")
		return
	}
	if removed {
		s.auditSuccess(r, audit.ActionRuntimeRemove, kind, "")
	}
	// 本来就没有托管的该运行时也算成功：DELETE 的语义是"让它不存在"。
	w.WriteHeader(http.StatusNoContent)
}
