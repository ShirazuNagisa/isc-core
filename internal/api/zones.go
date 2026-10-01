package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件实现 DNS 区域与记录的接口。
//
// 这些端点只对 Tier-1 服务商可用 —— 也就是支持完整记录 CRUD 的那几家。
// Tier-2 服务商只提供动态解析，对它们调用会得到一条明确的
// "该服务商不支持此操作"而不是空列表。空列表会让用户以为
// "我这个账号下没有域名"，那是完全不同的结论。

// ListZones 实现 GET /v1/credentials/{id}/zones。
func (s *Server) ListZones(w http.ResponseWriter, r *http.Request, id gen.CredentialId) {
	zones, err := s.DNS.ListZones(r.Context(), string(id))
	if err != nil {
		s.dnsError(w, r, err, "列出区域")
		return
	}

	resp := gen.ZoneList{Items: make([]gen.Zone, 0, len(zones))}
	for _, z := range zones {
		resp.Items = append(resp.Items, gen.Zone{Id: z.ID, Name: z.Name})
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// ListRecords 实现 GET /v1/credentials/{id}/zones/{zoneId}/records。
func (s *Server) ListRecords(w http.ResponseWriter, r *http.Request,
	id gen.CredentialId, zoneId gen.ZoneId, params gen.ListRecordsParams) {

	filter := dns.RecordFilter{}
	if params.Type != nil {
		filter.Type = dns.RecordType(*params.Type)
	}
	if params.Name != nil {
		filter.Name = *params.Name
	}

	records, err := s.DNS.ListRecords(r.Context(), string(id), string(zoneId), filter)
	if err != nil {
		s.dnsError(w, r, err, "列出记录")
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRecordList(records))
}

// GetRecord 实现 GET /v1/credentials/{id}/zones/{zoneId}/records/{recordId}。
func (s *Server) GetRecord(w http.ResponseWriter, r *http.Request,
	id gen.CredentialId, zoneId gen.ZoneId, recordId gen.RecordId) {

	rec, err := s.DNS.GetRecord(r.Context(), string(id), string(zoneId), string(recordId))
	if err != nil {
		s.dnsError(w, r, err, "读取记录")
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRecord(rec))
}

// CreateRecord 实现 POST /v1/credentials/{id}/zones/{zoneId}/records。
func (s *Server) CreateRecord(w http.ResponseWriter, r *http.Request,
	id gen.CredentialId, zoneId gen.ZoneId) {

	in, ok := decodeRecordInput(w, r, s)
	if !ok {
		return
	}

	rec, err := s.DNS.CreateRecord(r.Context(), string(id), string(zoneId), in)
	if err != nil {
		s.auditFailure(r, audit.ActionRecordCreate, rec.Name, err)
		s.dnsError(w, r, err, "新增记录")
		return
	}

	s.auditSuccess(r, audit.ActionRecordCreate, rec.ID,
		fmtRecord(rec))
	writeJSON(w, s.Log, http.StatusCreated, "application/json", toGenRecord(rec))
}

// UpdateRecord 实现 PUT /v1/credentials/{id}/zones/{zoneId}/records/{recordId}。
func (s *Server) UpdateRecord(w http.ResponseWriter, r *http.Request,
	id gen.CredentialId, zoneId gen.ZoneId, recordId gen.RecordId) {

	in, ok := decodeRecordInput(w, r, s)
	if !ok {
		return
	}

	rec, err := s.DNS.UpdateRecord(r.Context(), string(id), string(zoneId), string(recordId), in)
	if err != nil {
		s.auditFailure(r, audit.ActionRecordUpdate, string(recordId), err)
		s.dnsError(w, r, err, "修改记录")
		return
	}

	s.auditSuccess(r, audit.ActionRecordUpdate, rec.ID, fmtRecord(rec))
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenRecord(rec))
}

// DeleteRecord 实现 DELETE /v1/credentials/{id}/zones/{zoneId}/records/{recordId}。
func (s *Server) DeleteRecord(w http.ResponseWriter, r *http.Request,
	id gen.CredentialId, zoneId gen.ZoneId, recordId gen.RecordId) {

	err := s.DNS.DeleteRecord(r.Context(), string(id), string(zoneId), string(recordId))
	if err != nil {
		s.auditFailure(r, audit.ActionRecordDelete, string(recordId), err)
		s.dnsError(w, r, err, "删除记录")
		return
	}

	s.auditSuccess(r, audit.ActionRecordDelete, string(recordId), "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

type recordInput struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Content  string  `json:"content"`
	TTL      *int    `json:"ttl"`
	Proxied  *bool   `json:"proxied"`
	Comment  *string `json:"comment"`
	Priority *int    `json:"priority"`
}

func decodeRecordInput(w http.ResponseWriter, r *http.Request, s *Server) (dns.Record, bool) {
	var in recordInput
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "请求体为空")
		return dns.Record{}, false
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return dns.Record{}, false
	}

	rec := dns.Record{
		Name:    in.Name,
		Type:    dns.RecordType(in.Type),
		Content: in.Content,
	}
	if in.TTL != nil {
		rec.TTL = *in.TTL
	}
	if in.Proxied != nil {
		rec.Proxied = *in.Proxied
	}
	if in.Comment != nil {
		rec.Comment = *in.Comment
	}
	if in.Priority != nil {
		rec.Priority = *in.Priority
	}
	return rec, true
}

