package api

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/acme"
	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
)

// 本文件实现证书的查询与手动续期。
//
// 自动续期在守护进程里常驻运行；这两个端点是给用户**主动介入**用的：
// 刚加了一条 HTTPS 路由想立刻拿到证书，或者想看看到底哪张证书快过期了。

// ListCerts 实现 GET /v1/certs。
func (s *Server) ListCerts(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.Certs.Status(s.certWants(r))
	if err != nil {
		s.internalError(w, r, i18n.T("api.cert.list_failed"), err)
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenCertList(statuses))
}

// RenewCerts 实现 POST /v1/certs/renew。
func (s *Server) RenewCerts(w http.ResponseWriter, r *http.Request) {
	reqs := s.CertRequests()
	if len(reqs) == 0 {
		// 没有 HTTPS 路由时不是错误 —— 用户可能还没配。
		// 但要说清楚，否则他点了"续期"看到空列表会以为坏了。
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "cert.no_tls_routes", "")
		return
	}

	var (
		issued int
		failed []string
	)
	for _, req := range reqs {
		if r.Context().Err() != nil {
			break
		}

		// Ensure 内部会先判断"现有证书够不够用"，因此这里是幂等的 ——
		// 已经有效的证书不会被重新签发（那会白白消耗 ACME 的配额）。
		if _, ok, err := s.Certs.Ensure(r.Context(), req); err != nil {
			s.Log.Error(i18n.T("api.cert.issue_failed"), "domains", req.Domains, "err", err)
			failed = append(failed, req.Domains[0])
			continue
		} else if ok {
			issued++
		}
	}

	if s.CertInvalidate != nil && issued > 0 {
		// 新证书已经写进磁盘，而代理的证书缓存里还是旧的 ——
		// 不清的话用户会看到"续期成功了但浏览器仍然报证书过期"。
		s.CertInvalidate()
	}

	statuses, err := s.Certs.Status(s.certWants(r))
	if err != nil {
		s.internalError(w, r, i18n.T("api.cert.list_failed"), err)
		return
	}

	s.auditSuccess(r, audit.ActionCertRenew, "certs",
		describeRenew(issued, failed))
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenCertList(statuses))
}

// certWants 构造"证书名 → 期望覆盖的域名"映射。
//
// 它让 Status 能判断"现有证书是否还够用" —— 用户加了一个域名之后，
// 现有证书不覆盖它，而**这与剩余有效期无关**。少了这份映射，
// 界面上会显示"不需要续期"，而那个域名永远拿不到证书。
func (s *Server) certWants(r *http.Request) map[string][]string {
	reqs := s.CertRequests()
	if len(reqs) == 0 {
		return nil
	}

	out := make(map[string][]string, len(reqs))
	for _, req := range reqs {
		out[acme.CertName(req.Domains)] = req.Domains
	}
	return out
}

func describeRenew(issued int, failed []string) string {
	if len(failed) == 0 {
		if issued == 0 {
			return i18n.T("api.cert.none_needed")
		}
		return fmt.Sprintf(i18n.T("api.cert.issued"), issued)
	}
	return i18n.T("api.cert.issued_partial") + strings.Join(failed, "、")
}

func toGenCertList(statuses []acme.CertStatus) gen.CertList {
	resp := gen.CertList{Items: make([]gen.CertStatus, 0, len(statuses))}
	for _, st := range statuses {
		item := gen.CertStatus{
			Name:       st.Name,
			NeedsRenew: st.NeedsRenew,
		}
		if len(st.Domains) > 0 {
			item.Domains = &st.Domains
		}
		if !st.IssuedAt.IsZero() {
			item.IssuedAt = &st.IssuedAt
		}
		if !st.ExpiresAt.IsZero() {
			item.ExpiresAt = &st.ExpiresAt
		}
		if st.Staging {
			item.Staging = &st.Staging
		}
		if st.Reason != "" {
			item.Reason = &st.Reason
		}
		if st.Error != "" {
			item.Error = &st.Error
		}
		resp.Items = append(resp.Items, item)
	}
	return resp
}
