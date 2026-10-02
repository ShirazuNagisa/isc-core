package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

// newDoctorCmd 提供一键自检。
//
// # 它要回答的问题
//
// "手机打不开服务"这一句话背后有至少六个不同的原因，而它们在用户
// 眼里长得一模一样：
//
//	运营商没给 IPv6
//	给了 IPv6 但路由器没下发前缀
//	路由器防火墙没放行
//	本机防火墙没放行
//	服务没在监听
//	运营商封了入站端口
//
// 用户能自己看到的只有最后那句"打不开"。doctor 的职责是把这个链条
// **逐层拆开**，指出到底断在哪一环，并给出那一环的具体做法。
//
// 因此输出里最重要的不是"✅/❌"，而是每项后面的「→ 该怎么办」。
func newDoctorCmd(app *App) *cobra.Command {
	var providerName string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "自检：到底哪一环断了",
		Long: `逐层检查"服务能否从公网访问"这条链路。

检查按从下到上的顺序进行：

  1. 本机是否有全局 IPv6 地址
  2. 是否有 IPv6 委派前缀
  3. 本机防火墙后端是否可用
  4. 上游可达性（本机无法自测，必须用手机流量验证）

失败项会附带「该怎么办」。按顺序逐项处理即可 —— 一次修一个，
比同时动五个设置更容易定位问题。

注意：命令**不会**产生任何系统变更，只读取状态。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var providers gen.ReachProviderList
			if err := client.getInto(ctx, "/v1/reach/providers", &providers); err != nil {
				return app.fail(cmd, err)
			}
			if len(providers.Items) == 0 {
				_, _ = fmt.Fprintln(app.out, "没有可用的可达方式。")
				return nil
			}

			// 只检查指定的一种，或全部。
			var results []doctorResult
			for _, p := range providers.Items {
				if providerName != "" && p.Name != providerName {
					continue
				}

				var rd gen.ReachReadiness
				path := "/v1/reach/providers/" + p.Name + "/probe"
				if err := client.getInto(ctx, path, &rd); err != nil {
					return app.fail(cmd, err)
				}
				results = append(results, doctorResult{provider: p, readiness: rd})
			}
			if len(results) == 0 {
				return app.fail(cmd, fmt.Errorf("没有找到名为 %q 的可达方式", providerName))
			}

			// 顺便检查有没有上次没走完的系统变更 ——
			// 它会让"本机看起来都正常但就是不通"。
			var interrupted gen.ChangeList
			if err := client.getInto(ctx, "/v1/changes/interrupted", &interrupted); err != nil {
				// 这一项失败不该让整个自检失败：主要结论仍然有价值。
				// 但不静默吞掉 —— 用户可能正因为那条记录而困惑。
				_, _ = fmt.Fprintf(app.out, "（提示：读取未完成的系统变更失败：%v）`n", err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]any{
					"results":     results,
					"interrupted": interrupted.Items,
				})
			}
			renderDoctor(app, results, interrupted.Items)
			return nil
		},
	}

	cmd.Flags().StringVar(&providerName, "provider", "", "只检查指定的可达方式")
	return cmd
}

type doctorResult struct {
	provider  gen.ReachProvider
	readiness gen.ReachReadiness
}

