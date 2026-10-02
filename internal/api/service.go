package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/audit"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// 本文件实现系统服务的安装与生命周期管理。
//
// # 为什么这些操作要通过接口暴露
//
// 它们需要管理员权限，而"以管理员身份运行"在图形界面上是一件不自然的
// 事 —— 用户已经打开了控制台，不该再去找一个管理员终端。接口返回的
// 权限错误是可读的（"请以管理员身份运行"），比 Windows 的
// "Access is denied" 有用得多。
//
// 需要强调的是：这些操作**不会**绕过权限检查。接口只是把同一个
// 平台调用暴露出来，由操作系统来拒绝。

// GetServiceStatus 实现 GET /v1/service/status。
func (s *Server) GetServiceStatus(w http.ResponseWriter, r *http.Request) {
	if s.Platform == nil {
		s.internalError(w, r, i18n.T("api.provider_missing"), errors.New(i18n.T("api.platform_missing")))
		return
	}

	info := gen.ServiceStatusInfo{
		Backend: s.Platform.ServiceManager.Describe().Backend,
	}

	// 内核可达性 —— 这一项**永远能查**。
	//
	// 它比"服务在不在跑"更接近用户真正想问的问题，而且在没有管理员
	// 权限时仍然有答案。
	info.DaemonReachable = true

	st, err := s.Platform.ServiceManager.Status(r.Context())
	if err != nil {
		// 查询失败**不让整个响应失败**。
		//
		// 上面那一项已经回答了用户最可能想问的问题，而把整个请求
		// 变成错误会让界面上什么都不显示。
		msg := err.Error()
		info.Error = &msg
	} else {
		status := gen.ServiceStatusInfoStatus(st)
		info.Status = &status
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", info)
}

// InstallService 实现 POST /v1/service/install。
func (s *Server) InstallService(w http.ResponseWriter, r *http.Request) {
	if s.Platform == nil {
		s.internalError(w, r, i18n.T("api.provider_missing"), errors.New(i18n.T("api.platform_missing")))
		return
	}

	autoStart, restart := true, true
	if r.Body != nil {
		var in gen.ServiceInstallRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err == nil {
			if in.AutoStart != nil {
				autoStart = *in.AutoStart
			}
			if in.RestartOnFailure != nil {
				restart = *in.RestartOnFailure
			}
		}
		// 解析失败**不报错**：字段都是可选的，一个空 body 或 `{}`
		// 是合法的，用默认值即可。
	}

	exe, err := selfExecutable()
	if err != nil {
		s.auditFailure(r, audit.ActionServiceInstall, "service", err)
		writeProblem(w, r, s.Log, http.StatusInternalServerError,
			CodeInternal, "error.internal", err.Error())
		return
	}

	cfg := platform.ServiceConfig{
		Executable:       exe,
		Arguments:        []string{"daemon", "run"},
		WorkingDirectory: filepath.Dir(exe),
		AutoStart:        autoStart,
		RestartOnFailure: restart,
	}

	if err := s.Platform.ServiceManager.Install(r.Context(), cfg); err != nil {
		s.auditFailure(r, audit.ActionServiceInstall, "service", err)
		// 权限不足是可预期的常见结果，返回 403 而不是 500 ——
		// 客户端据此可以显示"请以管理员身份重试"而不是"服务器出错"。
		status := http.StatusInternalServerError
		if isPermissionError(err) {
			status = http.StatusForbidden
		}
		writeProblem(w, r, s.Log, status,
			CodeInternal, "service.install_failed", err.Error())
		return
	}

	s.auditSuccess(r, audit.ActionServiceInstall, "service", exe)
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		gen.ServiceActionResult{Ok: true, Message: okPtr(i18n.T("api.service.installed"))})
}

// UninstallService 实现 POST /v1/service/uninstall。
func (s *Server) UninstallService(w http.ResponseWriter, r *http.Request) {
	s.runServiceAction(w, r, audit.ActionServiceUninstall,
		i18n.T("api.service.uninstalled"), s.Platform.ServiceManager.Uninstall)
}

// StartService 实现 POST /v1/service/start。
func (s *Server) StartService(w http.ResponseWriter, r *http.Request) {
	s.runServiceAction(w, r, audit.ActionServiceStart,
		i18n.T("api.service.started"), s.Platform.ServiceManager.Start)
}

// StopService 实现 POST /v1/service/stop。
func (s *Server) StopService(w http.ResponseWriter, r *http.Request) {
	s.runServiceAction(w, r, audit.ActionServiceStop,
		i18n.T("api.service.stopped"), s.Platform.ServiceManager.Stop)
}

// runServiceAction 执行一个统一签名的服务操作。
//
// 四个操作（卸载 / 启动 / 停止，以及安装之外的那些）的差异只有
// 调用的方法与提示文本，其余（权限判定、错误映射、审计）完全一致。
func (s *Server) runServiceAction(w http.ResponseWriter, r *http.Request,
	action, successMsg string, fn func(context.Context) error) {

	if s.Platform == nil {
		s.internalError(w, r, i18n.T("api.provider_missing"), errors.New(i18n.T("api.platform_missing")))
		return
	}

	if err := fn(r.Context()); err != nil {
		s.auditFailure(r, action, "service", err)
		status := http.StatusInternalServerError
		if isPermissionError(err) {
			status = http.StatusForbidden
		}
		writeProblem(w, r, s.Log, status,
			CodeInternal, "service.action_failed", err.Error())
		return
	}

	s.auditSuccess(r, action, "service", "")
	writeJSON(w, s.Log, http.StatusOK, "application/json",
		gen.ServiceActionResult{Ok: true, Message: okPtr(successMsg)})
}

// selfExecutable 返回当前进程的可执行文件路径。
func selfExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", errors.New(i18n.T("api.service.no_self_path") + err.Error())
	}
	// 解析符号链接：不解析的话，通过 /usr/local/bin/isc 这类链接调用
	// 时会注册链接本身，而服务启动时的上下文不同，链接可能解析不到。
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// isPermissionError 判断错误是否为权限不足。
//
// 各平台的措辞不同，但它们都来自我们自己的 requireAdmin /
// requireRoot，因此匹配我们自己写的那几句提示是可靠的 ——
// 比匹配操作系统的本地化文本可靠得多。
func isPermissionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{
		i18n.T("api.service.needs_admin"), i18n.T("api.service.needs_root"), "Access is denied", i18n.T("api.service.access_denied"),
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

func okPtr(s string) *string { return &s }
