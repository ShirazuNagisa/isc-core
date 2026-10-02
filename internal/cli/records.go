package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Use:   "zones <凭据ID>",
		Short: "列出某个凭据可管理的 DNS 区域",
		Long: `列出某个凭据可管理的 DNS 区域。

用 isc credential list 拿到凭据 ID。

并非所有服务商都支持 —— Tier-2（只做动态解析的那 30 家）没有列区域的
能力。遇到时这条命令会明确说明，而不是给你一个空列表。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
				_, _ = fmt.Fprintln(app.out,
					"该凭据下没有可管理的区域。\n"+
						"常见原因：凭据的权限范围不包含任何域名，"+
						"或该服务商不支持列出区域（Tier-2）。")
				return nil
			}

			_, _ = fmt.Fprintf(app.out, "区域（%d）\n", len(list.Items))
			_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
			for _, z := range list.Items {
				_, _ = fmt.Fprintf(app.out, "  %s  %s\n", z.Id, z.Name)
			}
			_, _ = fmt.Fprintln(app.out,
				"\n下一步：isc records list "+args[0]+" <区域ID>")
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
		Short: "管理 DNS 记录（仅 Tier-1 服务商）",
		Long: `浏览与编辑 DNS 记录。

**仅 Tier-1 服务商可用**：Cloudflare / 阿里云 / 腾讯云 / DNSPod /
华为云 / GoDaddy。Tier-2（只做动态解析的那 30 家）没有记录管理能力。

注意各家的记录模型不同：华为云的一条记录属于一个「记录集」，
GoDaddy 的记录没有独立 ID —— 它们的删除会波及同名的其它值。
详见 docs/PROVIDER-MATRIX.md。`,
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
		Use:   "list <凭据ID> <区域ID>",
		Short: "列出区域内的记录",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
				_, _ = fmt.Fprintln(app.out, "该区域下没有匹配的记录。")
				return nil
			}

			_, _ = fmt.Fprintf(app.out, "记录（%d）\n", len(list.Items))
			_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 78))
			_, _ = fmt.Fprintf(app.out, "  %-8s %-34s %-22s %s\n",
				"类型", "名称", "内容", "TTL")
			for _, rec := range list.Items {
				ttl := "默认"
				if rec.Ttl != nil {
					ttl = fmt.Sprintf("%d", *rec.Ttl)
				}
				_, _ = fmt.Fprintf(app.out, "  %-8s %-34s %-22s %s\n",
					rec.Type, truncate(rec.Name, 34), truncate(rec.Content, 22), ttl)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&recType, "type", "", "只看某个类型（A / AAAA / CNAME / MX / TXT …）")
	cmd.Flags().StringVar(&name, "name", "", "只看某个名字")
	return cmd
}

func newRecordsAddCmd(app *App) *cobra.Command {
	var (
		recType string
		content string
		ttl     int
	)

	cmd := &cobra.Command{
		Use:   "add <凭据ID> <区域ID> <记录名>",
		Short: "新增一条记录",
		Long: `新增一条 DNS 记录。

记录名用**完整名字**（www.example.com），而不是相对名（www）——
各家对相对名的处理不一致，而完整名字在六家上含义相同。`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if recType == "" {
				return app.fail(cmd, fmt.Errorf("必须用 --type 指定记录类型"))
			}
			if content == "" {
				return app.fail(cmd, fmt.Errorf("必须用 --content 指定记录内容"))
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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

			_, _ = fmt.Fprintf(app.out, "✅ 已新增 %s %s → %s\n",
				created.Type, created.Name, created.Content)
			if created.Id != "" {
				_, _ = fmt.Fprintf(app.out, "   ID: %s\n", created.Id)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&recType, "type", "", "记录类型（必填）：A / AAAA / CNAME / MX / TXT …")
	cmd.Flags().StringVar(&content, "content", "", "记录内容（必填）")
	cmd.Flags().IntVar(&ttl, "ttl", 0, "TTL 秒数（0 = 用服务商默认值）")
	return cmd
}

func newRecordsRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "rm <凭据ID> <区域ID> <记录ID>",
		Short: "删除一条记录",
		Long: `删除一条 DNS 记录。

**注意部分服务商的语义差异**：GoDaddy 的记录没有独立 ID，删一条
同名记录会波及该名字下的**全部**同类型值。华为云的一条记录属于一个
「记录集」，删除的粒度与其它家不同。详见 docs/PROVIDER-MATRIX.md。`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				_, _ = fmt.Fprintf(app.out,
					"将删除记录 %s。确认请加 --yes。\n", args[2])
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			path := "/v1/credentials/" + args[0] + "/zones/" + args[1] +
				"/records/" + args[2]
			if err := client.delete(ctx, path); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, "✅ 记录 %s 已删除\n", args[2])
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, "跳过确认")
	return cmd
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

func newSettingsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "查看与修改内核设置",
		Long: `查看与修改内核设置。

