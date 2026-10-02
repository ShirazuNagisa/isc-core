package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Short: "管理 DNS 服务商凭据",
		Long: `管理 DNS 服务商凭据。

凭据加密存储在主密钥保护的信封里，而主密钥在系统密钥库里
（Windows DPAPI / macOS 钥匙串 / Linux Secret Service）。
接口只返回敏感字段的**掩码值**，明文永远不会被发回来。

用 'isc credential fields <服务商>' 查看某家需要哪些字段。`,
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
		Short: "列出已保存的凭据",
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
				_, _ = fmt.Fprintln(app.out, "还没有任何凭据。")
				_, _ = fmt.Fprintln(app.out,
					"用 isc credential add <服务商> 添加一个；"+
						"isc credential providers 可以看到支持哪些服务商。")
				return nil
			}

			_, _ = fmt.Fprintf(app.out, "凭据（%d）\n", len(list.Items))
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
					_, _ = fmt.Fprintf(app.out,
						"      ⚠ 该服务商的实现尚未完成\n")
				}

				// 上次校验的结果：用户最关心的"这个凭据还能用吗"。
				if c.LastVerifyOk != nil && !*c.LastVerifyOk {
					_, _ = fmt.Fprintf(app.out, "      ⚠ 上次校验未通过")
					if c.LastVerifiedAt != nil {
						_, _ = fmt.Fprintf(app.out, "（%s）",
							c.LastVerifiedAt.Local().Format("2006-01-02 15:04"))
					}
					_, _ = fmt.Fprintln(app.out)
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
		Use:   "add <服务商>",
		Short: "添加一个凭据",
		Long: `添加一个 DNS 服务商凭据。

字段用 --field 传入，可以重复：

  isc credential add cloudflare --label 我的CF --field token=<API-TOKEN>
  isc credential add dnspod --label 主域名 \
      --field id=<ID> --field secret=<TOKEN>

用 'isc credential fields <服务商>' 查看它需要哪些字段名。

**最小权限**：只需 DNS 记录的编辑权限。以 Cloudflare 为例，
Token 只开 Zone:DNS:Edit 即可 —— 内核不会碰其它任何设置。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			provider := strings.TrimSpace(args[0])
			if provider == "" {
				return app.fail(cmd, fmt.Errorf("服务商名不能为空"))
			}
			if strings.TrimSpace(label) == "" {
				return app.fail(cmd, fmt.Errorf(
					"必须用 --label 给凭据起一个名字 —— "+
						"同一家服务商可以有多组凭据，而名字是界面上区分它们的方式"))
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
				return app.fail(cmd, fmt.Errorf(
					"%w\n用 'isc credential fields %s' 查看它需要哪些字段",
					err, provider))
			}

			if app.jsonOut {
				return writeJSONOut(app.out, created)
			}

			_, _ = fmt.Fprintf(app.out, "✅ 凭据已添加（%s）\n", created.Id)
			_, _ = fmt.Fprintf(app.out, "   %s（%s）\n",
				created.Label, created.Provider)
			_, _ = fmt.Fprintln(app.out,
				"\n下一步：用这个 ID 创建动态解析任务，或在控制台里管理 DNS 记录。")
			return nil
		},
	}

	cmd.Flags().StringVar(&label, "label", "",
		"凭据的可读名称（必填）—— 同一家服务商可以有多组凭据")
	cmd.Flags().StringArrayVar(&fields, "field", nil,
		"字段，形如 name=value，可重复")
	return cmd
}

func newCredentialFieldsCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:     "fields <服务商>",
		Aliases: []string{"providers"},
		Short:   "查看某家服务商需要哪些凭据字段",
		Long: `查看某家服务商需要哪些凭据字段。

不带参数时列出全部服务商。`,
		Args: cobra.MaximumNArgs(1),
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
				return app.fail(cmd, fmt.Errorf(
					"没有名为 %q 的服务商。不带参数运行可以看到全部", want))
			}
			return nil
		},
	}
}

func renderProviderFields(app *App, p gen.Provider) {
	w := app.out

	status := ""
	if !p.Capabilities.Available {
		status = "  ⚠ 尚未实现"
	}
	tier := ""
	if p.Tier != nil {
		tier = fmt.Sprintf("  [Tier-%d]", int(*p.Tier))
	}
	_, _ = fmt.Fprintf(w, "%s（%s）%s%s\n", p.Name, p.DisplayName, tier, status)

	if len(p.CredentialFields) == 0 {
		_, _ = fmt.Fprintln(w, "    （无需凭据字段）")
	}
	for _, f := range p.CredentialFields {
		required := ""
		if f.Required {
			required = "（必填）"
		}
		secret := ""
		if f.Secret {
			secret = " [敏感]"
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
		add(p.Capabilities.Dynamic, "动态解析")
		add(p.Capabilities.ZoneList, "列区域")
		add(p.Capabilities.RecordCreate, "新增记录")
		add(p.Capabilities.RecordUpdate, "修改记录")
		add(p.Capabilities.RecordDelete, "删除记录")
		add(p.Capabilities.Dns01, "DNS-01 证书")
		if len(caps) > 0 {
			_, _ = fmt.Fprintf(w, "    能力：%s\n", strings.Join(caps, " / "))
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
		return "  例如 " + *f.Example
	}
	return ""
}

func newCredentialRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <凭据ID>",
		Aliases: []string{"remove", "delete"},
		Short:   "删除一个凭据",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if !force {
				// 默认要确认：删掉凭据会让引用它的任务全部失效，
				// 而那是一个用户不容易自己发现的连锁后果。
				_, _ = fmt.Fprintf(app.out,
					"删除凭据 %s 会让引用它的动态解析任务全部失效。\n"+
						"确认请加 --yes。\n", id)
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

			_, _ = fmt.Fprintf(app.out, "✅ 凭据 %s 已删除\n", id)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, "跳过确认")
	return cmd
}

func newCredentialVerifyCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "verify <凭据ID>",
		Short: "校验凭据是否可用",
		Long: `校验凭据是否可用（"测试连接"）。

**不是所有服务商都支持**：阿里云 / 腾讯云 / 华为云 / GoDaddy 没有只读的
校验端点，用"列一次域名"来冒充会要求额外的权限，把只有 DNS 编辑权限的
最小权限账号误判为无效。

不支持时这条命令会明确说明，而不是给你一个假的"失败"。`,
		Args: cobra.ExactArgs(1),
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
				_, _ = fmt.Fprintln(app.out, "✅ 凭据可用")
			} else {
				_, _ = fmt.Fprintln(app.out, "❌ 凭据不可用")
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
		return nil, fmt.Errorf(
			"至少要用 --field 提供一个字段。" +
				"用 'isc credential fields <服务商>' 查看需要哪些")
	}

	out := make(map[string]string, len(items))
	for _, item := range items {
		idx := strings.Index(item, "=")
		if idx <= 0 {
			return nil, fmt.Errorf(
				"--field 的格式是 name=value，收到 %q", item)
		}
		name := strings.TrimSpace(item[:idx])
		value := item[idx+1:] // 值里允许有等号，因此不 Trim 也不切分
		if name == "" {
			return nil, fmt.Errorf("--field 缺少字段名：%q", item)
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
