package platform

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

// 本文件覆盖 IP 变化检测。它是"前缀变了 → AAAA 要重写"这条核心链路的
// 第一环，判错的后果是要么漏更新（域名指向旧地址）、要么风暴
// （每次轮询都触发一次全量更新）。

func mkSnap(v4, v6 []string, prefixes []string) ifaceSnapshot {
	s := ifaceSnapshot{
		ipv4:     map[netip.Addr]bool{},
		ipv6:     map[netip.Addr]bool{},
		prefixes: map[netip.Prefix]bool{},
	}
	for _, a := range v4 {
		s.ipv4[netip.MustParseAddr(a)] = true
	}
	for _, a := range v6 {
		s.ipv6[netip.MustParseAddr(a)] = true
	}
	for _, p := range prefixes {
		s.prefixes[netip.MustParsePrefix(p)] = true
	}
	return s
}

// TestPrefixChangeIsDetected 是本项目最核心的一条检测。
//
// ISP 重拨后整个 /64 前缀会变 —— 地址变了、前缀也变了。
// 上层必须能区分"前缀级变化"（要重写该前缀下的全部记录）
// 与"单个地址变化"。
func TestPrefixChangeIsDetected(t *testing.T) {
	old := mkSnap(nil,
		[]string{"240e:3b0:1111:2200::1"},
		[]string{"240e:3b0:1111:2200::/64"})
	cur := mkSnap(nil,
		[]string{"240e:3b0:9999:8800::1"},
		[]string{"240e:3b0:9999:8800::/64"})

	events := emitForIface("eth0", cur, old, time.Now())

	var prefixEvents int
	for _, ev := range events {
		if ev.IsPrefixEvent() {
			prefixEvents++
		}
	}
	if prefixEvents == 0 {
		t.Fatalf("前缀变化未被识别，事件列表: %v", events)
	}

	// 必须包含新前缀的新增事件，否则上层不知道该往哪些记录里写新地址。
	var sawNewPrefix bool
	for _, ev := range events {
		if ev.IsPrefixEvent() && ev.Prefix.String() == "240e:3b0:9999:8800::/64" {
			sawNewPrefix = true
		}
	}
	if !sawNewPrefix {
		t.Errorf("未报告新前缀，事件列表: %v", events)
	}
}

// TestNoChangeProducesNoEvents 验证非变化不产生事件。
//
// 这是防"事件风暴"的关键：轮询每 5 秒一次，如果没变化也发事件，
// 上层会被触发成每 5 秒一次全量解析更新 —— 服务商那边很快会限流。
func TestNoChangeProducesNoEvents(t *testing.T) {
	snap := mkSnap(
		[]string{"203.0.113.7"},
		[]string{"240e:3b0:1111:2200::1"},
		[]string{"240e:3b0:1111:2200::/64"})

	if events := emitForIface("eth0", snap, snap, time.Now()); len(events) != 0 {
		t.Fatalf("状态未变时不应产生任何事件，得到 %d 个: %v", len(events), events)
	}
}

