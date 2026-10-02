//go:build windows

package platform

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// 本文件通过 Windows 服务控制管理器（SCM）安装与管理内核服务。
//
// # 需要管理员权限
//
// 创建、删除、启停服务都需要管理员。**普通用户下这些操作会失败**，
// 而错误信息是 Windows 的 "Access is denied" —— 它不会告诉用户
// "你需要用管理员身份运行"。
//
// 因此这里在动手之前先自检权限，并给出可执行的提示。
//
// # 查询同样需要管理员（真机上确认）
//
// 起初以为查询不需要权限，因为"读状态"听起来是无害的操作。实际上
// mgr.Connect() 打开 SCM 时请求的是 SC_MANAGER_ALL_ACCESS，而普通
// 用户拿不到它 —— 于是 isc service status 报 "Access is denied"。
//
// 这暴露了一个更根本的问题：用户问"服务在跑吗"，真正想知道的是
// "内核在跑吗"，而那个问题**不需要任何权限**就能回答 —— 看一眼
// runtime.json 即可。因此 CLI 层会把两者都报出来。

// 服务名。用固定的名字而不是从参数来：服务是"这台机器上装了一个
// 内核"这件事的标识，能被改名只会让用户在 SCM 里找不到它。
const windowsServiceName = coreServiceName

// windowsDisplayName 是 SCM 里显示的名字。
//
// 它是 var 而不是 const：文案来自消息目录，而函数调用不是常量表达式。
// 这一点在迁移时才会暴露 —— 编译期就能发现，算是运气好的那一类。
var windowsDisplayName = i18n.T("platform.svc_display_name")

// windowsServiceManager 实现 ServiceManager。
type windowsServiceManager struct{}

// NewServiceManager 返回当前平台的服务管理器。
func newServiceManager() ServiceManager { return &windowsServiceManager{} }

func (s *windowsServiceManager) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "windows-scm",
		Note:      i18n.T("platform.scm_note"),
	}
}

// Install 安装并配置服务。
func (s *windowsServiceManager) Install(ctx context.Context, cfg ServiceConfig) error {
	if err := ValidateServiceConfig(cfg); err != nil {
		return err
	}
	if err := s.requireAdmin(); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf(i18n.T("platform.scm_connect"), err)
	}
	defer m.Disconnect() //nolint:errcheck // 只读句柄

	// 已经装过时**更新配置**而不是报错。
	//
	// 用户重新运行安装命令是常态（换了路径、想改自启设置），
	// 而报"服务已存在"会让他去手工卸载 —— 那是多余的步骤。
	if existing, err := m.OpenService(windowsServiceName); err == nil {
		defer existing.Close() //nolint:errcheck // 只读句柄

		if err := s.reconfigure(existing, cfg); err != nil {
			return err
		}
		return nil
	}

	service, err := m.CreateService(windowsServiceName, cfg.Executable, mgr.Config{
		DisplayName: displayNameOf(cfg),
		Description: descriptionOf(cfg),
		// 自动启动用**延迟自启**：内核依赖网络，而开机瞬间网络栈
		// 往往还没就绪 —— 立即启动会让第一轮地址检测拿到空结果。
		//
		// 延迟自启让它在其它自动服务启动之后再等一小会儿，
		// 那时网络通常已经可用。
		StartType:        startTypeOf(cfg),
		DelayedAutoStart: cfg.AutoStart,
		// 参数要拼进二进制路径里。
		//
		// Windows 服务没有"参数数组"这个概念，整条命令行就是一个
		// 字符串。CreateService 的第二个参数是**可执行文件路径**，
		// 因此参数必须自己拼上去。
		BinaryPathName: buildBinaryPath(cfg),
	})
	if err != nil {
		return fmt.Errorf(i18n.T("platform.svc_create"), err)
	}
	defer service.Close() //nolint:errcheck // 只读句柄

	if cfg.RestartOnFailure {
		// 失败恢复不是必需的：设不上只影响"崩溃后自动重启"，
		// 而不该让整个安装失败。
		if err := s.setRecovery(service); err != nil {
			return fmt.Errorf(
				i18n.T("platform.svc_created_no_restart"), err)
		}
	}

	return nil
}

