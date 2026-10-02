package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// newProxyCmd 提供反向代理的命令行入口。
//
// # 它解决什么
//
// 一个家庭网络通常只有**一个**能开放的入口，而用户想跑的服务往往
// 有好几个。反代让这些服务共用一个入口、按域名分流。
func newProxyCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: i18n.T("cli.proxy.short"),
		Long:  i18n.T("cli.proxy.long"),
	}

	cmd.AddCommand(
		newProxyStatusCmd(app),
		newProxyListCmd(app),
		newProxyAddCmd(app),
		newProxyRmCmd(app),
	)
	return cmd
}

func newProxyStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: i18n.T("cli.proxy.status_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var st gen.ProxyStatus
			if err := client.getInto(ctx, "/v1/proxy/status", &st); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, st)
			}

			if st.Running {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.running"),
					st.Port, st.Routes)
			} else {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.proxy.stopped"))
				_, _ = fmt.Fprintln(app.out,
					i18n.T("cli.proxy.stopped_hint"))
			}
			// 失败原因必须显示出来 —— 只写进日志的话，用户在界面上
			// 看到的就是"代理没开"，而不知道为什么。
			if st.Error != nil && *st.Error != "" {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.last_error"), *st.Error)
			}
			return nil
		},
	}
}

func newProxyListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: i18n.T("cli.proxy.routes_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			routes, err := fetchRoutes(ctx, client)
			if err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, routes)
			}
			renderRoutes(app, routes.Items)
			return nil
		},
	}
}

func newProxyAddCmd(app *App) *cobra.Command {
	var (
		upstream string
		label    string
		tls      bool
	)

	cmd := &cobra.Command{
		Use:   i18n.T("cli.proxy.add_use"),
		Short: i18n.T("cli.proxy.add_short"),
		Long:  i18n.T("cli.proxy.add_long"),
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			current, err := fetchRoutes(ctx, client)
			if err != nil {
				return app.fail(cmd, err)
			}

			// 生成一个稳定的 ID：用域名本身比随机值更有用 ——
			// 它出现在日志与错误信息里，域名能让人立刻认出是哪一条。
			id := routeID(args[0])

			// 同 ID 时替换而不是追加：用户重复执行同一个 add 时，
			// 期待的是"确保这条规则存在"，而不是建出两条。
			items := make([]gen.ProxyRoute, 0, len(current.Items)+1)
			replaced := false
			for _, r := range current.Items {
				if r.Id == id {
					replaced = true
					continue
				}
				items = append(items, r)
			}

			labelPtr := &label
			tlsPtr := &tls
			items = append(items, gen.ProxyRoute{
				Id: id, Domains: args, Upstream: upstream,
				Label: labelPtr, Tls: tlsPtr,
			})

			saved, err := putRoutes(ctx, client, items)
			if err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, saved)
			}
			if replaced {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.updated"),
					strings.Join(args, ", "), upstream)
			} else {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.added"),
					strings.Join(args, ", "), upstream)
			}
			renderRoutes(app, saved.Items)
			return nil
		},
	}

	cmd.Flags().StringVar(&upstream, "to", "", i18n.T("cli.proxy.flag_to"))
	cmd.Flags().StringVar(&label, "label", "", i18n.T("cli.proxy.flag_label"))
	cmd.Flags().BoolVar(&tls, "tls", false, i18n.T("cli.proxy.flag_tls"))
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func newProxyRmCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   i18n.T("cli.proxy.rm_use"),
		Short: i18n.T("cli.proxy.rm_short"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			current, err := fetchRoutes(ctx, client)
			if err != nil {
				return app.fail(cmd, err)
			}

			target := args[0]
			items := make([]gen.ProxyRoute, 0, len(current.Items))
			removed := 0
			for _, r := range current.Items {
				// 允许用域名或 ID 指定：用户手边更可能记得域名。
				if r.Id == target || containsString(r.Domains, target) {
					removed++
					continue
				}
				items = append(items, r)
			}

			if removed == 0 {
				return app.fail(cmd, fmt.Errorf(i18n.T("cli.proxy.rm_notfound"), target))
			}

			saved, err := putRoutes(ctx, client, items)
			if err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.removed"), removed)
			if !app.jsonOut {
				renderRoutes(app, saved.Items)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func fetchRoutes(ctx context.Context, client *Client) (gen.ProxyRouteList, error) {
	var routes gen.ProxyRouteList
	err := client.getInto(ctx, "/v1/proxy/routes", &routes)
	return routes, err
}

func putRoutes(ctx context.Context, client *Client, items []gen.ProxyRoute) (gen.ProxyRouteList, error) {
	body, err := json.Marshal(gen.ProxyRouteList{Items: items})
	if err != nil {
		return gen.ProxyRouteList{}, err
	}

	var saved gen.ProxyRouteList
	err = client.putInto(ctx, "/v1/proxy/routes", body, &saved)
	return saved, err
}

func renderRoutes(app *App, items []gen.ProxyRoute) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.proxy.list_empty"))
		_, _ = fmt.Fprintln(app.out,
			i18n.T("cli.proxy.list_empty_hint"))
		return
	}

	_, _ = fmt.Fprintf(app.out, i18n.T("cli.proxy.list_title"), len(items))
	_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
	for _, r := range items {
		name := r.Id
		if r.Label != nil && *r.Label != "" {
			name = *r.Label
		}
		scheme := "http"
		if r.Tls != nil && *r.Tls {
			scheme = "https"
		}
		for i, d := range r.Domains {
			if i == 0 {
				_, _ = fmt.Fprintf(app.out, "  %-7s %-34s → %s\n", scheme, d, r.Upstream)
				continue
			}
			_, _ = fmt.Fprintf(app.out, "  %-7s %-34s\n", "", d)
		}
		if name != r.Id {
			_, _ = fmt.Fprintf(app.out, "          （%s）\n", name)
		}
	}
}

// routeID 由域名生成一个稳定的规则 ID。
func routeID(domain string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(domain) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.':
			b.WriteRune(r)
		case r == '*':
			b.WriteString("wild")
		default:
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-.")
	if id == "" {
		return "route"
	}
	return id
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
