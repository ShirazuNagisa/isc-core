package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

// 本文件提供 IP 状态与动态解析任务的命令行入口。
//
// 依据 docs/DECISIONS.md D19：内核没有 GUI，CLI 与验证控制台是开发与运维
// 全过程唯一的手和眼睛。因此"能看 IP、能列任务、能手动跑一次"必须能在
// 命令行里完成，而不是只能通过 HTTP 接口。

func newIPCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "ip",
		Short: "查看当前网卡地址与 IPv6 前缀",
		Long: `查看当前可用于解析的地址。

IPv6 前缀是本产品的核心概念：ISP 重拨后变化的是整个 /64 前缀，
该前缀下的所有 AAAA 记录都要重写，而不是只改一个地址。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var status gen.IPStatus
			if err := client.getInto(ctx, "/v1/ip/current", &status); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, status)
			}

			if len(status.Interfaces) == 0 {
				_, _ = fmt.Fprintln(app.out, "没有找到可用于解析的网卡。")
				return nil
			}
			for _, iface := range status.Interfaces {
				up := "down"
				if iface.IsUp != nil && *iface.IsUp {
					up = "up"
				}
				_, _ = fmt.Fprintf(app.out, "%s (%s)\n", iface.Name, up)
				printList(app.out, "ipv4", iface.Ipv4)
				printList(app.out, "ipv6", iface.GlobalIpv6)
				printList(app.out, "prefix", iface.Prefixes)
			}
			return nil
		},
	}
}

func newDdnsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ddns",
		Short: "管理动态解析任务",
		Long: `管理动态解析任务。

一条任务 = 一组凭据 + 一组域名 + 一组地址来源。调度器会在地址变化时
立刻执行，并按固定周期兜底重试。`,
	}

	cmd.AddCommand(
		newDdnsListCmd(app),
		newDdnsAddCmd(app),
		newDdnsRemoveCmd(app),
		newDdnsRunCmd(app),
	)
	return cmd
}

func newDdnsListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出全部动态解析任务",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.DdnsTaskList
			if err := client.getInto(ctx, "/v1/ddns-tasks", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}
			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, "还没有配置任何动态解析任务。")
				return nil
			}
			for _, t := range list.Items {
				_, _ = fmt.Fprintf(app.out, "%s  %s\n", mark(t.Enabled), t.Label)
				_, _ = fmt.Fprintf(app.out, "    id        %s\n", t.Id)
				_, _ = fmt.Fprintf(app.out, "    状态      %s\n", describeStatus(t))
				if t.LastMessage != nil && *t.LastMessage != "" {
					_, _ = fmt.Fprintf(app.out, "    说明      %s\n", *t.LastMessage)
				}
				if t.Ipv4.Enable {
					_, _ = fmt.Fprintf(app.out, "    IPv4      %s → %s\n",
						t.Ipv4.GetType, strings.Join(t.Ipv4.Domains, ", "))
				}
				if t.Ipv6.Enable {
					_, _ = fmt.Fprintf(app.out, "    IPv6      %s → %s\n",
						t.Ipv6.GetType, strings.Join(t.Ipv6.Domains, ", "))
				}
			}
			return nil
		},
	}
}

func newDdnsRunCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "run <任务ID>",
		Short: "立即执行一次任务（忽略防抖）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			if err := client.post(ctx, "/v1/ddns-tasks/"+args[0]+"/run", nil); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]string{
					"task_id": args[0], "result": "accepted",
				})
			}
			_, _ = fmt.Fprintf(app.out,
				"已受理（任务 %s）。执行结果请用 'isc ddns list' 查看。\n", args[0])
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func mark(enabled bool) string {
	if enabled {
		return "[启用]"
	}
	return "[停用]"
}

func describeStatus(t gen.DdnsTask) string {
	if t.LastStatus == nil || *t.LastStatus == gen.DdnsStatusEmpty {
		return "从未执行"
	}
	if t.LastRunAt != nil {
		return fmt.Sprintf("%s（%s）", string(*t.LastStatus),
			t.LastRunAt.Local().Format("2006-01-02 15:04:05"))
	}
	return string(*t.LastStatus)
}

func printList(w io.Writer, label string, list *[]string) {
	if list == nil || len(*list) == 0 {
		return
	}
	for i, v := range *list {
		prefix := "    "
		if i > 0 {
			prefix = "        "
		}
		_, _ = fmt.Fprintf(w, "%s%-7s %s\n", prefix, label, v)
	}
}

func writeJSONOut(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
