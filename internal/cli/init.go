package cli

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// newInitCmd 是首次使用的引导。
//
// # 它做什么、不做什么
//
// 它**探测环境并给出下一步的具体命令**，而不是一个问二十个问题的向导。
//
// 理由：这个产品的使用者多半已经知道自己想干什么（把某个服务开放出去），
// 而一个交互式向导会把他已经知道的事情再问一遍。他真正需要的是：
//
//	这台机器到底行不行（IPv6 有没有、前缀是什么、端口能不能绑）
//	接下去该敲哪几条命令（带上探测到的真实值）
//
// 因此 init 的输出是一份**填好了具体值的清单**，用户可以直接复制粘贴。
//
// 唯一的交互是问"要不要现在就把内核跑起来"—— 因为那一步之后
// 就不再需要终端了。
func newInitCmd(app *App) *cobra.Command {
	var (
		port   int
		domain string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: i18n.T("cli.init.short"),
		Long:  i18n.T("cli.init.long"),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := signalContext(cmd.Context())
			defer cancel()

			bundle := platform.Current(app.paths.Root())

			// 1. 环境探测（只读）。
			env := probeEnvironment(ctx, bundle)

			// 2. 内核是否在跑。
			running := daemonReachable(ctx, app.paths.RuntimeFile())

			if app.jsonOut {
				return writeJSONOut(app.out, map[string]any{
					"environment":      env,
					"daemon_reachable": running,
					"next_steps":       nextSteps(env, port, domain, running),
				})
			}

			renderInit(app, env, running, port, domain)
			return nil
		},
	}

	cmd.Flags().IntVar(&port, "port", 0,
		i18n.T("cli.init.flag_port"))
	cmd.Flags().StringVar(&domain, "domain", "",
		i18n.T("cli.init.flag_domain"))

	return cmd
}

// environment 是一次环境探测的结果。
type environment struct {
	// IPv6 是找到的公网 IPv6 地址（可能多个）。
	IPv6 []string `json:"ipv6"`
	// Prefix 是委派前缀。
	Prefix string `json:"prefix,omitempty"`
	// HasPublicIPv4 表示是否有公网 IPv4。
	HasPublicIPv4 bool `json:"has_public_ipv4"`
	// FirewallReady 表示防火墙后端是否可用。
	FirewallReady bool   `json:"firewall_ready"`
	FirewallNote  string `json:"firewall_note,omitempty"`
	// CanBindLowPorts 表示能否绑定 <1024 端口。
	CanBindLowPorts bool   `json:"can_bind_low_ports"`
	LowPortNote     string `json:"low_port_note,omitempty"`
	// LowPortBackend 是低端口能力检测的后端名。
	LowPortBackend string `json:"low_port_backend,omitempty"`

	// Interfaces 是找到地址的网卡名。
	Interfaces []string `json:"interfaces,omitempty"`
}

// probeEnvironment 只读地探测本机环境。
//
// **它不做任何系统变更**：init 是一个"看看情况"的命令，
// 而一个会改系统的引导命令会让用户不敢运行它。
func probeEnvironment(ctx context.Context, bundle *platform.Bundle) environment {
	var env environment

	// 网络地址与前缀。
	//
	// 用 GlobalIPv6() 而不是自己过滤：那个方法已经把链路本地、回环、
	// ULA、以及 Teredo/6to4 这类伪全局地址全都排除了，而"哪些地址
	// 算真的能对外用"这件事只该有一处定义。
	if bundle.IPMonitor != nil {
		if snap, err := bundle.IPMonitor.Snapshot(ctx); err == nil {
			for _, iface := range snap {
				// 虚拟接口（VPN、Docker 网桥、隧道）不参与 ——
				// 它们的地址对外没有意义，而列出来只会让用户困惑。
				if iface.IsLoopback || iface.IsVirtual {
					continue
				}

				for _, addr := range iface.GlobalIPv6() {
					env.IPv6 = append(env.IPv6, addr.String())
					env.Interfaces = appendUnique(env.Interfaces, iface.Name)
				}
				for _, addr := range iface.IPv4 {
					if isPublicIPv4(addr.AsSlice()) {
						env.HasPublicIPv4 = true
					}
				}
				// 取第一个委派前缀作为展示用的代表。
				//
				// 多网卡时会有多个，而 init 只需要让用户看到
				// "前缀是这样东西、它会变" —— 完整列表在 isc ip 里。
				if len(iface.Prefixes) > 0 && env.Prefix == "" {
					env.Prefix = iface.Prefixes[0].String()
				}
			}
		}
	}

	// 防火墙后端。
	fw := bundle.Capabilities().Firewall
	env.FirewallReady = fw.Available
	env.FirewallNote = fw.Note

	// 低端口能力。
	if bundle.LowPortBinder != nil {
		env.CanBindLowPorts = bundle.LowPortBinder.CanBindLowPorts()
		st := bundle.LowPortBinder.Describe()
		env.LowPortNote = st.Note
		env.LowPortBackend = st.Backend
	}

	return env
}

