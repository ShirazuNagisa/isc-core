package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// newRemoteCmd 提供远程访问（ISC Mizar）的开关、配对与设备管理。
//
// # 为什么它在命令行里也必须存在
//
// Phecda 的「远程访问」页是主要入口，但它有一个不可替代的场景：**无头
// 服务端**。把内核装成系统服务之后（`isc service install`），机器上
// 不一定有人在登录、更不一定有图形界面，而"我要让手机连上这台机器"
// 这件事仍然要能做。只有 GUI 的话，用户会被迫为了点一个开关去开一次
// 远程桌面。
//
// 于是它与界面走的是**同一批接口**，没有一条只有命令行才能做的事
// （D19：CLI 是接口的消费者，不是另一套实现）。
func newRemoteCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: i18n.T("cli.remote.short"),
		Long:  i18n.T("cli.remote.long"),
	}

	cmd.AddCommand(
		newRemoteStatusCmd(app),
		newRemoteEnableCmd(app),
		newRemoteDisableCmd(app),
		newRemotePairCmd(app),
		newRemoteDevicesCmd(app),
		newRemoteRevokeCmd(app),
		newRemoteLogCmd(app),
		newRemoteApnsCmd(app),
		newRemoteTestPushCmd(app),
	)
	return cmd
}

func newRemoteStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: i18n.T("cli.remote.status_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var status gen.RemoteStatus
			if err := client.getInto(ctx, "/v1/remote/status", &status); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, status)
			}
			renderRemoteStatus(app, status)
			return nil
		},
	}
}

func newRemoteEnableCmd(app *App) *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "enable",
		Short: i18n.T("cli.remote.enable_short"),
		Long:  i18n.T("cli.remote.enable_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			patch := map[string]any{"enabled": true}
			if cmd.Flags().Changed("port") {
				patch["port"] = port
			}
			body, err := json.Marshal(patch)
			if err != nil {
				return app.fail(cmd, err)
			}

			var status gen.RemoteStatus
			if err := client.doBody(ctx, "PATCH", "/v1/remote/settings", body, &status); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, status)
			}
			renderRemoteStatus(app, status)

			// 监听没起来时必须说清楚。
			//
			// 设置已经保存了，而用户手上那个"已开启"的界面会让他以为
			// 手机马上就能连上 —— 不把失败原因顶到眼前，他会去手机上
			// 反复重试，而问题根本不在这边。
			if status.LastError != nil && *status.LastError != "" {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.listen_failed")+"\n",
					*status.LastError)
				return nil
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.next_steps"))
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, i18n.T("cli.remote.flag_port"))
	return cmd
}

func newRemoteDisableCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: i18n.T("cli.remote.disable_short"),
		Long:  i18n.T("cli.remote.disable_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			body, err := json.Marshal(map[string]any{"enabled": false})
			if err != nil {
				return app.fail(cmd, err)
			}

			var status gen.RemoteStatus
			if err := client.doBody(ctx, "PATCH", "/v1/remote/settings", body, &status); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, status)
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.disabled"))
			// 已配对设备**不受影响**：关掉监听只是"暂时连不上"，
			// 再打开时它们仍然有效。把这一点说清楚，否则用户会以为
			// 关一次就要重新配对一遍。
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.devices_kept")+"\n",
				status.DeviceCount)
			return nil
		},
	}
}

func newRemotePairCmd(app *App) *cobra.Command {
	var (
		role  string
		label string
	)

	cmd := &cobra.Command{
		Use:   "pair",
		Short: i18n.T("cli.remote.pair_short"),
		Long:  i18n.T("cli.remote.pair_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			if role != "viewer" && role != "operator" {
				return app.fail(cmd, fmt.Errorf(i18n.T("cli.remote.bad_role"), role))
			}

			body, err := json.Marshal(map[string]any{"role": role, "label": label})
			if err != nil {
				return app.fail(cmd, err)
			}

			var session gen.PairingSession
			if err := client.doBody(ctx, "POST", "/v1/remote/pairing", body, &session); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, session)
			}
			renderPairing(app, session)
			return nil
		},
	}

	cmd.Flags().StringVar(&role, "role", "viewer", i18n.T("cli.remote.flag_role"))
	cmd.Flags().StringVar(&label, "label", "", i18n.T("cli.remote.flag_label"))
	return cmd
}

func newRemoteDevicesCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "devices",
		Short: i18n.T("cli.remote.devices_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var list gen.RemoteDeviceList
			if err := client.getInto(ctx, "/v1/remote/devices", &list); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, list)
			}
			renderRemoteDevices(app, list.Items)
			return nil
		},
	}
}

