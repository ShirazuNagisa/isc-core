package remote

import (
	"net"
	"testing"
)

// IPv6 地址的选择。
//
// # 这一层为什么值得测
//
// 挑错一个地址的症状是"域名昨天还能用，今天连不上了"，而用户什么都没改；
// 更糟的是它在地址轮换**之前**是能用的，于是看起来像网络抖动。
// 而真实机器上只有恰好那几条地址 —— 分支覆盖不到，因此用夹具。

// 下面这些行是从一台真实的 macOS 机器上抄下来的（`ifconfig en1`）。
//
// 那台机器正好四种情况齐全：一条 DHCPv6、一条 RFC 7217 稳定隐私地址、
// 两条会轮换的临时地址（其中一条已废弃）。这是最值钱的一组夹具 ——
// 它们证明我们不是"取第一个全局地址"，而是真的在读系统给的来历。
const (
	lineDHCPv6     = "\tinet6 2409:8a50:6a1:7450::560 prefixlen 64 dynamic"
	lineStableID   = "\tinet6 2409:8a50:6a1:7450:1847:3ee8:1edc:d3c9 prefixlen 64 autoconf secured"
	lineTemp       = "\tinet6 2409:8a50:6a1:7450:986e:c2be:2968:359c prefixlen 64 autoconf temporary"
	lineDeprecated = "\tinet6 2409:8a50:6a1:7450:7dd7:ece6:474:e99 prefixlen 64 deprecated autoconf temporary"
	lineULA        = "\tinet6 fd98:b9bb:be0::2 prefixlen 64"
	lineLinkLocal  = "\tinet6 fe80::41d:142c:678:d221%en1 prefixlen 64 secured scopeid 0xf"
)

func TestParseIfconfigInet6ReadsTheLifetime(t *testing.T) {
	t.Parallel()

	cases := []struct {
		line     string
		want     string
		lifetime Lifetime
	}{
		{lineDHCPv6, "2409:8a50:6a1:7450::560", LifetimeStable},
		{lineStableID, "2409:8a50:6a1:7450:1847:3ee8:1edc:d3c9", LifetimeStable},
		{lineTemp, "2409:8a50:6a1:7450:986e:c2be:2968:359c", LifetimeTemporary},
		// 一条地址可以同时是 temporary 与 deprecated（隐私扩展生命周期
		// 的末尾就是这样）。**"即将消失"是更强的结论**，必须赢。
		{lineDeprecated, "2409:8a50:6a1:7450:7dd7:ece6:474:e99", LifetimeDeprecated},
		// ULA 与链路本地没有标记，但也不该被选中。
		{lineULA, "fd98:b9bb:be0::2", LifetimeUnknown},
		// scope 后缀（%en1）必须去掉，否则 ParseIP 会失败。
		//
		// 注意它的来历是 **stable** 而不是 unknown：一条链路本地地址
		// 完全可以同时是稳定的（`secured` 就是 RFC 7217 的稳定隐私地址）。
		// 「够不够格进 DNS」与「会不会轮换」是**正交**的两件事 ——
		// 把它挡在外面的是 IsGlobalUnicastV6，不是它的来历。
		{lineLinkLocal, "fe80::41d:142c:678:d221", LifetimeStable},
	}

	for _, c := range cases {
		ip, lifetime, ok := parseIfconfigInet6(c.line)
		if !ok {
			t.Fatalf("解析失败：%q", c.line)
		}
		if ip.String() != c.want {
			t.Errorf("地址 = %s，期望 %s", ip, c.want)
		}
		if lifetime != c.lifetime {
			t.Errorf("%s 的来历 = %s，期望 %s", c.want, lifetime, c.lifetime)
		}
	}
}

func TestParseIfconfigInet6RejectsOtherLines(t *testing.T) {
	t.Parallel()

	for _, line := range []string{
		"\tinet 192.168.31.92 netmask 0xffffff00 broadcast 192.168.31.255",
		"\tether 88:66:5a:11:22:33",
		"en1: flags=8863<UP,BROADCAST,SMART,RUNNING,SIMPLEX,MULTICAST> mtu 1500",
		"",
	} {
		if _, _, ok := parseIfconfigInet6(line); ok {
			t.Errorf("不该解析出地址：%q", line)
		}
	}
}

