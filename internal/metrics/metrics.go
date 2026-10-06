// Package metrics 采样主机与托管站点的资源占用。
//
// # 口径
//
//   - **主机**：CPU 占用率、内存已用/总量、网络收发速率。
//   - **站点**：CPU 占用率与常驻内存（RSS），以及进程号与运行时长。
//
// # 两件刻意不做的事
//
//  1. **不按站点归因网络流量**。同一台机器上的多个站点共享网络栈，
//     没有可靠的办法把字节数摊到某个站点上；给一个看起来精确但其实是
//     编出来的数字，比不给更糟。因此网络只有主机级的。
//  2. **静态站点没有独立的资源数字**。它们由内核进程自己托管，没有独立
//     进程可测；此时只报运行时长，不假装知道它的 CPU 与内存。
//
// # 为什么解析文本而不是调系统 API
//
// macOS 上拿这些数字要么走 mach 调用（需要 cgo，而本项目禁止 cgo，D11），
// 要么解析 `ps` / `netstat` 的输出。Linux 上直接读 /proc。两条路径都收敛成
// **纯函数解析器**，可以用抓下来的真实输出做单元测试 —— 否则这些代码只能
// 靠"在这台机器上看着对"来验证。
package metrics

import (
	"context"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"
)

// HostSample 是一次主机采样。
type HostSample struct {
	At time.Time
	// CPUPercent 是 0..100 的整体占用率。
	CPUPercent float64
	// MemoryUsedBytes / MemoryTotalBytes 是物理内存。
	MemoryUsedBytes  uint64
	MemoryTotalBytes uint64
	// NetRxBytesPerSec / NetTxBytesPerSec 是所有非回环接口的合计速率。
	NetRxBytesPerSec float64
	NetTxBytesPerSec float64
	// Disks 是这台机器上**真实的**挂载点及其容量，按挂载点排序；
	// 伪文件系统（procfs/sysfs/tmpfs/devfs…）与总容量为 0 的挂载点
	// 已经滤掉（见 keepMount 与 finalizeDisks）。不支持的平台上是空的。
	//
	// # 为什么是挂载点列表，而不是只报"数据卷"那一个数字
	//
	// 只报一个数看着更简洁，但那个数必须先回答"是哪个卷"。一台机器上
	// 通常同时有系统卷、数据卷、外接硬盘、NAS 与 U 盘，而用户真正担心的
	// 往往是其中**某一个**满了：压成一个数字之后，要么在挂了外接盘的
	// 机器上显示"快满了"（其实是系统盘空着），要么正好漏掉那块真的满了
	// 的盘。挂载点是内核给出的、用户在 Finder / df 里也看得到的划分方式，
	// 照搬它比自己发明一个聚合口径更不容易骗人。
	//
	// 历史环形缓冲里也存着这份列表，但 60 个历史点各带一遍全部挂载点
	// 会让一次轮询的响应体膨胀十几倍，因此只有**当前快照**会把它发出去
	//（见 api/openapi.yaml 里 HostMetrics.disks 的说明）。
	Disks []DiskSample
	// GPU 是 GPU 占用；nil 表示**没有可采样的 GPU**：Linux / Windows 上还
	// 没有实现，macOS 上也可能匹配不到加速器（无头机器、虚拟机）。
	// 它与"占用为 0"是两件事 —— 后者由 GPUSample.Utilization == nil 表达。
	GPU *GPUSample
}

// DiskSample 是一个真实挂载点的容量。
//
// 三个字节数的口径刻意**不**满足 used + free == total：FreeBytes 取
// statfs 的 Bavail（普通用户真的写得进去的量），而被预留给 root 的那部分
// 两边都不算。把差额摊进任何一边都是在编一个用户对不上的数字。
//
// macOS 上还有一处口径差要留意：APFS 的同一个容器里，多个卷共享容量，
// 因此 getfsstat 报的是容器级的块计数 —— 具体说明见 source_darwin.go。
type DiskSample struct {
	MountPoint string
	FSType     string
	TotalBytes uint64
	UsedBytes  uint64
	FreeBytes  uint64
}

