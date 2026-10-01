package platform

import (
	"net/netip"
	"testing"
)

// TestIsGlobalIPv6ExcludesSpecialPurpose 钉住"哪些 IPv6 地址真的能用"。
//
// # 这条测试来自一次真机发现
//
// Windows 默认启用 Teredo，于是"Teredo Tunneling Pseudo-Interface"
// 上报出 4 个 2001:0:... 地址。在此之前，`isc doctor` 的结论是
// "找到 4 个可用于公网访问的 IPv6 地址" —— 而它们**一个都用不了**。
//
// 根因是 netip 的 `IsGlobalUnicast()` 对这些网段返回 true：它们确实
// 落在 2000::/3 里，但 Teredo 与 6to4 都是**隧道**地址，只能用于
// 出站穿透，不接受入站连接。把它们当作可用地址会把用户引向一条
// 走不通的路，而他会在路由器上白白折腾很久。
func TestIsGlobalIPv6ExcludesSpecialPurpose(t *testing.T) {
	t.Parallel()

	usable := []string{
		"2409:8a50:6a1:7450::50b", // 中国移动家宽
		"240e:3b0:1111:2200::1",   // 中国电信
		"2606:4700::1111",         // Cloudflare
		"2a00:1450:4001::1",       // Google
	}
	for _, raw := range usable {
		if !IsGlobalIPv6(netip.MustParseAddr(raw)) {
			t.Errorf("%s 应当被判定为可用于公网访问", raw)
		}
	}

	unusable := []struct {
		addr string
		why  string
	}{
		{"2001:0:53aa:64c:2c8c:8f4a:3f57:ff9b", "Teredo 隧道地址（只能出站穿透）"},
		{"2002:c000:204::1", "6to4 隧道地址（需要公网 IPv4，且已弃用）"},
		{"2001:db8::1", "文档专用网段"},
		{"fe80::1", "链路本地"},
		{"fd00::1", "唯一本地地址"},
		{"::1", "回环"},
		{"ff02::1", "组播"},
	}
	for _, tc := range unusable {
		if IsGlobalIPv6(netip.MustParseAddr(tc.addr)) {
			t.Errorf("%s 被判定为可用于公网访问，但它是%s", tc.addr, tc.why)
		}
	}
}

func TestIsSpecialPurposeIPv6(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{
		"2001:0::1":      true, // Teredo
		"2001:0:ffff::1": true,
		"2002::1":        true, // 6to4
		"2001:db8::1":    true, // 文档
		"2001:20::1":     true, // ORCHIDv2
		"2409:8a50::1":   false,
		"2001:4860::1":   false, // 这是正常的 Google DNS 地址，不是 2001:0::/32
	}

	for addr, want := range cases {
		got := isSpecialPurposeIPv6(netip.MustParseAddr(addr))
		if got != want {
			t.Errorf("isSpecialPurposeIPv6(%s) = %v, 期望 %v", addr, got, want)
		}
	}
}

// TestIsGlobalIPv6AcceptsSimilarLookingAddresses 验证前缀匹配的边界。
//
// 2001:0::/32 与 2001:4860::/32 只差几位，而后者是完全正常的地址。
// 前缀匹配写错一位会把大量正常地址误判为不可用 —— 那比漏判更糟，
// 因为用户会看到"没有 IPv6"，然后去改一个本来没问题的设置。
func TestIsGlobalIPv6AcceptsSimilarLookingAddresses(t *testing.T) {
	t.Parallel()

	// 这些都是 2001: 开头，但不在 2001:0::/32 里。
	normal := []string{
		"2001:4860:4860::8888", // Google Public DNS
		"2001:200::1",          // WIDE
		"2001:1::1",            // 紧邻 Teredo 段，但不在其中
	}
	for _, raw := range normal {
		if !IsGlobalIPv6(netip.MustParseAddr(raw)) {
			t.Errorf("%s 是正常地址，不该被排除", raw)
		}
	}
}
