package platform

import (
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"path"
	"path/filepath"
	"strings"
)

// 本文件是**服务定义文件的内容生成**，不含任何平台调用。
//
// # 为什么单独放在一个没有构建标签的文件里
//
// 生成 unit / plist 的内容是纯字符串逻辑，而它恰恰是最容易出错的部分：
//
//	引号     含空格的路径不加引号会被拆成两个参数
//	转义     plist 是 XML，路径里的 `&` 会让它无法解析
//	注入     参数里的换行符会越出那一行配置
//
// 而"写文件、执行 systemctl/launchctl"才是真正平台相关的部分。
//
// 分开之后，内容生成可以在**任何平台**上被测试 —— 否则在 Windows 上
// 开发时，Linux 与 macOS 的这部分逻辑只能靠肉眼审查，而它们恰恰是
// 本机无法真机验证的两块（见 docs/PLAN.md R8）。

// 服务标识。三平台用同一个名字，便于文档与脚本跨平台复用。
//
// 名字与 secret_unix.go 里的密钥库标识一致（都是 "isc-core"），
// 但那是**巧合而非依赖** —— 密钥库标识只出现在类 Unix 平台，
// 而服务标识三个平台都要用。
const (
	coreServiceName    = "isc-core"
	darwinServiceLabel = "com.isc.core"

	// 下面是各平台的 unit / plist 名字与落点。
	//
	// 它们放在**共享文件**里（而不是各自的平台文件）有两个理由：
	//
	//  1. 它们只是取值不同，本身与平台无关；
	//  2. CI 的 service-linux / service-macos 两个 job 会去找这两个路径 ——
	//     放在共享文件里，一条测试就能在**任何平台**钉住它们。
	//     真实踩过的坑：CI 里把 unit 名写成了 isc.service（实为
	//     isc-core.service），于是 `systemctl show` 什么都不输出，
	//     断言拿着空路径去 cat。
	linuxSystemdDir   = "/etc/systemd/system"
	linuxUnitFileName = coreServiceName + ".service"

	darwinLaunchDir = "/Library/LaunchDaemons"
	darwinLabel     = darwinServiceLabel
	darwinPlistName = darwinLabel + ".plist"
)

// ---------------------------------------------------------------------------
// systemd
// ---------------------------------------------------------------------------

// RenderSystemdUnit 生成 systemd 单元文件内容。
func RenderSystemdUnit(cfg ServiceConfig) string {
	args := make([]string, 0, len(cfg.Arguments))
	for _, a := range cfg.Arguments {
		args = append(args, quoteSystemdArg(a))
	}

	workDir := cfg.WorkingDirectory
	if workDir == "" {
		// 用 path.Dir 而不是 filepath.Dir。
		//
		// systemd 单元里的路径**永远是 Unix 路径**，与生成它的机器
		// 无关。而 filepath 用的是宿主操作系统的分隔符 ——
		// 在 Windows 上开发时，filepath.Dir("/usr/local/bin/isc")
		// 会得到 `\usr\local\bin`，写进单元文件就是一条无效路径。
		//
		// 这个错误是**测试抓到的**，而不是肉眼审查 ——
		// 它恰好说明了为什么要把这段逻辑做成可跨平台测试的。
		workDir = path.Dir(cfg.Executable)
	}

	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=" + serviceDescription(cfg) + "\n")
	// 等网络真正可用再启动。
	//
	// 内核依赖网络，而 network-online.target 是 systemd 提供的
	// "网络已就绪"信号。少了它，开机时第一轮地址检测会拿到空结果。
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n")

	b.WriteString("\n[Service]\n")
	b.WriteString("Type=simple\n")

	execStart := quoteSystemdArg(cfg.Executable)
	if len(args) > 0 {
		execStart += " " + strings.Join(args, " ")
	}
	b.WriteString("ExecStart=" + execStart + "\n")
	b.WriteString("WorkingDirectory=" + workDir + "\n")

	if cfg.RestartOnFailure {
		// on-failure 而不是 always：用户主动 stop 时不该被拉起来。
		b.WriteString("Restart=on-failure\n")
		b.WriteString("RestartSec=5\n")

		// 5 分钟内最多重启 5 次，超过就放弃并把状态标成 failed。
		//
		// 这条限制很重要：没有它的话，一个必然启动失败的配置会让
		// 系统日志被刷满，而用户完全看不出"它在反复重启"。
		b.WriteString("StartLimitIntervalSec=300\n")
		b.WriteString("StartLimitBurst=5\n")
	}

	// 让内核能被 systemd 干净地停掉（SIGTERM 触发优雅关闭）。
	b.WriteString("KillSignal=SIGTERM\n")
	// 给优雅关闭留足时间：内核要在退出前把在途请求收尾、
	// 把运行时文件删掉。
	b.WriteString("TimeoutStopSec=20\n")

	b.WriteString("\n[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return b.String()
}

// quoteSystemdArg 按 systemd 的规则给参数加引号。
//
// systemd 的 ExecStart 按**它自己的规则**分词，而不是交给 shell。
// 含空格的参数必须用双引号包起来，否则会被拆成两个参数 ——
// 而症状是"服务启动失败"，看不出根因是路径里的空格。
func quoteSystemdArg(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	// systemd 用反斜杠转义，与 shell 的规则不同。
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
	)
	return `"` + replacer.Replace(s) + `"`
}