// GPUSample 是 GPU 的占用。Utilization 为 nil 表示后端支持采样但这一次没读到值。
//
// # 为什么它自己带一个 Backend
//
// GPU 的采样后端与主机指标**不是同一个**：主机那边是 top / vm_stat /
// netstat（Linux 是 /proc），而 GPU 只有 ioreg 这一条路，其它平台上则
// 根本没有（Linux 要走 nvidia-smi 或 sysfs，是另一件事）。
// 接口里 gpu.backend 因此是独立字段：界面要能对"主机有数字、GPU 不支持"
// 这个常见组合给出正确的说明，而拿主机的 backend 去解释 GPU 的空值会把
// 用户引到错误的方向（他会去查 ioreg，而问题其实是这台机器上没有 GPU）。
type GPUSample struct {
	// Name 是加速器的型号名（如 "Apple M4"）；取不到时为空。
	// 它是展示用的点缀，不是指标，因此读不到不算失败。
	Name string
	// Utilization 是 0..100 的占用率；nil 表示后端在跑、但这次没读到值。
	Utilization *float64
	// Backend 报告这个数字是哪条路来的，例如 "darwin-ioreg"。
	Backend string
}

// ProcessSample 是单个进程的采样。
type ProcessSample struct {
	PID         int
	CPUPercent  float64
	MemoryBytes uint64
}

// AppSample 是一个站点的采样。
type AppSample struct {
	AppID string
	// PID 为 0 表示该站点没有独立进程（静态站点由内核托管）。
	PID int
	// CPUPercent / MemoryBytes 仅在 PID != 0 时有意义。
	CPUPercent    float64
	MemoryBytes   uint64
	UptimeSeconds int64
}

// NetCounters 是一个进程的**累计**收发字节。
//
// 累计而不是增量：来源（macOS 的 nettop）给的就是进程生命周期内的总和，
// 速率必须由两次采样差分得到 —— 和 CPU、主机网络速率的做法一致。
type NetCounters struct {
	RxBytes uint64
	TxBytes uint64
}

// FootprintSample 是"内核自己 + 它托管的站点"的合计占用。
//
// # 为什么要单独有一个口径，而不是复用 HostSample
//
// HostSample 描述的是**整台机器**：用户在界面上看到"CPU 60%"，会以为
// 那是 Phecda 吃的，于是机器一卡就去怀疑它。这里回答的是另一个问题：
// 这套东西自己占了多少。两者的差值才是"别的程序占的"。
//
// # 口径的三条边界
//
//  1. **进程集合**是内核自身 + 被托管站点的进程树（含后代）。必须含后代：
//     预设里 `npm start` 这类命令的监管 PID 是 npm，真正干活的是它的
//     子进程 node —— 只看监管 PID 会把占用算成零头。
//  2. **内存是各进程 RSS 之和**，共享页会被重复计入。它是一个上界，
//     不是一个精确值；宁可偏大也不要漏掉一个正在吃内存的站点。
//  3. **CPUPercent 可能超过 100**：它是各进程占用率之和，而单个进程的
//     100% 在 macOS 上指"一个核跑满"。10 核机器上的 180% 读作 1.8 个核。
//     这里不做归一化，因为归一化之后就再也看不出是几个核了。
//
// # 为什么没有 GPU 字段
//
// macOS 没有任何按进程归因 GPU 的途径（powermetrics 需要 root）。把设备级
// 利用率塞进这里，就是把"整机 GPU 56%"冒充成"Phecda 用了 56% GPU" ——
// 那正是这个口径要消灭的误导。GPU 仍由 HostSample.GPU 提供，界面标注"整机"。
type FootprintSample struct {
	At         time.Time
	CPUPercent float64
	// MemoryBytes 是各进程 RSS 之和（见上面的口径说明）。
	MemoryBytes uint64
	// NetRxBytesPerSec / NetTxBytesPerSec 是按进程归因后求和的速率。
	// HasNetwork 为 false 时这两个值无意义（平台不支持、首次采样还没有
	// 上一轮可比、或本次读取失败）。
	NetRxBytesPerSec float64
	NetTxBytesPerSec float64
	HasNetwork       bool
	// NetBackend 报告网络数字的来源，取值与既有约定一致：
	// 后端名（如 "darwin-nettop"）、"unsupported"（平台没实现）、
	// "unavailable"（实现了但这次没读到）。
	//
	// 三种状态必须分开：把"没读到"显示成 0 会让用户以为 Phecda 不占网络。
	NetBackend string
	// Processes 是参与合计的进程数，供界面解释"这个数字算了几个人"。
	Processes int
}

