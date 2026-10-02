package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Short: "管理反向代理",
		Long: `管理反向代理的转发规则。

规则把域名映射到本机服务，例如：

    home.example.com   →  127.0.0.1:8096
    *.lab.example.com  →  127.0.0.1:3000

上游**必须是本机或内网地址**。反代监听在公网上，若允许任意上游，
任何人都能拿它当跳板 —— 而所有流量都记在你头上。

注意通配的规则：*.example.com 只匹配**一级**子域名，
不匹配 example.com 本身，也不匹配 a.b.example.com。
这与 TLS 证书的通配规则一致。`,
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
		Short: "查看反向代理状态",
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
				_, _ = fmt.Fprintf(app.out, "运行中：监听端口 %d，%d 条规则\n",
					st.Port, st.Routes)
			} else {
				_, _ = fmt.Fprintln(app.out, "未运行。")
				_, _ = fmt.Fprintln(app.out,
					"在设置中开启「反向代理」并指定监听端口即可启动。")
			}
			// 失败原因必须显示出来 —— 只写进日志的话，用户在界面上
			// 看到的就是"代理没开"，而不知道为什么。
			if st.Error != nil && *st.Error != "" {
				_, _ = fmt.Fprintf(app.out, "\n最近一次失败：%s\n", *st.Error)
			}
			return nil
		},
	}
}

func newProxyListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出全部转发规则",
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
		Use:   "add <域名> [域名...] --to <上游>",
		Short: "添加一条转发规则",
		Long: `把一组域名指向一个本机服务。

上游要写全 host:port，例如 127.0.0.1:8096。
不写端口会被拒绝 —— 猜端口会转发到意想不到的服务上，
而那种问题很难被发现。`,
		Args: cobra.MinimumNArgs(1),
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
				_, _ = fmt.Fprintf(app.out, "已更新：%s → %s\n",
					strings.Join(args, ", "), upstream)
			} else {
				_, _ = fmt.Fprintf(app.out, "已添加：%s → %s\n",
					strings.Join(args, ", "), upstream)
			}
			renderRoutes(app, saved.Items)
			return nil
		},
	}

	cmd.Flags().StringVar(&upstream, "to", "", "上游地址，例如 127.0.0.1:8096（必填）")
	cmd.Flags().StringVar(&label, "label", "", "规则的可读名称")
	cmd.Flags().BoolVar(&tls, "tls", false, "该域名使用 HTTPS")
	_ = cmd.MarkFlagRequired("to")

	return cmd
}

func newProxyRmCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <域名或规则ID>",
		Short: "删除一条转发规则",
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
				return app.fail(cmd, fmt.Errorf("没有找到匹配 %q 的转发规则", target))
			}

			saved, err := putRoutes(ctx, client, items)
			if err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, "已删除 %d 条规则。\n", removed)
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
		_, _ = fmt.Fprintln(app.out, "还没有配置任何转发规则。")
		_, _ = fmt.Fprintln(app.out,
			"用 'isc proxy add home.example.com --to 127.0.0.1:8096' 添加一条。")
		return
	}

	_, _ = fmt.Fprintf(app.out, "转发规则（%d）\n", len(items))
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
