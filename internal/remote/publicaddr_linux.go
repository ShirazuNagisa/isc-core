//go:build linux

package remote

import (
	"net"
	"os/exec"
	"strings"
)

// localScopedAddresses 通过 `ip -6 addr` 取本机地址及其来历。
//
// Linux 上与 darwin 同样的理由：`/proc/net/if_inet6` 里**没有**生存期
// 信息，而 `IPV6_ADDR_TEMPORARY` 这类标志只能从 netlink 拿到。
// 手写 netlink 消息解析（RTM_GETADDR + IFA_F_TEMPORARY/IFA_F_DEPRECATED）
// 是可行的，但那是一大块与业务无关的二进制解析，而 `ip` 命令输出的
// 恰恰就是那几个词的文本形式。
//
// 容器里可能没有 `ip`（极简镜像）—— 那时返回错误，上层会拒绝自动选择
// 并让用户手填，而不是随便挑一个可能几小时后就失效的地址。
func localScopedAddresses() ([]ScopedAddress, error) {
	out, err := exec.Command("ip", "-6", "addr", "show").Output()
	if err != nil {
		return nil, err
	}

	var (
		result []ScopedAddress
		iface  string
	)
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// 接口行：`2: en0: <BROADCAST,...> mtu 1500`
		if !strings.HasPrefix(line, " ") && strings.Contains(trimmed, ": <") {
			parts := strings.SplitN(trimmed, ":", 3)
			if len(parts) >= 2 {
				iface = strings.TrimSpace(parts[1])
				// `ip` 会在接口名后带上 @parent（vlan 之类）。
				if idx := strings.IndexByte(iface, '@'); idx > 0 {
					iface = iface[:idx]
				}
			}
			continue
		}

		// 地址行：`inet6 2409:...::560/64 scope global dynamic mngtmpaddr`
		fields := strings.Fields(trimmed)
		if len(fields) < 3 || fields[0] != "inet6" {
			continue
		}
		// 只要 scope global：link 与 host 都不该出现在公网 DNS 里。
		global := false
		for _, f := range fields[2:] {
			if f == "scope" {
				continue
			}
			if f == "global" {
				global = true
			}
		}
		if !global {
			continue
		}

		raw := fields[1]
		if idx := strings.IndexByte(raw, '/'); idx > 0 {
			raw = raw[:idx]
		}
		ip := net.ParseIP(raw)
		if ip == nil {
			continue
		}

		// 与 darwin 同样的优先级：deprecated 是更强的结论。
		lifetime := LifetimeUnknown
		for _, f := range fields[2:] {
			switch f {
			case "deprecated":
				lifetime = LifetimeDeprecated
			case "temporary":
				if lifetime != LifetimeDeprecated {
					lifetime = LifetimeTemporary
				}
			}
		}
		if lifetime == LifetimeUnknown {
			for _, f := range fields[2:] {
				// `dynamic` 是 DHCPv6 / RA 给的；`mngtmpaddr` 是
				// 隐私扩展用的模板地址，它本身长期不变。
				if f == "dynamic" || f == "mngtmpaddr" || f == "permanent" {
					lifetime = LifetimeStable
					break
				}
			}
		}
		result = append(result, ScopedAddress{Address: ip, Iface: iface, Lifetime: lifetime})
	}
	return result, nil
}