// 这条是整件事的核心：四条全局地址，只有一条能进 DNS。
func TestSelectPublicIPv6SkipsRotatingAddresses(t *testing.T) {
	t.Parallel()

	// 刻意按"最容易被误选"的顺序排列：临时地址在前。
	// 一个"取第一个全局地址"的实现会挑中临时地址。
	addrs := []ScopedAddress{
		{Address: net.ParseIP("2409:8a50:6a1:7450:986e:c2be:2968:359c"), Iface: "en1", Lifetime: LifetimeTemporary},
		{Address: net.ParseIP("2409:8a50:6a1:7450:7dd7:ece6:474:e99"), Iface: "en1", Lifetime: LifetimeDeprecated},
		{Address: net.ParseIP("fe80::41d:142c:678:d221"), Iface: "en1", Lifetime: LifetimeStable},
		{Address: net.ParseIP("fd98:b9bb:be0::2"), Iface: "en1", Lifetime: LifetimeStable},
		{Address: net.ParseIP("2409:8a50:6a1:7450::560"), Iface: "en1", Lifetime: LifetimeStable},
	}

	ip, reason, ok := SelectPublicIPv6(addrs)
	if !ok {
		t.Fatalf("应当选中 ::560，得到失败：%s", reason)
	}
	if ip.String() != "2409:8a50:6a1:7450::560" {
		t.Fatalf("选中了 %s —— 链路本地、ULA 与临时地址都必须被排除", ip)
	}
}

// 同样的输入必须给出同样的结果。
//
// 依赖"先看到的那个"的实现在插拔网线、切 Wi-Fi 之后会换地址，
// 于是 DNS 记录跟着抖 —— 而用户看到的是"域名时好时坏"。
func TestSelectPublicIPv6IsDeterministic(t *testing.T) {
	t.Parallel()

	a := net.ParseIP("2409:8a50:6a1:7450::560")
	b := net.ParseIP("2409:8a50:6a1:7450::aa0")

	for _, order := range [][]ScopedAddress{
		{{Address: a, Lifetime: LifetimeStable}, {Address: b, Lifetime: LifetimeStable}},
		{{Address: b, Lifetime: LifetimeStable}, {Address: a, Lifetime: LifetimeStable}},
	} {
		ip, _, ok := SelectPublicIPv6(order)
		if !ok {
			t.Fatal("应当选中一个")
		}
		// 字典序最小者：「2409:...::560」 < 「2409:...::aa0」。
		if ip.String() != a.String() {
			t.Fatalf("枚举顺序影响了结果：%s", ip)
		}
	}
}

// 只有临时地址时**必须失败**，而不是退回其中一个。
func TestSelectPublicIPv6RefusesTemporaryOnly(t *testing.T) {
	t.Parallel()

	addrs := []ScopedAddress{
		{Address: net.ParseIP("2409:8a50:6a1:7450:986e:c2be:2968:359c"), Lifetime: LifetimeTemporary},
		{Address: net.ParseIP("2409:8a50:6a1:7450:7dd7:ece6:474:e99"), Lifetime: LifetimeDeprecated},
	}
	if ip, reason, ok := SelectPublicIPv6(addrs); ok {
		t.Fatalf("只有临时地址时不该给出结果，却给了 %s", ip)
	} else if reason == "" {
		t.Fatal("失败时必须说明原因 —— 界面要直接显示它")
	}
}

