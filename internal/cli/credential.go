package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件补齐**凭据管理的命令行入口**。
//
// # 它为什么必须存在
//
// 在此之前，凭据**只能**通过 Web 控制台或直接调 HTTP 接口管理 —— CLI
// 里根本没有这个命令。而这是整条链路的第一步：没有凭据就建不了动态解析
// 任务，也就什么都做不了。
//
// 更糟的是文档与 isc init 都在让用户运行 `isc credential add` ——
// 一个不存在的命令。这是对照验收标准逐条核实时发现的：功能在接口里
// 齐全（控制台也有面板），但"完整 CLI"这条要求没有满足。

func newCredentialCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "credential",
		Short: i18n.T("cli.credential.short"),
		Long:  i18n.T("cli.credential.long"),
	}

	cmd.AddCommand(
		newCredentialListCmd(app),
		newCredentialAddCmd(app),
		newCredentialFieldsCmd(app),
		newCredentialRemoveCmd(app),
		newCredentialVerifyCmd(app),
	)
	return cmd
}

func newCredentialListCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: i18n.T("cli.credential.list_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.CredentialList
			if err := client.getInto(ctx, "/v1/credentials", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			if len(list.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.credential.list_empty"))
				_, _ = fmt.Fprintln(app.out,
					i18n.T("cli.credential.list_empty_hint"))
				return nil
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.credential.list_title")+"\n", len(list.Items))
			_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
			for _, c := range list.Items {
				_, _ = fmt.Fprintf(app.out, "  %s  %s（%s）\n",
					c.Id, c.Label, c.Provider)

				// 字段以掩码显示 —— 那是接口返回的全部内容，
				// 而用户需要它来确认"我填的字段对不对"。
				for _, k := range sortedKeys(c.Fields) {
					_, _ = fmt.Fprintf(app.out, "      %s = %s\n", k, c.Fields[k])
				}
				// Capabilities 是**指针**，可以是 nil。
				//
				// 早先这里写成 `!c.Capabilities.Available` —— 那在
				// 服务商没有能力位时直接 panic。命令崩溃比报错糟得多：
				// 用户看到的是 Go 的堆栈而不是一句能照着做的提示。
				//
				// 这是真机上跑出来的（isc credential list 打出了
				// nil pointer dereference），而不是从代码里看出来的。
				if c.Capabilities != nil && !c.Capabilities.Available {
					_, _ = fmt.Fprintf(app.out, "      ⚠ %s\n",
						i18n.T("cli.credential.not_implemented"))
				}

				// 上次校验的结果：用户最关心的"这个凭据还能用吗"。
				if c.LastVerifyOk != nil && !*c.LastVerifyOk {
					when := ""
					if c.LastVerifiedAt != nil {
						when = c.LastVerifiedAt.Local().Format("2006-01-02 15:04")
					}
					_, _ = fmt.Fprintf(app.out, "      ⚠ %s\n",
						i18n.T("cli.credential.verify_failed_at", when))
					if c.LastVerifyError != nil && *c.LastVerifyError != "" {
						_, _ = fmt.Fprintf(app.out, "        %s\n",
							firstLine(*c.LastVerifyError))
					}
				}
			}
			return nil
		},
	}
}

func newCredentialAddCmd(app *App) *cobra.Command {
	var (
		label  string
		fields []string
	)

	cmd := &cobra.Command{
		Use:   "add <provider>",
		Short: i18n.T("cli.credential.add_short"),
		Long:  i18n.T("cli.credential.add_long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.TrimSpace(args[0])
			if provider == "" {
				return app.fail(cmd, fmt.Errorf("%s", i18n.T("cli.credential.provider_empty")))
			}
			if strings.TrimSpace(label) == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.credential.label_required")))
			}

			kv, err := parseFields(fields)
			if err != nil {
				return app.fail(cmd, err)
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			body := map[string]any{
				"provider": provider,
				"label":    label,
				"fields":   kv,
			}

			var created gen.Credential
			payload, _ := json.Marshal(body)
			if err := client.postInto(ctx, "/v1/credentials", payload, &created); err != nil {
				// 缺字段之类的错误，补一句"怎么查需要哪些字段"——
				// 那是用户下一步唯一想做的事。
				return app.fail(cmd, fmt.Errorf("%w\n%s", err,
					i18n.T("cli.credential.fields_hint", provider)))
			}

			if app.jsonOut {
				return writeJSONOut(app.out, created)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.credential.added")+"\n", created.Id)
			_, _ = fmt.Fprintf(app.out, "   %s（%s）\n",
				created.Label, created.Provider)
			_, _ = fmt.Fprintln(app.out,
				"\n"+i18n.T("cli.credential.added_hint"))
			return nil
		},
	}

	cmd.Flags().StringVar(&label, "label", "",
		i18n.T("cli.credential.label_flag"))
	cmd.Flags().StringArrayVar(&fields, "field", nil,
		i18n.T("cli.credential.field_flag"))
	return cmd
}

func newCredentialFieldsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "fields <provider>",
		Aliases: []string{"providers"},
		Short:   i18n.T("cli.credential.fields_short"),
		Long:    i18n.T("cli.credential.fields_long"),
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var list struct {
				Items []gen.Provider `json:"items"`
			}
			if err := client.getInto(ctx, "/v1/providers", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}

			want := ""
			if len(args) == 1 {
				want = strings.ToLower(strings.TrimSpace(args[0]))
			}

			found := false
			for _, p := range list.Items {
				if want != "" && strings.ToLower(p.Name) != want {
					continue
				}
				found = true
				renderProviderFields(app, p)
			}

			if want != "" && !found {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.credential.fields_unknown", want)))
			}
			return nil
		},
	}
}