func toGenRecord(r dns.Record) gen.Record {
	out := gen.Record{
		Id:      r.ID,
		Name:    r.Name,
		Type:    string(r.Type),
		Content: r.Content,
		Ttl:     &r.TTL,
		Proxied: &r.Proxied,
	}
	if r.Comment != "" {
		out.Comment = &r.Comment
	}
	if r.Priority != 0 {
		out.Priority = &r.Priority
	}
	return out
}

func toGenRecordList(records []dns.Record) gen.RecordList {
	resp := gen.RecordList{Items: make([]gen.Record, 0, len(records))}
	for _, r := range records {
		resp.Items = append(resp.Items, toGenRecord(r))
	}
	return resp
}

// fmtRecord 生成审计用的记录描述。
//
// **不含记录内容**：TXT 记录里可能有验证令牌、CAA 里可能有账号信息。
// 审计表往往比业务表被更多人看到，把内容写进去是常见的疏忽。
func fmtRecord(r dns.Record) string {
	return r.Name + " " + string(r.Type)
}

// dnsError 把 DNS 领域错误翻译成 HTTP 响应。
func (s *Server) dnsError(w http.ResponseWriter, r *http.Request, err error, op string) {
	var unsupported *dns.ErrUnsupported
	switch {
	case errors.As(err, &unsupported):
		// 400 而不是 500：这是"这家服务商不支持"，属于请求本身的问题，
		// 而不是内核出错。消息里点名是哪个操作、哪家服务商 ——
		// 用户据此才知道该换服务商还是换个做法。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeUnsupported, "dns.unsupported", unsupported.Error())
	case errors.Is(err, dns.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "dns.record_not_found", "")
	case errors.Is(err, credential.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "credential.not_found", "")
	default:
		s.Log.Error("DNS 操作失败", "op", op, "err", err,
			"path", r.URL.Path, "request_id", RequestIDFrom(r.Context()))

		// 服务商返回的错误要原样带给用户。
		//
		// 那些错误（"记录已存在""域名不在该账号下""令牌权限不足"）
		// 恰恰是用户能据此行动的信息，而一个笼统的"操作失败"
		// 会让他们只能去猜。这里用 400 而不是 500：绝大多数情况是
		// 请求本身有问题，而不是内核出错。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeUpstreamError, "dns.upstream_error", err.Error())
	}
}
