package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// statusOutput 是 `isc status --json` 的输出结构。
//
// 它把 runtime.json 的发现结果与 API 的元信息合成一份完整画像：
// 调用方（脚本或 GUI 的"连通性检查"）只需要一次调用就能知道
// 内核在不在、走的哪条通道、本平台哪些能力可用。
type statusOutput struct {
	Running bool `json:"running"`

	// Transport 是实际连上的通道类型（named_pipe / unix_socket / loopback）。
	Transport string `json:"transport,omitempty"`
	// Endpoint 是通道地址。
	Endpoint string `json:"endpoint,omitempty"`

	Health *gen.Health `json:"health,omitempty"`
	Meta   *gen.Meta   `json:"meta,omitempty"`

	// Capabilities 是各平台后端的可用性摘要，便于一眼看出功能降级。
	Capabilities map[string]gen.ImplState `json:"capabilities,omitempty"`
}

func newStatusCmd(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: i18n.T("cli.status.short"),
		Long:  i18n.T("cli.status.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			client, err := app.connect(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			health, err := client.Health(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}
			meta, err := client.Meta(ctx)
			if err != nil {
				return app.fail(cmd, err)
			}

			out := statusOutput{
				Running:   true,
				Transport: transportKey(client.Endpoint()),
				Endpoint:  client.Endpoint().String(),
				Health:    &health,
				Meta:      &meta,
				Capabilities: map[string]gen.ImplState{
					"firewall":        meta.Capabilities.Firewall,
					"service_manager": meta.Capabilities.ServiceManager,
					"ip_monitor":      meta.Capabilities.IpMonitor,
					"secret_store":    meta.Capabilities.SecretStore,
					"transport":       meta.Capabilities.Transport,
					"low_port_binder": meta.Capabilities.LowPortBinder,
				},
			}

			if app.jsonOut {
				enc := json.NewEncoder(app.out)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}
			return printStatusText(app, out)
		},
	}
}

// transportKey 把 endpoint 归一到稳定的机器可读标识。
func transportKey(ep platform.Endpoint) string {
	switch ep.Scheme() {
	case platform.SchemeNamedPipe:
		return "named_pipe"
	case platform.SchemeUnix:
		return "unix_socket"
	case platform.SchemeTCP:
		return "loopback"
	default:
		return "unknown"
	}
}

// printStatusText 以人类可读形式输出状态。
func printStatusText(app *App, s statusOutput) error {
	w := app.out
	_, _ = fmt.Fprintf(w, "%s\n", i18n.T("cli.status_header"))
	_, _ = fmt.Fprintf(w, "  %-14s %s\n", "status", "running")
	_, _ = fmt.Fprintf(w, "  %-14s %s\n", "transport", s.Transport)
	_, _ = fmt.Fprintf(w, "  %-14s %s\n", "endpoint", s.Endpoint)

	if s.Meta != nil {
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", "version", s.Meta.Version)
		_, _ = fmt.Fprintf(w, "  %-14s %s/%s\n", "platform", s.Meta.Os, s.Meta.Arch)
	}
	if s.Health != nil {
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", "health", string(s.Health.Status))
		_, _ = fmt.Fprintf(w, "  %-14s %s\n", "uptime", formatDuration(s.Health.UptimeSeconds))
	}

	_, _ = fmt.Fprintln(w, i18n.T("cli.status.backends"))
	for _, name := range []string{
		"firewall", "service_manager", "ip_monitor",
		"secret_store", "transport", "low_port_binder",
	} {
		st := s.Capabilities[name]
		mark := "✓"
		if !st.Available {
			mark = "✗"
		}
		line := fmt.Sprintf("  %s %-16s %s", mark, name, st.Backend)
		if st.Note != nil && *st.Note != "" {
			line += "  — " + *st.Note
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	return nil
}

// formatDuration 把秒数格式化成便于阅读的形式。
func formatDuration(sec int64) string {
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	if sec < 3600 {
		return fmt.Sprintf("%dm%ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh%dm", sec/3600, (sec%3600)/60)
}
