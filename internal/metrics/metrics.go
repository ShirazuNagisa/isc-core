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

// Snapshot 是某一时刻的完整视图。
type Snapshot struct {
	Host HostSample
	Apps []AppSample
}

// AppRef 是采样器需要知道的、关于一个正在运行的站点的最小信息。
type AppRef struct {
	AppID     string
	PID       int
	StartedAt time.Time
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
	pids := make([]int, 0, len(refs))
	for _, ref := range refs {
		if ref.PID > 0 {
			pids = append(pids, ref.PID)
		}
	}

	samples := make([]AppSample, 0, len(refs))
	byPID := map[int]ProcessSample{}
	if len(pids) > 0 {
		if found, err := s.source.Processes(ctx, pids); err == nil {
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

	s.mu.Lock()
	s.latest.Apps = samples
	s.mu.Unlock()
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