// TestAddressOnlyChangeEmitsAddressEvent 验证只换地址（前缀不变）的情形。
//
// 这在 SLAAC 隐私扩展开启时很常见：接口标识符会定期变化，
// 而前缀不变。此时只需要更新该地址对应的记录，不必全量重写。
func TestAddressOnlyChangeEmitsAddressEvent(t *testing.T) {
	old := mkSnap(nil,
		[]string{"240e:3b0:1111:2200::1"},
		[]string{"240e:3b0:1111:2200::/64"})
	cur := mkSnap(nil,
		[]string{"240e:3b0:1111:2200::abcd"},
		[]string{"240e:3b0:1111:2200::/64"})

	events := emitForIface("eth0", cur, old, time.Now())

	// 前缀没变，因此不应有前缀级事件。
	for _, ev := range events {
		if ev.IsPrefixEvent() {
			t.Errorf("前缀未变却报告了前缀事件: %v", ev)
		}
	}

	var added, removed int
	for _, ev := range events {
		switch ev.Kind {
		case AddrAdded:
			added++
		case AddrRemoved:
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("应当报告 1 个新增与 1 个消失，得到 added=%d removed=%d", added, removed)
	}
}

func TestNewInterfaceEmitsAllAddresses(t *testing.T) {
	empty := ifaceSnapshot{
		ipv4:     map[netip.Addr]bool{},
		ipv6:     map[netip.Addr]bool{},
		prefixes: map[netip.Prefix]bool{},
	}
	cur := mkSnap(
		[]string{"203.0.113.7"},
		[]string{"240e:3b0:1111:2200::1"},
		[]string{"240e:3b0:1111:2200::/64"})

	events := emitForIface("eth0", cur, empty, time.Now())

	var added int
	for _, ev := range events {
		if ev.Kind == AddrAdded {
			added++
		}
	}
	// 1 个 IPv4 + 1 个 IPv6
	if added != 2 {
		t.Errorf("新接口应当报告 2 个新增地址，得到 %d: %v", added, events)
	}
}

// TestIndexSnapshotsSkipsLoopbackAndVirtual 验证虚拟网卡被排除。
//
// 放进来会制造噪音：Docker 网桥增删、VPN 连接断开都会触发事件，
// 而它们与"公网地址变了"毫无关系，用户会收到莫名其妙的解析通知。
func TestIndexSnapshotsSkipsLoopbackAndVirtual(t *testing.T) {
	list := []InterfaceAddrs{
		{Name: "lo", IsLoopback: true, IPv6: []netip.Addr{netip.MustParseAddr("::1")}},
		{Name: "docker0", IsVirtual: true, IPv4: []netip.Addr{netip.MustParseAddr("172.17.0.1")}},
		{Name: "utun3", IsVirtual: true, IPv6: []netip.Addr{netip.MustParseAddr("fd00::1")}},
		{Name: "eth0", IPv6: []netip.Addr{netip.MustParseAddr("240e:3b0::1")}},
		{Name: "en0", IPv4: []netip.Addr{netip.MustParseAddr("203.0.113.7")}},
	}

	idx := indexSnapshots(list)
	if len(idx) != 2 {
		t.Fatalf("应当只保留 2 个物理网卡，得到 %d 个: %v", len(idx), keysOf(idx))
	}
	if _, ok := idx["eth0"]; !ok {
		t.Error("物理网卡 eth0 不应被排除")
	}
	if _, ok := idx["en0"]; !ok {
		t.Error("物理网卡 en0 不应被排除")
	}
}

func keysOf(m map[string]ifaceSnapshot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestIsVirtualInterface(t *testing.T) {
	virtual := []string{
		"docker0", "veth1a2b", "br-abc123", "virbr0", "vmnet8",
		"tun0", "tap0", "wg0", "utun3", "awdl0", "Loopback Pseudo-Interface 1",
		"vEthernet (Default Switch)", "Bluetooth Network Connection",
	}
	for _, name := range virtual {
		if !isVirtualInterface(name) {
			t.Errorf("%q 应当被判定为虚拟网卡", name)
		}
	}

	physical := []string{"eth0", "en0", "enp3s0", "wlan0", "以太网", "WLAN", "Ethernet"}
	for _, name := range physical {
		if isVirtualInterface(name) {
			t.Errorf("%q 不应被判定为虚拟网卡", name)
		}
	}
}

// TestSnapshotIsReadable 是唯一一条触碰真实系统的断言。
//
// 它不断言具体网卡（CI 环境千差万别），只验证调用链能跑通、
// 且返回的数据自洽 —— 例如"标为全局的 IPv6 地址必须真的能用于公网访问"。
func TestSnapshotIsReadable(t *testing.T) {
	m := newPollingIPMonitor()

	list, err := m.Snapshot(t.Context())
	if err != nil {
		t.Fatalf("读取网卡快照失败: %v", err)
	}
	// 任何一台机器都至少有一个回环接口。
	if len(list) == 0 {
		t.Fatal("快照为空，至少应当有回环接口")
	}

	for _, i := range list {
		for _, a := range i.Prefixes {
			if !a.Addr().Is6() {
				t.Errorf("接口 %s 的前缀 %s 不是 IPv6 —— 只有 IPv6 需要跟踪委派前缀",
					i.Name, a)
			}
		}
		for _, a := range i.GlobalIPv6() {
			if !IsGlobalIPv6(a) {
				t.Errorf("GlobalIPv6 返回了非全局地址 %s", a)
			}
		}
	}
}

// TestWatchEstablishesBaseline 验证 Watch 不会把"当前已有地址"当成新增。
//
// 没有基线的话，内核每次启动都会先触发一次全量更新 ——
// 一次毫无意义的服务商 API 调用。
func TestWatchEstablishesBaseline(t *testing.T) {
	m := newPollingIPMonitor()
	// 把周期调到很短，让测试能观察到至少一轮轮询。
	m.interval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ch, err := m.Watch(ctx)
	if err != nil {
		t.Fatalf("建立监控失败: %v", err)
	}

	// 机器状态在测试期间不会变，因此不该收到任何事件。
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("事件通道意外关闭")
			}
			t.Fatalf("状态未变却收到事件: %v", ev)
		case <-deadline:
			return // 通过
		}
	}
}

func TestDescribeReportsBackend(t *testing.T) {
	state := newPollingIPMonitor().Describe()
	if !state.Available {
		t.Error("轮询实现必须报告为可用")
	}
	if state.Backend == "" || state.Note == "" {
		t.Errorf("必须如实报告后端名称与说明，得到 %+v", state)
	}
}
