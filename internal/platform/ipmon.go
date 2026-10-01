package platform

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// 本文件实现一个**跨平台通用**的 IP / IPv6 前缀监控器。
//
// # 为什么不做三套平台实现
//
// docs/ARCHITECTURE.md 原计划是 Windows 用 GetAdaptersAddresses、Linux 用
// netlink、macOS 用 getifaddrs。实际动手后调整为：**先用一套可移植的轮询实现**。
//
// 理由是标准库已经把平台差异吃掉了：
//
//	net.Interfaces()  Windows → GetAdaptersAddresses
//	                  Linux   → netlink RTM_GETADDR（一次性）
//	                  macOS   → getifaddrs
//
// 而 net.Interface.Addrs() 返回的 *net.IPNet 自带前缀长度，正是我们需要的
// 委派前缀。也就是说，"读取当前地址与前缀"这件事本来就已经是跨平台的，
// 写三套只会得到三份行为略有差异的代码。
//
// 真正平台相关的是**事件通知的实时性**：Linux 的 netlink 能即时推送地址变化，
// 而 Windows / macOS 只能轮询。但这个差异对本产品的价值很小 ——
// 动态解析的间隔是分钟级，轮询周期设成 5 秒带来的额外延迟完全可以忽略，
// 而代价是要为三个平台维护三套监听代码与各自的一致性测试。
//
// 因此本实现选择轮询，并在 Describe 里如实报告后端名称。将来若确实需要
// 更低延迟，Linux 的 netlink 后端可以作为一个更快的实现插进来，
// 接口不变（见 docs/PLAN.md R2 的处置方式：先保证三平台行为一致，
// 再按需优化单个平台）。

// defaultPollInterval 是轮询周期。
//
// 5 秒的依据：家用宽带重拨后，DDNS 记录的更新本来就要经过 DNS 传播，
// 多等几秒没有任何实际影响；而更短的周期会让一台 24 小时运行的机器
// 白白多跑很多次系统调用。
const defaultPollInterval = 5 * time.Second

// pollingIPMonitor 是可移植的 IP 监控实现。
type pollingIPMonitor struct {
	interval time.Duration

	mu       sync.Mutex
	previous map[string]ifaceSnapshot
}

// ifaceSnapshot 是一个接口在上一次轮询时的状态。
type ifaceSnapshot struct {
	ipv4     map[netip.Addr]bool
	ipv6     map[netip.Addr]bool
	prefixes map[netip.Prefix]bool
}

// newPollingIPMonitor 构造可移植的 IP 监控实现。
//
// 返回具体类型而不是 IPMonitor 接口：测试需要把轮询周期调到毫秒级，
// 否则一条"状态未变时不发事件"的断言要跑满 5 秒。
func newPollingIPMonitor() *pollingIPMonitor {
	return &pollingIPMonitor{
		interval: defaultPollInterval,
		previous: make(map[string]ifaceSnapshot),
	}
}

// Describe 实现 describer。
func (m *pollingIPMonitor) Describe() ImplState {
	return ImplState{
		Available: true,
		Backend:   "polling",
		Note: fmt.Sprintf(
			"通过标准库轮询（每 %s 一次）读取网卡地址与前缀；"+
				"该方式在三平台行为一致，代价是地址变化的感知有最多一个轮询周期的延迟",
			m.interval),
	}
}

// Snapshot 返回当前全部接口的地址与前缀。
func (m *pollingIPMonitor) Snapshot(context.Context) ([]InterfaceAddrs, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("platform: 枚举网卡失败: %w", err)
	}

	out := make([]InterfaceAddrs, 0, len(ifaces))
	for _, iface := range ifaces {
		snap, err := snapshotInterface(iface)
		if err != nil {
			// 单个网卡读不到地址不该让整个快照失败 ——
			// 例如某个 VPN 网卡刚好在被拆除。
			continue
		}
		out = append(out, snap)
	}
	return out, nil
}

