package remote

import (
	"net"
	"strings"
	"testing"
)

// 可达性自检的探测计划。
//
// # 这一层最容易错的是什么
//
// 不是"地址拼得对不对"，而是**该不该把某个地址列进去**。特别是：
// 计划里必须有局域网地址，因为手机在自家 Wi-Fi 上连公网地址也会
// 成功 —— 那个连接根本没出局域网。把它当成"公网可达"会让用户
// 以为已经验证过，而其实什么都没验证。

func TestBuildPublicProbeOrdersDomainFirstThenIPv6(t *testing.T) {
	t.Parallel()

	probe := BuildPublicProbe("mizar-abc.example.com", 8788, "https",
		net.ParseIP("2409:8a50:6a1:7450::560"), net.ParseIP("120.227.48.210"),
		[]string{"192.168.31.92:8788"})

	if len(probe.Targets) != 3 {
		t.Fatalf("目标数 = %d，期望 3", len(probe.Targets))
	}
	// 域名在前：它是**用户实际会用的那一条**，走地址成功只说明"这条路通"。
	if probe.Targets[0].Family != "host" {
		t.Fatalf("第一个目标应当是域名，实际是 %s", probe.Targets[0].Family)
	}
	// IPv6 第二：家用宽带上唯一可能可达的那一条。
	if probe.Targets[1].Family != "ipv6" {
		t.Fatalf("第二个目标应当是 IPv6，实际是 %s", probe.Targets[1].Family)
	}
	// IPv4 最后：多为大内网，最可能失败。
	if probe.Targets[2].Family != "ipv4" {
		t.Fatalf("第三个目标应当是 IPv4，实际是 %s", probe.Targets[2].Family)
	}

	if probe.Targets[0].URL != "https://mizar-abc.example.com:8788"+PublicProbePath {
		t.Fatalf("URL 不对: %s", probe.Targets[0].URL)
	}
	// IPv6 必须带方括号，否则那不是合法的 URL。
	if !strings.Contains(probe.Targets[1].URL, "[2409:8a50:6a1:7450::560]:8788") {
		t.Fatalf("IPv6 的 URL 缺少方括号: %s", probe.Targets[1].URL)
	}
	if probe.Port != 8788 {
		t.Fatalf("端口 = %d", probe.Port)
	}
}

// 局域网地址必须出现在计划里。
//
// 少了它，手机在自家 Wi-Fi 上就会把一次**根本没出局域网**的连接
// 报成"公网可达"。
func TestBuildPublicProbeCarriesLANAddresses(t *testing.T) {
	t.Parallel()

	lan := []string{"192.168.31.92:8788", "[2409:8a50:6a1:7450::560]:8788", "mac.local:8788"}
	probe := BuildPublicProbe("mizar-abc.example.com", 8788, "https",
		net.ParseIP("2409:8a50:6a1:7450::560"), nil, lan)

	if len(probe.LANAddresses) != len(lan) {
		t.Fatalf("局域网地址数 = %d，期望 %d", len(probe.LANAddresses), len(lan))
	}
	if probe.LANAddresses[0] != lan[0] {
		t.Fatalf("局域网地址被改动了: %v", probe.LANAddresses)
	}
}

// 没有可探测的地址时要**说明为什么**，而不是给一个空数组。
//
// 空数组在界面上显示成"什么都没有"，而用户需要知道的是
// "你还没开启公网访问"。
func TestBuildPublicProbeExplainsWhenEmpty(t *testing.T) {
	t.Parallel()

	probe := BuildPublicProbe("", 8788, "https", nil, nil, nil)
	if len(probe.Targets) != 0 {
		t.Fatalf("不该有目标: %+v", probe.Targets)
	}
	if probe.Note == "" {
		t.Fatal("没有目标时必须说明原因 —— 界面要直接显示它")
	}
}

// 同一个状态必须给出同样的顺序。
//
// 顺序不稳定时，两次自检的结论会因为"这次先试了 IPv6"而对不上，
// 而用户看到的是"结果时好时坏"。
func TestBuildPublicProbeIsDeterministic(t *testing.T) {
	t.Parallel()

	ipv6 := net.ParseIP("2409:8a50:6a1:7450::560")
	ipv4 := net.ParseIP("120.227.48.210")
	first := BuildPublicProbe("h.example.com", 8788, "https", ipv6, ipv4, nil)
	for i := 0; i < 20; i++ {
		again := BuildPublicProbe("h.example.com", 8788, "https", ipv6, ipv4, nil)
		if len(again.Targets) != len(first.Targets) {
			t.Fatal("目标数不稳定")
		}
		for j := range again.Targets {
			if again.Targets[j] != first.Targets[j] {
				t.Fatalf("第 %d 次调用给出了不同的顺序", i)
			}
		}
	}
}

// 没有 IPv4 时计划里就没有 IPv4 —— 这正是不写 A 记录的另一面。
func TestBuildPublicProbeWithoutIPv4(t *testing.T) {
	t.Parallel()

	probe := BuildPublicProbe("h.example.com", 8788, "https",
		net.ParseIP("2409::1"), nil, nil)
	for _, target := range probe.Targets {
		if target.Family == "ipv4" {
			t.Fatal("没有 IPv4 时不该出现 IPv4 目标")
		}
	}
}

func TestNormalizeReportedFamily(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"ipv6": "ipv6", "IPv6": "ipv6", " ipv6 ": "ipv6",
		"ipv4": "ipv4", "IPv4": "ipv4", "ip4": "ipv4",
		"": "", "bogus": "", "ipv5": "",
	}
	for in, want := range cases {
		if got := NormalizeReportedFamily(in); got != want {
			t.Errorf("NormalizeReportedFamily(%q) = %q，期望 %q", in, got, want)
		}
	}
}
