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
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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

	// langExplicit 表示语言是用户用 --lang **显式指定**的。
	//
	// 只有显式指定时才把它带给服务端：没指定时应当跟随内核自己的
	// 语言设置，而"把默认值也发过去"会让内核设置永远不生效。
	langExplicit bool

	// jsonOut 为 true 时所有输出改为 JSON。
	jsonOut bool

	// verbose 为 true 时输出调试日志。
	verbose bool

	// out 是标准输出；测试中可替换。
	out io.Writer

	// in 是标准输入，用于需要交互确认的命令。
	//
	// 与 out 一样可替换，这样确认流程能被测试覆盖到 ——
	// 而"修改系统状态前的确认"恰恰是最需要被测试的一条路径。
	in io.Reader

	// logHandler 在守护进程模式下创建，供 daemon 接管。
	logHandler *logx.BusHandler

	// log 是 CLI 自身的日志器。
	log *slog.Logger
}

// New 构造根命令。
func New() *cobra.Command {
	app := &App{out: os.Stdout, in: os.Stdin}

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
		newCredentialCmd(app),
		newZonesCmd(app),
		newRecordsCmd(app),
		newSettingsCmd(app),
		newDdnsCmd(app),
		newConsoleCmd(app),
		newVerifyCmd(app),
		newExposeCmd(app),
		newProxyCmd(app),
		newCertCmd(app),
		newNotifyCmd(app),
		newInitCmd(app),
		newServiceCmd(app),
		newChangesCmd(app),
		newRollbackCmd(app),
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

	// 语言优先级：--lang（显式）> 内核设置 > 默认值。
	//
	// # 为什么必须读内核设置
	//
	// 早先这里**只**看 --lang，于是用户在内核设置里把语言改成 English
	// 之后会出现三处不一致：
	//
	//	控制台              英文 ✓
	//	服务端生成的内容    英文 ✓
	//	CLI 的输出          中文 ✗（除非每次命令都带 --lang en）
	//
	// 而"我在设置里选过英文了"是用户唯一记得的事 —— 他不会想到还要
	// 在每条命令上再带一次标志。
	if v, err := flags.GetString("lang"); err == nil {
		a.lang = i18n.Parse(v)
	}
	if flags.Changed("lang") {
		a.langExplicit = true
	}
	if !a.langExplicit {
		// 只有**没显式指定**时才去问内核。
		//
		// --lang 是显式覆盖，它不该被内核设置盖掉 —— 那会让
		// "临时用英文看一眼"变成做不到的事。
		if l, ok := a.langFromDaemon(); ok {
			a.lang = l
		}
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

// connect 连接内核，并把**显式指定**的语言带给它。
//
// # 为什么做成方法
//
// 在这之前，`Connect(ctx, app.paths.RuntimeFile())` 在 31 处被逐字重复。
// 那不只是啰嗦：语言需要跟着每一次连接走，而在 31 个地方各加一行
// 意味着漏掉任何一处都会产生"这一条命令的语言不对"这种难查的问题。
func (a *App) connect(ctx context.Context) (*Client, error) {
	client, err := Connect(ctx, a.paths.RuntimeFile())
	if err != nil {
		return nil, err
	}
	if a.langExplicit {
		client.SetLang(string(a.lang))
	}
	return client, nil
}

// langFromDaemon 向运行中的内核询问当前的语言设置。
//
// # 成本
//
// 内核**没在运行时**它立刻返回（Connect 读不到运行时文件就直接
// 返回 ErrNotRunning，不等任何超时），因此 `isc daemon run` 之类的
// 命令不会因此变慢。
//
// 内核在运行时，代价是一次本地往返（命名管道 / 回环），可以忽略。
//
// # 失败一律静默
//
// 问不到就用默认值 —— 语言是**界面偏好**，而为了它让一条命令失败
// 是本末倒置的。
func (a *App) langFromDaemon() (i18n.Lang, bool) {
	if a.paths.RuntimeFile() == "" {
		return "", false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client, err := Connect(ctx, a.paths.RuntimeFile())
	if err != nil {
		return "", false
	}

	var s gen.Settings
	if err := client.getInto(ctx, "/v1/settings", &s); err != nil {
		return "", false
	}
	if s.Lang == "" {
		return "", false
	}
	return i18n.Parse(string(s.Lang)), true
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
