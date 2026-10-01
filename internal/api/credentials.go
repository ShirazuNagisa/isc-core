package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/credential"
)

// ListCredentials 实现 GET /v1/credentials。
func (s *Server) ListCredentials(w http.ResponseWriter, r *http.Request, params gen.ListCredentialsParams) {
	var cursor string
	if params.Cursor != nil {
		cursor = string(*params.Cursor)
	}
	limit := 0
	if params.Limit != nil {
		limit = int(*params.Limit)
	}

	items, next, err := s.Credentials.List(r.Context(), cursor, limit)
	if err != nil {
		s.internalError(w, r, "查询凭据列表失败", err)
		return
	}

	resp := gen.CredentialList{Items: make([]gen.Credential, 0, len(items))}
	for _, c := range items {
		specs, _ := s.Credentials.SpecsFor(c.Provider)
		resp.Items = append(resp.Items, toGenCredential(c, specs))
	}
	if next != "" {
		resp.NextCursor = &next
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// GetCredential 实现 GET /v1/credentials/{id}。
func (s *Server) GetCredential(w http.ResponseWriter, r *http.Request, id gen.CredentialId) {
	c, err := s.Credentials.Get(r.Context(), string(id))
	if err != nil {
		s.credentialError(w, r, err, string(id))
		return
	}
	specs, _ := s.Credentials.SpecsFor(c.Provider)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenCredential(c, specs))
}

// CreateCredential 实现 POST /v1/credentials。
func (s *Server) CreateCredential(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeCredentialInput(w, r, s)
	if !ok {
		return
	}

	c, err := s.Credentials.Create(r.Context(), credential.Credential{
		Provider: body.Provider,
		Label:    body.Label,
		Fields:   body.Fields,
	})
	if err != nil {
		s.auditFailure(r, audit.ActionCredentialCreate, body.Provider+"/"+body.Label, err)
		s.credentialError(w, r, err, body.Label)
		return
	}

	s.auditSuccess(r, audit.ActionCredentialCreate, c.ID, c.Provider+"/"+c.Label)
	specs, _ := s.Credentials.SpecsFor(c.Provider)
	writeJSON(w, s.Log, http.StatusCreated, "application/json", toGenCredential(c, specs))
}

// UpdateCredential 实现 PATCH /v1/credentials/{id}。
func (s *Server) UpdateCredential(w http.ResponseWriter, r *http.Request, id gen.CredentialId) {
	body, ok := decodeCredentialInput(w, r, s)
	if !ok {
		return
	}

	c, err := s.Credentials.Update(r.Context(), string(id), credential.Credential{
		Provider: body.Provider,
		Label:    body.Label,
		Fields:   body.Fields,
	})
	if err != nil {
		s.auditFailure(r, audit.ActionCredentialUpdate, string(id), err)
		s.credentialError(w, r, err, string(id))
		return
	}

	s.auditSuccess(r, audit.ActionCredentialUpdate, c.ID, c.Provider+"/"+c.Label)
	specs, _ := s.Credentials.SpecsFor(c.Provider)
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenCredential(c, specs))
}

// DeleteCredential 实现 DELETE /v1/credentials/{id}。
func (s *Server) DeleteCredential(w http.ResponseWriter, r *http.Request, id gen.CredentialId) {
	err := s.Credentials.Delete(r.Context(), string(id))
	if err != nil {
		s.auditFailure(r, audit.ActionCredentialDelete, string(id), err)
		s.credentialError(w, r, err, string(id))
		return
	}
	s.auditSuccess(r, audit.ActionCredentialDelete, string(id), "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// credentialInput 是请求体。
//
// 不用生成的 gen.CredentialInput 直接解码：那个类型把 fields 声明为
// map[string]string，而我们需要区分"字段缺失"与"字段为空对象"——
// 前者是"不改字段"，后者是"清空全部字段"？其实二者语义相同，
// 因此这里用生成类型即可。保留这个包装只是为了让可选字段的默认值
// 逻辑集中在一处。
type credentialInput struct {
	Provider string            `json:"provider"`
	Label    string            `json:"label"`
	Fields   map[string]string `json:"fields"`
}

func decodeCredentialInput(w http.ResponseWriter, r *http.Request, s *Server) (credentialInput, bool) {
	var in credentialInput
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "请求体为空")
		return in, false
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return in, false
	}
	if in.Fields == nil {
		in.Fields = map[string]string{}
	}
	return in, true
}

// credentialError 把领域错误翻译成 HTTP 响应。
//
// 集中在一处而不是散在各 handler 里：状态码与错误码的映射是契约的一部分，
// 分散实现迟早会出现"同一个错误在不同端点返回不同状态码"。
func (s *Server) credentialError(w http.ResponseWriter, r *http.Request, err error, target string) {
	switch {
	case errors.Is(err, credential.ErrNotFound):
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "credential.not_found", target)
	case errors.Is(err, credential.ErrDuplicateLabel):
		writeProblem(w, r, s.Log, http.StatusConflict,
			CodeConflict, "credential.duplicate", target)
	case errors.Is(err, credential.ErrUnknownProvider),
		errors.Is(err, credential.ErrLabelEmpty),
		errors.Is(err, credential.ErrProviderEmpty),
		errors.Is(err, credential.ErrMissingField),
		errors.Is(err, credential.ErrUnknownField),
		errors.Is(err, credential.ErrProviderImmutable):
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
	default:
		s.internalError(w, r, "凭据操作失败", err)
	}
}

// internalError 记录并返回 500。
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.Log.Error(what, "err", err, "path", r.URL.Path, "request_id", RequestIDFrom(r.Context()))
	writeProblem(w, r, s.Log, http.StatusInternalServerError,
		CodeInternal, "error.internal", "")
}

// auditSuccess 记一条成功审计。
func (s *Server) auditSuccess(r *http.Request, action, target, detail string) {
	if s.Audit == nil {
		return
	}
	s.Audit.Success(r.Context(), action, target, detail,
		RequestIDFrom(r.Context()), remoteOf(r))
}

// auditFailure 记一条失败审计。
func (s *Server) auditFailure(r *http.Request, action, target string, err error) {
	if s.Audit == nil {
		return
	}
	// 只记错误文本，不记请求体 —— 请求体里有明文密钥。
	s.Audit.Failure(r.Context(), action, target, err.Error(),
		RequestIDFrom(r.Context()), remoteOf(r))
}

// remoteOf 描述请求来源。
//
// 刻意不记 IP：本机管理通道下所有请求都来自回环，记一个
// 127.0.0.1 不提供任何信息，反而会让人误以为"这是远程来的"。
// 记录传输类型才有意义 —— 它能区分"命令行工具"与"浏览器页面"。
func remoteOf(r *http.Request) string {
	if r.Header.Get("Origin") != "" {
		return "browser"
	}
	if r.Header.Get("User-Agent") != "" {
		return "http-client"
	}
	return "local"
}