// reconfigure 更新一个已存在的服务的配置。
func (s *windowsServiceManager) reconfigure(service *mgr.Service, cfg ServiceConfig) error {
	conf, err := service.Config()
	if err != nil {
		return fmt.Errorf(i18n.T("platform.svc_read_config"), err)
	}

	conf.DisplayName = displayNameOf(cfg)
	conf.Description = descriptionOf(cfg)
	conf.StartType = startTypeOf(cfg)
	conf.DelayedAutoStart = cfg.AutoStart
	conf.BinaryPathName = buildBinaryPath(cfg)

	if err := service.UpdateConfig(conf); err != nil {
		return fmt.Errorf(i18n.T("platform.svc_update_config"), err)
	}
	if cfg.RestartOnFailure {
		if err := s.setRecovery(service); err != nil {
			return fmt.Errorf(i18n.T("platform.svc_set_restart"), err)
		}
	}
	return nil
}

// setRecovery 配置崩溃后的自动重启。
//
// 策略是三次递增的延迟重启（5 秒 / 30 秒 / 60 秒）。
//
// 递增而不是固定间隔：内核崩溃如果是**持续性的**（数据文件损坏、
// 端口被永久占用），固定间隔会变成每 5 秒一次的无限重启循环，
// 而那会把日志刷满、也会让用户完全看不出问题在哪。
//
// 用 mgr 自带的 SetRecoveryActions 而不是自己拼
// ChangeServiceConfig2：后者需要 unsafe 传裸指针，而那个指针的
// 生命周期靠 runtime.KeepAlive 维持 —— 一个纯粹的隐患来源，
// 而标准库已经把它封装好了。
func (s *windowsServiceManager) setRecovery(service *mgr.Service) error {
	actions := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}

	// resetPeriod 是一天：一天之内没有再次失败就重新从头计数。
	//
	// 不设的话，服务运行三个月后偶发崩溃一次就会直接用上最后一档
	//（60 秒延迟），而那时它其实只需要立刻重启。
	const resetPeriod = 86400

	if err := service.SetRecoveryActions(actions, resetPeriod); err != nil {
		return fmt.Errorf(i18n.T("platform.svc_set_failure"), err)
	}
	return nil
}

// Uninstall 停止并删除服务。
func (s *windowsServiceManager) Uninstall(ctx context.Context) error {
	if err := s.requireAdmin(); err != nil {
		return err
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf(i18n.T("platform.scm_connect"), err)
	}
	defer m.Disconnect() //nolint:errcheck // 只读句柄

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		// 服务本来就不存在：卸载是**幂等**的。
		//
		// 报错会让"重复执行卸载脚本"失败，而那是部署脚本里最常见
		// 的写法（先卸再装）。
		if isNotInstalled(err) {
			return nil
		}
		return fmt.Errorf(i18n.T("platform.svc_open"), err)
	}
	defer service.Close() //nolint:errcheck // 只读句柄

	// 先停再删。
	//
	// 不先停的话 DeleteService 会把服务标记为"待删除"，而它要等到
	// 所有句柄关闭、进程退出之后才真正消失 —— 用户会看到服务还在
	// 列表里，然后以为卸载失败了。
	if _, err := service.Control(svc.Stop); err == nil {
		_ = s.waitStopped(ctx, service, 20*time.Second)
	}

	if err := service.Delete(); err != nil {
		return fmt.Errorf(i18n.T("platform.svc_delete"), err)
	}
	return nil
}

// Status 返回服务状态。
//
// 与 Install 一样需要管理员权限 —— 见文件头的说明。
func (s *windowsServiceManager) Status(_ context.Context) (ServiceStatus, error) {
	m, err := mgr.Connect()
	if err != nil {
		if isAccessDenied(err) {
			return ServiceUnknown, errors.New(
				i18n.T("platform.svc_query_needs_admin"))
		}
		return ServiceUnknown, fmt.Errorf(i18n.T("platform.scm_connect"), err)
	}
	defer m.Disconnect() //nolint:errcheck // 只读句柄

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		if isNotInstalled(err) {
			// 没装不是错误，是一种正常状态。
			return ServiceStopped, nil
		}
		return ServiceUnknown, fmt.Errorf(i18n.T("platform.svc_open"), err)
	}
	defer service.Close() //nolint:errcheck // 只读句柄

	st, err := service.Query()
	if err != nil {
		return ServiceUnknown, fmt.Errorf(i18n.T("platform.svc_query"), err)
	}
	return mapWindowsState(st.State), nil
}

// Start 启动服务。
func (s *windowsServiceManager) Start(ctx context.Context) error {
	if err := s.requireAdmin(); err != nil {
		return err
	}

	return s.withService(func(service *mgr.Service) error {
		if err := service.Start(); err != nil {
			// 已经在运行：当作成功。
			//
			// 报错会让"确保它在跑"这类脚本失败，而那正是最常见的
			// 用法。
			if isAlreadyRunning(err) {
				return nil
			}
			return fmt.Errorf(i18n.T("platform.svc_start"), err)
		}
		return nil
	})
}