// renderInit 打印引导内容。
func renderInit(app *App, env environment, running bool, port int, domain string) {
	w := app.out

	_, _ = fmt.Fprintln(w, i18n.T("cli.init.title"))
	_, _ = fmt.Fprintln(w, strings.Repeat("=", 62))
	_, _ = fmt.Fprintln(w)

	// --- 环境 ---
	_, _ = fmt.Fprintln(w, i18n.T("cli.init.env_section"))

	if len(env.IPv6) > 0 {
		_, _ = fmt.Fprintf(w, i18n.T("cli.init.ipv6_ok"), strings.Join(env.IPv6, ", "))
		if len(env.Interfaces) > 0 {
			_, _ = fmt.Fprintf(w, i18n.T("cli.init.iface"), strings.Join(env.Interfaces, ", "))
		}
	} else {
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_a"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_b"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_c"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_d"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_e"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv6_missing_f"))
	}

	if env.Prefix != "" {
		_, _ = fmt.Fprintf(w, i18n.T("cli.init.prefix"), env.Prefix)
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.init.prefix_hint"))
	}

	if env.HasPublicIPv4 {
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.ipv4_public"))
	} else {
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.init.ipv4_cgnat"))
	}

	if env.FirewallReady {
		// "后端可用"与"你现在就能改防火墙"是两件事。
		//
		// 不说清楚的话，用户会以为接下来那步一定成功，而在
		// Windows 上放行端口需要管理员权限 —— 那个失败会来得
		// 很突然，且错误信息与他刚看到的"✅ 可用"矛盾。
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.fw_ok"))
	} else {
		_, _ = fmt.Fprintf(w, i18n.T("cli.init.fw_bad_a"), env.FirewallNote)
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.fw_bad_b"))
	}

	if env.CanBindLowPorts {
		_, _ = fmt.Fprintf(w, i18n.T("cli.init.lowport_ok"), env.LowPortBackend)
	} else {
		_, _ = fmt.Fprintf(w, i18n.T("cli.init.lowport_bad_a"), env.LowPortNote)
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.lowport_bad_b"))
	}

	// --- 内核状态 ---
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, i18n.T("cli.init.kernel_section"))
	if running {
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.kernel_running"))
	} else {
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.kernel_stopped"))
		_, _ = fmt.Fprintln(w, i18n.T("cli.init.start_now"))
		_, _ = fmt.Fprintln(w,
			i18n.T("cli.init.install_service"))
	}

	// --- 下一步 ---
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, i18n.T("cli.init.steps_section"))

	steps := nextSteps(env, port, domain, running)
	for i, s := range steps {
		_, _ = fmt.Fprintf(w, "  %d. %s\n", i+1, s.Desc)
		if s.Command != "" {
			_, _ = fmt.Fprintf(w, "     %s\n", s.Command)
		}
		if s.Note != "" {
			_, _ = fmt.Fprintf(w, "     %s\n", s.Note)
		}
	}

	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, strings.Repeat("-", 62))
	_, _ = fmt.Fprintln(w, i18n.T("cli.init.footer_undo"))
	_, _ = fmt.Fprintln(w,
		i18n.T("cli.init.footer_console"))
}

