package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

// newNotifyCmd 提供通知通道的查看与测试。
//
// "我的通知到底发出去了没有"是配置通道时最常被问到的问题，
// 因此投递记录与测试发送是这两个子命令的重点。
func newNotifyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "查看与测试通知通道",
		Long: `查看通知通道与最近的投递结果，或发一条测试通知。

配置通道用接口（PUT /v1/notify/channels）—— 通道的字段较多
（地址、请求头、请求体模板），命令行不适合编辑它们。

通道的去重与静默期由内核统一处理：同一个去重键在 5 分钟内只发一条，
静默期过后若期间有被抑制的消息，会补发一条汇总。`,
	}

	cmd.AddCommand(
		newNotifyListCmd(app),
		newNotifyDeliveriesCmd(app),
		newNotifyTestCmd(app),
	)
	return cmd
}

func newNotifyListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出通知通道",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.NotifyChannelList
			if err := client.getInto(ctx, "/v1/notify/channels", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, "还没有配置任何通知通道。")
				_, _ = fmt.Fprintln(app.out,
					"（日志通道始终可用，通知会出现在 isc daemon 的日志里。）")
				return nil
			}

			for _, c := range list.Items {
				state := "启用"
				if c.Enabled != nil && !*c.Enabled {
					state = "停用"
				}
				_, _ = fmt.Fprintf(app.out, "[%s] %s（%s）\n", state, c.Name, c.Kind)
				if c.Url != nil && *c.Url != "" {
					_, _ = fmt.Fprintf(app.out, "    %s %s\n",
						derefOr(c.Method, "POST"), *c.Url)
				}
				if c.MinSeverity != nil && *c.MinSeverity != "info" {
					_, _ = fmt.Fprintf(app.out, "    仅在 %s 及以上时发送\n", *c.MinSeverity)
				}
				if c.BodyTemplate != nil && *c.BodyTemplate != "" {
					_, _ = fmt.Fprintln(app.out, "    （使用自定义请求体模板）")
				}
			}
			return nil
		},
	}
}

func newNotifyDeliveriesCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "deliveries",
		Short: "列出最近的通知投递结果",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.NotifyDeliveryList
			if err := client.getInto(ctx, "/v1/notify/deliveries", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}
			renderDeliveries(app, list.Items)
			return nil
		},
	}
}

func newNotifyTestCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "test",
		Short: "向全部通道发送一条测试通知",
		Long: `立刻向全部通道发一条测试消息。

它**绕过去重与队列**：你点了之后应当立刻看到结果，
而不是等下一个投递循环。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.NotifyDeliveryList
			if err := client.postInto(ctx, "/v1/notify/test", []byte("{}"), &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			_, _ = fmt.Fprintln(app.out, "测试通知已发送：")
			renderDeliveries(app, list.Items)
			return nil
		},
	}
}

func renderDeliveries(app *App, items []gen.NotifyDelivery) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(app.out, "还没有任何投递记录。")
		return
	}

	for _, d := range items {
		icon := "✅"
		if !d.Ok {
			icon = "❌"
		}
		_, _ = fmt.Fprintf(app.out, "  %s %s（%s）  %s\n",
			icon, d.Channel, d.Kind,
			d.At.Local().Format("2006-01-02 15:04:05"))

		// 失败原因必须显示出来 —— 那是用户唯一能据此行动的线索。
		if !d.Ok && d.Error != nil && *d.Error != "" {
			_, _ = fmt.Fprintf(app.out, "      %s\n", wrapText(*d.Error, "      "))
		}
	}
}

func derefOr(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}
