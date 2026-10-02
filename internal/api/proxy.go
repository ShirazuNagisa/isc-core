package api

import (
	"encoding/json"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/proxy"
)

// 本文件实现反向代理的接口。
//
// 路由用**整体替换**而不是增删改：规则表必须始终自洽（同一个域名不能
// 同时指向两个上游），而增量修改会让"检查冲突"变成一件需要跨多次调用
// 才能完成的事 —— 中间任何一个时刻的状态都可能有冲突，而那期间进来的
// 请求会打到哪一条是不确定的。

// GetProxyStatus 实现 GET /v1/proxy/status。
func (s *Server) GetProxyStatus(w http.ResponseWriter, _ *http.Request) {
	st := s.Proxy.Status()

	resp := gen.ProxyStatus{
		Running: st.Running,
		Port:    st.Port,
		Routes:  st.Routes,
	}
	if st.Error != "" {
		resp.Error = &st.Error
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", resp)
}

// ListProxyRoutes 实现 GET /v1/proxy/routes。
func (s *Server) ListProxyRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.ProxyRoutes.List(r.Context())
	if err != nil {
		s.internalError(w, r, i18n.T("api.proxy.read_failed"), err)
		return
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenProxyRouteList(routes))
}

// ReplaceProxyRoutes 实现 PUT /v1/proxy/routes。
func (s *Server) ReplaceProxyRoutes(w http.ResponseWriter, r *http.Request) {
	var in gen.ProxyRouteList
	if r.Body == nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.T("api.empty_body"))
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}

	routes := make([]proxy.Route, 0, len(in.Items))
	for _, item := range in.Items {
		route := proxy.Route{
			ID:       item.Id,
			Hosts:    item.Domains,
			Upstream: item.Upstream,
		}
		if item.Label != nil {
			route.Label = *item.Label
		}
		if item.Tls != nil {
			route.TLS = *item.Tls
		}
		routes = append(routes, route)
	}

	// SaveRoutes 会先校验再落库，成功后立即生效。
	//
	// 失败时把原因原样返回：那些原因（上游不是本机地址、域名冲突）
	// 恰恰是用户能据此行动的信息。
	if err := s.Proxy.SaveRoutes(r.Context(), routes); err != nil {
		s.auditFailure(r, audit.ActionProxyRoutes, "routes", err)
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "proxy.invalid_routes", err.Error())
		return
	}

	s.auditSuccess(r, audit.ActionProxyRoutes, "routes", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json", toGenProxyRouteList(routes))
}

func toGenProxyRouteList(routes []proxy.Route) gen.ProxyRouteList {
	resp := gen.ProxyRouteList{Items: make([]gen.ProxyRoute, 0, len(routes))}
	for _, r := range routes {
		item := gen.ProxyRoute{
			Id:       r.ID,
			Domains:  r.Hosts,
			Upstream: r.Upstream,
		}
		if r.Label != "" {
			item.Label = &r.Label
		}
		if r.TLS {
			item.Tls = &r.TLS
		}
		resp.Items = append(resp.Items, item)
	}
	return resp
}
