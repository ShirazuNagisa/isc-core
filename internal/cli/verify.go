package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
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
		Short: "外部验证：用手机确认能不能从公网访问",
		Long: `开始一次外部验证，确认服务能不能从公网访问。

流程：
  1. 内核在本机临时监听一个端口（默认随机分配）；
  2. 命令给出一个带随机路径的地址；
  3. 用手机**关闭 Wi-Fi、走 4G/5G** 打开那个地址；
  4. 内核根据**来源地址**判断这次访问算不算数。

判断分三档，其中第二档是最容易被误解的：

  公网地址    → 链路确实通
  本机地址    → 什么也证明不了（IPv6 没有 NAT，本机访问自己的
                公网地址是直连的，不经过运营商）
  一直没访问  → 结合本机检测全绿，结论指向上游封禁

注意：验证会占用一个真实端口。在此之前你可能需要先在防火墙里
放行它 —— 那一步同样可以走 isc 的变更流程。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := Connect(ctx, app.paths.RuntimeFile())
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

	cmd.Flags().IntVar(&port, "port", 0, "监听的端口（0 = 由内核分配空闲端口）")
	cmd.Flags().DurationVar(&wait, "wait", 3*time.Minute,
		"等待外部访问的时长（0 = 不等待，只打印地址）")
	return cmd
}

func renderVerifyStart(app *App, sess gen.VerifySession) {
	w := app.out
	_, _ = fmt.Fprintln(w, "外部验证已开始")
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 60))

	if sess.Url == nil || *sess.Url == "" {
		_, _ = fmt.Fprintln(w,
			"⚠ 没有找到可用的公网地址，无法生成验证链接。\n"+
				"  请先运行 isc doctor 确认本机的 IPv6 状态。")
		return
	}

	_, _ = fmt.Fprintln(w, "\n请用手机（关闭 Wi-Fi，走 4G/5G）打开：")
	_, _ = fmt.Fprintf(w, "\n    %s\n\n", *sess.Url)
	_, _ = fmt.Fprintf(w, "监听端口：%d\n", sess.Port)
	_, _ = fmt.Fprintln(w, "\n在内核里先放行这个端口：")
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
		case gen.Reachable, gen.HairpinOnly, gen.Stopped:
			renderVerifyResult(app, sess)
			return nil
		}

		if time.Now().After(deadline) {
			_, _ = fmt.Fprintln(app.out, "\n等待超时。")
			renderVerifyResult(app, sess)
			return nil
		}
	}
}

func renderVerifyResult(app *App, sess gen.VerifySession) {
	w := app.out
	_, _ = fmt.Fprintln(w, "\n"+strings.Repeat("-", 60))

	switch sess.Status {
	case gen.Reachable:
		_, _ = fmt.Fprintln(w, "✅ 外部访问成功 —— 链路是通的。")
		_, _ = fmt.Fprintln(w,
			"   现在可以放行你真正要用的服务端口了。")

	case gen.HairpinOnly:
		_, _ = fmt.Fprintln(w, "⚠️  这次访问**不能作为凭据**。")
		_, _ = fmt.Fprintln(w, "   "+sess.Message)
		_, _ = fmt.Fprintln(w,
			"\n   请确认手机已关闭 Wi-Fi、走的是移动数据。")

	case gen.Unreachable:
		_, _ = fmt.Fprintln(w, "❌ 在有效期内没有收到任何外部访问。")
		_, _ = fmt.Fprintln(w, "   "+sess.Message)
		_, _ = fmt.Fprintln(w,
			"\n   若 isc doctor 的本机检测全部通过，那么问题不在本机：")
		_, _ = fmt.Fprintln(w,
			"     · 路由器防火墙没有放行该端口（检查路由器的 IPv6 防火墙设置）")
		_, _ = fmt.Fprintln(w,
			"     · 运营商封禁了入站连接（部分省份确实如此）")

	default:
		_, _ = fmt.Fprintln(w, sess.Message)
	}

	if len(sess.Hits) > 0 {
		_, _ = fmt.Fprintln(app.out, "\n收到的访问：")
		for _, h := range sess.Hits {
			_, _ = fmt.Fprintf(app.out, "    %-46s %s\n", h.RemoteAddr, h.Kind)
		}
	}
}
