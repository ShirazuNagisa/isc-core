package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件补齐**动态解析任务的创建与删除**。
//
// # 它为什么必须存在
//
// 在此之前 isc ddns 只有 list 与 run —— 建不了任务、也删不掉。
// 而 README 的快速上手第 3 步就是 `isc ddns add …`，isc init 的引导
// 里也写着同一条命令。**文档在教用户运行一个不存在的命令。**
//
// 这与上一轮的 isc credential 是同一类缺口：能力在接口里齐全（控制台
// 有完整的新建表单），CLI 侧没有入口，而文档假设它存在。
//
// 顺带说明为什么上一轮的「20 个命令都存在」检查没抓到它：那个检查只看
// **顶层命令**，不看子命令与标志。这一轮把检查扩展到了子命令。

func newDdnsAddCmd(app *App) *cobra.Command {
	var (
		label      string
		credential string
		domains    []string
		recType    string
		source     string
		getType    string
		selector   string
		ttl        string
		disabled   bool
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: i18n.T("cli.ddns.add_short"),
		Long:  i18n.T("cli.ddns.add_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(label) == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.need_label")))
			}
			if strings.TrimSpace(credential) == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.need_credential")))
			}
			if len(domains) == 0 {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.need_domain")))
			}

			recType = strings.ToUpper(strings.TrimSpace(recType))
			if recType != "A" && recType != "AAAA" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.bad_type", recType)))
			}

			// 地址来源：默认从网卡读取。
			//
			// 对 AAAA 用网卡是**最稳的默认** —— 内核已经过滤掉了
			// 链路本地、Teredo、6to4 这些不能用于公网的地址，
			// 而外部接口查询会引入一个不必要的依赖。
			if getType == "" {
				getType = "netInterface"
			}
			switch getType {
			case "netInterface", "url", "cmd":
			default:
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.bad_get_type", getType)))
			}
			if getType != "netInterface" && strings.TrimSpace(source) == "" {
				return app.fail(cmd, fmt.Errorf("%s",
					i18n.T("cli.ddns.need_value", getType, map[string]string{
						"url": i18n.T("cli.ddns.value_url"),
						"cmd": i18n.T("cli.ddns.value_cmd"),
					}[getType])))
			}

			// 只有被选中的那一类记录参与解析，另一类显式关闭。
			//
			// 服务端的 DdnsSource 要求 enable 字段，而"没提到的那一类"
			// 在语义上就是关闭 —— 不显式关闭会让任务同时去解析 A 与
			// AAAA，而用户只填了其中一类。
			off := gen.DdnsSource{Enable: false}
			on := gen.DdnsSource{
				Enable:  true,
				GetType: gen.DdnsSourceGetType(getType),
				Value:   source,
				Domains: domains,
			}
			if selector != "" {
				on.Selector = &selector
			}

			body := map[string]any{
				"credential_id": credential,
				"label":         label,
				"enabled":       !disabled,
			}
			if recType == "AAAA" {
				body["ipv6"] = on
				body["ipv4"] = off
			} else {
				body["ipv4"] = on
				body["ipv6"] = off
			}
			if ttl != "" {
				body["ttl"] = ttl
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			payload, _ := json.Marshal(body)
			var created gen.DdnsTask
			if err := client.postInto(ctx, "/v1/ddns-tasks", payload, &created); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, created)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.ddns.created"), created.Id)
			_, _ = fmt.Fprintf(app.out, "   %s：%s → %s\n",
				created.Label, recType, strings.Join(domains, ", "))
			_, _ = fmt.Fprintln(app.out,
				i18n.T("cli.ddns.created_hint", created.Id))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&label, "label", "", i18n.T("cli.ddns.flag_label"))
	f.StringVar(&credential, "credential", "",
		i18n.T("cli.ddns.flag_credential"))
	f.StringArrayVar(&domains, "domain", nil, i18n.T("cli.ddns.flag_domain"))
	f.StringVar(&recType, "type", "AAAA", i18n.T("cli.ddns.flag_type"))
	f.StringVar(&source, "source", "", i18n.T("cli.ddns.flag_source"))
	f.StringVar(&getType, "get-type", "netInterface",
		i18n.T("cli.ddns.flag_get_type"))
	f.StringVar(&selector, "selector", "", i18n.T("cli.ddns.flag_selector"))
	f.StringVar(&ttl, "ttl", "", i18n.T("cli.ddns.flag_ttl"))
	f.BoolVar(&disabled, "disabled", false, i18n.T("cli.ddns.flag_disabled"))
	return cmd
}

func newDdnsRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     i18n.T("cli.ddns.rm_use"),
		Aliases: []string{"remove", "delete"},
		Short:   i18n.T("cli.ddns.rm_short"),
		Long:    i18n.T("cli.ddns.rm_long"),
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.ddns.rm_confirm"), args[0])
				return nil
			}

			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			if err := client.delete(ctx, "/v1/ddns-tasks/"+args[0]); err != nil {
				return app.fail(cmd, err)
			}

			_, _ = fmt.Fprintf(app.out, i18n.T("cli.ddns.removed"), args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, i18n.T("cli.ddns.yes_flag"))
	return cmd
}