func renderProviderFields(app *App, p gen.Provider) {
	w := app.out

	status := ""
	if !p.Capabilities.Available {
		status = i18n.T("cli.credential.unavailable")
	}
	tier := ""
	if p.Tier != nil {
		tier = i18n.T("cli.credential.tier", int(*p.Tier))
	}
	_, _ = fmt.Fprintf(w, "%s（%s）%s%s\n", p.Name, p.DisplayName, tier, status)

	if len(p.CredentialFields) == 0 {
		_, _ = fmt.Fprintln(w, "    "+i18n.T("cli.credential.no_fields"))
	}
	for _, f := range p.CredentialFields {
		required := ""
		if f.Required {
			required = i18n.T("cli.credential.field_required")
		}
		secret := ""
		if f.Secret {
			secret = i18n.T("cli.credential.field_secret")
		}
		_, _ = fmt.Fprintf(w, "    --field %s=<%s>  %s%s%s%s\n",
			f.Key, f.Label, required, secret, fieldHint(f), exampleHint(f))
	}

	// 能力位：用户据此知道这家能不能做记录管理 / DNS-01。
	{
		var caps []string
		add := func(ok bool, name string) {
			if ok {
				caps = append(caps, name)
			}
		}
		add(p.Capabilities.Dynamic, i18n.T("cli.credential.cap_dynamic"))
		add(p.Capabilities.ZoneList, i18n.T("cli.credential.cap_zones"))
		add(p.Capabilities.RecordCreate, i18n.T("cli.credential.cap_create"))
		add(p.Capabilities.RecordUpdate, i18n.T("cli.credential.cap_update"))
		add(p.Capabilities.RecordDelete, i18n.T("cli.credential.cap_delete"))
		add(p.Capabilities.Dns01, i18n.T("cli.credential.cap_dns01"))
		if len(caps) > 0 {
			_, _ = fmt.Fprintf(w, "%s\n",
				i18n.T("cli.credential.capabilities", strings.Join(caps, " / ")))
		}
	}
	_, _ = fmt.Fprintln(w)
}

func fieldHint(f gen.ProviderField) string {
	if f.Help != nil && *f.Help != "" {
		return "  " + *f.Help
	}
	return ""
}

func exampleHint(f gen.ProviderField) string {
	if f.Example != nil && *f.Example != "" {
		return i18n.T("cli.credential.field_example", *f.Example)
	}
	return ""
}

func newCredentialRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <credential-id>",
		Aliases: []string{"remove", "delete"},
		Short:   i18n.T("cli.credential.rm_short"),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if !force {
				// 默认要确认：删掉凭据会让引用它的任务全部失效，
				// 而那是一个用户不容易自己发现的连锁后果。
				_, _ = fmt.Fprintf(app.out,
					i18n.T("cli.credential.rm_confirm")+"\n", id)
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			if err := client.delete(ctx, "/v1/credentials/"+id); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.credential.removed")+"\n", id)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, i18n.T("cli.credential.yes_flag"))
	return cmd
}

func newCredentialVerifyCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <credential-id>",
		Short: i18n.T("cli.credential.verify_short"),
		Long:  i18n.T("cli.credential.verify_long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var res gen.CredentialVerifyResult
			path := "/v1/credentials/" + args[0] + "/verify"
			if err := client.postInto(ctx, path, nil, &res); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, res)
			}

			if res.Ok {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.credential.verify_ok"))
			} else {
				_, _ = fmt.Fprintln(app.out, i18n.T("cli.credential.verify_bad"))
			}
			if res.Message != nil && *res.Message != "" {
				_, _ = fmt.Fprintf(app.out, "   %s\n", *res.Message)
			}
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// firstLine 取一段文本的第一行。
//
// 校验失败的说明可能是多行的（humanizeVerifyError 会追加一段"可能是
// 权限不足"），而列表里只显示第一行 —— 完整说明在 isc credential verify 里。
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return s[:idx]
	}
	return s
}

// parseFields 把 --field name=value 解析成映射。
//
// **值里允许出现等号**：只按第一个等号切分。Token 与密钥里出现 `=`
// 是常见的（base64 的填充字符就是 =），按最后一个切会把它截断，
// 而症状是"凭据看起来填对了但校验失败"。
func parseFields(items []string) (map[string]string, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("%s", i18n.T("cli.credential.field_none"))
	}

	out := make(map[string]string, len(items))
	for _, item := range items {
		idx := strings.Index(item, "=")
		if idx <= 0 {
			return nil, fmt.Errorf("%s",
				i18n.T("cli.credential.field_format", item))
		}
		name := strings.TrimSpace(item[:idx])
		value := item[idx+1:] // 值里允许有等号，因此不 Trim 也不切分
		if name == "" {
			return nil, fmt.Errorf("%s",
				i18n.T("cli.credential.field_noname", item))
		}
		out[name] = value
	}
	return out, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
