package api

import (
	"encoding/json"
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"net/netip"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/ddns"
)

// 本文件实现 IP 状态与动态解析任务的接口。

// GetCurrentIP 实现 GET /v1/ip/current。
func (s *Server) GetCurrentIP(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.Platform.IPMonitor.Snapshot(r.Context())
	if err != nil {
		s.internalError(w, r, i18n.T("api.ddns.snapshot_failed"), err)
		return
	}

	resp := gen.IPStatus{Interfaces: make([]gen.InterfaceAddrs, 0, len(snapshot))}
	for _, iface := range snapshot {
		// 回环、虚拟、以及没有任何可用地址的接口都不展示。
		//
		// 最后一条是在真机上发现的：Windows 上会有若干虚拟适配器
		// （Wi-Fi Direct 的"本地连接* N"、断开的蓝牙网络连接等），
		// 它们的名称随系统语言变化，靠名称列表判断必然漏；
		// 而它们只有 169.254.x / fe80:: 这类地址，
		// 列出来只会让用户在配置时选错网卡。
		if iface.IsLoopback || iface.IsVirtual || !iface.HasUsableAddress() {
			continue
		}

		item := gen.InterfaceAddrs{
			Name:       iface.Name,
			Index:      &iface.Index,
			Ipv4:       ptrSlice(addrsToStrings(iface.IPv4)),
			Ipv6:       ptrSlice(addrsToStrings(iface.IPv6)),
			GlobalIpv6: ptrSlice(addrsToStrings(iface.GlobalIPv6())),
			Prefixes:   ptrSlice(prefixesToStrings(iface.Prefixes)),
			IsUp:       &iface.IsUp,
		}
		resp.Interfaces = append(resp.Interfaces, item)

		// 便捷字段取第一个**可用于公网**的地址。
		//
		// 不能简单取第一个：真机上第一个 IPv4 常常是 169.254.x
		// （APIPA 自分配地址），把它当成"当前公网地址"展示会直接误导用户。
		if resp.PrimaryIpv4 == nil {
			if v := firstPublicIPv4(iface.IPv4); v != "" {
				resp.PrimaryIpv4 = &v
			}
		}
		if resp.PrimaryIpv6 == nil {
			if g := iface.GlobalIPv6(); len(g) > 0 {
				v := g[0].String()
				resp.PrimaryIpv6 = &v
			}
		}
		if resp.PrimaryPrefix == nil && len(iface.Prefixes) > 0 {
			v := iface.Prefixes[0].String()
			resp.PrimaryPrefix = &v
		}
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// firstPublicIPv4 返回第一个非链路本地、非私有的 IPv4。
//
// 找不到时返回空串；调用方继续看下一个网卡。
func firstPublicIPv4(addrs []netip.Addr) string {
	for _, a := range addrs {
		if a.IsLinkLocalUnicast() || a.IsLoopback() || a.IsPrivate() {
			continue
		}
		return a.String()
	}
	return ""
}

// ListDdnsTasks 实现 GET /v1/ddns-tasks。
func (s *Server) ListDdnsTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Tasks.List(r.Context())
	if err != nil {
		s.internalError(w, r, i18n.T("api.job.list_failed"), err)
		return
	}

	resp := gen.DdnsTaskList{Items: make([]gen.DdnsTask, 0, len(tasks))}
	for _, t := range tasks {
		resp.Items = append(resp.Items, toGenDdnsTask(t))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// GetDdnsTask 实现 GET /v1/ddns-tasks/{id}。
func (s *Server) GetDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	t, err := s.Tasks.Get(r.Context(), string(id))
	if err != nil {
		s.taskError(w, r, err, string(id))
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenDdnsTask(t))
}

// CreateDdnsTask 实现 POST /v1/ddns-tasks。
func (s *Server) CreateDdnsTask(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeTaskInput(w, r, s)
	if !ok {
		return
	}

	t, err := s.Tasks.Create(r.Context(), in)
	if err != nil {
		s.auditFailure(r, audit.ActionTaskCreate, in.Label, err)
		s.taskError(w, r, err, in.Label)
		return
	}

	s.auditSuccess(r, audit.ActionTaskCreate, t.ID, t.Label)
	writeJSON(w, s.Log, http.StatusCreated, "application/json", toGenDdnsTask(t))
}

// UpdateDdnsTask 实现 PATCH /v1/ddns-tasks/{id}。
func (s *Server) UpdateDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	in, ok := decodeTaskInput(w, r, s)
	if !ok {
		return
	}

	t, err := s.Tasks.Update(r.Context(), string(id), in)
	if err != nil {
		s.auditFailure(r, audit.ActionTaskUpdate, string(id), err)
		s.taskError(w, r, err, string(id))
		return
	}

	s.auditSuccess(r, audit.ActionTaskUpdate, t.ID, t.Label)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenDdnsTask(t))
}

// DeleteDdnsTask 实现 DELETE /v1/ddns-tasks/{id}。
func (s *Server) DeleteDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	err := s.Tasks.Delete(r.Context(), string(id))
	if err != nil {
		s.auditFailure(r, audit.ActionTaskDelete, string(id), err)
		s.taskError(w, r, err, string(id))
		return
	}
	s.auditSuccess(r, audit.ActionTaskDelete, string(id), "")
	w.WriteHeader(http.StatusNoContent)
}

