package api

import (
	"net/http"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/reachcheck"
)

// GetReachability 实现 GET /v1/reachability。
//
// 返回**最近一轮**的结果，为空时给一个空列表而不是 404：界面据此显示
// "还没有结果"，而那与"接口不存在"要用户做的事完全不同（等一轮 vs
// 升级内核）。
func (s *Server) GetReachability(w http.ResponseWriter, r *http.Request) {
	items := []gen.ReachabilityItem{}
	if s.ReachCheck != nil {
		for _, item := range s.ReachCheck.Latest() {
			items = append(items, toGenReachability(item))
		}
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.ReachabilityList{Items: items})
}

func toGenReachability(r reachcheck.Result) gen.ReachabilityItem {
	out := gen.ReachabilityItem{
		AppId:               r.AppID,
		Name:                r.Name,
		Domain:              r.Domain,
		Ok:                  r.OK,
		Trustworthy:         r.Trustworthy,
		ConsecutiveFailures: r.ConsecutiveFailures,
	}
	if r.StatusCode != 0 {
		out.StatusCode = &r.StatusCode
	}
	if r.LatencyMS != 0 {
		ms := int(r.LatencyMS)
		out.LatencyMs = &ms
	}
	if r.Error != "" {
		out.Error = &r.Error
	}
	if t, err := time.Parse(time.RFC3339, r.CheckedAt); err == nil {
		out.CheckedAt = t
	}
	return out
}
