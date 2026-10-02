package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// newServiceCmd 提供把内核注册为系统服务的能力。
//
// 这是"装完之后不用管它"的关键：注册为服务之后内核会开机自启、
// 崩溃自动重启，而用户不必一直开着一个终端窗口。
func newServiceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: i18n.T("cli.service.short"),
		Long:  i18n.T("cli.service.long"),
	}

	cmd.AddCommand(
		newServiceInstallCmd(app),
		newServiceUninstallCmd(app),
		newServiceStatusCmd(app),
		newServiceStartCmd(app),
		newServiceStopCmd(app),
	)
	return cmd
}

func newServiceInstallCmd(app *App) *cobra.Command {
	var (
		autoStart bool
		noRestart bool
		exePath   string
	)

	cmd := &cobra.Command{
		Use:   "install",
		Short: i18n.T("cli.service.install_short"),
		Long:  i18n.T("cli.service.install_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := resolveExecutable(exePath)
			if err != nil {
				return app.fail(cmd, err)
			}

			bundle := platform.Current(app.paths.Root())

			cfg := platform.ServiceConfig{
				Executable: exe,
				// 服务启动的是守护进程本体，不是 CLI 的其它子命令。
				Arguments: []string{"daemon", "run"},
				// 数据目录必须传给服务进程。
				//
				// 服务以 SYSTEM 身份运行，而 SYSTEM 的环境变量与
				// 当前用户完全不同 —— 不显式指定的话，内核会去
				// C:\Windows\System32\config\systemprofile 下建数据目录，
				// 而用户在原来的位置看不到任何数据。
				WorkingDirectory: filepath.Dir(exe),
				AutoStart:        autoStart,
				RestartOnFailure: !noRestart,
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			// 数据目录作为**参数**传给服务，而不是靠环境变量。
			//
			// 服务进程的环境与当前 shell 无关，环境变量传不过去。
			dataDir := app.paths.Root()
			if !isDefaultDataDir(dataDir) {
				cfg.Arguments = append(cfg.Arguments, "--data-dir", dataDir)
			}

			if err := bundle.ServiceManager.Install(ctx, cfg); err != nil {
				return app.fail(cmd, err)
			}

			st := bundle.ServiceManager.Describe()
			if app.jsonOut {
				return writeJSONOut(app.out, map[string]any{
					"installed":  true,
					"backend":    st.Backend,
					"executable": exe,
					"data_dir":   dataDir,
					"auto_start": autoStart,
				})
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.installed"), st.Backend)
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.exe_path"), exe)
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.data_dir"), dataDir)
			if autoStart {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.autostart_yes"))
			} else {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.autostart_no"))
			}
			if !noRestart {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.restart_yes"))
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.next_steps"))
			return nil
		},
	}

	cmd.Flags().BoolVar(&autoStart, "auto-start", true,
		i18n.T("cli.service.flag_autostart"))
	cmd.Flags().BoolVar(&noRestart, "no-restart", false,
		i18n.T("cli.service.flag_no_restart"))
	cmd.Flags().StringVar(&exePath, "exe", "",
		i18n.T("cli.service.flag_exe"))
	return cmd
}

func newServiceUninstallCmd(app *App) *cobra.Command {
	var keepRunning bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: i18n.T("cli.service.uninstall_short"),
		Long:  i18n.T("cli.service.uninstall_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if keepRunning {
				// 用户明确要求保留运行中的进程时，只删服务注册。
				// 平台后端不支持这种拆分，因此直接说明。
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.service.uninstall_busy")))
			}

			if err := bundle.ServiceManager.Uninstall(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.uninstalled"))
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.data_kept")+"\n",
				app.paths.Root())
			return nil
		},
	}

	cmd.Flags().BoolVar(&keepRunning, "keep-running", false,
		i18n.T("cli.service.keep_running"))
	return cmd
}

func newServiceStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: i18n.T("cli.service.status_short"),
		Long:  i18n.T("cli.service.status_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			st, svcErr := bundle.ServiceManager.Status(ctx)

			// 内核本身是否可达 —— 这一项**永远能查**，
			// 因此即使在服务查询失败时它也能给出有用的信息。
			running := daemonReachable(ctx, app.paths.RuntimeFile())

			if app.jsonOut {
				out := map[string]any{
					"backend":          bundle.ServiceManager.Describe().Backend,
					"daemon_reachable": running,
				}
				if svcErr == nil {
					out["status"] = st
				} else {
					out["error"] = svcErr.Error()
				}
				return writeJSONOut(app.out, out)
			}

			// 先报"内核在不在跑"：那是用户最关心的，而且它总是有答案。
			if running {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.kernel_running"))
			} else {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.kernel_stopped"))
			}

			if svcErr != nil {
				// 服务查询失败**不让整条命令失败** —— 上面那一行
				// 已经回答了用户最可能想问的问题。
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.query_failed"),
					bundle.ServiceManager.Describe().Backend)
				_, _ = fmt.Fprintf(app.out, "   %s\n", svcErr.Error())
				return nil
			}

			icon, label := "⏹ ", i18n.T("cli.service.state_stopped")
			if st == platform.ServiceRunning {
				icon, label = "▶ ", i18n.T("cli.service.state_running")
			}
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.service.state_line"),
				icon, label, bundle.ServiceManager.Describe().Backend)
			return nil
		},
	}
}

func newServiceStartCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: i18n.T("cli.service.start_short"),
		Long:  i18n.T("cli.service.start_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if err := bundle.ServiceManager.Start(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.started"))
			return nil
		},
	}
}

func newServiceStopCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: i18n.T("cli.service.stop_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if err := bundle.ServiceManager.Stop(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, i18n.T("cli.service.stopped"))
			return nil
		},
	}
}

// resolveExecutable 确定要注册的可执行文件路径。
//
// 默认用**当前正在运行的这个**：用户刚用它跑通了各种命令，
// 而那个路径显然是可用的。让他在命令行里手输一遍只会引入拼写错误。
func resolveExecutable(override string) (string, error) {
	path := override
	if path == "" {
		self, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf(i18n.T("cli.service.no_self_path"), err)
		}
		path = self
	}

	// 解析符号链接。
	//
	// 不解析的话，通过 /usr/local/bin/isc 这类链接调用时会注册链接
	// 本身，而服务启动时的工作目录与权限上下文不同，链接可能解析
	// 不到目标。
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf(i18n.T("cli.service.not_abs"), path, err)
	}

	// 服务要求绝对路径且文件必须存在 —— 一个打不开的路径会让服务
	// 装好之后启动即失败，而错误信息里只有一句含糊的失败。
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf(i18n.T("cli.service.not_found"), abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf(i18n.T("cli.service.is_dir"), abs)
	}

	return abs, nil
}

// daemonReachable 检查本地接口是否可连通。
//
// 用它而不是读运行时文件的存在性：文件可能是上次异常退出留下的，
// 而"能连上"才是内核真的在跑的证明。
func daemonReachable(ctx context.Context, runtimeFile string) bool {
	client, err := Connect(ctx, runtimeFile)
	if err != nil {
		return false
	}
	var health any
	return client.getInto(ctx, "/v1/health", &health) == nil
}

// isDefaultDataDir 判断数据目录是否是平台默认值。
//
// 是默认值时不必显式传给服务进程：服务会用同样的规则推导出同一个
// 路径，而少一个参数就少一处可能的拼写错误。
func isDefaultDataDir(dir string) bool {
	// 显式设了环境变量就一定不是"默认"。
	return strings.TrimSpace(os.Getenv("ISC_DATA_DIR")) == ""
}