func newRemoteRevokeCmd(app *App) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "revoke <device-id>",
		Short: i18n.T("cli.remote.revoke_short"),
		Long:  i18n.T("cli.remote.revoke_long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			// 吊销会**级联**断掉这台设备派生出来的设备（手表的令牌来自
			// 手机）。级联本身是好事，但它不显眼，因此要先说一句 ——
			// 用户以为只断了手机、结果手表也断了，是那种"我什么都没干"
			// 的困惑。
			if !yes && !app.jsonOut {
				_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.revoke_hint")+"\n", args[0])
			}

			if err := client.delete(ctx, "/v1/remote/devices/"+args[0]); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]any{"revoked": args[0]})
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.revoked"))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, i18n.T("cli.remote.flag_yes"))
	return cmd
}

func newRemoteLogCmd(app *App) *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "log",
		Short: i18n.T("cli.remote.log_short"),
		Long:  i18n.T("cli.remote.log_long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			entries, err := fetchAudit(ctx, client, "remote.", "", limit)
			if err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, entries)
			}
			renderRemoteLog(app, entries)
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 50, i18n.T("cli.remote.flag_limit"))
	return cmd
}

// newRemoteApnsCmd 管理 APNs 凭据。
//
// 与界面走同一批接口（D19）：无头服务端上没有图形界面，而"把 .p8 装上"
// 这件事在装完系统服务之后仍然要能做。
func newRemoteApnsCmd(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apns",
		Short: i18n.T("cli.remote.apns_short"),
		Long:  i18n.T("cli.remote.apns_long"),
	}

	var (
		teamID   string
		keyID    string
		bundleID string
		keyFile  string
	)

	set := &cobra.Command{
		Use:   "set",
		Short: i18n.T("cli.remote.apns_set_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			// 私钥**从文件读**，不从命令行参数取。
			//
			// 命令行参数会出现在 shell 历史与进程列表里，而 .p8 是一把
			// 能给所有用户的手机发推送的钥匙。
			key, err := os.ReadFile(keyFile)
			if err != nil {
				return app.fail(cmd, fmt.Errorf(i18n.T("cli.remote.apns_key_read"), keyFile, err))
			}

			body, err := json.Marshal(map[string]string{
				"team_id": teamID, "key_id": keyID,
				"bundle_id": bundleID, "private_key": string(key),
			})
			if err != nil {
				return app.fail(cmd, err)
			}

			var status gen.ApnsStatus
			if err := client.putInto(ctx, "/v1/remote/apns", body, &status); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, status)
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.apns_saved"))
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.apns_hint"))
			return nil
		},
	}
	set.Flags().StringVar(&teamID, "team-id", "", i18n.T("cli.remote.flag_team"))
	set.Flags().StringVar(&keyID, "key-id", "", i18n.T("cli.remote.flag_key_id"))
	set.Flags().StringVar(&bundleID, "bundle-id", "", i18n.T("cli.remote.flag_bundle"))
	set.Flags().StringVar(&keyFile, "key-file", "", i18n.T("cli.remote.flag_key_file"))
	_ = set.MarkFlagRequired("team-id")
	_ = set.MarkFlagRequired("key-id")
	_ = set.MarkFlagRequired("bundle-id")
	_ = set.MarkFlagRequired("key-file")

	remove := &cobra.Command{
		Use:   "rm",
		Short: i18n.T("cli.remote.apns_rm_short"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}
			if err := client.delete(ctx, "/v1/remote/apns"); err != nil {
				return app.fail(cmd, err)
			}
			_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.apns_removed"))
			return nil
		},
	}

	cmd.AddCommand(set, remove)
	return cmd
}

// newRemoteTestPushCmd 给一台设备发一条测试推送。
func newRemoteTestPushCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "test-push <device-id>",
		Short: i18n.T("cli.remote.test_push_short"),
		Long:  i18n.T("cli.remote.test_push_long"),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			var result gen.ServiceActionResult
			if err := client.postInto(ctx,
				"/v1/remote/devices/"+args[0]+"/test-push", nil, &result); err != nil {
				return app.fail(cmd, err)
			}

			if app.jsonOut {
				return writeJSONOut(app.out, result)
			}
			// **失败也返回 0**：发送失败是一个结果，不是一次接口错误 ——
			// 而 Apple 给的那句话（BadDeviceToken、TopicDisallowed…）
			// 正是用户排查时唯一有用的东西。
			_, _ = fmt.Fprintln(app.out, result.Message)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