// Stop 停止服务。
func (s *windowsServiceManager) Stop(ctx context.Context) error {
	if err := s.requireAdmin(); err != nil {
		return err
	}

	return s.withService(func(service *mgr.Service) error {
		// Stop 是**异步**的：它只发送控制请求。
		// 必须等状态真正变成 stopped，否则紧接着的删除会失败。
		if _, err := service.Control(svc.Stop); err != nil {
			if isNotRunning(err) {
				return nil
			}
			return fmt.Errorf(i18n.T("platform.svc_stop"), err)
		}
		return s.waitStopped(ctx, service, 20*time.Second)
	})
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func (s *windowsServiceManager) waitStopped(ctx context.Context,
	service *mgr.Service, timeout time.Duration) error {

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		st, err := service.Query()
		if err != nil {
			return fmt.Errorf(i18n.T("platform.svc_query"), err)
		}
		if st.State == svc.Stopped {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}

	return fmt.Errorf(i18n.T("platform.svc_stop_timeout"), timeout)
}

func (s *windowsServiceManager) withService(fn func(*mgr.Service) error) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf(i18n.T("platform.scm_connect"), err)
	}
	defer m.Disconnect() //nolint:errcheck // 只读句柄

	service, err := m.OpenService(windowsServiceName)
	if err != nil {
		if isNotInstalled(err) {
			return errors.New(i18n.T("platform.svc_missing"))
		}
		return fmt.Errorf(i18n.T("platform.svc_open"), err)
	}
	defer service.Close() //nolint:errcheck // 只读句柄

	return fn(service)
}

// requireAdmin 检查当前进程是否有管理员权限。
//
// 提前检查是为了给出**能照着做**的提示。不检查的话，用户拿到的会是
// Windows 的 "Access is denied"，而那句话不告诉他该做什么。
func (s *windowsServiceManager) requireAdmin() error {
	var sid *windows.SID

	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid)
	if err != nil {
		return fmt.Errorf(i18n.T("platform.svc_admin_sid"), err)
	}
	defer windows.FreeSid(sid) //nolint:errcheck // 释放失败无补救

	token := windows.Token(0)
	member, err := token.IsMember(sid)
	if err != nil {
		return fmt.Errorf(i18n.T("platform.svc_admin_check"), err)
	}
	if !member {
		return errors.New(i18n.T("platform.svc_need_admin"))
	}
	return nil
}

// buildBinaryPath 把可执行文件与参数拼成 Windows 服务要求的命令行。
//
// **路径含空格时必须加引号**，否则 Windows 会试图运行
// `C:\Program` 这个程序 —— 而报错是"找不到文件"，完全看不出
// 根因是路径没引起来。这一点在 Program Files 下是必然踩到的。
func buildBinaryPath(cfg ServiceConfig) string {
	exe := cfg.Executable
	if strings.ContainsAny(exe, " \t") {
		exe = `"` + exe + `"`
	}
	if len(cfg.Arguments) == 0 {
		return exe
	}
	return exe + " " + strings.Join(cfg.Arguments, " ")
}

func displayNameOf(cfg ServiceConfig) string {
	if cfg.DisplayName != "" {
		return cfg.DisplayName
	}
	return windowsDisplayName
}

func descriptionOf(cfg ServiceConfig) string {
	if cfg.Description != "" {
		return cfg.Description
	}
	return i18n.T("platform.svc_description")
}

func startTypeOf(cfg ServiceConfig) uint32 {
	if cfg.AutoStart {
		return mgr.StartAutomatic
	}
	return mgr.StartManual
}

func mapWindowsState(st svc.State) ServiceStatus {
	switch st {
	case svc.Running, svc.StartPending, svc.ContinuePending:
		return ServiceRunning
	case svc.Stopped, svc.StopPending, svc.Paused, svc.PausePending:
		return ServiceStopped
	default:
		return ServiceUnknown
	}
}

// isNotInstalled 判断错误是否为"服务不存在"。
//
// 用错误码而不是匹配错误文本：文本会随系统语言变化，而中文 Windows 上
// 那句话根本不含 "does not exist"。
func isNotInstalled(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST)
}

// isAlreadyRunning 判断错误是否为"服务已在运行"。
func isAlreadyRunning(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING)
}

// isNotRunning 判断错误是否为"服务未在运行"。
func isNotRunning(err error) bool {
	return errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE)
}

// isAccessDenied 判断错误是否为权限不足。
//
// 用错误码而不是匹配文本：中文 Windows 上那句话是"拒绝访问"，
// 而英文匹配完全失效。
func isAccessDenied(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