// Snapshot 是某一时刻的完整视图。
type Snapshot struct {
	Host HostSample
	Apps []AppSample
	// Footprint 是内核自身 + 站点的合计占用（见 FootprintSample）。
	Footprint FootprintSample
}

// AppRef 是采样器需要知道的、关于一个正在运行的站点的最小信息。
type AppRef struct {
	AppID     string
	PID       int
	StartedAt time.Time
}

// ProcessNetwork 是能按进程给出网络字节数的 Source（可选实现）。
//
// 单独一个可选接口，而不是塞进 Source：Linux 上 /proc/net/dev 是**接口级**
// 的，没有等价的进程级来源，硬加进 Source 会逼着那边返回假数据。
type ProcessNetwork interface {
	// NetworkCounters 返回各进程的累计收发字节；没有网络活动的进程可以缺席。
	NetworkCounters(ctx context.Context) (map[int]NetCounters, error)
	// NetworkBackend 报告来源名（如 "darwin-nettop"）。
	NetworkBackend() string
}

// TreeSource 是能给出进程父子关系的 Source（可选实现）。
//
// 需要它是因为"一个站点的占用"不止它自己：内核按预设拉起的是 `npm start`，
// 而 npm 会再 fork 出 node。不展开后代的话，站点占用的 CPU 与内存会严重偏低，
// 而且偏得毫无规律 —— 取决于该预设是直接执行还是经过一层包装。
type TreeSource interface {
	// Descendants 返回 roots 及其全部后代的 PID。
	Descendants(ctx context.Context, roots []int) ([]int, error)
}

// Source 提供平台相关的原始采样。
//
// 它是接口而不是直接调用：采样逻辑（差分、环形缓冲、容错）与"数字从哪来"
// 分开之后，前者可以用一个假实现完整测试，不必依赖机器上装了什么。
type Source interface {
	// Host 返回一次主机采样。
	Host(ctx context.Context) (HostSample, error)
	// Processes 返回给定进程的采样；查不到的 PID 不出现在结果里。
	Processes(ctx context.Context, pids []int) (map[int]ProcessSample, error)
	// Describe 报告后端名称，供 /v1/meta 展示。
	Describe() string
}

// DefaultInterval 是默认采样间隔。
//
// 5 秒：采样在 macOS 上要起一两个短命进程（见包注释），每秒都做太浪费；
// 而界面上的资源曲线 5 秒一个点已经足够反映趋势。
const DefaultInterval = 5 * time.Second

// DefaultHistory 是主机采样的保留点数（5s × 60 ≈ 5 分钟）。
const DefaultHistory = 60

// Sampler 周期性采样并保留最近若干点。
type Sampler struct {
	source   Source
	interval time.Duration
	history  int
	now      func() time.Time

	mu       sync.RWMutex
	latest   Snapshot
	hostRing []HostSample

	// 上一次的按进程累计字节与取样时刻，用来差分出速率。
	//
	// 只有网络需要这个：CPU 与内存是即时量，速率不是。
	prevNet   map[int]NetCounters
	prevNetAt time.Time
}

// NewSampler 构造采样器。interval <= 0 时用 DefaultInterval。
func NewSampler(source Source, interval time.Duration, history int) *Sampler {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if history <= 0 {
		history = DefaultHistory
	}
	return &Sampler{source: source, interval: interval, history: history, now: time.Now}
}

// Run 周期性采样，直到 ctx 结束。
//
// apps 是一个回调而不是快照：站点会被拉起与停止，采样器每次都要拿最新的
// 列表，否则新站点的资源永远是空的、已删站点会一直出现在结果里。
func (s *Sampler) Run(ctx context.Context, apps func() []AppRef) {
	if s.source == nil {
		return
	}
	// 先立刻采一次：界面刚打开时不该等一个间隔才看到数字。
	s.sampleOnce(ctx, apps)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sampleOnce(ctx, apps)
		}
	}
}

