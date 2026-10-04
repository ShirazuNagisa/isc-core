package remote

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件解决一个看起来简单、实际上很容易搞错的问题：
//
//	**本机有好几个全局 IPv6 地址，哪一个能写进公网 DNS？**
//
// # 为什么不能随便挑一个
//
// 现代系统默认开启 IPv6 隐私扩展（RFC 8981）：除了一条稳定的地址之外，
// 还会有一到多条**临时地址**，出站连接优先用它们，并且每隔几小时轮换一次。
//
// 如果 DNS 里写的是临时地址，后果是：
//
//   - 几小时（甚至几十分钟）之后那条记录就指向一个已经不存在的地址；
//   - 症状是"域名昨天还能用，今天连不上了"，而用户什么都没改；
//   - 更糟的是它**偶尔能连上**（轮换前），于是看起来像网络抖动。
//
// 因此选择规则不是"取第一个全局地址"，而是**按来历挑**：稳定优先，
// 临时的一律不要。系统其实知道每一条的来历（`ifconfig` 里的
// `temporary` / `deprecated` / `dynamic` 标记），只是 Go 的
// `net.Interfaces()` 把它丢掉了 —— 于是这里按平台去问系统。

// Lifetime 描述一条地址的来历。
type Lifetime int

const (
	// LifetimeUnknown 表示平台没有告诉我们。
	//
	// 它不是"临时"，也不是"稳定" —— 上层据此拒绝自动选择，
	// 而不是赌一把。
	LifetimeUnknown Lifetime = iota
	// LifetimeStable 是长期不变的地址：DHCPv6 分配，或 RFC 7217
	// 的稳定隐私地址（`autoconf secured`）。
	LifetimeStable
	// LifetimeTemporary 是隐私扩展生成的临时地址，会轮换。
	LifetimeTemporary
	// LifetimeDeprecated 已被标记废弃，即将消失。
	LifetimeDeprecated
)

func (l Lifetime) String() string {
	switch l {
	case LifetimeStable:
		return "stable"
	case LifetimeTemporary:
		return "temporary"
	case LifetimeDeprecated:
		return "deprecated"
	default:
		return "unknown"
	}
}

// ScopedAddress 是一条本机地址及其来历。
type ScopedAddress struct {
	Address  net.IP
	Iface    string
	Lifetime Lifetime
}

// IsGlobalUnicastV6 报告这个地址能不能出现在公网 DNS 里。
//
// 排除三类：
//
//   - 回环与未指定；
//   - 链路本地（fe80::/10）—— 只在单个链路内有效；
//   - **唯一本地地址（fc00::/7）**—— 用户机器上常见（路由器会给一个
//     ULA 前缀做内网用），但它永远不可路由，写进公网 DNS 只会得到一个
//     "能解析但连不上"的域名，而那比解析失败更难排查。
func IsGlobalUnicastV6(ip net.IP) bool {
	if ip == nil || ip.To4() != nil {
		return false
	}
	if !ip.IsGlobalUnicast() {
		return false
	}
	if ip.IsLinkLocalUnicast() || ip.IsLoopback() || ip.IsUnspecified() {
		return false
	}
	// fc00::/7 覆盖 fc00:: 与 fd00::。
	if len(ip) == net.IPv6len && ip[0]&0xfe == 0xfc {
		return false
	}
	return true
}

