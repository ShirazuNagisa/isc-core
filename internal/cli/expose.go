package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
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
		Short: i18n.T("cli.expose.short"),
		Long:  i18n.T("cli.expose.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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

			// --json 且**没有** --yes：只把计划交出去，不交互、不应用。
			//
			// 这里曾经是无条件的 `if app.jsonOut { return ... }` —— 于是
			// `expose --port N --yes --json` 打印完计划就退出，**什么都没做**
			// 却返回 0。脚本判断成败只看退出码，因此这是"看起来成功、实际没做"
			// 的那一类缺陷。它是在真机验收（CI 里跑 sudo）时被抓出来的，
			// 回归测试见 expose_json_test.go。
			if app.jsonOut && !yes {
				return writeJSONOut(app.out, preview)
			}

			renderPreview(app, preview)

			// 无需改动不是错误。
			if preview.Empty {
				_, _ = fmt.Fprintln(app.out,
					i18n.T("cli.expose.no_change"))
				return nil
			}

			// --- 2. 确认 ---
			if !yes {
				ok, err := confirm(app, i18n.T("cli.expose.confirm"))
				if err != nil {
					return app.fail(cmd, err)
				}
				if !ok {
					// 计划会在 10 分钟后自动过期，因此这里不需要显式
					// 丢弃 —— 而少一个"丢弃"端点就少一处可以出错的接口。
					_, _ = fmt.Fprintln(app.out, i18n.T("cli.expose.cancelled"))
					return nil
				}
			}

			// --- 3. 应用 ---
			var rec gen.ChangeRecord
			if err := client.postInto(ctx, "/v1/changes/"+preview.Id+"/apply", nil, &rec); err != nil {
				return app.fail(cmd, err)
			}

			// 应用之后，--json 输出的是**变更记录**（有 status / plan_id），
			// 而不是计划 —— 调用方据此判断"到底应用了没有"。
			if app.jsonOut {
				return writeJSONOut(app.out, rec)
			}

			renderChangeResult(app, rec)
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, i18n.T("cli.expose.flag_port"))
	cmd.Flags().StringVar(&protocol, "protocol", "tcp", i18n.T("cli.expose.flag_protocol"))
	cmd.Flags().StringVar(&label, "label", "", i18n.T("cli.expose.flag_label"))
	cmd.Flags().StringVar(&provider, "provider", "ipv6-native", i18n.T("cli.expose.flag_provider"))
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cli.expose.flag_yes"))
	_ = cmd.MarkFlagRequired("port")

	return cmd
}

// renderPreview 打印一份变更计划。
func renderPreview(app *App, p gen.ChangePreview) {
	w := app.out

	_, _ = fmt.Fprintln(w, i18n.T("cli.expose.plan_title"))
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 66))
	_, _ = fmt.Fprintf(w, "%s\n", p.Title)
	_, _ = fmt.Fprintf(w, i18n.T("cli.expose.plan_risk"), riskLabel(p.Risk))
	_, _ = fmt.Fprintf(w, i18n.T("cli.expose.plan_id"), p.Id)

	if len(p.Diff) > 0 {
		_, _ = fmt.Fprintln(w, i18n.T("cli.expose.plan_diff"))
		for _, d := range p.Diff {
			_, _ = fmt.Fprintf(w, "  %s %s\n", diffMark(d.Op), d.Text)
		}
	}

	for _, s := range p.Steps {
		if s.Details != nil && *s.Details != "" {
			_, _ = fmt.Fprintf(w, "\n%s\n", *s.Details)
		}
		if !s.Revertable {
			_, _ = fmt.Fprint(w, i18n.T("cli.expose.plan_irreversible"))
		}
	}

	renderChangeNotes(app, p.Warnings, p.Notes)
}