func (s *Sampler) sampleOnce(ctx context.Context, apps func() []AppRef) {
	host, err := s.source.Host(ctx)
	if err == nil {
		s.mu.Lock()
		s.latest.Host = host
		s.hostRing = append(s.hostRing, host)
		if len(s.hostRing) > s.history {
			s.hostRing = s.hostRing[len(s.hostRing)-s.history:]
		}
		s.mu.Unlock()
	}
	// 主机采样失败不影响站点采样：两者走的是不同来源，
	// 一个坏掉不该让另一个也消失。
	//
	// 同理，主机采样**内部**的各路也要互不牵连：磁盘（getfsstat / 挂载表）
	// 与 GPU（ioreg）是两类容易失败的来源（一个失联的网络挂载、一台没有
	// 加速器的机器），因此它们的失败在 Host 里就被吞掉了 —— 拿不到就留空，
	// 而不是让整个 Host 返回错误。否则一次 ioreg 超时会把已经读到的 CPU、
	// 内存、网络一起丢掉，界面上那一整块直接变空。这条契约在这里只写一次：
	// sampleOnce 不需要知道哪些字段"可能缺席"。

	var refs []AppRef
	if apps != nil {
		refs = apps()
	}
	appPIDs := make([]int, 0, len(refs))
	for _, ref := range refs {
		if ref.PID > 0 {
			appPIDs = append(appPIDs, ref.PID)
		}
	}

	// footprint 的进程集合要在采样**之前**算出来，这样站点与 footprint
	// 能共用同一次 ps —— 多一个子进程不值得，而两者本来就看着同一批进程。
	footprintPIDs := s.footprintPIDs(ctx, appPIDs)

	samples := make([]AppSample, 0, len(refs))
	byPID := map[int]ProcessSample{}
	if all := unionPIDs(appPIDs, footprintPIDs); len(all) > 0 {
		if found, err := s.source.Processes(ctx, all); err == nil {
			byPID = found
		}
	}
	now := s.now()
	for _, ref := range refs {
		item := AppSample{AppID: ref.AppID, PID: ref.PID}
		if !ref.StartedAt.IsZero() {
			item.UptimeSeconds = int64(now.Sub(ref.StartedAt).Seconds())
		}
		if proc, ok := byPID[ref.PID]; ok {
			item.CPUPercent = proc.CPUPercent
			item.MemoryBytes = proc.MemoryBytes
		}
		samples = append(samples, item)
	}

	footprint := s.footprint(ctx, footprintPIDs, byPID, now)

	s.mu.Lock()
	s.latest.Apps = samples
	s.latest.Footprint = footprint
	s.mu.Unlock()
}

// footprintPIDs 返回"内核自身 + 站点进程树"的 PID 集合。
//
// 展开失败时退回只含根节点：一个少算了子进程的数字，仍然比整块缺失有用，
// 而且它偏小的方向是**可解释**的（用户能看到进程数变少）。
func (s *Sampler) footprintPIDs(ctx context.Context, appPIDs []int) []int {
	roots := make([]int, 0, len(appPIDs)+1)
	roots = append(roots, os.Getpid())
	roots = append(roots, appPIDs...)

	tree, ok := s.source.(TreeSource)
	if !ok {
		return roots
	}
	all, err := tree.Descendants(ctx, roots)
	if err != nil || len(all) == 0 {
		return roots
	}
	return all
}

// footprint 汇总一次 footprint 采样。
//
// byPID 是已经采好的进程样本（与站点共用），counters 现取。
func (s *Sampler) footprint(
	ctx context.Context,
	pids []int,
	byPID map[int]ProcessSample,
	now time.Time,
) FootprintSample {
	out := FootprintSample{At: now, Processes: len(pids)}
	for _, pid := range pids {
		if proc, ok := byPID[pid]; ok {
			out.CPUPercent += proc.CPUPercent
			out.MemoryBytes += proc.MemoryBytes
		}
	}
	out.NetRxBytesPerSec, out.NetTxBytesPerSec, out.HasNetwork, out.NetBackend =
		s.footprintNetwork(ctx, pids, now)
	return out
}

// footprintNetwork 用两次采样的差值算出被归因到这套进程上的速率。
//
// 返回的 HasNetwork 表示速率是否有效。三种"没有速率"必须区分开，
// 因为界面上的三句话完全不同：平台不支持 / 首次采样还没得比 / 这次没读到。
func (s *Sampler) footprintNetwork(ctx context.Context, pids []int, now time.Time) (rx, tx float64, ok bool, backend string) {
	net, supported := s.source.(ProcessNetwork)
	if !supported {
		return 0, 0, false, "unsupported"
	}
	counters, err := net.NetworkCounters(ctx)
	if err != nil {
		// 读失败**不**沿用上一轮的速率：那会让一个已经停掉的站点继续
		// 显示流量，而用户正盯着面板判断"现在还有没有在跑"。
		return 0, 0, false, "unavailable"
	}

	s.mu.Lock()
	prev, prevAt := s.prevNet, s.prevNetAt
	s.prevNet, s.prevNetAt = counters, now
	s.mu.Unlock()

	if prevAt.IsZero() {
		// 第一次采样没有可比的上一次，速率留空而不是报 0。
		return 0, 0, false, net.NetworkBackend()
	}
	elapsed := now.Sub(prevAt).Seconds()
	if elapsed <= 0 {
		return 0, 0, false, net.NetworkBackend()
	}
	for _, pid := range pids {
		rx += rateDelta(prev[pid].RxBytes, counters[pid].RxBytes, elapsed)
		tx += rateDelta(prev[pid].TxBytes, counters[pid].TxBytes, elapsed)
	}
	return rx, tx, true, net.NetworkBackend()
}

