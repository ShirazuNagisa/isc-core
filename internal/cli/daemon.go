package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/daemon"
)

func newDaemonCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "管理内核守护进程",
		Long: `管理内核守护进程。

内核是一个无 GUI 的常驻进程，CLI、验证控制台与下游 GUI 都通过
本地接口与它通信。它必须常驻才能保证动态解析的定时任务可靠执行。`,
	}

	cmd.AddCommand(
		newDaemonRunCmd(app),
		newDaemonInstallCmd(app),
		newDaemonUninstallCmd(app),
	)
	return cmd
}

func newDaemonRunCmd(app *App) *cobra.Command {
	var (
		loopbackAddr    string
		disableLoopback bool
		allowedOrigins  []string
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "在前台运行内核",
		Long: `在前台运行内核，直到收到中断信号。

生产环境应当使用 isc daemon install 安装为系统服务，
这样内核能在无人登录时运行、开机自启，并以足够的权限
修改防火墙与绑定低端口。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			d := daemon.New(daemon.Options{
				Paths:           app.paths,
				LogHandler:      app.logHandler,
				Lang:            app.lang,
				LoopbackAddr:    loopbackAddr,
				DisableLoopback: disableLoopback,
				AllowedOrigins:  allowedOrigins,
			})

			return d.Run(ctx)
		},
	}

	cmd.Flags().StringVar(&loopbackAddr, "loopback", "",
		"回环监听地址（默认 127.0.0.1:0，即自动分配端口）")
	cmd.Flags().BoolVar(&disableLoopback, "no-loopback", false,
		"关闭回环监听（注意：浏览器控制台将无法连接）")
	cmd.Flags().StringSliceVar(&allowedOrigins, "allow-origin", nil,
		"允许的 WebSocket Origin 模式（仅用于本地开发调试控制台）")

	return cmd
}

// errNotImplementedYet 统一"里程碑未到"的错误文案风格。
func errNotImplementedYet(what, milestone string) error {
	return errors.New(what + "将在 " + milestone + " 实现（见 docs/PLAN.md）")
}

func newDaemonInstallCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "安装为系统服务（需要管理员权限）",
		Long: `把内核安装为系统服务。

为什么必须是系统服务（见 docs/DECISIONS.md D20）：
服务能在无人登录时运行、开机自启，并以足够权限修改防火墙、
绑定低端口 —— 用户级进程在 Windows 上每次改防火墙都要弹 UAC。`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNotImplementedYet("服务安装", "M5")
		},
	}
}

func newDaemonUninstallCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "卸载系统服务（需要管理员权限）",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNotImplementedYet("服务卸载", "M5")
		},
	}
}
