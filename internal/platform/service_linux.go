//go:build linux

package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 本文件通过 systemd 安装与管理内核服务。
//
// # 需要 root
//
// 写 /etc/systemd/system/ 与执行 systemctl 都需要 root。非 root 时
// systemctl 的报错是 "Interactive authentication required"，那句话
// 不告诉用户该做什么 —— 因此这里提前自检并给出可执行的提示。
//
// # 为什么直接写单元文件而不是让用户手工放
//
// 单元文件的内容有几处容易写错（数据目录、重启策略、启动时机），
// 而写错之后 systemd 的错误信息通常只有一句 "failed"。由内核生成
// 能保证这些细节与当前实际路径一致。

// 服务名。与 Windows 保持一致，便于文档与脚本跨平台复用。
const (
	linuxServiceName  = coreServiceName
	linuxSystemdDir   = "/etc/systemd/system"
	linuxUnitFileName = coreServiceName + ".service"
)

type linuxServiceManager struct{}

func newServiceManager() ServiceManager { return &linuxServiceManager{} }

func (s *linuxServiceManager) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "systemd",
		Note:      "/etc/systemd/system/" + linuxUnitFileName + "；安装与启停需要 root",
	}
}

// Install 写入单元文件并启用服务。
func (s *linuxServiceManager) Install(ctx context.Context, cfg ServiceConfig) error {
	if err := ValidateServiceConfig(cfg); err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}

	unit := RenderSystemdUnit(cfg)
	path := filepath.Join(linuxSystemdDir, linuxUnitFileName)

	// 0644 是单元文件的标准权限 —— systemd 会以 root 读取它，
	// 而它不含任何机密。
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("platform: 写入 unit 文件失败 %s：%w", path, err)
	}

	// daemon-reload 是必须的：不重新加载的话 systemd 仍然用缓存的
	// 单元定义，而 enable 会报"没有这个单元"。
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}

	if cfg.AutoStart {
		if err := systemctl(ctx, "enable", linuxServiceName); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall 停止、禁用并删除服务。
func (s *linuxServiceManager) Uninstall(ctx context.Context) error {
	if err := requireRoot(); err != nil {
		return err
	}

	// 全部**幂等**：单元本来就不存在时不该报错。
	// "先卸再装"是最常见的部署写法，而报错会让那段脚本失败。
	_ = systemctl(ctx, "stop", linuxServiceName)
	_ = systemctl(ctx, "disable", linuxServiceName)

	path := filepath.Join(linuxSystemdDir, linuxUnitFileName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("platform: 删除 unit 文件失败 %s：%w", path, err)
	}

	_ = systemctl(ctx, "daemon-reload")
	return nil
}

// Status 返回服务状态。
func (s *linuxServiceManager) Status(ctx context.Context) (ServiceStatus, error) {
	// is-active 对未安装的单元返回非零退出码，因此这里不看错误，
	// 只看输出 —— "inactive" 与 "unknown" 都要映射成"未运行"。
	out, _ := systemctlOutput(ctx, "is-active", linuxServiceName)

	switch strings.TrimSpace(out) {
	case "active", "activating", "reloading":
		return ServiceRunning, nil
	default:
		return ServiceStopped, nil
	}
}

// Start 启动服务。
func (s *linuxServiceManager) Start(ctx context.Context) error {
	if err := requireRoot(); err != nil {
		return err
	}
	return systemctl(ctx, "start", linuxServiceName)
}

// Stop 停止服务。
func (s *linuxServiceManager) Stop(ctx context.Context) error {
	if err := requireRoot(); err != nil {
		return err
	}
	return systemctl(ctx, "stop", linuxServiceName)
}

// ---------------------------------------------------------------------------
// 平台调用
// ---------------------------------------------------------------------------

func requireRoot() error {
	if os.Geteuid() == 0 {
		return nil
	}
	return errors.New(
		"platform: 安装与管理系统服务需要 root 权限。\n" +
			"请用 sudo 重新运行这条命令")
}

func systemctl(ctx context.Context, args ...string) error {
	out, err := systemctlOutput(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("platform: systemctl %s 失败：%s",
			strings.Join(args, " "), msg)
	}
	return nil
}

func systemctlOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
