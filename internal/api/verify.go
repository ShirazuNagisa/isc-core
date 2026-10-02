package api

import (
	"encoding/json"
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/verify"
)

// 本文件实现引导式外部验证的接口。
//
// 它是唯一能回答"从外面到底能不能连上"的手段 —— 本机做不到，
// 因为从本机访问自己的公网地址走的是回环或直连，无论运营商是否
// 放行都会"成功"。判断依据只有一处：**来源地址**。

// ListVerifySessions 实现 GET /v1/verify/sessions。
func (s *Server) ListVerifySessions(w http.ResponseWriter, _ *http.Request) {
	sessions := s.Verify.List()

	resp := gen.VerifySessionList{Items: make([]gen.VerifySession, 0, len(sessions))}
	for _, sess := range sessions {
		resp.Items = append(resp.Items, toGenVerifySession(sess))
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// StartVerifySession 实现 POST /v1/verify/sessions。
func (s *Server) StartVerifySession(w http.ResponseWriter, r *http.Request) {
	var in gen.VerifyStartRequest
	if r.Body != nil {
		// 空 body 是合法的：全部字段取默认值，端口由内核分配。
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && err.Error() != "EOF" {
			writeProblem(w, r, s.Log, http.StatusBadRequest,
				CodeInvalidRequest, "error.invalid_request", err.Error())
			return
		}
	}

	req := verify.StartRequest{}
	if in.Port != nil {
		req.Port = *in.Port
	}

	sess, err := s.Verify.Start(r.Context(), req)
	if err != nil {
		// 端口被占用是**用户能自己处理**的问题（换一个端口），
		// 因此用 400 而不是 500，并把原因原样带上。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "verify.start_failed", err.Error())
		return
	}

	writeJSON(w, s.Log, http.StatusCreated, "application/json", toGenVerifySession(sess))
}

// GetVerifySession 实现 GET /v1/verify/sessions/{id}。
func (s *Server) GetVerifySession(w http.ResponseWriter, r *http.Request, id gen.VerifySessionId) {
	sess, ok := s.Verify.Get(string(id))
	if !ok {
		writeProblem(w, r, s.Log, http.StatusNotFound,
			CodeNotFound, "verify.session_not_found", string(id))
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenVerifySession(sess))
}

// StopVerifySession 实现 DELETE /v1/verify/sessions/{id}。
func (s *Server) StopVerifySession(w http.ResponseWriter, r *http.Request, id gen.VerifySessionId) {
	err := s.Verify.Stop(string(id))
	if err != nil {
		if errors.Is(err, verify.ErrNotFound) {
			writeProblem(w, r, s.Log, http.StatusNotFound,
				CodeNotFound, "verify.session_not_found", string(id))
			return
		}
		s.internalError(w, r, i18n.T("api.verify_stop_failed"), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toGenVerifySession(sess verify.Session) gen.VerifySession {
	out := gen.VerifySession{
		Id:        sess.ID,
		Port:      sess.Port,
		Token:     sess.Token,
		TargetIp:  strPtr(sess.TargetIP),
		Url:       strPtr(sess.URL()),
		Status:    gen.VerifySessionStatus(sess.Status),
		Message:   sess.Message,
		CreatedAt: sess.CreatedAt,
		ExpiresAt: sess.ExpiresAt,
		Hits:      make([]gen.VerifyHit, 0, len(sess.Hits)),
	}
	for _, h := range sess.Hits {
		hit := gen.VerifyHit{
			RemoteAddr: h.RemoteAddr,
			Kind:       gen.VerifyHitKind(h.Kind),
			At:         h.At,
		}
		if h.UserAgent != "" {
			hit.UserAgent = &h.UserAgent
		}
		out.Hits = append(out.Hits, hit)
	}
	return out
}
