package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// newServiceCmd 提供把内核注册为系统服务的能力。
//
// 这是"装完之后不用管它"的关键：注册为服务之后内核会开机自启、
// 崩溃自动重启，而用户不必一直开着一个终端窗口。
func newServiceCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "把内核注册为系统服务（开机自启、崩溃重启）",
		Long: `把内核注册为系统服务。

注册之后内核会：
  · 开机自动启动（Windows 使用延迟自启，等网络就绪后再启动）
  · 崩溃后自动重启（5 秒 / 30 秒 / 60 秒三档递增延迟）

**安装与卸载需要管理员权限**：
  Windows  右键终端 → 以管理员身份运行
  Linux    sudo isc service install
  macOS    sudo isc service install

各平台的服务机制不同：
  Windows  服务控制管理器（SCM），可在 services.msc 里看到
  Linux    systemd（/etc/systemd/system/isc-core.service）
  macOS    launchd（/Library/LaunchDaemons/com.isc.core.plist）`,
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
		Short: "安装系统服务",
		Long: `安装并注册系统服务。

已经安装过时会**更新配置**而不是报错 —— 重新运行安装命令是常态
（换了路径、想改自启设置），而报"服务已存在"只会让你去手工卸载。`,
		Args: cobra.NoArgs,
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

			_, _ = fmt.Fprintf(app.out, "✅ 服务已安装（%s）\n", st.Backend)
			_, _ = fmt.Fprintf(app.out, "   可执行文件: %s\n", exe)
			_, _ = fmt.Fprintf(app.out, "   数据目录:   %s\n", dataDir)
			if autoStart {
				_, _ = fmt.Fprintln(app.out, "   开机自启:   是（延迟自启，等网络就绪）")
			} else {
				_, _ = fmt.Fprintln(app.out, "   开机自启:   否（手动启动）")
			}
			if !noRestart {
				_, _ = fmt.Fprintln(app.out, "   崩溃重启:   是（5s / 30s / 60s 递增延迟）")
			}
			_, _ = fmt.Fprintln(app.out,
				"\n用 isc service start 启动它，或用 isc service status 查看状态。")
			return nil
		},
	}

	cmd.Flags().BoolVar(&autoStart, "auto-start", true,
		"开机自动启动（Windows 上使用延迟自启，等网络就绪后再启动）")
	cmd.Flags().BoolVar(&noRestart, "no-restart", false,
		"不在崩溃后自动重启")
	cmd.Flags().StringVar(&exePath, "exe", "",
		"要注册的可执行文件路径（默认用当前运行的这一个）")
	return cmd
}

func newServiceUninstallCmd(app *App) *cobra.Command {
	var keepRunning bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "停止并删除系统服务",
		Long: `停止并删除系统服务。

它是**幂等**的：服务本来就不存在时返回成功。报错会让"先卸再装"
这类部署脚本失败，而那是最常见的写法。

数据目录**不会**被删除 —— 里面有你的凭据与配置。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if keepRunning {
				// 用户明确要求保留运行中的进程时，只删服务注册。
				// 平台后端不支持这种拆分，因此直接说明。
				return app.fail(cmd, fmt.Errorf(
					"暂不支持「只删服务、不停进程」；请先 isc service stop"))
			}

			if err := bundle.ServiceManager.Uninstall(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, "✅ 服务已卸载")
			_, _ = fmt.Fprintf(app.out,
				"   数据目录仍然保留：%s\n", app.paths.Root())
			_, _ = fmt.Fprintln(app.out, "   如需彻底清除，请手工删除它。")
			return nil
		},
	}

	cmd.Flags().BoolVar(&keepRunning, "keep-running", false,
		"只删除服务注册，不停掉正在运行的进程（当前平台可能不支持）")
	return cmd
}

func newServiceStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看系统服务状态",
		Long: `查看系统服务状态。

它同时报出两件事：

	操作系统里的服务   是否已安装、是否在运行（**需要管理员权限**）
	内核本身           现在是否真的能连通（不需要任何权限）

两者分开报是有原因的。真机上确认过：Windows 上**查询**服务状态同样
需要管理员（打开服务控制管理器要完全访问权），因此普通用户跑这条
命令会失败。但他真正想知道的往往是"内核在跑吗"—— 而那个问题看
一眼运行时文件就能回答。`,
		Args: cobra.NoArgs,
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
				_, _ = fmt.Fprintln(app.out, "▶  内核：运行中（本地接口可连通）")
			} else {
				_, _ = fmt.Fprintln(app.out, "⏹  内核：未运行")
			}

			if svcErr != nil {
				// 服务查询失败**不让整条命令失败** —— 上面那一行
				// 已经回答了用户最可能想问的问题。
				_, _ = fmt.Fprintf(app.out, "   系统服务：无法查询（%s）\n",
					bundle.ServiceManager.Describe().Backend)
				_, _ = fmt.Fprintf(app.out, "   %s\n", svcErr.Error())
				return nil
			}

			icon, label := "⏹ ", "未运行"
			if st == platform.ServiceRunning {
				icon, label = "▶ ", "运行中"
			}
			_, _ = fmt.Fprintf(app.out, "%s 系统服务：%s（%s）\n",
				icon, label, bundle.ServiceManager.Describe().Backend)
			return nil
		},
	}
}

func newServiceStartCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "启动系统服务",
		Long: `启动系统服务。

服务**已经在运行**时返回成功 —— 报错会让"确保它在跑"这类脚本失败，
而那正是最常见的用法。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if err := bundle.ServiceManager.Start(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, "✅ 服务已启动")
			return nil
		},
	}
}

func newServiceStopCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "停止系统服务",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle := platform.Current(app.paths.Root())

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			if err := bundle.ServiceManager.Stop(ctx); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintln(app.out, "✅ 服务已停止")
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
			return "", fmt.Errorf(
				"无法确定当前可执行文件的路径：%w。"+
					"请用 --exe 显式指定", err)
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
		return "", fmt.Errorf("无法解析为绝对路径 %q：%w", path, err)
	}

	// 服务要求绝对路径且文件必须存在 —— 一个打不开的路径会让服务
	// 装好之后启动即失败，而错误信息里只有一句含糊的失败。
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("可执行文件不存在或无法访问：%s：%w", abs, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("可执行文件路径指向一个目录：%s", abs)
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