// Watch 持续推送地址与前缀变化事件。
func (m *pollingIPMonitor) Watch(ctx context.Context) (<-chan AddrEvent, error) {
	ch := make(chan AddrEvent, 32)

	// 先建立基线快照，否则第一次轮询会把"当前已有的全部地址"
	// 当成"刚刚新增"，触发一次毫无意义的全量更新。
	initial, err := m.Snapshot(ctx)
	if err != nil {
		close(ch)
		return nil, err
	}
	m.mu.Lock()
	m.previous = indexSnapshots(initial)
	m.mu.Unlock()

	go m.poll(ctx, ch)
	return ch, nil
}

func (m *pollingIPMonitor) poll(ctx context.Context, ch chan<- AddrEvent) {
	defer close(ch)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			events := m.diffOnce(ctx)
			for _, ev := range events {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// diffOnce 取一次快照，与上次比对，返回变化事件。
func (m *pollingIPMonitor) diffOnce(ctx context.Context) []AddrEvent {
	current, err := m.Snapshot(ctx)
	if err != nil {
		return nil
	}
	next := indexSnapshots(current)

	m.mu.Lock()
	prev := m.previous
	m.previous = next
	m.mu.Unlock()

	now := time.Now().UTC()
	var events []AddrEvent

	// 新增与变化的接口。
	for name, cur := range next {
		old, existed := prev[name]
		if !existed {
			// 新出现的接口（例如刚插入的网卡）：把它的地址全部当作新增。
			events = append(events, emitForIface(name, cur, ifaceSnapshot{
				ipv4: map[netip.Addr]bool{}, ipv6: map[netip.Addr]bool{},
				prefixes: map[netip.Prefix]bool{},
			}, now)...)
			continue
		}
		events = append(events, emitForIface(name, cur, old, now)...)
	}

	// 消失的接口：不发事件。
	//
	// 刻意如此：网卡被拔掉时"地址消失"本身不构成需要更新 DNS 的理由 ——
	// 真正需要更新的是"这台机器现在没有那个地址了"，而那时我们也没有
	// 新的地址可以写进去。发一堆 removed 事件只会造成日志噪音。
	return events
}

// emitForIface 比较一个接口的前后状态，产生事件。
func emitForIface(name string, cur, old ifaceSnapshot, at time.Time) []AddrEvent {
	var events []AddrEvent

	// 前缀变化优先且**只报一次**。
	//
	// 这是本项目的核心事件：ISP 重拨后整个 /64 前缀会变，
	// 该前缀下的所有 AAAA 记录都要重写。逐个地址报事件会让上层
	// 触发 N 次全量更新，而它们本该合并成一次。
	addedPrefixes, removedPrefixes := diffPrefixes(old.prefixes, cur.prefixes)
	for p := range addedPrefixes {
		events = append(events, AddrEvent{
			Kind: AddrChanged, Iface: name, Prefix: p, At: at,
		})
	}
	for p := range removedPrefixes {
		events = append(events, AddrEvent{
			Kind: AddrRemoved, Iface: name, Prefix: p, At: at,
		})
	}

	// 单个地址的变化。
	//
	// 地址级事件与上面的前缀级事件会同时发出：前缀事件用于触发
	// "该前缀下所有 AAAA 重写"，地址事件用于日志与界面展示。
	// 上层需要按接口去重，避免一次重拨触发多次全量更新。
	for a := range diffAddrs(cur.ipv6, old.ipv6) {
		events = append(events, AddrEvent{Kind: AddrAdded, Iface: name, Addr: a, At: at})
	}
	for a := range diffAddrs(old.ipv6, cur.ipv6) {
		events = append(events, AddrEvent{Kind: AddrRemoved, Iface: name, Addr: a, At: at})
	}
	for a := range diffAddrs(cur.ipv4, old.ipv4) {
		events = append(events, AddrEvent{Kind: AddrAdded, Iface: name, Addr: a, At: at})
	}
	for a := range diffAddrs(old.ipv4, cur.ipv4) {
		events = append(events, AddrEvent{Kind: AddrRemoved, Iface: name, Addr: a, At: at})
	}

	return events
}

// diffAddrs 返回 a 中有而 b 中没有的地址。
func diffAddrs(a, b map[netip.Addr]bool) map[netip.Addr]bool {
	out := make(map[netip.Addr]bool)
	for addr := range a {
		if !b[addr] {
			out[addr] = true
		}
	}
	return out
}

// diffPrefixes 返回新增与消失的前缀。
func diffPrefixes(old, cur map[netip.Prefix]bool) (added, removed map[netip.Prefix]bool) {
	added = make(map[netip.Prefix]bool)
	removed = make(map[netip.Prefix]bool)
	for p := range cur {
		if !old[p] {
			added[p] = true
		}
	}
	for p := range old {
		if !cur[p] {
			removed[p] = true
		}
	}
	return added, removed
}

// ---------------------------------------------------------------------------
// 快照
// ---------------------------------------------------------------------------

// snapshotInterface 读取单个网卡的地址与前缀。
func snapshotInterface(iface net.Interface) (InterfaceAddrs, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return InterfaceAddrs{}, err
	}

	out := InterfaceAddrs{
		Name:       iface.Name,
		Index:      iface.Index,
		MTU:        iface.MTU,
		IsUp:       iface.Flags&net.FlagUp != 0,
		IsLoopback: iface.Flags&net.FlagLoopback != 0,
		IsVirtual:  isVirtualInterface(iface.Name),
	}
	if iface.HardwareAddr != nil {
		out.HardwareAddr = iface.HardwareAddr.String()
	}

	prefixSeen := make(map[netip.Prefix]bool)
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipNet.IP)
		if !ok {
			continue
		}
		// 去掉 IPv4-mapped 前缀，让 4 与 6 的判定干净。
		addr = addr.Unmap()

		ones, bits := ipNet.Mask.Size()
		if bits == 0 {
			continue
		}
		prefix := netip.PrefixFrom(addr, ones).Masked()

		switch {
		case addr.Is4():
			out.IPv4 = append(out.IPv4, addr)
		case addr.Is6():
			out.IPv6 = append(out.IPv6, addr)
			// 只保留可用于公网访问的地址所对应的前缀。
			//
			// 链路本地地址（fe80::/10）也有 /64 前缀，但把它当成
			// "委派前缀"毫无意义 —— 它永远不可能出现在 AAAA 记录里，
			// 却会在每次网卡抖动时产生大量无意义的前缀事件。
			if IsGlobalIPv6(addr) {
				if !prefixSeen[prefix] {
					prefixSeen[prefix] = true
					out.Prefixes = append(out.Prefixes, prefix)
				}
			}
		}
	}
	return out, nil
}

