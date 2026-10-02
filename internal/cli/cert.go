package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// newCertCmd 提供证书的查询与手动续期。
//
// 正常情况下证书会在到期前**自动续期**，这个命令主要用于两件事：
//
//	刚加了一条 HTTPS 路由，想立刻拿到证书而不想等下一轮检查；
//	想看看到底哪张证书快过期了、上次为什么没签成。
func newCertCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cert",
		Short: i18n.T("cli.cert.short"),
		Long:  i18n.T("cli.cert.long"),
	}

	cmd.AddCommand(
		newCertListCmd(app),
		newCertRenewCmd(app),
	)
	return cmd
}

func newCertListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: i18n.T("cli.cert.list_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			list, err := fetchCerts(ctx, client)
			if err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}
			renderCerts(app, list.Items)
			return nil
		},
	}
}

func newCertRenewCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "renew",
		Short: i18n.T("cli.cert.renew_short"),
		Long:  i18n.T("cli.cert.renew_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.CertList
			if err := client.postInto(ctx, "/v1/certs/renew", nil, &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.cert.renew_triggered"))
			renderCerts(app, list.Items)
			return nil
		},
	}
}

func fetchCerts(ctx context.Context, client *Client) (gen.CertList, error) {
	var list gen.CertList
	err := client.getInto(ctx, "/v1/certs", &list)
	return list, err
}

func renderCerts(app *App, items []gen.CertStatus) {
	w := app.out

	if len(items) == 0 {
		_, _ = fmt.Fprintln(w, i18n.T("cli.cert.list_empty"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.cert.list_empty_hint"))
		return
	}

	_, _ = fmt.Fprintf(w, i18n.T("cli.cert.list_title"), len(items))
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 66))

	for _, c := range items {
		icon := "✅"
		if c.NeedsRenew {
			icon = "⚠️ "
		}

		domains := ""
		if c.Domains != nil {
			domains = strings.Join(*c.Domains, ", ")
		}

		_, _ = fmt.Fprintf(w, "  %s %s\n", icon, c.Name)
		if domains != "" {
			_, _ = fmt.Fprintf(w, i18n.T("cli.cert.covers"), domains)
		}

		if c.ExpiresAt != nil {
			_, _ = fmt.Fprintf(w, i18n.T("cli.cert.valid_until"),
				c.ExpiresAt.Local().Format("2006-01-02"),
				daysUntil(*c.ExpiresAt))
		}

		// 测试环境的证书必须显著标出 —— 它**不被浏览器信任**，
		// 而用户在界面上只会看到"证书无效"。
		if c.Staging != nil && *c.Staging {
			_, _ = fmt.Fprintln(w, i18n.T("cli.cert.staging_warn"))
			_, _ = fmt.Fprintln(w, i18n.T("cli.cert.staging_hint"))
		}

		if c.NeedsRenew && c.Reason != nil && *c.Reason != "" {
			_, _ = fmt.Fprintf(w, i18n.T("cli.cert.needs_renewal"), *c.Reason)
		}
		if c.Error != nil && *c.Error != "" {
			_, _ = fmt.Fprintf(w, i18n.T("cli.cert.last_failure"), *c.Error)
		}
	}
}

// daysUntil 返回距离某个时刻还有多少天（负数表示已过期）。
func daysUntil(t time.Time) int {
	return int(time.Until(t).Hours() / 24)
}
