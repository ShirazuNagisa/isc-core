package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

// newExposeCmd 提供"放行端口"的命令行入口。
//
// # 它是 M3 那条链路的前半段
//
// 用户拿到一台新机器，要让它能从公网访问，实际步骤是：
//
//	isc doctor          确认本机差什么
//	isc ddns ...        把域名指过来
//	isc expose --port N 放行端口      ← 本命令
//	isc verify          用手机确认真的通了
//
// # 为什么必须有 --yes 之外的一道确认
//
// 放行端口是**修改系统状态**。它会先生成计划、把差异打印出来，
// 然后等用户确认。自动化场景（脚本、CI）用 --yes 跳过这一步，
// 但默认行为必须是"先看清楚再动手"。
func newExposeCmd(app *App) *cobra.Command {
	var (
		port     int
		protocol string
		label    string
		provider string
		yes      bool
	)

	cmd := &cobra.Command{
		Use:   "expose",
		Short: "在防火墙中放行一个端口（先生成计划、可预览、可撤销）",
		Long: `生成一份"放行端口"的变更计划，确认后应用。

命令会先把要做的改动打印出来再等你确认。放行端口是修改系统状态，
你会看到：

  · 当前已有的 ISC 规则（不会动它们）
  · 本次要新增的规则
  · 风险等级与注意事项

应用后可以用 'isc changes' 查看历史、用 'isc rollback <计划ID>' 撤销。

注意：
  · 创建防火墙规则需要管理员权限（Windows 上需以管理员身份运行内核）；
  · 规则对全部网络配置文件生效 —— Windows 常把家庭网络归类为
    「公用」，只限「专用」的规则会在你自己家里静默失效。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			if provider == "" {
				provider = "ipv6-native"
			}

			// --- 1. 生成计划（不产生任何系统变更）---
			body, _ := json.Marshal(map[string]any{
				"port":     port,
				"protocol": protocol,
				"label":    label,
			})
			var preview gen.ChangePreview
			path := "/v1/reach/providers/" + provider + "/plan"
			if err := client.postInto(ctx, path, body, &preview); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, preview)
			}

			renderPreview(app, preview)

			// 无需改动不是错误。
			if preview.Empty {
				_, _ = fmt.Fprintln(app.out,
					"\n无需改动 —— 规则已经存在。")
				return nil
			}

			// --- 2. 确认 ---
			if !yes {
				ok, err := confirm(app, "确认应用这份变更？")
				if err != nil {
					return app.fail(cmd, err)
				}
				if !ok {
					// 计划会在 10 分钟后自动过期，因此这里不需要显式
					// 丢弃 —— 而少一个"丢弃"端点就少一处可以出错的接口。
					_, _ = fmt.Fprintln(app.out, "已取消。")
					return nil
				}
			}

			// --- 3. 应用 ---
			var rec gen.ChangeRecord
			if err := client.postInto(ctx, "/v1/changes/"+preview.Id+"/apply", nil, &rec); err != nil {
				return app.fail(cmd, err)
			}

			renderChangeResult(app, rec)
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, "要放行的端口（必填）")
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", "协议：tcp 或 udp")
	cmd.Flags().StringVar(&label, "label", "", "服务名，会出现在系统防火墙界面里")
	cmd.Flags().StringVar(&provider, "provider", "ipv6-native", "可达方式")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认直接应用")
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

// renderPreview 打印一份变更计划。
func renderPreview(app *App, p gen.ChangePreview) {
	w := app.out

	_, _ = fmt.Fprintln(w, "变更计划")
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 66))
	_, _ = fmt.Fprintf(w, "%s\n", p.Title)
	_, _ = fmt.Fprintf(w, "风险：%s\n", riskLabel(p.Risk))
	_, _ = fmt.Fprintf(w, "计划 ID：%s\n", p.Id)

	if len(p.Diff) > 0 {
		_, _ = fmt.Fprintln(w, "\n将要发生的改动：")
		for _, d := range p.Diff {
			_, _ = fmt.Fprintf(w, "  %s %s\n", diffMark(d.Op), d.Text)
		}
	}

	for _, s := range p.Steps {
		if s.Details != nil && *s.Details != "" {
			_, _ = fmt.Fprintf(w, "\n%s\n", *s.Details)
		}
		if !s.Revertable {
			_, _ = fmt.Fprintf(w, "  ⚠ 这一步不可撤销\n")
		}
	}

	renderChangeNotes(app, p.Warnings, p.Notes)
}

func renderChangeNotes(app *App, warnings, notes *[]string) {
	if warnings != nil && len(*warnings) > 0 {
		_, _ = fmt.Fprintln(app.out, "\n注意：")
		for _, s := range *warnings {
			_, _ = fmt.Fprintf(app.out, "  ⚠ %s\n", wrapText(s, "    "))
		}
	}
	if notes != nil && len(*notes) > 0 {
		_, _ = fmt.Fprintln(app.out, "\n说明：")
		for _, s := range *notes {
			_, _ = fmt.Fprintf(app.out, "  · %s\n", wrapText(s, "    "))
		}
	}
}

// renderChangeResult 打印一次变更的执行结果。
func renderChangeResult(app *App, rec gen.ChangeRecord) {
	w := app.out
	_, _ = fmt.Fprintln(w, "\n"+strings.Repeat("-", 66))

	switch rec.Status {
	case gen.ChangeRecordStatusApplied:
		_, _ = fmt.Fprintln(w, "✅ 变更已生效。")
		_, _ = fmt.Fprintf(w, "   撤销：isc rollback %s\n", rec.PlanId)

	case gen.ChangeRecordStatusFailed:
		_, _ = fmt.Fprintln(w, "❌ 变更失败，已自动回滚到执行前的状态。")
		renderStepErrors(app, rec)

	case gen.ChangeRecordStatusRollbackFailed:
		// 最严重的结果：系统既不是原状、也不是目标状态。
		_, _ = fmt.Fprintln(w, "‼️  变更失败，**且自动回滚未能完成**。")
		_, _ = fmt.Fprintln(w,
			"   系统可能处于中间状态，请用 'isc doctor' 检查，\n"+
				"   必要时在系统防火墙中手动清理以 isc- 开头的规则。")
		renderStepErrors(app, rec)

	default:
		_, _ = fmt.Fprintf(w, "状态：%s\n", describeChangeStatus(rec.Status))
	}
}

func renderStepErrors(app *App, rec gen.ChangeRecord) {
	for _, s := range rec.Steps {
		if s.Error != nil && *s.Error != "" {
			_, _ = fmt.Fprintf(app.out, "   失败步骤：%s\n", s.Title)
			_, _ = fmt.Fprintf(app.out, "     原因：%s\n", *s.Error)
		}
	}
}

// ---------------------------------------------------------------------------
// 变更历史与撤销
// ---------------------------------------------------------------------------

func newChangesCmd(app *App) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "changes",
		Short: "查看系统变更历史与待确认的计划",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var pending gen.ChangePreviewList
			if err := client.getInto(ctx, "/v1/changes/pending", &pending); err != nil {
				return app.fail(cmd, err)
			}

			var history gen.ChangeList
			path := fmt.Sprintf("/v1/changes?limit=%d", limit)
			if err := client.getInto(ctx, path, &history); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]any{
					"pending": pending.Items, "history": history.Items,
				})
			}

			renderPending(app, pending.Items)
			renderHistory(app, history.Items)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 20, "历史记录条数")
	return cmd
}

func renderPending(app *App, items []gen.ChangePreview) {
	if len(items) == 0 {
		return
	}
	_, _ = fmt.Fprintf(app.out, "待确认的计划（%d）\n", len(items))
	_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
	for _, p := range items {
		_, _ = fmt.Fprintf(app.out, "  %s  %s\n", p.Id, p.Title)
		_, _ = fmt.Fprintf(app.out, "      风险 %s，%s 过期\n",
			riskLabel(p.Risk), p.ExpiresAt.Local().Format("15:04:05"))
	}
	_, _ = fmt.Fprintln(app.out, "\n  这些计划还没有生效。应用：isc expose --port ... 或经接口确认。")
	_, _ = fmt.Fprintln(app.out)
}

func renderHistory(app *App, items []gen.ChangeRecord) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(app.out, "还没有任何系统变更记录。")
		return
	}

	_, _ = fmt.Fprintln(app.out, "变更历史")
	_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
	for _, rec := range items {
		_, _ = fmt.Fprintf(app.out, "  %s  %s\n", statusIcon(rec.Status), rec.Title)
		_, _ = fmt.Fprintf(app.out, "      %s  %s\n",
			rec.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			describeChangeStatus(rec.Status))
		_, _ = fmt.Fprintf(app.out, "      计划 ID %s", rec.PlanId)

		// 只有已生效的才提示可撤销 —— 对失败或已撤销的提示撤销
		// 会让用户以为还有事情要做。
		if rec.Status == gen.ChangeRecordStatusApplied {
			_, _ = fmt.Fprintf(app.out, "   撤销：isc rollback %s", rec.PlanId)
		}
		_, _ = fmt.Fprintln(app.out)
	}
}

func newRollbackCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <计划ID>",
		Short: "撤销一次已生效的系统变更",
		Long: `撤销一次此前应用过的变更。

它跨越内核重启依然可用 —— 撤销所需的信息随变更一起持久化了。
用 'isc changes' 可以查到计划 ID。

撤销是幂等的：已经撤销过的再撤一次会成功返回。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
			if err != nil {
				return app.fail(cmd, err)
			}

			var rec gen.ChangeRecord
			path := "/v1/changes/" + args[0] + "/rollback"
			if err := client.postInto(ctx, path, nil, &rec); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, rec)
			}

			if rec.Status == gen.ChangeRecordStatusRolledBack {
				_, _ = fmt.Fprintf(app.out, "✅ 已撤销：%s\n", rec.Title)
				return nil
			}
			_, _ = fmt.Fprintf(app.out, "状态：%s\n", describeChangeStatus(rec.Status))
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// confirm 向用户要一次确认。
func confirm(app *App, prompt string) (bool, error) {
	_, _ = fmt.Fprintf(app.out, "\n%s [y/N] ", prompt)

	reader := bufio.NewReader(app.in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// 非交互环境（管道、CI）读不到输入：默认拒绝。
		//
		// 默认**拒绝**而不是默认同意：这是一个修改系统状态的操作，
		// 在拿不到用户意图时保守才是对的。
		_, _ = fmt.Fprintln(app.out, "\n（无法读取输入，已取消；自动化场景请加 --yes）")
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "是":
		return true, nil
	default:
		return false, nil
	}
}

func riskLabel(r gen.ChangePreviewRisk) string {
	switch r {
	case gen.ChangePreviewRiskHigh:
		return "高 —— 可能影响你与这台机器的连接"
	case gen.ChangePreviewRiskMedium:
		return "中 —— 会开放一个端口"
	default:
		return "低"
	}
}

func diffMark(op gen.ChangeDiffLineOp) string {
	switch op {
	case gen.Add:
		return "+"
	case gen.Remove:
		return "-"
	case gen.Change:
		return "~"
	default:
		return " "
	}
}

func statusIcon(s gen.ChangeRecordStatus) string {
	switch s {
	case gen.ChangeRecordStatusApplied:
		return "✅"
	case gen.ChangeRecordStatusFailed:
		return "❌"
	case gen.ChangeRecordStatusRollbackFailed:
		return "‼️ "
	case gen.ChangeRecordStatusRolledBack:
		return "↩️ "
	case gen.ChangeRecordStatusApplying:
		return "⏳"
	default:
		return "·"
	}
}

// wrapText 折行并对齐续行。
func wrapText(text, indent string) string {
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
