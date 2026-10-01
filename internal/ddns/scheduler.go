package ddns

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// 本文件是任务的触发时序。
//
// 两个触发源：
//
//	定时  每 N 秒遍历一次全部启用中的任务
//	事件  IPMonitor 报告地址或前缀变化时立刻执行
//
// # 为什么两个都要
//
// 只用定时：ISP 重拨后要等到下一个周期才更新，而 IPv6 前缀变化在
// 家用宽带上很频繁（重拨、租约到期）。用户看到的现象是"域名指向旧地址
// 持续好几分钟"。
//
// 只用事件：外部接口获取的地址（url 方式）不会产生网卡事件，
// 而某些运营商会在不改变本机地址的情况下改变出口地址。
//
// # 事件合流
//
// 一次重拨会产生多个事件（前缀变了、旧地址消失、新地址出现）。
// 逐个触发会执行多次全量任务 —— 虽然防抖缓存保证只有一次真的去请求
// 服务商，但每次都遍历一遍全部任务、读一次网卡快照，纯属浪费。
// 因此这里把短时间内的多个事件合流成一次执行。

// defaultInterval 是定时轮询周期。
//
// 5 分钟与 ddns-go 的默认值一致：地址变化由事件即时触发，
// 定时轮询只是兜底（覆盖 url 方式获取的地址、以及事件机制漏掉的情况）。
const defaultInterval = 5 * time.Minute

// eventCoalesceWindow 是事件合流窗口。
//
// 1 秒的依据：一次重拨的多个事件几乎同时到达（同一轮轮询里产生），
// 而 1 秒足以把它们收进同一次执行，又不会让用户感觉到延迟。
const eventCoalesceWindow = time.Second

// Scheduler 按定时与事件触发执行动态解析任务。
type Scheduler struct {
	repo   Repository
	engine *Engine
	bus    *event.Bus
	log    *slog.Logger

	interval time.Duration

	// trigger 用于从外部请求一次立即执行。空串表示"全部任务"。
	trigger chan string

	// mu 保护 running，防止同一个任务被并发执行两次。
	//
	// 这是必要的：定时器与事件触发可能同时到来，而同一个任务被并发执行
	// 会产生两次服务商请求，甚至两次写入同一个记录 —— 后者在部分服务商
	// 那边会导致记录 ID 冲突。
	mu      sync.Mutex
	running map[string]bool

	// onResult 在每次任务执行后被调用，用于写入运行状态。
	// 允许为 nil（测试场景）。
	onResult func(ctx context.Context, t Task, run TaskRun)
}

// NewScheduler 构造调度器。
func NewScheduler(repo Repository, engine *Engine, bus *event.Bus, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		repo:     repo,
		engine:   engine,
		bus:      bus,
		log:      log,
		interval: defaultInterval,
		trigger:  make(chan string, 16),
		running:  make(map[string]bool),
	}
}

// SetBus 设置事件总线，用于把 IP 变化转成执行触发。
func (s *Scheduler) SetBus(bus *event.Bus) { s.bus = bus }

// SetInterval 覆盖定时周期，供测试使用。
func (s *Scheduler) SetInterval(d time.Duration) {
	if d > 0 {
		s.interval = d
	}
}

// SetResultHook 设置任务执行后的回调。
func (s *Scheduler) SetResultHook(fn func(ctx context.Context, t Task, run TaskRun)) {
	s.onResult = fn
}

// Trigger 请求立即执行。taskID 为空表示全部启用中的任务。
//
// 非阻塞：调用方（HTTP 请求、事件处理）不该因为调度器正忙而卡住。
// 通道满时丢弃请求而不是阻塞 —— 因为定时轮询马上会兜底，
// 而阻塞一个 HTTP 请求去等调度器是更糟的选择。
func (s *Scheduler) Trigger(taskID string) {
	select {
	case s.trigger <- taskID:
	default:
		s.log.Debug("调度触发队列已满，丢弃本次请求（定时轮询会兜底）", "task", taskID)
	}
}

