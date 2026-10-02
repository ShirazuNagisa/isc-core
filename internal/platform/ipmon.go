package platform

import (
	"context"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
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
		Note:      fmt.Sprintf(i18n.T("platform.ipmon_note"), m.interval),
	}
}

// Snapshot 返回当前全部接口的地址与前缀。
func (m *pollingIPMonitor) Snapshot(context.Context) ([]InterfaceAddrs, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf(i18n.T("platform.ipmon_enum"), err)
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
			// 只保留**委派前缀**，不保留主机路由。
			//
			// 这一步是必须的，而且是在真机上才发现的：Windows 把 IPv6
			// 主机地址报成 /128，于是"用地址自身的前缀长度"会得到一个
			// /128 "前缀"。而 Windows 的隐私扩展地址默认**每小时轮换**，
			// 那意味着内核会每小时检测到一次"前缀变化"并触发全量更新 ——
			// 白白消耗服务商配额，还会让用户收到莫名其妙的通知。
			//
			// ISP 委派的网段在实践中是 /56 ~ /64，因此以 /64 为界：
			// 比 /64 更具体的都不是委派前缀。
			if isDelegatedPrefix(prefix) && IsGlobalIPv6(addr) {
				if !prefixSeen[prefix] {
					prefixSeen[prefix] = true
					out.Prefixes = append(out.Prefixes, prefix)
				}
			}
		}
	}
	return out, nil
}

// maxDelegatedPrefixBits 是"委派前缀"的最大长度。
//
// 依据：ISP 向家宽委派的网段在实践中是 /56 ~ /64（中国电信/联通/移动
// 的家宽工单通常写 /60 或 /64，路由器再切成 /64 下发）。
// 比 /64 更具体的条目要么是主机路由（/128），要么是路由器内部的细分，
// 都不是"整个网段变了"这个信号。
const maxDelegatedPrefixBits = 64

// isDelegatedPrefix 报告一个 IPv6 前缀是否可能是 ISP 委派的网段。
func isDelegatedPrefix(p netip.Prefix) bool {
	if !p.Addr().Is6() || p.Addr().Is4In6() {
		return false
	}
	return p.Bits() >= 0 && p.Bits() <= maxDelegatedPrefixBits
}

// HasUsableAddress 报告接口是否有任何可用于解析的地址。
//
// "可用"的定义刻意宽松：全局 IPv6，或者非链路本地的 IPv4。
// 用它过滤掉那些只剩 169.254.x / fe80:: 的接口 —— 那类接口
// （断开的以太网、蓝牙网络连接、Wi-Fi Direct 虚拟适配器）
// 在各语言版本的 Windows 上名称完全不同，靠名称前缀判断必然漏。
func (i InterfaceAddrs) HasUsableAddress() bool {
	if len(i.GlobalIPv6()) > 0 {
		return true
	}
	for _, a := range i.IPv4 {
		if !a.IsLinkLocalUnicast() && !a.IsLoopback() {
			return true
		}
	}
	return false
}

// indexSnapshots 把快照列表按接口名索引。
func indexSnapshots(list []InterfaceAddrs) map[string]ifaceSnapshot {
	out := make(map[string]ifaceSnapshot, len(list))
	for _, i := range list {
		// 回环、虚拟、以及没有任何可用地址的接口不参与变化检测。
		//
		// 最后一类很关键：断开状态的 "$ 本地连接* 1" 这类虚拟适配器
		// 在各语言版本的 Windows 上名称完全不同，靠名称判断必然漏网，
		// 而它们的地址（169.254.x / fe80::）变化与"公网地址变了"
		// 毫无关系。
		if i.IsLoopback || i.IsVirtual || !i.HasUsableAddress() {
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

// virtualInterfacePrefixes 是虚拟/隧道网卡的名称片段。
//
// ⚠️ 这个判据是**尽力而为**，不是可靠依据：Windows 会本地化网卡名称
// （中文系统上是"蓝牙网络连接""本地连接* 1"），而各家虚拟化软件
// 的命名风格也各不相同（"VMware Network Adapter VMnet1" 以 VMware 开头，
// 不是 vmnet）。
//
// 因此它只是第一道筛子，真正的兜底是 hasUsableAddress：
// 虚拟网卡通常只有私有地址或链路本地地址，会被那道筛子挡掉。
// 两道筛子叠加后，界面上剩下的基本就是真正能用的上联网卡。
var virtualInterfacePrefixes = []string{
	// Linux
	"docker", "veth", "br-", "virbr", "vmnet", "tun", "tap", "wg", "zt",
	// Windows（英文）
	"loopback", "bluetooth", "vethernet", "hyper-v", "wi-fi direct",
	// Windows（中文）—— 名称随系统语言变化，只能逐个列出。
	//
	// ⚠️ 这四条**不能**走消息目录。
	//
	// 它们不是给用户看的文案，而是**匹配系统网卡名的模式**：中文 Windows 上
	// 网卡就叫「蓝牙网络连接」「本地连接* 12」。把它们翻译成英文，匹配就会
	// 在中文系统上全部失效 —— 而症状是"虚拟网卡没有被过滤掉"，
	// 界面上多出一堆用不了的地址。
	//
	// 迁移时把它们也换成了 i18n.T，被 TestIsVirtualInterface 拦住。
	"蓝牙", "本地连接*", "虚拟", "回环",
	// 虚拟化软件
	"vmware", "virtualbox", "host-only", "vbox",
	// 隧道伪接口
	"teredo", "isatap", "6to4",
	// macOS
	"utun", "awdl", "llw", "bridge", "ap1", "gif", "stf",
}

// isVirtualInterface 判断网卡是否是虚拟/隧道类型。
func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(lower, p) || strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