// SelectPublicIPv6 从本机地址里挑出**唯一**一个可以写进公网 DNS 的地址。
//
// 它必须满足两个性质，否则上层的记录会来回抖：
//
//   - **确定性**：同样的输入总是给出同样的结果。多个同等候选时按地址
//     字典序取最小，而不是"先看到的那个"—— 后者取决于接口枚举顺序，
//     而那个顺序在插拔网线、切 Wi-Fi 之后会变。
//   - **拒绝而不是猜**：候选里只要还有来历不明的地址，就不在它们之间
//     乱选。宁可让用户看到一个"无法确定"并自己指定，也不要写一条
//     几小时后就失效的记录。
//
// 返回的 reason 在失败时说明为什么，供界面直接显示。
func SelectPublicIPv6(addrs []ScopedAddress) (net.IP, string, bool) {
	var stable []ScopedAddress

	for _, a := range addrs {
		if !IsGlobalUnicastV6(a.Address) {
			continue
		}
		switch a.Lifetime {
		case LifetimeDeprecated:
			// 已经在消失的路上了。
			continue
		case LifetimeTemporary:
			// 轮换中。写进 DNS 就是几小时后失效。
			continue
		case LifetimeStable:
			stable = append(stable, a)
		case LifetimeUnknown:
			// 见下：它不会进入候选，但会影响结论。
		}
	}

	if len(stable) > 0 {
		// # 为什么按字典序，而不是"更好的那一个"
		//
		// 一台机器上通常有两条稳定地址：DHCPv6 分配的，与 RFC 7217 的
		// 稳定隐私地址（macOS 的 `autoconf secured`）。两者都长期不变，
		// 而**哪一个更稳取决于网络环境**：前缀不变时 RFC 7217 更稳，
		// 路由器的 DHCPv6 池不变时它更稳。没有普遍更优的选择。
		//
		// 于是这里只保证**确定性** —— 同样的输入永远给出同样的地址。
		// 真正"哪一条能连上"由可达性自检去**测**，而不是在这里猜：
		// 自检会拿真实客户端去连我们实际写进 DNS 的那一条。
		sort.Slice(stable, func(i, j int) bool {
			return stable[i].Address.String() < stable[j].Address.String()
		})
		return stable[0].Address, "", true
	}

	// 走到这里说明没有"已知稳定"的地址。分三种情况给不同的话，
	// 因为用户该做的事完全不同。
	var unknown int
	for _, a := range addrs {
		if IsGlobalUnicastV6(a.Address) && a.Lifetime == LifetimeUnknown {
			unknown++
		}
	}
	switch {
	case unknown > 1:
		return nil, i18n.T("remote.public.err.addr_ambiguous"), false
	case unknown == 1:
		// 只有一个，没有可挑的余地，用它。
		for _, a := range addrs {
			if IsGlobalUnicastV6(a.Address) && a.Lifetime == LifetimeUnknown {
				return a.Address, "", true
			}
		}
	}
	return nil, i18n.T("remote.public.err.no_ipv6"), false
}

// PublicIPv6Candidates 返回可以出现在界面上的全部候选，供用户手动选择。
//
// 包含临时地址但**标注出来**：用户可能出于自己的理由想用它，而
// "我们把它藏起来，然后他不知道为什么连不上"更糟。
func PublicIPv6Candidates(addrs []ScopedAddress) []ScopedAddress {
	out := make([]ScopedAddress, 0, len(addrs))
	for _, a := range addrs {
		if IsGlobalUnicastV6(a.Address) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lifetime != out[j].Lifetime {
			return out[i].Lifetime < out[j].Lifetime
		}
		return out[i].Address.String() < out[j].Address.String()
	})
	return out
}

// parseIfconfigInet6 解析 `ifconfig` 的一条 `inet6` 行。
//
// 抽成纯函数是为了能测：`ifconfig` 的输出格式固定但看着啰嗦，
// 而在真实机器上只有恰好那几条地址 —— 分支覆盖不到。
//
// 形如：
//
//	inet6 2409:8a50:6a1:7450::560 prefixlen 64 dynamic
//	inet6 2409:8a50:...:359c prefixlen 64 autoconf temporary
//	inet6 fd98:b9bb:be0::2 prefixlen 64
func parseIfconfigInet6(line string) (net.IP, Lifetime, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "inet6" {
		return nil, LifetimeUnknown, false
	}

	// 去掉 %en1 这样的 scope 后缀。
	raw := fields[1]
	if idx := strings.IndexByte(raw, '%'); idx >= 0 {
		raw = raw[:idx]
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, LifetimeUnknown, false
	}

	// deprecated 优先于 temporary：一条地址可以同时是两者
	// （隐私扩展的正常生命周期末尾就是这样），而"即将消失"
	// 是更强的结论。
	lifetime := LifetimeUnknown
	for _, f := range fields[2:] {
		switch f {
		case "deprecated":
			return ip, LifetimeDeprecated, true
		case "temporary":
			lifetime = LifetimeTemporary
		}
	}
	if lifetime == LifetimeTemporary {
		return ip, lifetime, true
	}

	// `dynamic` 是 DHCPv6 分配的；`secured` 是 RFC 7217 的稳定隐私地址。
	// 两者都长期不变。
	for _, f := range fields[2:] {
		if f == "dynamic" || f == "secured" {
			return ip, LifetimeStable, true
		}
	}
	return ip, LifetimeUnknown, true
}

// describeAddresses 把候选压成一行，供日志与界面使用。
func describeAddresses(addrs []ScopedAddress) string {
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, fmt.Sprintf("%s(%s)", a.Address, a.Lifetime))
	}
	return strings.Join(parts, " ")
}
