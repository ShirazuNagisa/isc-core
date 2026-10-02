package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// newVerifyCmd 提供引导式外部验证的命令行入口。
//
// # 它为什么必须存在
//
// 本机的一切检测通过之后，还剩最后一个问题：**从外面到底能不能连上**。
// 本机回答不了 —— 从本机访问自己的公网地址走的是回环或直连，
// 无论运营商是否放行都会"成功"。
//
// 唯一的办法是让一台真正在外面的设备去访问一次，而用户手边就有：
// 他的手机（关掉 Wi-Fi，走 4G/5G）。
func newVerifyCmd(app *App) *cobra.Command {
	var (
		port int
		wait time.Duration
	)

	cmd := &cobra.Command{
		Use:   "verify",
		Short: i18n.T("cli.verify.short"),
		Long:  i18n.T("cli.verify.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			body, _ := json.Marshal(map[string]any{"port": port})
			var sess gen.VerifySession
			if err := client.postInto(ctx, "/v1/verify/sessions", body, &sess); err != nil {
				return app.fail(cmd, err)
			}
			defer func() {
				// 命令结束时停掉监听 —— 用户不该因为忘记而留下一个
				// 一直开着的端口。
				_ = client.delete(ctx, "/v1/verify/sessions/"+sess.Id)
			}()

			if app.jsonOut {
				return writeJSONOut(app.out, sess)
			}

			renderVerifyStart(app, sess)
			if wait <= 0 {
				return nil
			}
			return pollVerify(ctx, app, client, sess.Id, wait)
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, i18n.T("cli.verify.flag_port"))
	cmd.Flags().DurationVar(&wait, "wait", 3*time.Minute,
		i18n.T("cli.verify.flag_wait"))
	return cmd
}

func renderVerifyStart(app *App, sess gen.VerifySession) {
	w := app.out
	_, _ = fmt.Fprintln(w, i18n.T("cli.verify.started"))
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 60))

	if sess.Url == nil || *sess.Url == "" {
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.no_address")+
				i18n.T("cli.verify.no_address_hint"))
		return
	}

	_, _ = fmt.Fprintln(w, i18n.T("cli.verify.open_hint"))
	_, _ = fmt.Fprintf(w, "\n    %s\n\n", *sess.Url)
	_, _ = fmt.Fprintf(w, i18n.T("cli.verify.listen_port"), sess.Port)
	_, _ = fmt.Fprintln(w, i18n.T("cli.verify.allow_first"))
	_, _ = fmt.Fprintf(w, "    isc expose --port %d\n", sess.Port)
}

func pollVerify(ctx context.Context, app *App, client *Client, id string, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()

	lastStatus := ""
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		var sess gen.VerifySession
		if err := client.getInto(ctx, "/v1/verify/sessions/"+id, &sess); err != nil {
			return app.fail(nil, err)
		}

		if string(sess.Status) != lastStatus {
			lastStatus = string(sess.Status)
			_, _ = fmt.Fprintf(app.out, "… %s\n", sess.Message)
		}

		// 终态就停：继续等没有意义。
		switch sess.Status {
		case gen.VerifySessionStatusReachable, gen.VerifySessionStatusHairpinOnly, gen.VerifySessionStatusStopped:
			renderVerifyResult(app, sess)
			return nil
		}

		if time.Now().After(deadline) {
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.verify.timeout"))
			renderVerifyResult(app, sess)
			return nil
		}
	}
}

func renderVerifyResult(app *App, sess gen.VerifySession) {
	w := app.out
	_, _ = fmt.Fprintln(w, "\n"+strings.Repeat("-", 60))

	switch sess.Status {
	case gen.VerifySessionStatusReachable:
		_, _ = fmt.Fprintln(w, i18n.T("cli.verify.reachable"))
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.next_step"))

	case gen.VerifySessionStatusHairpinOnly:
		_, _ = fmt.Fprintln(w, i18n.T("cli.verify.hairpin"))
		_, _ = fmt.Fprintln(w, "   "+sess.Message)
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.hairpin_hint"))

	case gen.VerifySessionStatusUnreachable:
		_, _ = fmt.Fprintln(w, i18n.T("cli.verify.unreachable"))
		_, _ = fmt.Fprintln(w, "   "+sess.Message)
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.unreachable_hint"))
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.cause_router"))
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.verify.cause_isp"))

	default:
		_, _ = fmt.Fprintln(w, sess.Message)
	}

	if len(sess.Hits) > 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.verify.hits"))
		for _, h := range sess.Hits {
			_, _ = fmt.Fprintf(app.out, "    %-46s %s\n", h.RemoteAddr, h.Kind)
		}
	}
}