// ---------------------------------------------------------------------------
// launchd
// ---------------------------------------------------------------------------

// RenderLaunchdPlist 生成 launchd plist 内容。
//
// 手写 XML 而不是引一个 plist 库：需要的字段很少，而多一个依赖
// 只为生成十几行固定结构的 XML 不划算。
func RenderLaunchdPlist(cfg ServiceConfig) string {
	args := []string{cfg.Executable}
	args = append(args, cfg.Arguments...)

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" ` +
		`"http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")

	b.WriteString("  <key>Label</key>\n")
	b.WriteString("  <string>" + xmlEscape(darwinServiceLabel) + "</string>\n")

	b.WriteString("  <key>ProgramArguments</key>\n")
	b.WriteString("  <array>\n")
	for _, a := range args {
		b.WriteString("    <string>" + xmlEscape(a) + "</string>\n")
	}
	b.WriteString("  </array>\n")

	if cfg.WorkingDirectory != "" {
		b.WriteString("  <key>WorkingDirectory</key>\n")
		b.WriteString("  <string>" + xmlEscape(cfg.WorkingDirectory) + "</string>\n")
	}

	if cfg.AutoStart {
		// RunAtLoad 让它在系统启动时就跑起来。
		b.WriteString("  <key>RunAtLoad</key>\n  <true/>\n")
	}

	if cfg.RestartOnFailure {
		// SuccessfulExit=false：只在**异常**退出时重启。
		//
		// 用笼统的 KeepAlive=true 会让用户主动 stop 之后服务立刻
		// 被拉起来 —— 那是"我明明停掉了它"这类困惑的来源。
		b.WriteString("  <key>KeepAlive</key>\n  <dict>\n")
		b.WriteString("    <key>SuccessfulExit</key>\n    <false/>\n")
		b.WriteString("  </dict>\n")

		// ThrottleInterval 限制重启频率（秒）。
		//
		// 一个必然启动失败的配置会变成无限重启，而它会把系统日志
		// 刷满 —— 10 秒的下限让那种情况至少不会拖垮机器。
		b.WriteString("  <key>ThrottleInterval</key>\n  <integer>10</integer>\n")
	}

	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.String()
}

// xmlEscape 转义 XML 里的特殊字符。
//
// 路径里出现 `&` 或 `<` 是可能的（尤其在用户自定的数据目录里），
// 而不转义会让 plist 无法解析 —— 报错是"格式错误"，
// 完全看不出根因是路径里的一个字符。
func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

// ---------------------------------------------------------------------------
// 共用的校验
// ---------------------------------------------------------------------------

func serviceDescription(cfg ServiceConfig) string {
	if cfg.Description != "" {
		return cfg.Description
	}
	return i18n.T("platform.unit_description")
}

// ValidateServiceConfig 在动手之前检查配置。
//
// # 它校验的是**当前主机**的配置
//
// 因此用 filepath 而不是 path：Windows 上的合法路径是 `C:\...`，
// 而 `/usr/local/bin/isc` 在 Windows 上**不是**绝对路径 —— 那是正确
// 的判断，因为用 Unix 路径装不出一个能用的 Windows 服务。
//
// 与之相对，RenderSystemdUnit / RenderLaunchdPlist 生成的是**目标
// 平台**的配置文件，因此那里用 path（见 RenderSystemdUnit 的说明）。
//
// 要挡的几件事在三个平台上都成立：绝对路径、不含引号、不含换行。
func ValidateServiceConfig(cfg ServiceConfig) error {
	if strings.TrimSpace(cfg.Executable) == "" {
		return errors.New(i18n.T("platform.unit_no_exe"))
	}
	// 服务要求**绝对路径**。
	//
	// 相对路径在服务启动时的解析基准与用户 shell 不同（Windows 上是
	// System32，systemd 上是 /），而可执行文件显然不在那里 ——
	// 症状是"服务装好了但一启动就退出"，错误信息还很含糊。
	if !filepath.IsAbs(cfg.Executable) {
		return fmt.Errorf(
			i18n.T("platform.unit_exe_abs"),
			cfg.Executable)
	}

	for _, s := range append([]string{cfg.Executable}, cfg.Arguments...) {
		// 换行符会**越出那一行配置**，把后面的内容变成新的指令 ——
		// 对 unit 文件与 plist 都是一个改写文件其余部分的注入点。
		if strings.ContainsAny(s, "\n\r") {
			return fmt.Errorf(i18n.T("platform.unit_arg_newline"), s)
		}
		// 引号会破坏命令行解析。
		if strings.Contains(s, `"`) {
			return fmt.Errorf(i18n.T("platform.unit_arg_quote"), s)
		}
	}

	if cfg.WorkingDirectory != "" {
		if !filepath.IsAbs(cfg.WorkingDirectory) {
			return fmt.Errorf(
				i18n.T("platform.unit_dir_abs"), cfg.WorkingDirectory)
		}
		if strings.ContainsAny(cfg.WorkingDirectory, "\n\r") {
			return errors.New(i18n.T("platform.unit_dir_newline"))
		}
	}

	return nil
}
