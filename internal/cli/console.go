package cli

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
)

// newConsoleCmd 提供打开验证控制台的入口。
//
// # 为什么需要这个命令
//
// 控制台只监听回环 TCP，端口是内核启动时随机分配的。用户没法凭记忆
// 打开它 —— 端口写在 runtime.json 里，而让用户去翻那个文件是荒谬的。
//
// 命令本身不碰网络：它只读 runtime.json 并拼出地址。
// 因此在内核刚起、控制台还没法用时它照样能给出正确地址。
func newConsoleCmd(app *App) *cobra.Command {
	var open bool

	cmd := &cobra.Command{
		Use:   "console",
		Short: i18n.T("cli.console.short"),
		Long:  i18n.T("cli.console.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info, err := runtimeinfo.Read(app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, fmt.Errorf(
					i18n.T("cli.console.not_running"), err))
			}

			// 控制台必须走回环 TCP：浏览器无法访问命名管道。
			//
			// 内核始终同时监听管道与 TCP（见 docs/DECISIONS.md D08），
			// 因此正常情况下 fallback 一定存在；缺失时说明内核版本不对。
			if info.FallbackEndpoint == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.console.no_tcp")))
			}
			ep := platform.Endpoint(info.FallbackEndpoint)
			if ep.Scheme() != platform.SchemeTCP {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.console.not_tcp", ep)))
			}

			url := ep.HTTPBaseURL() + "/console/"

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]string{
					"url":      url,
					"endpoint": string(ep),
					"token":    info.Token,
				})
			}

			_, _ = fmt.Fprintln(app.out, url)
			if !open {
				_, _ = fmt.Fprint(app.out, i18n.T("cli.console.open_hint"))
				return nil
			}
			return openBrowser(url)
		},
	}

	cmd.Flags().BoolVar(&open, "open", false, i18n.T("cli.console.open_flag"))
	return cmd
}

// openBrowser 用系统默认浏览器打开一个地址。
//
// 刻意不等待命令结束：浏览器会一直运行到用户关掉它，
// 等待会让 isc console --open 看起来像是卡住了。
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		// 用 rundll32 而不是 `cmd /c start`：后者会把 URL 里的 & 当成
		// 命令分隔符，而带查询串的 URL 里 & 很常见。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf(i18n.T("cli.console.open_failed"), err)
	}
	// 回收子进程，避免留下僵尸。
	go func() { _ = cmd.Wait() }()
	return nil
}