不带子命令时打印当前的全部设置。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
		Short: "修改设置",
		Long: `修改设置。**只提交你显式给出的字段**，其余保持不变。

例：
  isc settings set --acme-email you@example.com --acme-dns-credential-id <凭据ID>
  isc settings set --proxy-enabled --proxy-port 443 --proxy-tls

ACME 设置是 HTTPS 的前置条件：启用 proxy-tls 之前必须先指定
DNS-01 凭据，否则证书签不出来，而症状是"浏览器报证书错误"。`,
		Args: cobra.NoArgs,
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
				return app.fail(cmd, fmt.Errorf(
					"--proxy-enabled 与 --proxy-disabled 不能同时给出"))
			}
			if fl.Changed("proxy-tls") && fl.Changed("no-proxy-tls") {
				return app.fail(cmd, fmt.Errorf(
					"--proxy-tls 与 --no-proxy-tls 不能同时给出"))
			}

			if len(patch) == 0 {
				_, _ = fmt.Fprintln(app.out,
					"没有给出任何要修改的字段。用 isc settings 查看当前值。")
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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
			_, _ = fmt.Fprintln(app.out, "✅ 设置已更新")
			renderSettings(app, s)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&lang, "lang", "", "界面语言：zh-CN 或 en")
	f.StringVar(&logLevel, "log-level", "", "日志级别：debug / info / warn / error")
	f.BoolVar(&proxyOn, "proxy-enabled", false, "启用反向代理")
	f.BoolVar(&proxyOff, "proxy-disabled", false, "停用反向代理")
	f.IntVar(&proxyPort, "proxy-port", 0, "反向代理监听端口")
	f.BoolVar(&proxyTLS, "proxy-tls", false, "反向代理使用 HTTPS")
	f.BoolVar(&noProxyTLS, "no-proxy-tls", false, "反向代理改回明文 HTTP")
	f.StringVar(&acmeEmail, "acme-email", "",
		"ACME 账户邮箱（续期失败时 CA 用它提醒你）")
	f.StringVar(&acmeDir, "acme-directory", "",
		"ACME 目录地址，留空用生产环境；测试环境签的证书浏览器不信任")
	f.StringVar(&acmeCred, "acme-dns-credential-id", "",
		"做 DNS-01 校验用的凭据 ID")
	return cmd
}

func renderSettings(app *App, s gen.Settings) {
	w := app.out
	_, _ = fmt.Fprintln(w, "内核设置")
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 66))

	if s.Lang != "" {
		_, _ = fmt.Fprintf(w, "  界面语言      %s\n", s.Lang)
	}
	if s.LogLevel != "" {
		_, _ = fmt.Fprintf(w, "  日志级别      %s\n", s.LogLevel)
	}
	if s.EventBufferSize != nil {
		_, _ = fmt.Fprintf(w, "  事件缓冲      %d\n", *s.EventBufferSize)
	}

	if s.ProxyEnabled != nil {
		state := "停用"
		if *s.ProxyEnabled {
			state = "启用"
		}
		_, _ = fmt.Fprintf(w, "  反向代理      %s", state)
		if s.ProxyPort != nil {
			_, _ = fmt.Fprintf(w, "（端口 %d", *s.ProxyPort)
			if s.ProxyTls != nil && *s.ProxyTls {
				_, _ = fmt.Fprint(w, "，HTTPS")
			} else {
				_, _ = fmt.Fprint(w, "，明文 HTTP")
			}
			_, _ = fmt.Fprint(w, "）")
		}
		_, _ = fmt.Fprintln(w)
	}

	// ACME 那一组是 HTTPS 的前置条件，因此单独提示。
	_, _ = fmt.Fprintln(w, "\n  ACME（HTTPS 的前置条件）")
	email := "（未设置）"
	if s.AcmeEmail != nil && *s.AcmeEmail != "" {
		email = *s.AcmeEmail
	}
	_, _ = fmt.Fprintf(w, "    邮箱        %s\n", email)

	dir := "生产环境"
	if s.AcmeDirectory != nil && *s.AcmeDirectory != "" {
		dir = *s.AcmeDirectory
		if strings.Contains(dir, "staging") {
			dir += "  ⚠ 测试环境签的证书浏览器不信任"
		}
	}
	_, _ = fmt.Fprintf(w, "    目录        %s\n", dir)

	cred := "（未设置）"
	if s.AcmeDnsCredentialId != nil && *s.AcmeDnsCredentialId != "" {
		cred = *s.AcmeDnsCredentialId
	}
	_, _ = fmt.Fprintf(w, "    DNS-01 凭据 %s\n", cred)

	if cred == "（未设置）" {
		_, _ = fmt.Fprintln(w,
			"    ⚠ 未设置凭据时无法签发证书，也就无法启用 HTTPS")
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