func renderRemoteStatus(app *App, status gen.RemoteStatus) {
	state := string(status.State)
	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.state_line")+"\n", state, status.Port)

	switch {
	case status.Listening != nil && *status.Listening:
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.listening"))
	default:
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.not_listening"))
	}

	if status.FingerprintShort != nil && *status.FingerprintShort != "" {
		// 短码同时显示：它是用户在两端用眼睛核对的那一串，
		// 而完整的 SPKI 太长，念不出来也记不住。
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.fingerprint")+"\n",
			*status.FingerprintShort)
	}

	if status.Addresses != nil && len(*status.Addresses) > 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.addresses"))
		for _, addr := range *status.Addresses {
			_, _ = fmt.Fprintf(app.out, "    https://%s\n", addr)
		}
	}

	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.device_count")+"\n", status.DeviceCount)

	if status.Pairing != nil {
		// 会话还开着是一件有时效的事，用户需要知道它什么时候失效。
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.pairing_active")+"\n",
			status.Pairing.Id, status.Pairing.ExpiresAt.Local().Format(time.Kitchen))
	}
	if status.LastError != nil && *status.LastError != "" {
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.listen_failed")+"\n", *status.LastError)
	}
}

func renderPairing(app *App, session gen.PairingSession) {
	_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.pair_title"))
	_, _ = fmt.Fprintln(app.out)

	// 六位码放在最显眼的位置：手输的人要照着一个字符一个字符敲，
	// 混在别的信息里会看错行。
	_, _ = fmt.Fprintf(app.out, "    %s\n\n", session.ManualCode)

	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.pair_role")+"\n", session.Role)
	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.pair_expires")+"\n",
		session.ExpiresAt.Local().Format(time.Kitchen))
	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.pair_fingerprint")+"\n",
		session.FingerprintShort)

	if len(session.Addresses) > 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.addresses"))
		for _, addr := range session.Addresses {
			_, _ = fmt.Fprintf(app.out, "    %s\n", addr)
		}
	}

	// 二维码内容也打出来：终端里扫不了码，但可以把它交给另一个
	// 工具（或者复制进一个二维码生成器）。手输路径永远可用。
	_, _ = fmt.Fprintln(app.out)
	_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.pair_payload")+"\n    %s\n", session.QrPayload)
	_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.pair_verify_hint"))
}

func renderRemoteDevices(app *App, devices []gen.RemoteDevice) {
	if len(devices) == 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.no_devices"))
		return
	}

	for _, d := range devices {
		state := i18n.T("cli.remote.device_active")
		if d.RevokedAt != nil {
			state = i18n.T("cli.remote.device_revoked")
		}
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.device_line")+"\n",
			state, d.Label, d.Role, d.Id)

		seen := i18n.T("cli.remote.device_never")
		if d.LastSeenAt != nil {
			seen = d.LastSeenAt.Local().Format(time.RFC3339)
		}
		_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.device_seen")+"\n", seen)

		// 派生的设备缩进显示一行来源：用户看到手表时会想知道
		// "它的权限是从哪来的"，而答案决定了吊销哪一台。
		if d.ParentDeviceId != nil && *d.ParentDeviceId != "" {
			_, _ = fmt.Fprintf(app.out, i18n.T("cli.remote.device_parent")+"\n",
				*d.ParentDeviceId)
		}
	}
}

func renderRemoteLog(app *App, entries []gen.AuditEntry) {
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(app.out, i18n.T("cli.remote.no_log"))
		return
	}
	for _, e := range entries {
		remote := ""
		if e.Remote != nil && *e.Remote != "" {
			remote = " [" + *e.Remote + "]"
		}
		_, _ = fmt.Fprintf(app.out, "%s  %-14s %-24s%s\n",
			e.Ts.Local().Format("01-02 15:04:05"), e.Result, e.Action, remote)
	}
}

// fetchAudit 拉取审计记录。
//
// 单独一个函数是因为 `isc remote log` 与将来的其它过滤视图都要用它，
// 而查询串的拼装容易在转义上出错。
func fetchAudit(ctx context.Context, client *Client, prefix, result string, limit int) ([]gen.AuditEntry, error) {
	query := "?limit=" + fmt.Sprint(limit)
	if prefix != "" {
		query += "&action_prefix=" + prefix
	}
	if result != "" {
		query += "&result=" + result
	}

	var list gen.AuditList
	if err := client.getInto(ctx, "/v1/audit"+query, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// trimRole 把角色名规范化，供展示用。
func trimRole(role string) string { return strings.TrimSpace(role) }
