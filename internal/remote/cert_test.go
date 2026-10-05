package remote

import (
	"net"
	"strings"
	"testing"
)

// **局域网候选里绝不能出现隧道地址，`.local` 也不能用 Unix 主机名。**
//
// # 这两条真机上都咬过人
//
// 用户日志里有 `请求超时 https://mac.local:8788/v1/remote/self`，而
// Bonjour 注册的是 `Shirazus-Mac-mini.local` —— `Mac.local` 在同一局域网里
// **永远解析不了**。原因是 `os.Hostname()`（Unix 主机名）被当成了
// Bonjour 名（LocalHostName），而两者可以完全无关。
//
// 同一份记录里还有 `fdc0:7a75:a688::2`（utun9）与 `fd81:ab12:10dd::2`
// （utun8）—— VPN 隧道地址。手机在同一个 Wi-Fi 下永远够不到它们，
// 而它们在每一次连接的竞速里都是纯粹的陪跑。
//
// 代价不是"多几条没用"，而是**每次连接都要为它们白等一轮超时**。
func TestLocalAddressesExcludeTunnelsAndUseTheBonjourName(t *testing.T) {
	t.Parallel()

	candidates := Candidates(8788)
	if len(candidates) == 0 {
		t.Fatal("一个候选都没有")
	}

	for _, c := range candidates {
		host, _, err := net.SplitHostPort(c)
		if err != nil {
			t.Fatalf("候选 %q 不是 host:port: %v", c, err)
		}
		if strings.HasSuffix(host, ".local") {
			// 必须与 Bonjour 注册的名字一致，否则这一条永远解析不了。
			// 断言方式：拿它去解析，解析不出来就说明名字是错的。
			if _, err := net.LookupHost(host); err != nil {
				t.Errorf("候选 %q 解析不了 —— 它多半是用 Unix 主机名拼的，"+
					"而 Bonjour 注册的是 LocalHostName（两者可以无关）", host)
			}
			continue
		}
		ip := net.ParseIP(host)
		if ip == nil {
			continue
		}
		// 隧道地址的前缀不可能出现在局域网候选里。用一个宽松的判据：
		// 这条地址必须归属某个**可广告**的接口。
		if !addressBelongsToAdvertisableInterface(t, ip) {
			t.Errorf("候选 %q 属于隧道或点对点接口 —— "+
				"同一个局域网里的手机永远到不了它，而每次连接都要为它等一轮超时", c)
		}
	}
}

// addressBelongsToAdvertisableInterface 报告某个 IP 是否挂在可广告的接口上。
func addressBelongsToAdvertisableInterface(t *testing.T, ip net.IP) bool {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("读不到接口列表: %v", err)
	}
	for _, iface := range ifaces {
		if !advertisableInterface(iface) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// 隧道接口名前缀必须被挡住。
func TestAdvertisableInterfaceRejectsTunnels(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"utun8", "utun9", "gif0", "stf0", "awdl0", "llw0", "ipsec0", "ppp0"} {
		iface := net.Interface{Name: name, Flags: net.FlagUp}
		if advertisableInterface(iface) {
			t.Errorf("%s 被当成了局域网接口 —— 手机在同一个 Wi-Fi 下到不了它", name)
		}
	}
	for _, name := range []string{"en0", "en1", "bridge0"} {
		iface := net.Interface{Name: name, Flags: net.FlagUp | net.FlagBroadcast}
		if !advertisableInterface(iface) {
			t.Errorf("%s 是正常的局域网接口，不该被过滤掉", name)
		}
	}
	// 没起来的接口不该被报出去。
	if advertisableInterface(net.Interface{Name: "en0"}) {
		t.Error("未启用（FlagUp 未置）的接口不该被广告")
	}
	// 点对点接口没有"局域网"可言。
	if advertisableInterface(net.Interface{Name: "en0", Flags: net.FlagUp | net.FlagPointToPoint}) {
		t.Error("点对点接口不该被广告")
	}
}
