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
