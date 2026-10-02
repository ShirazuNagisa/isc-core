package cli

import (
	"net"
	"strings"
	"testing"
)

// TestIsPublicIPv4 钉住"什么算公网 IPv4"。
//
// # 为什么 CGNAT 那一条特别重要
//
// 国内家宽最常见的说法是"运营商给了公网 IP"，而实际拿到的地址常常落在
// 100.64.0.0/10（RFC 6598，运营商级 NAT）里。那段地址**不能**用来对外
// 提供服务 —— 它是运营商内部的一层 NAT。
//
// 不排除它会让 isc init 给出"你有公网 IPv4"的结论，而用户据此去配
// A 记录，然后在外面怎么都访问不通。他会去查路由器、查 DNS、查运营商，
// 而真正的原因是那个地址从一开始就不通。
func TestIsPublicIPv4(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ip   string
		want bool
		why  string
	}{
		// --- 真的公网 ---
		{"203.0.113.7", true, "普通公网地址"},
		{"8.8.8.8", true, "公网"},
		{"1.1.1.1", true, "公网"},

		// --- 私有段 ---
		{"192.168.1.1", false, "RFC1918"},
		{"10.0.0.1", false, "RFC1918"},
		{"172.16.0.1", false, "RFC1918"},
		{"172.31.255.254", false, "RFC1918 边界内"},

		// --- CGNAT：国内家宽最常见的情况 ---
		{"100.64.0.1", false, "RFC6598 CGNAT 起始"},
		{"100.100.100.100", false, "CGNAT 中段（运营商常用）"},
		{"100.127.255.254", false, "RFC6598 CGNAT 结束"},

		// --- 其它不能对外用的 ---
		{"127.0.0.1", false, "回环"},
		{"169.254.1.1", false, "APIPA 链路本地"},
		{"0.0.0.0", false, "未指定"},
		{"224.0.0.1", false, "组播"},

		// --- IPv6 不是 IPv4 ---
		{"2409:8a50:6a1:7450::50b", false, "IPv6 不是公网 IPv4"},
		{"::1", false, "IPv6 回环"},
	}

	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("测试数据里的地址无法解析: %q", tc.ip)
		}
		if got := isPublicIPv4(ip); got != tc.want {
			t.Errorf("isPublicIPv4(%s) = %v，期望 %v（%s）",
				tc.ip, got, tc.want, tc.why)
		}
	}
}

// TestIsPublicIPv4CGNATBoundary 钉住 CGNAT 段的两个边界。
//
// 100.64.0.0/10 覆盖 100.64.0.0 – 100.127.255.255。
// 边界写错一格就会把 100.63.x.x（真实公网）误判成 CGNAT，
// 或者把 100.128.x.x（真实公网）当成可用地址。
func TestIsPublicIPv4CGNATBoundary(t *testing.T) {
	t.Parallel()

	// 刚好在段外 —— 是公网。
	if !isPublicIPv4(net.ParseIP("100.63.255.255")) {
		t.Error("100.63.255.255 在 CGNAT 段之外，应当算公网")
	}
	if !isPublicIPv4(net.ParseIP("100.128.0.0")) {
		t.Error("100.128.0.0 在 CGNAT 段之外，应当算公网")
	}

	// 刚好在段内 —— 不是公网。
	if isPublicIPv4(net.ParseIP("100.64.0.0")) {
		t.Error("100.64.0.0 是 CGNAT 段的起点，不该算公网")
	}
	if isPublicIPv4(net.ParseIP("100.127.255.255")) {
		t.Error("100.127.255.255 是 CGNAT 段的终点，不该算公网")
	}
}

func TestAppendUnique(t *testing.T) {
	t.Parallel()

	got := appendUnique(nil, "a")
	got = appendUnique(got, "b")
	got = appendUnique(got, "a") // 重复
	got = appendUnique(got, "b") // 重复

	if len(got) != 2 {
		t.Fatalf("应当只有 2 个元素，得到 %v", got)
	}
	if got[0] != "a" || got[1] != "b" {
		t.Errorf("顺序被改变了: %v", got)
	}
}

// TestSourceFor 验证地址来源的选择。
//
// 没有 IPv6 时仍给 AAAA 会让生成出来的命令在用户机器上必然失败，
// 而那条命令是 init 直接给他复制的。
func TestSourceFor(t *testing.T) {
	t.Parallel()

	withV6 := environment{IPv6: []string{"2409::1"}}
	withoutV6 := environment{}

	if got := sourceFor(withV6, "AAAA"); got != "ipv6" {
		t.Errorf("有 IPv6 时应当用 ipv6 来源，得到 %q", got)
	}
	if got := sourceFor(withoutV6, "AAAA"); got != "ipv4" {
		t.Errorf("没有 IPv6 时不该仍然给 AAAA，得到 %q", got)
	}
	if got := sourceFor(withV6, "A"); got != "ipv4" {
		t.Errorf("A 记录应当用 ipv4 来源，得到 %q", got)
	}
}

// TestNextStepsIncludesVerify 钉住"验证那一步永远在"。
//
// 它是整条链路里唯一能区分「本机没配好」与「运营商封了」的手段。
// 少了它，用户会在一个自己无法判断的状态里反复折腾。
func TestNextStepsIncludesVerify(t *testing.T) {
	t.Parallel()

	cases := []environment{
		{},
		{IPv6: []string{"2409::1"}},
		{Prefix: "2409::/64", FirewallReady: true},
	}

	for i, env := range cases {
		steps := nextSteps(env, 0, "", false)

		found := false
		for _, s := range steps {
			if strings.Contains(s.Command, "isc verify") {
				found = true
				// 而且必须说明**为什么**不能省。
				if !strings.Contains(s.Note, "公网") {
					t.Errorf("第 %d 组：verify 那步应当解释为什么要从公网验",
						i+1)
				}
			}
		}
		if !found {
			t.Errorf("第 %d 组：下一步里必须包含 isc verify", i+1)
		}
	}
}

// TestNextStepsFillsInDetectedValues 钉住"清单里填的是真实值"。
//
// 一份带占位符的清单需要用户自己想清楚每个位置填什么，
// 而那样他多半会去翻文档 —— 那就失去了引导的意义。
func TestNextStepsFillsInDetectedValues(t *testing.T) {
	t.Parallel()

	env := environment{IPv6: []string{"2409:8a50::1"}}
	steps := nextSteps(env, 8096, "home.example.com", false)

	var joined strings.Builder
	for _, s := range steps {
		joined.WriteString(s.Command + "\n")
	}
	all := joined.String()

	if !strings.Contains(all, "home.example.com") {
		t.Error("域名没有被填进命令里")
	}
	if !strings.Contains(all, "8096") {
		t.Error("端口没有被填进命令里")
	}
	if !strings.Contains(all, "--type AAAA") {
		t.Error("有 IPv6 时应当建议 AAAA 记录")
	}
}

// TestNextStepsSkipsRunningDaemon 验证内核已在跑时不再提示启动它。
func TestNextStepsSkipsRunningDaemon(t *testing.T) {
	t.Parallel()

	steps := nextSteps(environment{}, 0, "", true)
	for _, s := range steps {
		if strings.Contains(s.Command, "daemon run") {
			t.Error("内核已在运行时不该提示再启动一次")
		}
	}
}