// 来历不明的地址：只有一个就用它，多个就拒绝。
//
// "拒绝"那一条不是保守过头：在开了隐私扩展的机器上，多个未知地址里
// 有一半以上是临时地址，赌错了就是一条几小时后失效的 DNS 记录。
// 而用户自己能看出哪个是哪个，让他填一步比让他排查"域名时好时坏"便宜得多。
func TestSelectPublicIPv6HandlesUnknownLifetimes(t *testing.T) {
	t.Parallel()

	one := []ScopedAddress{{Address: net.ParseIP("2409:8a50:6a1:7450::560"), Lifetime: LifetimeUnknown}}
	if _, _, ok := SelectPublicIPv6(one); !ok {
		t.Fatal("只有一条未知来历的地址时应当用它（没有可挑的余地）")
	}

	two := []ScopedAddress{
		{Address: net.ParseIP("2409:8a50:6a1:7450::560"), Lifetime: LifetimeUnknown},
		{Address: net.ParseIP("2409:8a50:6a1:7450::aa0"), Lifetime: LifetimeUnknown},
	}
	if ip, _, ok := SelectPublicIPv6(two); ok {
		t.Fatalf("多条来历不明的地址时应当拒绝，却选了 %s", ip)
	}
}

// 完全没有全局 IPv6 时给一句用户能懂的话。
func TestSelectPublicIPv6WithoutAnyGlobalAddress(t *testing.T) {
	t.Parallel()

	addrs := []ScopedAddress{
		{Address: net.ParseIP("fe80::1"), Lifetime: LifetimeStable},
		{Address: net.ParseIP("fd98:b9bb:be0::2"), Lifetime: LifetimeStable},
		{Address: net.ParseIP("192.168.31.92"), Lifetime: LifetimeStable},
		{Address: net.ParseIP("127.0.0.1"), Lifetime: LifetimeStable},
	}
	ip, reason, ok := SelectPublicIPv6(addrs)
	if ok {
		t.Fatalf("不该选出地址：%s", ip)
	}
	if reason == "" {
		t.Fatal("失败时必须说明原因")
	}
}

// 候选列表要**包含**临时地址，但排在后面。
//
// 把它们藏起来更糟：用户可能出于自己的理由想用其中一条，
// 而"我们不说，然后他不知道为什么连不上"是最难排查的一种。
func TestPublicIPv6CandidatesKeepsTemporaryButSortsItLast(t *testing.T) {
	t.Parallel()

	addrs := []ScopedAddress{
		{Address: net.ParseIP("2409:8a50:6a1:7450:986e:c2be:2968:359c"), Lifetime: LifetimeTemporary},
		{Address: net.ParseIP("2409:8a50:6a1:7450::560"), Lifetime: LifetimeStable},
		{Address: net.ParseIP("fe80::1"), Lifetime: LifetimeStable},
	}
	out := PublicIPv6Candidates(addrs)
	if len(out) != 2 {
		t.Fatalf("应当有 2 条候选（链路本地被排除），得到 %d", len(out))
	}
	if out[0].Lifetime != LifetimeStable {
		t.Fatalf("稳定的地址应当排在前面，实际第一条是 %s", out[0].Lifetime)
	}
	if out[1].Lifetime != LifetimeTemporary {
		t.Fatalf("临时地址应当保留在列表里，实际第二条是 %s", out[1].Lifetime)
	}
}

// ULA 永远不可路由。
//
// 路由器通常会给一个 ULA 前缀做内网用，而它**看起来**和公网地址很像 ——
// 写进公网 DNS 只会得到一个"能解析但连不上"的域名，比解析失败更难查。
func TestIsGlobalUnicastV6RejectsULAs(t *testing.T) {
	t.Parallel()

	good := []string{"2409:8a50:6a1:7450::560", "2001:db8::1"}
	bad := []string{
		"fd98:b9bb:be0::2", "fc00::1", // ULA
		"fe80::1",         // 链路本地
		"::1",             // 回环
		"::",              // 未指定
		"192.168.31.92",   // IPv4
		"::ffff:10.0.0.1", // IPv4 映射
	}
	for _, s := range good {
		if !IsGlobalUnicastV6(net.ParseIP(s)) {
			t.Errorf("%s 应当是全局单播", s)
		}
	}
	for _, s := range bad {
		if IsGlobalUnicastV6(net.ParseIP(s)) {
			t.Errorf("%s 不该被当成可用的公网地址", s)
		}
	}
	if IsGlobalUnicastV6(nil) {
		t.Error("nil 不该通过")
	}
}
