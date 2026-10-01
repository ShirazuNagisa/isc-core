// Package cli 实现 isc 命令行的子命令树。
//
// 设计原则（见 docs/DECISIONS.md D19）：
//
//   - CLI 是**通过本地 API 通信的客户端**，而不是直接调用内核内部函数。
//     这样它才能真正验证接口契约，也才能在未来被远程 GUI 复用同一套语义；
//   - 所有子命令支持 --json，输出结构稳定，可被脚本与下游 GUI 直接消费；
//   - 同一个二进制既是 CLI 也是守护进程入口（isc daemon run）。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/logx"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
)

// App 承载一次 CLI 调用的共享状态。
type App struct {
	// paths 是解析后的数据目录布局。
	paths paths.Paths

	// lang 是输出语言。
	lang i18n.Lang

	// jsonOut 为 true 时所有输出改为 JSON。
	jsonOut bool

	// verbose 为 true 时输出调试日志。
	verbose bool

	// out 是标准输出；测试中可替换。
	out io.Writer

	// logHandler 在守护进程模式下创建，供 daemon 接管。
	logHandler *logx.BusHandler

	// log 是 CLI 自身的日志器。
	log *slog.Logger
}

// New 构造根命令。
func New() *cobra.Command {
	app := &App{out: os.Stdout}

	root := &cobra.Command{
		Use:   "isc",
		Short: "ISC —— 把没有公网 IPv4 的电脑接入公网",
		Long: `ISC（接入编排器）让一台只有动态 IPv6 的普通电脑可以从公网访问。

它跟踪 IPv6 前缀变化、更新动态域名解析、编排防火墙、签发证书，
并通过反向代理把本地服务发布到一个可用的公网端口上。

本命令同时是 CLI 客户端与内核守护进程的入口：
  在终端里执行 isc status 是与运行中的内核通信；
  执行 isc daemon run 则是把当前进程变成内核本身。`,
		SilenceUsage:  true,
		SilenceErrors: true,
		// 子命令自行处理错误输出，这里只负责把错误向上传递。
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return app.init(cmd)
		},
	}

	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false,
		"以 JSON 输出，便于脚本消费")
	root.PersistentFlags().StringP("lang", "L", string(i18n.Default),
		"输出语言：zh-CN 或 en")
	root.PersistentFlags().BoolVarP(&app.verbose, "verbose", "v", false,
		"输出调试日志")
	root.PersistentFlags().String("data-dir", "",
		"覆盖数据目录（默认取平台标准位置，也可用 "+paths.EnvDataDir+" 环境变量）")

	root.AddCommand(
		newVersionCmd(app),
		newStatusCmd(app),
		newDaemonCmd(app),
		newIPCmd(app),
		newDdnsCmd(app),
		newConsoleCmd(app),
		newVerifyCmd(app),
		newDoctorCmd(app),
	)

	return root
}

// Execute 是 cmd/isc 的入口。
func Execute() int {
	root := New()
	if err := root.Execute(); err != nil {
		// 错误已经由各子命令写成了人类可读形式；
		// 这里只保证进程退出码非零，让脚本能判断成败。
		return 1
	}
	return 0
}

// init 在子命令执行前完成共享初始化。
func (a *App) init(cmd *cobra.Command) error {
	flags := cmd.Flags()

	// 数据目录：命令行 > 环境变量 > 平台默认。
	if v, err := flags.GetString("data-dir"); err == nil && v != "" {
		if err := os.Setenv(paths.EnvDataDir, v); err != nil {
			return err
		}
	}

	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	a.paths = p

	if v, err := flags.GetString("lang"); err == nil {
		a.lang = i18n.Parse(v)
	}
	i18n.SetDefault(a.lang)

	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	// 日志写 stderr：stdout 要留给命令的结构化输出，
	// 混在一起会破坏 --json 的可解析性。
	a.logHandler = logx.New(os.Stderr, level, nil)
	a.log = slog.New(a.logHandler)

	return nil
}

// signalContext 返回一个在收到中断信号时取消的 context。
//
// 同时处理 SIGINT 与 SIGTERM：前者是 Ctrl+C，后者是 systemd / launchd
// 停止服务时发的信号 —— 服务场景下 SIGTERM 才是常态。
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	return ctx, cancel
}

// fail 打印错误并返回它，供子命令统一处理。
func (a *App) fail(cmd *cobra.Command, err error) error {
	if errors.Is(err, ErrNotRunning) {
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("cli.daemon_not_running"))
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return err
}