// Run 启动调度循环，直到 ctx 被取消。
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// 订阅事件总线，把 IP 变化转成一次执行请求。
	//
	// 用 afterSeq=0 只收新事件：调度器不关心历史上发生过什么变化，
	// 它只需要"现在可能变了"这个信号，而具体地址总是现取的。
	var events <-chan event.Event
	if s.bus != nil {
		sub, err := s.bus.Subscribe(0)
		if err != nil {
			s.log.Warn("调度器订阅事件失败，将只使用定时触发", "err", err)
		} else {
			defer sub.Close()
			events = sub.C()
		}
	}

	// 合流窗口：收到 IP 事件后启动一个短延时定时器，期间再来事件就重置。
	var coalesce *time.Timer
	var coalesceC <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			if coalesce != nil {
				coalesce.Stop()
			}
			return nil

		case <-ticker.C:
			s.RunAll(ctx)

		case id := <-s.trigger:
			if id == "" {
				s.RunAll(ctx)
			} else {
				s.RunOne(ctx, id)
			}

		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !isIPEvent(ev.Type) {
				continue
			}
			if coalesce == nil {
				coalesce = time.NewTimer(eventCoalesceWindow)
				coalesceC = coalesce.C
			} else {
				coalesce.Reset(eventCoalesceWindow)
			}

		case <-coalesceC:
			coalesce = nil
			coalesceC = nil
			s.log.Info("检测到地址变化，触发动态解析")
			s.RunAll(ctx)
		}
	}
}

// isIPEvent 报告事件类型是否与地址变化相关。
func isIPEvent(typ string) bool {
	switch typ {
	case event.TypeIPChanged, event.TypeIPPrefixChanged:
		return true
	default:
		return false
	}
}

// RunAll 执行全部启用中的任务。
func (s *Scheduler) RunAll(ctx context.Context) {
	tasks, err := s.repo.List(ctx, true)
	if err != nil {
		s.log.Error("读取任务列表失败", "err", err)
		return
	}
	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		s.runTask(ctx, t)
	}
}

// RunOne 执行指定任务。
func (s *Scheduler) RunOne(ctx context.Context, id string) {
	t, found, err := s.repo.Get(ctx, id)
	if err != nil {
		s.log.Error("读取任务失败", "task", id, "err", err)
		return
	}
	if !found {
		s.log.Warn("任务不存在", "task", id)
		return
	}
	s.runTask(ctx, t)
}

// runTask 执行单个任务，处理防重入与结果记账。
func (s *Scheduler) runTask(ctx context.Context, t Task) {
	if !t.Enabled {
		return
	}

	// 防重入：同一个任务同时只允许一个实例在跑。
	if !s.acquire(t.ID) {
		s.log.Debug("任务正在执行中，跳过本次触发", "task", t.ID)
		return
	}
	defer s.release(t.ID)

	run, err := s.engine.RunTask(ctx, t)
	if err != nil {
		// 执行层面失败（凭据取不到、服务商不支持）：记进任务状态，
		// 让用户在界面上看到原因，而不是只留在日志里。
		s.log.Error("任务执行失败", "task", t.ID, "err", err)
		run = TaskRun{TaskID: t.ID, Status: StatusFailed, Message: err.Error()}
	}

	if s.onResult != nil {
		// 用 WithoutCancel：任务被取消时仍要把"已失败/已取消"的状态写进去。
		s.onResult(context.WithoutCancel(ctx), t, run)
	}

	switch run.Status {
	case StatusSuccess:
		s.log.Info("动态解析已更新",
			"task", t.ID, "label", t.Label, "message", run.Message)
	case StatusFailed:
		s.log.Warn("动态解析失败",
			"task", t.ID, "label", t.Label, "message", run.Message)
	default:
		s.log.Debug("动态解析无需改动",
			"task", t.ID, "label", t.Label, "message", run.Message)
	}
}

func (s *Scheduler) acquire(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Scheduler) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}
