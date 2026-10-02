//go:build darwin

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

// 本文件通过 launchd 安装与管理内核服务。
//
// # 需要 root
//
// 写 /Library/LaunchDaemons/ 需要 root。用 LaunchDaemon（而不是
// LaunchAgent）是必须的：Agent 只在用户登录后才运行，而内核要在
// 开机后就提供服务 —— 那正是"把它当服务器用"的前提。
//
// # 与 systemd 的一个关键差异
//
// launchd 的 KeepAlive 没有"最多重启几次"的概念。一个必然启动失败的
// 配置会变成**无限重启**，而 macOS 会把这种进程标记为 "throttled"
// 并放慢重启频率 —— 那是系统层面的保护，不是我们能控的。
//
// 因此这里用 SuccessfulExit=false 而不是笼统的 KeepAlive=true：
// 它表示"只在**异常**退出时重启"，从而让用户主动停止时不被拉起来。

const (
	darwinLabel     = darwinServiceLabel
	darwinLaunchDir = "/Library/LaunchDaemons"
	darwinPlistName = darwinLabel + ".plist"
)

type darwinServiceManager struct{}

func newServiceManager() ServiceManager { return &darwinServiceManager{} }

func (s *darwinServiceManager) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "launchd",
		Note:      filepath.Join(darwinLaunchDir, darwinPlistName) + "；安装与启停需要 root",
	}
}

// Install 写入 plist 并加载服务。
func (s *darwinServiceManager) Install(ctx context.Context, cfg ServiceConfig) error {
	if err := ValidateServiceConfig(cfg); err != nil {
		return err
	}
	if err := requireDarwinRoot(); err != nil {
		return err
	}

	path := filepath.Join(darwinLaunchDir, darwinPlistName)

	// 已经加载过时先卸载。
	//
	// launchctl load 对已加载的 label 会报 "service already loaded"，
	// 而那会让"重新运行安装命令"失败 —— 用户的意图显然是"用新配置
	// 替换旧的"。
	_ = launchctl(ctx, "unload", path)

	// 0644：plist 里不含机密，而 launchd 以 root 读取它。
	if err := os.WriteFile(path, []byte(RenderLaunchdPlist(cfg)), 0o644); err != nil {
		return fmt.Errorf("platform: 写入 plist 失败 %s：%w", path, err)
	}

	// -w 会同时把服务登记为开机自启。
	//
	// 不带 -w 的话只是本次加载，重启后就没有了 —— 而"开机自启"
	// 正是用户装服务的主要目的。
	if cfg.AutoStart {
		if err := launchctl(ctx, "load", "-w", path); err != nil {
			return err
		}
	} else {
		if err := launchctl(ctx, "load", path); err != nil {
			return err
		}
	}
	return nil
}

// Uninstall 卸载并删除服务。
func (s *darwinServiceManager) Uninstall(ctx context.Context) error {
	if err := requireDarwinRoot(); err != nil {
		return err
	}

	path := filepath.Join(darwinLaunchDir, darwinPlistName)

	// **幂等**：没装过时不报错。"先卸再装"是最常见的部署写法。
	_ = launchctl(ctx, "unload", "-w", path)

	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("platform: 删除 plist 失败 %s：%w", path, err)
	}
	return nil
}

// Status 返回服务状态。
func (s *darwinServiceManager) Status(ctx context.Context) (ServiceStatus, error) {
	// launchctl list <label> 对未加载的服务返回非零退出码，
	// 因此这里不看错误，只看有没有输出。
	out, _ := launchctlOutput(ctx, "list", darwinLabel)

	if strings.TrimSpace(out) == "" {
		return ServiceStopped, nil
	}
	// 输出里有一行 "PID" = <数字> 时表示进程在跑；
	// 没有 PID 那行说明服务已加载但进程没起来。
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "\"PID\"") {
			return ServiceRunning, nil
		}
	}
	return ServiceStopped, nil
}

// Start 启动服务。
func (s *darwinServiceManager) Start(ctx context.Context) error {
	if err := requireDarwinRoot(); err != nil {
		return err
	}
	return launchctl(ctx, "start", darwinLabel)
}

// Stop 停止服务。
func (s *darwinServiceManager) Stop(ctx context.Context) error {
	if err := requireDarwinRoot(); err != nil {
		return err
	}
	return launchctl(ctx, "stop", darwinLabel)
}

// ---------------------------------------------------------------------------
// 平台调用
// ---------------------------------------------------------------------------

func requireDarwinRoot() error {
	if os.Geteuid() == 0 {
		return nil
	}
	return errors.New(
		"platform: 安装与管理系统服务需要 root 权限。\n" +
			"请用 sudo 重新运行这条命令")
}

func launchctl(ctx context.Context, args ...string) error {
	out, err := launchctlOutput(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("platform: launchctl %s 失败：%s",
			strings.Join(args, " "), msg)
	}
	return nil
}

func launchctlOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "launchctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