func renderChangeNotes(app *App, warnings, notes *[]string) {
	if warnings != nil && len(*warnings) > 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.expose.plan_notes"))
		for _, s := range *warnings {
			_, _ = fmt.Fprintf(app.out, "  ⚠ %s\n", wrapText(s, "    "))
		}
	}
	if notes != nil && len(*notes) > 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.expose.plan_details"))
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
		_, _ = fmt.Fprintln(w, i18n.T("cli.expose.applied"))
		_, _ = fmt.Fprintf(w, i18n.T("cli.expose.applied_undo"), rec.PlanId)

	case gen.ChangeRecordStatusFailed:
		_, _ = fmt.Fprintln(w, i18n.T("cli.expose.failed"))
		renderStepErrors(app, rec)

	case gen.ChangeRecordStatusRollbackFailed:
		// 最严重的结果：系统既不是原状、也不是目标状态。
		_, _ = fmt.Fprintln(w, i18n.T("cli.expose.rollback_failed"))
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.expose.rollback_hint_a")+
				i18n.T("cli.expose.rollback_hint_b"))
		renderStepErrors(app, rec)

	default:
		_, _ = fmt.Fprintf(w, i18n.T("cli.expose.state"), describeChangeStatus(rec.Status))
	}
}

func renderStepErrors(app *App, rec gen.ChangeRecord) {
	for _, s := range rec.Steps {
		if s.Error != nil && *s.Error != "" {
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.expose.failed_step"), s.Title)
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.expose.failed_reason"), *s.Error)
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
		Short: i18n.T("cli.changes.short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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

	cmd.Flags().IntVar(&limit, "limit", 20, i18n.T("cli.changes.flag_limit"))
	return cmd
}

func renderPending(app *App, items []gen.ChangePreview) {
	if len(items) == 0 {
		return
	}
	_, _ = fmt.Fprintf(app.out, i18n.T("cli.changes.pending"), len(items))
	_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
	for _, p := range items {
		_, _ = fmt.Fprintf(app.out, "  %s  %s\n", p.Id, p.Title)
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.changes.pending_line"),
			riskLabel(p.Risk), p.ExpiresAt.Local().Format("15:04:05"))
	}
	_, _ = fmt.Fprintln(app.out, i18n.T("cli.changes.pending_hint"))
	_, _ = fmt.Fprintln(app.out)
}

func renderHistory(app *App, items []gen.ChangeRecord) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.changes.empty"))
		return
	}

	_, _ = fmt.Fprintln(app.out, i18n.T("cli.changes.title"))
	_, _ = fmt.Fprintln(app.out, strings.Repeat("-", 66))
	for _, rec := range items {
		_, _ = fmt.Fprintf(app.out, "  %s  %s\n", statusIcon(rec.Status), rec.Title)
		_, _ = fmt.Fprintf(app.out, "      %s  %s\n",
			rec.CreatedAt.Local().Format("2006-01-02 15:04:05"),
			describeChangeStatus(rec.Status))
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.changes.plan_id_line"), rec.PlanId)

		// 只有已生效的才提示可撤销 —— 对失败或已撤销的提示撤销
		// 会让用户以为还有事情要做。
		if rec.Status == gen.ChangeRecordStatusApplied {
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.changes.undo"), rec.PlanId)
		}
		_, _ = fmt.Fprintln(app.out)
	}
}

func newRollbackCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   i18n.T("cli.rollback.use"),
		Short: i18n.T("cli.rollback.short"),
		Long:  i18n.T("cli.rollback.long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
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
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.rollback.done"), rec.Title)
				return nil
			}
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.expose.state"), describeChangeStatus(rec.Status))
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
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.rollback.no_stdin"))
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", i18n.T("cli.rollback.yes"):
		return true, nil
	default:
		return false, nil
	}
}

func riskLabel(r gen.ChangePreviewRisk) string {
	switch r {
	case gen.ChangePreviewRiskHigh:
		return i18n.T("cli.risk.high")
	case gen.ChangePreviewRiskMedium:
		return i18n.T("cli.risk.medium")
	default:
		return i18n.T("cli.risk.low")
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