// step 是引导里的一条下一步。
//
// 字段导出以便 --json 输出 —— 而那个输出是给脚本与将来的 GUI 用的，
// 因此它必须是一个稳定的结构，而不是内部表示。
type step struct {
	Desc    string `json:"desc"`
	Command string `json:"command,omitempty"`
	Note    string `json:"note,omitempty"`
}

// nextSteps 生成下一步清单。
//
// 命令里**填入探测到的真实值**（端口、域名）—— 一份带占位符的清单
// 需要用户自己想清楚每个位置填什么，而那样他多半会去翻文档。
func nextSteps(env environment, port int, domain string, running bool) []step {
	var steps []step

	// 1. 内核跑起来。
	if !running {
		steps = append(steps, step{
			Desc:    i18n.T("cli.init.step_run_desc"),
			Command: "isc daemon run",
			Note:    i18n.T("cli.init.step_run_note"),
		})
	}

	// 2. 配 DNS 凭据。
	if domain != "" {
		steps = append(steps, step{
			Desc:    i18n.T("cli.init.step_cred_desc"),
			Command: i18n.T("cli.init.step_cred_cmd"),
			Note: i18n.T("cli.init.step_cred_note_a") +
				i18n.T("cli.init.step_cred_note_b"),
		})

		// 3. 建动态解析任务。
		recType := "AAAA"
		if len(env.IPv6) == 0 {
			recType = "A"
		}
		steps = append(steps, step{
			Desc: i18n.T("cli.init.step_ddns_desc"),
			Command: fmt.Sprintf(i18n.T("cli.init.step_ddns_cmd"),
				domain, recType, sourceFor(env, recType)),
			Note: i18n.T("cli.init.step_ddns_note"),
		})
	} else {
		steps = append(steps, step{
			Desc:    i18n.T("cli.init.step_cred_desc"),
			Command: i18n.T("cli.init.step_cred_cmd"),
			Note:    i18n.T("cli.init.step_cred_note2"),
		})
		steps = append(steps, step{
			Desc:    i18n.T("cli.init.step_ddns_desc"),
			Command: i18n.T("cli.init.step_ddns_cmd2"),
			Note:    i18n.T("cli.init.step_ddns_note2"),
		})
	}

	// 4. 放行端口。
	if port > 0 {
		steps = append(steps, step{
			Desc:    i18n.T("cli.init.step_expose_desc"),
			Command: fmt.Sprintf(i18n.T("cli.init.step_expose_cmd"), port),
			Note:    i18n.T("cli.init.step_expose_note"),
		})
	}

	// 5. 确认真的通了。
	steps = append(steps, step{
		Desc:    i18n.T("cli.init.step_verify_desc"),
		Command: "isc verify",
		Note: i18n.T("cli.init.step_verify_note_a") +
			i18n.T("cli.init.step_verify_note_b") +
			i18n.T("cli.init.step_verify_note_c"),
	})

	return steps
}

// sourceFor 按地址类型选择地址来源。
func sourceFor(env environment, recType string) string {
	if recType == "AAAA" && len(env.IPv6) > 0 {
		return "ipv6"
	}
	return "ipv4"
}

// isPublicIPv4 判断是否为公网 IPv4。
//
// 与 platform.IsGlobalIPv6 同样的思路：只认**真的能路由**的地址。
// 私有段、回环、链路本地、CGNAT 段（100.64/10）都不算 ——
// 国内家宽拿到的"公网 IP"很多其实是 CGNAT 段的，而那是不能对外服务的。
func isPublicIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	if v4.IsLoopback() || v4.IsPrivate() || v4.IsLinkLocalUnicast() ||
		v4.IsUnspecified() || v4.IsMulticast() {
		return false
	}
	// CGNAT 段 100.64.0.0/10（RFC 6598）。
	//
	// 这是国内家宽最常见的情况：运营商给的"公网 IP"落在这一段里，
	// 而它**不能**用来对外提供服务。不排除它会让 init 给出
	// "你有公网 IPv4" 的错误结论。
	if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	// 169.254/16（APIPA）已由 IsLinkLocalUnicast 覆盖。
	return true
}

// appendUnique 往切片里加一个不重复的元素。
func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}
