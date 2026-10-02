package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// newNotifyCmd 提供通知通道的查看与测试。
//
// "我的通知到底发出去了没有"是配置通道时最常被问到的问题，
// 因此投递记录与测试发送是这两个子命令的重点。
func newNotifyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: i18n.T("cli.notify.short"),
		Long:  i18n.T("cli.notify.long"),
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
		Short: i18n.T("cli.notify.list_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.notify.list_empty"))
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.notify.log_always"))
				return nil
			}

			for _, c := range list.Items {
				state := i18n.T("cli.notify.enabled")
				if c.Enabled != nil && !*c.Enabled {
					state = i18n.T("cli.notify.disabled")
				}
				_, _ = fmt.Fprintf(app.out, "[%s] %s（%s）\n", state, c.Name, c.Kind)
				if c.Url != nil && *c.Url != "" {
					_, _ = fmt.Fprintf(app.out, "    %s %s\n",
						derefOr(c.Method, "POST"), *c.Url)
				}
				if c.MinSeverity != nil && *c.MinSeverity != "info" {
					_, _ = fmt.Fprintf(app.out,
						i18n.T("cli.notify.min_severity"), *c.MinSeverity)
				}
				if c.BodyTemplate != nil && *c.BodyTemplate != "" {
					_, _ = fmt.Fprintln(app.out, i18n.T("cli.notify.custom_body"))
				}
			}
			return nil
		},
	}
}

func newNotifyDeliveriesCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "deliveries",
		Short: i18n.T("cli.notify.deliveries_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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
		Short: i18n.T("cli.notify.test_short"),
		Long:  i18n.T("cli.notify.test_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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

			_, _ = fmt.Fprintln(app.out, i18n.T("cli.notify.sent"))
			renderDeliveries(app, list.Items)
			return nil
		},
	}
}

func renderDeliveries(app *App, items []gen.NotifyDelivery) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.notify.no_deliveries"))
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