// rateDelta 把两个累计值变成速率。
//
// 计数回退（cur < prev）时返回 0，而不是算出一个负数：站点的进程重启之后
// 累计值会归零，差值直接算就是一条巨大的负速率 —— 界面上表现为曲线刺穿
// 坐标轴，或者更糟，被取绝对值之后变成一个凭空出现的尖峰。
func rateDelta(prev, cur uint64, elapsed float64) float64 {
	if cur <= prev {
		return 0
	}
	return float64(cur-prev) / elapsed
}

// expandDescendants 返回 roots 加上它们的全部后代。
//
// 结果里带 roots 自己：调用方要的是"这个站点涉及的所有进程"，而不是
// 只要它的子孙。用显式栈而不是递归 —— 进程树的深度理论上没有上限，
// 而一个坏掉的 ppid 环会让递归直接爆栈。
func expandDescendants(roots []int, children map[int][]int) []int {
	seen := make(map[int]bool, len(roots))
	out := make([]int, 0, len(roots))
	stack := make([]int, 0, len(roots))
	for _, pid := range roots {
		if pid > 0 && !seen[pid] {
			seen[pid] = true
			stack = append(stack, pid)
		}
	}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, pid)
		for _, child := range children[pid] {
			if child > 0 && !seen[child] {
				seen[child] = true
				stack = append(stack, child)
			}
		}
	}
	return out
}

// unionPIDs 合并两组 PID 并去重，保持稳定顺序（先 appPIDs）。
func unionPIDs(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	out := make([]int, 0, len(a)+len(b))
	for _, group := range [][]int{a, b} {
		for _, pid := range group {
			if pid <= 0 || seen[pid] {
				continue
			}
			seen[pid] = true
			out = append(out, pid)
		}
	}
	return out
}

// SampleNow 立刻采一次。
//
// 导出是为了让其它包的测试能在不启动 ticker 的前提下拿到一份真实快照；
// 生产路径用 Run。
func (s *Sampler) SampleNow(apps func() []AppRef) {
	s.sampleOnce(context.Background(), apps)
}

// Latest 返回最近一次采样。
func (s *Sampler) Latest() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Snapshot{Host: s.latest.Host}
	// Disks 是切片，连同它一起拷一份：调用方拿到的视图不该被下一次采样
	// 改写（Apps 早就是这么做的）。append 到 nil 上而不是空切片上，
	// 是为了让"没有磁盘"仍然是 nil 而不是一个长度 0 的切片。
	out.Host.Disks = append([]DiskSample(nil), s.latest.Host.Disks...)
	out.Apps = append([]AppSample{}, s.latest.Apps...)
	out.Footprint = s.latest.Footprint
	return out
}

// HostHistory 返回最近若干次主机采样（最旧在前）。
func (s *Sampler) HostHistory() []HostSample {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]HostSample{}, s.hostRing...)
}

// Describe 报告采样后端。
func (s *Sampler) Describe() string {
	if s.source == nil {
		return "unsupported"
	}
	return s.source.Describe()
}

// NewSource 返回当前平台的采样后端。
//
// 未实现的平台返回一个"总是失败"的实现：上层据此把指标显示为"不支持"，
// 而不是显示一堆零 —— 零看起来像"什么都没占用"。
func NewSource() Source { return newPlatformSource() }

// errUnsupported 表示当前平台没有实现采样。
var errUnsupported = errors.New("metrics are not available on this platform")

// itoa 是 strconv.Itoa 的短名，避免本包在两个平台文件里重复导入 strconv。
func itoa(value int) string { return strconv.Itoa(value) }
