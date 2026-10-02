package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件补齐**DNS 记录管理与设置**的命令行入口。
//
// 与 credential.go 同样的背景：这些能力在 HTTP 接口里齐全（控制台也有
// 面板），但 CLI 里没有 —— 而"完整 CLI"是明确的要求。
//
// 尤其是记录管理：Tier-1 六家的"全量 CRUD"是这个项目的核心能力之一，
// 而它此前只能通过 Web 控制台使用。

// ---------------------------------------------------------------------------
// zones
// ---------------------------------------------------------------------------

func newZonesCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "zones <credential-id>",
		Short: i18n.T("cli.zones.short"),
		Long:  i18n.T("cli.zones.long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.ZoneList
			path := "/v1/credentials/" + args[0] + "/zones"
			if err := client.getInto(ctx, path, &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.zones.empty"))
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.zones.empty_hint"))
				return nil
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.zones.title")+"\n", len(list.Items))
			_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
			for _, z := range list.Items {
				_, _ = fmt.Fprintf(app.out, "  %s  %s\n", z.Id, z.Name)
			}
			_, _ = fmt.Fprintf(app.out, "\n%s\n",
				i18n.T("cli.zones.next", args[0]))
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// records
// ---------------------------------------------------------------------------

func newRecordsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "records",
		Short: i18n.T("cli.records.short"),
		Long:  i18n.T("cli.records.long"),
	}

	cmd.AddCommand(
		newRecordsListCmd(app),
		newRecordsAddCmd(app),
		newRecordsRemoveCmd(app),
	)
	return cmd
}

func newRecordsListCmd(app *App) *cobra.Command {
	var (
		recType string
		name    string
	)

	cmd := &cobra.Command{
		Use:   "list <credential-id> <zone-id>",
		Short: i18n.T("cli.records.list_short"),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			path := "/v1/credentials/" + args[0] + "/zones/" + args[1] + "/records"
			var q []string
			if recType != "" {
				q = append(q, "type="+recType)
			}
			if name != "" {
				q = append(q, "name="+name)
			}
			if len(q) > 0 {
				path += "?" + strings.Join(q, "&")
			}

			var list gen.RecordList
			if err := client.getInto(ctx, path, &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.records.list_empty"))
				return nil
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.records.list_title")+"\n", len(list.Items))
			_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 78))
			_, _ = fmt.Fprintf(app.out, "  %-8s %-34s %-22s %s\n",
				i18n.T("cli.records.col_type"), i18n.T("cli.records.col_name"),
				i18n.T("cli.records.col_content"), "TTL")
			for _, rec := range list.Items {
				ttl := i18n.T("cli.records.ttl_default")
				if rec.Ttl != nil {
					ttl = fmt.Sprintf("%d", *rec.Ttl)
				}
				_, _ = fmt.Fprintf(app.out, "  %-8s %-34s %-22s %s\n",
					rec.Type, truncate(rec.Name, 34), truncate(rec.Content, 22), ttl)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&recType, "type", "",
		i18n.T("cli.records.filter_type"))
	cmd.Flags().StringVar(&name, "name", "", i18n.T("cli.records.filter_name"))
	return cmd
}

func newRecordsAddCmd(app *App) *cobra.Command {
	var (
		recType string
		content string
		ttl     int
	)

	cmd := &cobra.Command{
		Use:   "add <credential-id> <zone-id> <record-name>",
		Short: i18n.T("cli.records.add_short"),
		Long:  i18n.T("cli.records.add_long"),
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if recType == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.records.need_type")))
			}
			if content == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.records.need_content")))
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			body := map[string]any{
				"name":    args[2],
				"type":    recType,
				"content": content,
			}
			if ttl > 0 {
				body["ttl"] = ttl
			}
			payload, _ := json.Marshal(body)

			path := "/v1/credentials/" + args[0] + "/zones/" + args[1] + "/records"
			var created gen.Record
			if err := client.postInto(ctx, path, payload, &created); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, created)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.records.added")+"\n",
				created.Type, created.Name, created.Content)
			if created.Id != "" {
				_, _ = fmt.Fprintf(app.out, "   ID: %s\n", created.Id)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&recType, "type", "",
		i18n.T("cli.records.flag_type"))
	cmd.Flags().StringVar(&content, "content", "",
		i18n.T("cli.records.flag_content"))
	cmd.Flags().IntVar(&ttl, "ttl", 0, i18n.T("cli.records.flag_ttl"))
	return cmd
}

func newRecordsRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rm <credential-id> <zone-id> <record-id>",
		Short: i18n.T("cli.records.rm_short"),
		Long:  i18n.T("cli.records.rm_long"),
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				_, _ = fmt.Fprintf(app.out,
					i18n.T("cli.records.rm_confirm")+"\n", args[2])
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			path := "/v1/credentials/" + args[0] + "/zones/" + args[1] +
				"/records/" + args[2]
			if err := client.delete(ctx, path); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.records.removed")+"\n", args[2])
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, i18n.T("cli.records.yes_flag"))
	return cmd
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

func newSettingsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: i18n.T("cli.settings.short"),
		Long:  i18n.T("cli.settings.long"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var s gen.Settings
			if err := client.getInto(ctx, "/v1/settings", &s); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, s)
			}
			renderSettings(app, s)
			return nil
		},
	}

	cmd.AddCommand(newSettingsSetCmd(app))
	return cmd
}

func newSettingsSetCmd(app *App) *cobra.Command {
	var (
		lang       string
		logLevel   string
		proxyOn    bool
		proxyOff   bool
		proxyPort  int
		proxyTLS   bool
		noProxyTLS bool
		acmeEmail  string
		acmeDir    string
		acmeCred   string
	)

	cmd := &cobra.Command{
		Use:   "set",
		Short: i18n.T("cli.settings.set_short"),
		Long:  i18n.T("cli.settings.set_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			patch := map[string]any{}

			// 只把**用户显式给出的**标志放进请求。
			//
			// 全部塞进去会让"我只想改语言"变成"顺便把代理关掉" ——
			// 因为 bool 的零值是 false，而它会被当成用户的意图。
			fl := cmd.Flags()
			if fl.Changed("lang") {
				patch["lang"] = lang
			}
			if fl.Changed("log-level") {
				patch["log_level"] = logLevel
			}
			if fl.Changed("proxy-enabled") {
				patch["proxy_enabled"] = true
			}
			if fl.Changed("proxy-disabled") {
				patch["proxy_enabled"] = false
			}
			if fl.Changed("proxy-port") {
				patch["proxy_port"] = proxyPort
			}
			if fl.Changed("proxy-tls") {
				patch["proxy_tls"] = true
			}
			if fl.Changed("no-proxy-tls") {
				patch["proxy_tls"] = false
			}
			if fl.Changed("acme-email") {
				patch["acme_email"] = acmeEmail
			}
			if fl.Changed("acme-directory") {
				patch["acme_directory"] = acmeDir
			}
			if fl.Changed("acme-dns-credential-id") {
				patch["acme_dns_credential_id"] = acmeCred
			}

			// 两个互斥的开关同时给出是用户搞混了，而不是"后者覆盖前者"。
			if fl.Changed("proxy-enabled") && fl.Changed("proxy-disabled") {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.settings.conflict_proxy")))
			}
			if fl.Changed("proxy-tls") && fl.Changed("no-proxy-tls") {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.settings.conflict_tls")))
			}

			if len(patch) == 0 {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.settings.nothing"))
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			payload, _ := json.Marshal(patch)
			var s gen.Settings
			if err := client.doBody(ctx, "PATCH", "/v1/settings", payload, &s); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, s)
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.settings.updated"))
			renderSettings(app, s)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&lang, "lang", "", i18n.T("cli.settings.flag_lang"))
	f.StringVar(&logLevel, "log-level", "", i18n.T("cli.settings.flag_log_level"))
	f.BoolVar(&proxyOn, "proxy-enabled", false, i18n.T("cli.settings.flag_proxy_on"))
	f.BoolVar(&proxyOff, "proxy-disabled", false, i18n.T("cli.settings.flag_proxy_off"))
	f.IntVar(&proxyPort, "proxy-port", 0, i18n.T("cli.settings.flag_proxy_port"))
	f.BoolVar(&proxyTLS, "proxy-tls", false, i18n.T("cli.settings.flag_proxy_tls"))
	f.BoolVar(&noProxyTLS, "no-proxy-tls", false, i18n.T("cli.settings.flag_no_tls"))
	f.StringVar(&acmeEmail, "acme-email", "",
		i18n.T("cli.settings.flag_acme_email"))
	f.StringVar(&acmeDir, "acme-directory", "",
		i18n.T("cli.settings.flag_acme_dir"))
	f.StringVar(&acmeCred, "acme-dns-credential-id", "",
		i18n.T("cli.settings.flag_acme_cred"))
	return cmd
}

func renderSettings(app *App, s gen.Settings) {
	w := app.out
	_, _ = fmt.Fprintln(w, i18n.T("cli.settings.render_title"))
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 66))

	if s.Lang != "" {
		_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_lang")+"\n", s.Lang)
	}
	if s.LogLevel != "" {
		_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_level")+"\n", s.LogLevel)
	}
	if s.EventBufferSize != nil {
		_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_buffer")+"\n",
			*s.EventBufferSize)
	}

	if s.ProxyEnabled != nil {
		state := i18n.T("cli.settings.render_off")
		if *s.ProxyEnabled {
			state = i18n.T("cli.settings.render_on")
		}
		_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_proxy"), state)
		if s.ProxyPort != nil {
			_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_port"), *s.ProxyPort)
			if s.ProxyTls != nil && *s.ProxyTls {
				_, _ = fmt.Fprint(w, i18n.T("cli.settings.render_https"))
			} else {
				_, _ = fmt.Fprint(w, i18n.T("cli.settings.render_http"))
			}
			_, _ = fmt.Fprint(w, i18n.T("cli.settings.render_close"))
		}
		_, _ = fmt.Fprintln(w)
	}

	// ACME 那一组是 HTTPS 的前置条件，因此单独提示。
	_, _ = fmt.Fprintln(w, i18n.T("cli.settings.render_acme"))
	unset := i18n.T("cli.settings.render_unset")
	email := unset
	if s.AcmeEmail != nil && *s.AcmeEmail != "" {
		email = *s.AcmeEmail
	}
	_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_email")+"\n", email)

	dir := i18n.T("cli.settings.render_prod")
	if s.AcmeDirectory != nil && *s.AcmeDirectory != "" {
		dir = *s.AcmeDirectory
		// 只在**非生产**的目录上加警告。
		//
		// 判据是这个字符串，而不是某个常量：ACME 目录是可以自定义的，
		// 而"staging"是 Let's Encrypt 与其兼容实现共用的命名惯例。
		if strings.Contains(dir, "staging") {
			dir += i18n.T("cli.settings.render_staging")
		}
	}
	_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_dir")+"\n", dir)

	cred := unset
	if s.AcmeDnsCredentialId != nil && *s.AcmeDnsCredentialId != "" {
		cred = *s.AcmeDnsCredentialId
	}
	_, _ = fmt.Fprintf(w, i18n.T("cli.settings.render_cred")+"\n", cred)

	if cred == unset {
		_, _ = fmt.Fprintln(w, i18n.T("cli.settings.render_no_cred"))
	}
}

// truncate 把过长的字符串截断。
//
// 终端表格里一列太长会把整张表挤歪，而记录内容（TXT、长 CNAME）
// 很容易超长。
func truncate(s string, n int) string {
	// 按 rune 而不是 byte 计数：中文域名与内容按字节截断会出现半个字。
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "…"
}
