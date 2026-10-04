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
	"strings"
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

// New 构造根命令（供测试使用）。
//
// 注意：**语言必须在调用它之前定好**。Short / Long / 标志说明都是
// i18n.T(...) 的结果，而在函数返回时它们就已经被求值了 —— 之后再改
// 语言不会影响已经构造好的命令。
//
// 生产入口是 Execute，它保证了那个顺序。测试里如果不关心语言，
// 直接调 New 即可。
func New() *cobra.Command {
	return newWithApp(&App{out: os.Stdout, in: os.Stdin})
}

// newWithApp 用给定的 App 构造根命令。
func newWithApp(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:           "isc",
		Short:         i18n.T("cli.root.short"),
		Long:          i18n.T("cli.root.long"),
		SilenceUsage:  true,
		SilenceErrors: true,
		// 子命令自行处理错误输出，这里只负责把错误向上传递。
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return app.init(cmd)
		},
	}

	root.PersistentFlags().BoolVar(&app.jsonOut, "json", false,
		i18n.T("cli.flag.json"))
	root.PersistentFlags().StringP("lang", "L", string(i18n.Default),
		i18n.T("cli.flag.lang"))
	root.PersistentFlags().BoolVarP(&app.verbose, "verbose", "v", false,
		i18n.T("cli.flag.verbose"))
	root.PersistentFlags().String("data-dir", "",
		i18n.T("cli.flag.data_dir", paths.EnvDataDir))

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
		newRemoteCmd(app),
		newChangesCmd(app),
		newRollbackCmd(app),
		newDoctorCmd(app),
	)

	return root
}

// Execute 是 cmd/isc 的入口。
func Execute() int {
	app := &App{out: os.Stdout, in: os.Stdin}

	// **语言必须在构造命令树之前定好。**
	//
	// # 为什么要提前到这一步
	//
	// Short / Long / 标志说明都是 `i18n.T(...)` 的结果，而它们在 New()
	// 里就被求值了。更要紧的是 cobra 处理 `--help` 发生在
	// PersistentPreRunE **之前** —— 因此把语言解析放在 init 里，
	// 对帮助文本永远来不及。
	//
	// 症状是：`--lang en isc credential add --help` 的子命令输出已经是
	// 英文，而帮助正文仍是中文。帮助正文恰恰是用户最先看到的东西。
	app.resolveLangEarly(os.Args[1:])

	root := newWithApp(app)
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
	// 语言通常已经由 resolveLangEarly 定好了（它必须跑在构造命令树
	// 之前，否则帮助文本来不及）。这里只处理一种它覆盖不到的情况：
	// 调用方直接用了 New() 而没有走 Execute（测试就是这样）。
	if a.lang == "" {
		if v, err := flags.GetString("lang"); err == nil && v != "" {
			a.lang = i18n.Parse(v)
			a.langExplicit = true
		} else if l, ok := a.langFromDaemon(); ok {
			a.lang = l
		} else {
			a.lang = i18n.Default
		}
		i18n.SetDefault(a.lang)
	}

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

// resolveLangEarly 在**构造命令树之前**确定语言。
//
// 优先级与 init 里一致（--lang > 内核设置 > 默认值），区别只是时机：
// 这里跑在 New() 之前，让帮助文本也能用上正确的语言。
//
// 它自己解析 --data-dir，因为"内核设置"要从运行时文件所在的数据目录
// 里找 —— 而那个目录正是 --data-dir 决定的。
func (a *App) resolveLangEarly(args []string) {
	// 数据目录：命令行 > 环境变量 > 平台默认。
	if v := flagValue(args, "data-dir"); v != "" {
		_ = os.Setenv(paths.EnvDataDir, v)
	}
	p, err := paths.Resolve()
	if err == nil {
		a.paths = p
	}

	// 显式的 --lang 优先。
	if v := flagValue(args, "lang"); v != "" {
		a.lang = i18n.Parse(v)
		a.langExplicit = true
		i18n.SetDefault(a.lang)
		return
	}

	// 否则跟随内核设置；问不到就用默认值。
	if l, ok := a.langFromDaemon(); ok {
		a.lang = l
	} else {
		a.lang = i18n.Default
	}
	i18n.SetDefault(a.lang)
}

// flagValue 从参数列表里取一个字符串标志的值。
//
// 支持 `--name value` 与 `--name=value` 两种写法 —— cobra 两种都认，
// 而这里必须在 cobra 之前跑，所以只能自己解析。
//
// 只做**够用**的解析：不处理组合短标志、不管 `--` 分隔符。这里要的
// 是两个已知的长标志，而复杂化只会引入新的解析 bug。
func flagValue(args []string, name string) string {
	long := "--" + name
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, long+"="); ok {
			return v
		}
		if a == long && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
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

// systemDirHint 返回"你可能找错了地方"的提示；不需要提示时返回空串。
//
// # 它修的是什么
//
// 内核以系统服务身份运行时数据落在**系统目录**（D20：macOS 是
// /Library/Application Support/ISC，Linux 是 /var/lib/isc），而普通用户
// 跑 `isc status` 时用的是自己的回退目录。于是 CLI 会理直气壮地说
// "内核未运行。请先执行 'isc daemon run' 或安装为系统服务。" ——
// 而用户很可能**已经**装成系统服务了，只是他看的是另一个目录。
//
// 真机上就是这么发生的（2026-10-03，macOS）。这一句提示不解决问题，
// 但它把"没有内核"与"有内核但你看不到"区分开了 —— 而这两件事需要
// 完全不同的下一步动作。
//
// 纯函数，便于测试：不做 IO，只根据三个入参决定说不说。
func systemDirHint(currentRoot, sysRoot string, sysExists bool) string {
	// currentRoot 为空说明路径还没解析过 —— 那时提示只会造成困扰。
	if !sysExists || sysRoot == "" || currentRoot == "" || sysRoot == currentRoot {
		return ""
	}
	return i18n.T("cli.hint_system_dir", sysRoot)
}

// hintSystemDir 把 systemDirHint 的结论打出来（没可说的就什么都不打）。
func (a *App) hintSystemDir(w io.Writer) {
	sys, ok := paths.SystemDataDir()
	if msg := systemDirHint(a.paths.Root(), sys, ok); msg != "" {
		fmt.Fprintln(w, msg)
	}
}

// fail 打印错误并返回它，供子命令统一处理。
func (a *App) fail(cmd *cobra.Command, err error) error {
	if errors.Is(err, ErrNotRunning) {
		fmt.Fprintln(cmd.ErrOrStderr(), i18n.T("cli.daemon_not_running"))
		a.hintSystemDir(cmd.ErrOrStderr())
		return err
	}
	fmt.Fprintln(cmd.ErrOrStderr(), err)
	return err
}