// indexSnapshots 把快照列表按接口名索引。
func indexSnapshots(list []InterfaceAddrs) map[string]ifaceSnapshot {
	out := make(map[string]ifaceSnapshot, len(list))
	for _, i := range list {
		// 回环与虚拟接口不参与变化检测。
		//
		// 它们的变化（Docker 网桥增删、VPN 连接断开）与"公网地址变了"
		// 毫无关系，放进来只会制造噪音事件，让用户收到莫名其妙的解析通知。
		if i.IsLoopback || i.IsVirtual {
			continue
		}
		snap := ifaceSnapshot{
			ipv4:     make(map[netip.Addr]bool, len(i.IPv4)),
			ipv6:     make(map[netip.Addr]bool, len(i.IPv6)),
			prefixes: make(map[netip.Prefix]bool, len(i.Prefixes)),
		}
		for _, a := range i.IPv4 {
			snap.ipv4[a] = true
		}
		for _, a := range i.IPv6 {
			snap.ipv6[a] = true
		}
		for _, p := range i.Prefixes {
			snap.prefixes[p] = true
		}
		out[i.Name] = snap
	}
	return out
}

// virtualInterfacePrefixes 是虚拟/隧道网卡的名称前缀。
//
// 判断依据是名称而不是驱动信息：跨平台拿不到统一的"这是虚拟网卡"标志，
// 而名称前缀在实践中足够可靠，且误判的后果只是"该网卡的变化不触发解析更新"，
// 用户可以手动指定网卡绕过。
var virtualInterfacePrefixes = []string{
	// Linux
	"docker", "veth", "br-", "virbr", "vmnet", "tun", "tap", "wg", "zt",
	// Windows
	"loopback", "bluetooth", "vethernet", "hyper-v",
	// macOS
	"utun", "awdl", "llw", "bridge", "ap1", "gif", "stf",
}

// isVirtualInterface 判断网卡是否是虚拟/隧道类型。
func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}