func renderDoctor(app *App, results []doctorResult, interrupted []gen.ChangeRecord) {
	w := app.out

	_, _ = fmt.Fprintln(w, "ISC 自检")
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 66))

	// 未完成的系统变更放在最前面：它会让下面所有检查的结论失去意义
	//（本机配置都对，但规则是半生效的）。
	if len(interrupted) > 0 {
		_, _ = fmt.Fprintf(w, "\n⚠ 发现 %d 条上次未走完的系统变更：\n", len(interrupted))
		for _, rec := range interrupted {
			_, _ = fmt.Fprintf(w, "    %s  %s\n", rec.PlanId, rec.Title)
			_, _ = fmt.Fprintf(w, "      状态：%s\n", describeChangeStatus(rec.Status))
		}
		_, _ = fmt.Fprintf(w,
			"    这些变更可能只生效了一部分。请确认它们是否符合预期，\n"+
				"    必要时用 'isc changes' 查看详情后手动撤销。\n")
	}

	var anyBlocked bool

	for _, r := range results {
		_, _ = fmt.Fprintf(w, "\n%s\n", r.provider.DisplayName)
		if r.provider.NeedsExternalServer {
			_, _ = fmt.Fprintf(w, "  （需要一台外部服务器）\n")
		}
		_, _ = fmt.Fprintf(w, "  %s\n", r.readiness.Summary)

		// 按作用域分组输出。
		//
		// 这个分组不是排版偏好：用户必须清楚地知道哪些结论是
		// **本机测出来的**（可以据此行动），哪些是**测不了的**
		//（必须去做外部验证）。混在一起会让他以为本机自测通过
		// 就等于外面能连上。
		local := filterChecks(r.readiness.Checks, "local")
		upstream := filterChecks(r.readiness.Checks, "upstream")

		if len(local) > 0 {
			_, _ = fmt.Fprintf(w, "\n  本机检测\n")
			for _, c := range local {
				renderCheck(app, "    ", c)
			}
		}
		if len(upstream) > 0 {
			_, _ = fmt.Fprintf(w, "\n  外部验证（本机无法自测）\n")
			for _, c := range upstream {
				renderCheck(app, "    ", c)
			}
		}

		if _, ok := blockedCheck(r.readiness.Checks); ok {
			anyBlocked = true
		}
	}

	_, _ = fmt.Fprintln(w, "\n"+strings.Repeat("-", 66))

	// 结论：把"该做什么"明确说出来。
	switch {
	case anyBlocked:
		_, _ = fmt.Fprintf(w,
			"结论：本机配置没有问题，但**上游挡住了**。\n"+
				"      这不是你能在本机修复的 —— 请换用其它可达方式，\n"+
				"      或联系运营商确认是否封禁了入站连接。\n")
	default:
		// 只看最后一个（通常是 IPv6 直连）的结论。
		last := results[len(results)-1]
		if last.readiness.Viable {
			_, _ = fmt.Fprintf(w,
				"结论：本机已具备条件。但**本机自测通过不等于外网能连上** ——\n"+
					"      请务必用手机 4G/5G 打开验证地址确认。\n")
		} else {
			_, _ = fmt.Fprintf(w, "结论：%s\n", last.readiness.Summary)
			for _, c := range last.readiness.Checks {
				if c.Scope == "local" && c.Status == "fail" {
					_, _ = fmt.Fprintf(w, "      先处理这一项：%s\n", c.Name)
					break
				}
			}
		}
	}
}

func renderCheck(app *App, indent string, c gen.ReachCheck) {
	icon := "·"
	switch c.Status {
	case "pass":
		icon = "✅"
	case "fail":
		icon = "❌"
	case "warn":
		icon = "⚠️ "
	case "unknown":
		icon = "❓"
	case "blocked":
		icon = "🚫"
	}

	_, _ = fmt.Fprintf(app.out, "%s%s %s\n", indent, icon, c.Name)
	if c.Detail != nil && *c.Detail != "" {
		_, _ = fmt.Fprintf(app.out, "%s     %s\n", indent, wrap(*c.Detail, indent+"     "))
	}
	// 「该怎么办」用箭头单列 —— 它是用户真正要找的那句话。
	if c.Hint != nil && *c.Hint != "" {
		_, _ = fmt.Fprintf(app.out, "%s   → %s\n", indent, wrap(*c.Hint, indent+"     "))
	}
}

// wrap 把长文本折行，并让续行对齐。
func wrap(text, indent string) string {
	const width = 72
	if len([]rune(text)) <= width {
		return text
	}

	var (
		out   strings.Builder
		line  strings.Builder
		first = true
	)
	for _, r := range text {
		line.WriteRune(r)
		if line.Len() >= width && r == ' ' {
			if !first {
				out.WriteString("\n" + indent)
			}
			first = false
			out.WriteString(strings.TrimRight(line.String(), " "))
			line.Reset()
		}
	}
	if line.Len() > 0 {
		if !first {
			out.WriteString("\n" + indent)
		}
		out.WriteString(line.String())
	}
	return out.String()
}

func filterChecks(checks []gen.ReachCheck, scope string) []gen.ReachCheck {
	var out []gen.ReachCheck
	for _, c := range checks {
		if string(c.Scope) == scope {
			out = append(out, c)
		}
	}
	return out
}

func blockedCheck(checks []gen.ReachCheck) (gen.ReachCheck, bool) {
	for _, c := range checks {
		if c.Status == "blocked" {
			return c, true
		}
	}
	return gen.ReachCheck{}, false
}

func describeChangeStatus(s gen.ChangeRecordStatus) string {
	switch s {
	case gen.ChangeRecordStatusApplying:
		return "执行中（内核可能异常退出）"
	case gen.ChangeRecordStatusApplied:
		return "已生效"
	case gen.ChangeRecordStatusFailed:
		return "失败（已自动回滚）"
	case gen.ChangeRecordStatusRolledBack:
		return "已撤销"
	case gen.ChangeRecordStatusRollbackFailed:
		return "回滚失败（系统处于中间状态）"
	default:
		return string(s)
	}
}
