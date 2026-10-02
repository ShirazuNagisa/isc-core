package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Short: "查看与管理 TLS 证书",
		Long: `查看 TLS 证书的状态，或手动触发一次续期。

证书由内核自动申请与续期：到期前 1/3 寿命时进入续期窗口
（对 90 天的证书即提前 30 天）。因此正常情况下不需要手动干预。

"需要续期"后面会给出**理由** —— 一类是快过期了，另一类是
"现有证书不覆盖某个新加的域名"，而后者与剩余有效期无关。`,
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
		Short: "列出证书与续期状态",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
		Short: "立即检查并为全部 HTTPS 路由申请（或续期）证书",
		Long: `立即触发一次证书检查与签发。

它是**幂等**的：已经有效的证书不会被重新签发 —— 那会白白消耗
ACME 的失败配额（生产环境每小时 5 次）。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
			_, _ = fmt.Fprintln(app.out, "已触发一次证书检查。")
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
		_, _ = fmt.Fprintln(w, "还没有任何证书。")
		_, _ = fmt.Fprintln(w,
			"为一条路由启用 HTTPS（isc proxy add ... --tls）之后，内核会自动申请证书。")
		return
	}

	_, _ = fmt.Fprintf(w, "证书（%d）\n", len(items))
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
			_, _ = fmt.Fprintf(w, "      覆盖: %s\n", domains)
		}

		if c.ExpiresAt != nil {
			_, _ = fmt.Fprintf(w, "      有效期至: %s（还剩 %d 天）\n",
				c.ExpiresAt.Local().Format("2006-01-02"),
				daysUntil(*c.ExpiresAt))
		}

		// 测试环境的证书必须显著标出 —— 它**不被浏览器信任**，
		// 而用户在界面上只会看到"证书无效"。
		if c.Staging != nil && *c.Staging {
			_, _ = fmt.Fprintln(w,
				"      ⚠ 这张证书来自 ACME **测试环境**，浏览器不会信任它。")
			_, _ = fmt.Fprintln(w,
				"        要拿到正式证书，请把 acme_directory 清空后重新续期。")
		}

		if c.NeedsRenew && c.Reason != nil && *c.Reason != "" {
			_, _ = fmt.Fprintf(w, "      需要续期: %s\n", *c.Reason)
		}
		if c.Error != nil && *c.Error != "" {
			_, _ = fmt.Fprintf(w, "      上次失败: %s\n", *c.Error)
		}
	}
}

// daysUntil 返回距离某个时刻还有多少天（负数表示已过期）。
func daysUntil(t time.Time) int {
	return int(time.Until(t).Hours() / 24)
}
