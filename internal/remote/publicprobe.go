package remote

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件实现**可达性自检**。
//
// # 为什么内核测不了自己
//
// "外面能不能连进来"这件事，从里面测不了。所有本机发起的连接都会走
// 内部路径：同网段时走二层，跨网段时走 NAT 环回，而这两种都与真实
// 客户端的路径不同。实测过一次就知道这不是理论问题 —— 用公共探测
// 服务时，25 个节点里只有 2 个有 IPv6，而它们都在伊朗，
// 连不上中国移动的地址，得到的结论毫无意义。
//
// # 手机才是正确的探针
//
// 它就在真实客户端要走的网络上（蜂窝数据 / 别处的 Wi-Fi），
// 它要连的正是那个域名，而它**已经装着我们的代码**。
// 因此内核下发一份"探测计划"，手机逐个试，把结果报回来。
//
// # 假阳性是这里最容易犯的错
//
// 手机在自己家里的 Wi-Fi 上时，连公网地址**也会成功** ——
// 那个连接根本没出局域网。把它当成"公网可达"会让用户把内核暴露在
// 公网上却以为已经验证过了，或者反过来在真的不通时以为通了。
//
// 因此探测计划里必须带上局域网地址，让手机先试它们：
// 局域网通 → 它就在同一个网里 → 这次探测**不作数**。

// PublicTarget 是手机要试的一个地址。
type PublicTarget struct {
	// Family 是地址族：ipv6 / ipv4。
	Family string `json:"family"`
	// URL 是完整可请求的地址。
	//
	// 给完整 URL 而不是 host:port：手机不该自己拼路径，
	// 因为路径属于契约，将来会变。
	URL string `json:"url"`
	// Address 是这条 URL 指向的地址，供界面显示。
	Address string `json:"address"`
}

// PublicProbe 是一次可达性自检的探测计划。
type PublicProbe struct {
	// Host 是子域名；还没建立时为空。
	Host string `json:"host,omitempty"`
	// Port 是远程监听端口。
	Port int `json:"port"`
	// Targets 是要尝试的公网地址，按建议顺序排列。
	Targets []PublicTarget `json:"targets"`
	// LANAddresses 是局域网内的候选地址。
	//
	// 手机先试这些：**通了就说明它和内核在同一个网络里**，
	// 那次公网探测不作数（见文件开头的说明）。
	LANAddresses []string `json:"lan_addresses,omitempty"`
	// Note 是给用户看的一句话；没有可探测的地址时说明为什么。
	Note string `json:"note,omitempty"`
}

// PublicProbePath 是探针端点。
//
// 它**免鉴权**，因为用户最需要它的时刻恰好是"还没配对成功"的时候
// （想确认公网通不通，然后扫码）。它也必须是一个远程面的
// 公开路径，见 remoteRoutes。
const PublicProbePath = "/v1/remote/ping"

// PublicProbeURL 拼出给定 host:port 的探针地址。
func PublicProbeURL(hostPort, scheme string) string {
	if scheme == "" {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, hostPort, PublicProbePath)
}

// BuildPublicProbe 组装探测计划。
//
// 参数都是"当前已知的事实"，函数本身不取网络也不读文件 ——
// 因此它可以被完整地单元测试，而这一层最容易错的恰恰是
// "该不该把某个地址列进去"这类判断。
func BuildPublicProbe(host string, port int, scheme string,
	ipv6, ipv4 net.IP, lanAddresses []string) PublicProbe {

	probe := PublicProbe{Host: host, Port: port, LANAddresses: lanAddresses}

	if host != "" {
		// 子域名本身优先：它是**用户会用的那一条**，而手机在域名上
		// 成功，才算真的能用。走地址成功只说明"这条路通"。
		probe.Targets = append(probe.Targets, PublicTarget{
			Family:  familyOfHost(host),
			URL:     PublicProbeURL(net.JoinHostPort(host, fmt.Sprint(port)), scheme),
			Address: host,
		})
	}
	if ipv6 != nil {
		hostPort := net.JoinHostPort(ipv6.String(), fmt.Sprint(port))
		probe.Targets = append(probe.Targets, PublicTarget{
			Family: "ipv6", URL: PublicProbeURL(hostPort, scheme), Address: ipv6.String(),
		})
	}
	if ipv4 != nil {
		hostPort := net.JoinHostPort(ipv4.String(), fmt.Sprint(port))
		probe.Targets = append(probe.Targets, PublicTarget{
			Family: "ipv4", URL: PublicProbeURL(hostPort, scheme), Address: ipv4.String(),
		})
	}

	if len(probe.Targets) == 0 {
		probe.Note = i18n.T("remote.public.note.no_target")
	}
	// 计划要**确定**：同一个状态给出同样的顺序，否则两次自检的结论
	// 会因为顺序不同而对不上。
	sort.SliceStable(probe.Targets, func(i, j int) bool {
		return targetRank(probe.Targets[i]) < targetRank(probe.Targets[j])
	})
	return probe
}

// targetRank 决定尝试顺序。
//
// 域名在前（用户实际用的那一条），然后 IPv6（家用宽带上唯一可能
// 可达的那一条），最后 IPv4（多为大内网，最可能失败）。
func targetRank(t PublicTarget) int {
	switch {
	case t.Family == "host":
		return 0
	case t.Family == "ipv6":
		return 1
	default:
		return 2
	}
}

// familyOfHost 判断一个主机名会解析成哪一族。
//
// 它只是**标注**：真正的判定要等手机解析。写错了不会导致漏测，
// 只是界面上的标签不准。
func familyOfHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() != nil {
			return "ipv4"
		}
		return "ipv6"
	}
	// 域名：我们建的是 AAAA，因此标 ipv6。标记只用于显示。
	return "host"
}

// NormalizeReportedFamily 把手机报回来的地址族归一。
func NormalizeReportedFamily(family string) string {
	switch strings.ToLower(strings.TrimSpace(family)) {
	case "ipv6", "ip4", "ipv4":
		lowered := strings.ToLower(strings.TrimSpace(family))
		if lowered == "ipv6" {
			return "ipv6"
		}
		return "ipv4"
	default:
		return ""
	}
}
