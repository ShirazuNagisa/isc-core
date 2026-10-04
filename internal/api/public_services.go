package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/google/uuid"
)

// 本文件实现公网服务的接口。
//
// 与代理路由同形：用**整体替换**而不是增删改。集合必须始终自洽（同一个域名
// 不能同时被两个已发布服务占用），而增量修改会让"检查 + 生效"跨越多次调用，
// 中间任何一刻的状态都可能是不一致的。
//
// 记录由内核持有，而不是由界面自己保存一份。这正是 `phecda_deployments` 上
// `public_service_id` 能成立的前提：替换集合的那次事务会一并清空指向已不存在
// 服务的绑定（见 store.PublicServices.Replace），调用方不需要、也无法忘记清理。
//
// 这里**不**校验 ddns_id / route_id 是否存在：用户可以在"动态解析"或"反向代理"
// 页面删掉任务而保留已发布服务，界面把那种情况显示为"配置缺失"—— 那是一个
// 可恢复状态，而不是一次写入失败。

// ListPublicServices 实现 GET /v1/public-services。
func (s *Server) ListPublicServices(w http.ResponseWriter, r *http.Request) {
	if s.Phecda == nil {
		writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PublicServiceList{Items: []gen.PublicService{}})
		return
	}
	services, err := s.Phecda.ListPublicServices(r.Context())
	if err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "failed to list public services")
		return
	}
	// 归一化为空数组：`null` 会让按数组解析的客户端在这里破功。
	if services == nil {
		services = []gen.PublicService{}
	}
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PublicServiceList{Items: services})
}

// ReplacePublicServices 实现 PUT /v1/public-services。
func (s *Server) ReplacePublicServices(w http.ResponseWriter, r *http.Request) {
	var in gen.PublicServiceList
	if r.Body == nil || json.NewDecoder(r.Body).Decode(&in) != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", "invalid public service collection")
		return
	}
	if err := validatePublicServices(in.Items); err != nil {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", err.Error())
		return
	}
	if s.Phecda == nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "public service storage is unavailable")
		return
	}
	if err := s.Phecda.ReplacePublicServices(r.Context(), in.Items); err != nil {
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", "failed to replace public services")
		return
	}
	s.auditSuccess(r, audit.ActionPublicServices, "public-services", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.PublicServiceList{Items: in.Items})
}

// validatePublicServices 拒绝内核无法自洽保存的集合。
//
// 重复的 id 与重复的域名在这里挡掉，而不是等到写库时抛一个约束错误、
// 或者两个服务悄悄争用同一个域名。
func validatePublicServices(items []gen.PublicService) error {
	ids := make(map[string]bool, len(items))
	domains := make(map[string]bool)
	for _, item := range items {
		if item.Id == uuid.Nil {
			return errors.New("a public service is missing its id")
		}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("public service %s is missing its name", item.Id)
		}
		if !item.Kind.Valid() {
			return fmt.Errorf("public service %s has an unknown kind %q", item.Id, item.Kind)
		}
		if ids[item.Id.String()] {
			return fmt.Errorf("duplicate public service id %s", item.Id)
		}
		ids[item.Id.String()] = true
		for _, domain := range item.Domains {
			domain = strings.TrimSpace(domain)
			if domain == "" {
				continue
			}
			if domains[domain] {
				return fmt.Errorf("domain %q is claimed by more than one public service", domain)
			}
			domains[domain] = true
		}
	}
	return nil
}