// RunDdnsTask 实现 POST /v1/ddns-tasks/{id}/run。
func (s *Server) RunDdnsTask(w http.ResponseWriter, r *http.Request, id gen.DdnsTaskId) {
	// 先确认任务存在，再受理 —— 否则用户会对一个不存在的任务
	// 收到"已受理"，然后永远等不到结果。
	if _, err := s.Tasks.Get(r.Context(), string(id)); err != nil {
		s.taskError(w, r, err, string(id))
		return
	}

	s.Tasks.RunNow(string(id))

	// 返回一个作业 ID 供客户端轮询？这里没有创建真实作业 ——
	// 执行由调度器在后台完成，其过程与结果通过任务状态与事件流观察。
	// 返回占位 ID 会让客户端去查一个查不到的作业，因此返回任务 ID 本身
	// 并在描述里说明语义。
	writeJSON(w, s.Log, http.StatusAccepted, "application/json",
		gen.JobAccepted{JobId: string(id)})
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// taskInput 是请求体。
type taskInput struct {
	CredentialID  string          `json:"credential_id"`
	Label         string          `json:"label"`
	Enabled       *bool           `json:"enabled"`
	IPv4          *taskSourceJSON `json:"ipv4"`
	IPv6          *taskSourceJSON `json:"ipv6"`
	TTL           *string         `json:"ttl"`
	HTTPInterface *string         `json:"http_interface"`
}

type taskSourceJSON struct {
	Enable   *bool    `json:"enable"`
	GetType  string   `json:"get_type"`
	Value    string   `json:"value"`
	Domains  []string `json:"domains"`
	Selector string   `json:"selector"`
}

func decodeTaskInput(w http.ResponseWriter, r *http.Request, s *Server) (ddns.Task, bool) {
	var in taskInput
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.T("api.empty_body"))
		return ddns.Task{}, false
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return ddns.Task{}, false
	}

	task := ddns.Task{
		CredentialID: in.CredentialID,
		Label:        in.Label,
		// 默认启用：用户点"新建"时的意图几乎总是"让它跑起来"。
		Enabled: in.Enabled == nil || *in.Enabled,
		IPv4:    toDdnsSource(in.IPv4),
		IPv6:    toDdnsSource(in.IPv6),
	}
	if in.TTL != nil {
		task.TTL = *in.TTL
	}
	if in.HTTPInterface != nil {
		task.HTTPInterface = *in.HTTPInterface
	}
	return task, true
}

func toDdnsSource(in *taskSourceJSON) ddns.Source {
	if in == nil {
		return ddns.Source{}
	}
	src := ddns.Source{
		Enable:   in.Enable != nil && *in.Enable,
		GetType:  ddns.GetType(in.GetType),
		Value:    in.Value,
		Domains:  in.Domains,
		Selector: in.Selector,
	}
	return src
}

func toGenDdnsTask(t ddns.Task) gen.DdnsTask {
	status := gen.DdnsStatus(t.LastStatus)
	out := gen.DdnsTask{
		Id:            t.ID,
		CredentialId:  t.CredentialID,
		Label:         t.Label,
		Enabled:       t.Enabled,
		Ipv4:          toGenSource(t.IPv4),
		Ipv6:          toGenSource(t.IPv6),
		Ttl:           strPtr(t.TTL),
		HttpInterface: strPtr(t.HTTPInterface),
		CreatedAt:     t.CreatedAt,
		UpdatedAt:     t.UpdatedAt,
		LastRunAt:     t.LastRunAt,
		LastStatus:    &status,
		LastMessage:   strPtr(t.LastMessage),
		LastIpv4:      strPtr(t.LastIPv4),
		LastIpv6:      strPtr(t.LastIPv6),
	}
	return out
}

func toGenSource(s ddns.Source) gen.DdnsSource {
	getType := gen.DdnsSourceGetType(s.GetType)
	domains := s.Domains
	if domains == nil {
		domains = []string{}
	}
	out := gen.DdnsSource{
		Enable:  s.Enable,
		GetType: getType,
		Value:   s.Value,
		Domains: domains,
	}
	if s.Selector != "" {
		out.Selector = &s.Selector
	}
	return out
}

// taskError 把领域错误翻译成 HTTP 响应。
func (s *Server) taskError(w http.ResponseWriter, r *http.Request, err error, target string) {
	switch {
	case errors.Is(err, ddns.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "ddns.task_not_found", target)
	case errors.Is(err, ddns.ErrLabelEmpty),
		errors.Is(err, ddns.ErrCredentialEmpty),
		errors.Is(err, ddns.ErrNoSource),
		errors.Is(err, ddns.ErrNoDomains),
		errors.Is(err, ddns.ErrBadGetType),
		errors.Is(err, ddns.ErrSourceValueEmpty):
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
	default:
		s.internalError(w, r, i18n.T("api.ddns.op_failed"), err)
	}
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func addrsToStrings(addrs []netip.Addr) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

func prefixesToStrings(prefixes []netip.Prefix) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		out = append(out, p.String())
	}
	return out
}

// ptrSlice 在切片非空时返回指针；空切片返回 nil，配合 omitempty 语义。
func ptrSlice(s []string) *[]string {
	if len(s) == 0 {
		return nil
	}
	return &s
}
