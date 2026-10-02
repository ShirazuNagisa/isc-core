package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Short: "创建一条动态解析任务",
		Long: `创建一条动态解析任务。

一条任务 = 一组凭据 + 一组域名 + 一组地址来源。

例（IPv6，从网卡读取）：

  isc ddns add --label 家里的IPv6 --credential <凭据ID> \
      --domain home.example.com --type AAAA --source ipv6

例（IPv4，通过外部接口查询）：

  isc ddns add --label 家里的IPv4 --credential <凭据ID> \
      --domain home.example.com --type A --source ipv4 \
      --get-type url --value https://api.ipify.org

--source ipv6 时可以用 --selector 在多地址中挑一个：

  --selector "@2"        取第 2 个（从 1 开始）
  --selector "^240e:.*"  正则筛选，取第一个匹配的`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(label) == "" {
				return app.fail(cmd, fmt.Errorf("必须用 --label 给任务起一个名字"))
			}
			if strings.TrimSpace(credential) == "" {
				return app.fail(cmd, fmt.Errorf(
					"必须用 --credential 指定凭据 ID（用 isc credential list 查看）"))
			}
			if len(domains) == 0 {
				return app.fail(cmd, fmt.Errorf("至少要用 --domain 指定一个域名"))
			}

			recType = strings.ToUpper(strings.TrimSpace(recType))
			if recType != "A" && recType != "AAAA" {
				return app.fail(cmd, fmt.Errorf(
					"--type 只能是 A 或 AAAA，收到 %q", recType))
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
				return app.fail(cmd, fmt.Errorf(
					"--get-type 只能是 netInterface / url / cmd，收到 %q", getType))
			}
			if getType != "netInterface" && strings.TrimSpace(source) == "" {
				return app.fail(cmd, fmt.Errorf(
					"--get-type %s 时必须用 --value 给出%s",
					getType, map[string]string{
						"url": "接口地址",
						"cmd": "要执行的命令",
					}[getType]))
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

			_, _ = fmt.Fprintf(app.out, "✅ 任务已创建（%s）\n", created.Id)
			_, _ = fmt.Fprintf(app.out, "   %s：%s → %s\n",
				created.Label, recType, strings.Join(domains, ", "))
			_, _ = fmt.Fprintln(app.out,
				"\n立即跑一次看看：isc ddns run "+created.Id)
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&label, "label", "", "任务名称（必填）—— 会出现在通知与日志里")
	f.StringVar(&credential, "credential", "", "凭据 ID（必填）")
	f.StringArrayVar(&domains, "domain", nil,
		"要更新的域名，可重复；支持 www:example.com 显式指定根域名")
	f.StringVar(&recType, "type", "AAAA", "记录类型：A 或 AAAA")
	f.StringVar(&source, "source", "", "地址来源：ipv6 或 ipv4（用于默认的取值方式）")
	f.StringVar(&getType, "get-type", "netInterface",
		"取值方式：netInterface（从网卡读，推荐）/ url / cmd")
	f.StringVar(&selector, "selector", "",
		"仅 IPv6：地址选择器，如 @2 或 ^240e:.*")
	f.StringVar(&ttl, "ttl", "", "记录 TTL 秒数；留空用服务商默认值")
	f.BoolVar(&disabled, "disabled", false, "创建后先停用")
	return cmd
}

func newDdnsRemoveCmd(app *App) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:     "rm <任务ID>",
		Aliases: []string{"remove", "delete"},
		Short:   "删除一条动态解析任务",
		Long: `删除一条动态解析任务。

它只删除**任务**，不会动 DNS 里已有的记录 —— 记录会保持最后一次
解析出来的值。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				_, _ = fmt.Fprintf(app.out,
					"将删除任务 %s。确认请加 --yes。\n"+
						"（DNS 里的记录会保持最后一次解析出来的值，不会被删掉。）\n",
					args[0])
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

			_, _ = fmt.Fprintf(app.out, "✅ 任务 %s 已删除\n", args[0])
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "yes", false, "跳过确认")
	return cmd
}
