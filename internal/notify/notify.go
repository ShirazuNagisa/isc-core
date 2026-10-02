// Package notify 是通知中心。
//
// # 它要解决的核心问题不是"能发出去"，而是"不被刷屏"
//
// 内核产生的事件里有相当一部分会**成串出现**：
//
//	IPv6 前缀抖动      重拨一次产生若干条地址变化事件
//	服务商限流         连续几次更新失败
//	证书续期失败       定时重试，每小时一次
//
// 原样转发的话，用户会在几分钟内收到十几条一模一样的消息，
// 然后关掉通知 —— 而那之后真正重要的那条他也看不到了。
//
// 因此本包的核心是**去重与静默期**：同一个 DedupKey 在静默窗口内
// 只发一条，后续的被抑制并计数。窗口结束时如果抑制过消息，
// 补发一条"另有 N 次同类事件"。
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Severity 是消息的重要程度。
type Severity string

const (
	// SeverityInfo 常规信息，例如"地址已更新"。
	SeverityInfo Severity = "info"
	// SeverityWarning 需要留意但不紧急，例如"证书将在 30 天后过期"。
	SeverityWarning Severity = "warning"
	// SeverityError 需要处理，例如"动态解析连续失败"。
	SeverityError Severity = "error"
)

// Message 是一条通知。
type Message struct {
	// Event 是触发它的事件类型（例如 dns.update_failed）。
	Event string `json:"event"`
	// Title 是一行摘要。
	Title string `json:"title"`
	// Body 是详细说明。
	Body string `json:"body,omitempty"`
	// Severity 是重要程度。
	Severity Severity `json:"severity"`
	// At 是事件发生时间。
	At time.Time `json:"at"`

	// DedupKey 是去重键。
	//
	// **同一个键在静默期内只会发出一条消息。** 调用方应当让键
	// 只包含"这件事是什么"，而不包含变化的部分：
	//
	//	dns.update_failed:task-abc    好的键
	//	dns.update_failed:203.0.113.7 坏的键（地址一变就是新消息）
	//
	// 留空表示不去重 —— 每条都发。慎用：事件风暴时会刷屏。
	DedupKey string `json:"dedup_key,omitempty"`

	// Data 是给通道用的结构化数据（例如 Webhook 的模板变量）。
	Data map[string]any `json:"data,omitempty"`
}

// Validate 检查消息是否可发送。
func (m Message) Validate() error {
	if strings.TrimSpace(m.Title) == "" {
		return errors.New("notify: 消息缺少标题")
	}
	switch m.Severity {
	case SeverityInfo, SeverityWarning, SeverityError, "":
	default:
		return fmt.Errorf("notify: 不支持的重要程度 %q", m.Severity)
	}
	return nil
}

// Channel 是一种通知通道。
type Channel interface {
	// Name 是通道的可读名称，用于日志与界面。
	Name() string
	// Kind 是通道类型（webhook / serverchan / …）。
	Kind() string
	// Send 发送一条消息。
	//
	// 实现应当**自己控制超时**：通道挂住会让通知队列积压，
	// 而积压到一定程度就只能丢弃 —— 那意味着用户漏掉通知。
	Send(ctx context.Context, msg Message) error
}

// Delivery 是一次投递的结果。
type Delivery struct {
	// Channel 是通道名称。
	Channel string `json:"channel"`
	// Kind 是通道类型。
	Kind string `json:"kind"`
	// OK 表示是否成功。
	OK bool `json:"ok"`
	// Error 是失败原因。
	Error string `json:"error,omitempty"`
	// At 是投递时间。
	At time.Time `json:"at"`
}

// defaultQuietPeriod 是同一个去重键的默认静默期。
//
// 5 分钟的依据：它足够长到把一次事件风暴收进一条通知，又足够短到
// 让"持续存在的问题"在一刻钟内被再次提醒。更长会让用户以为通知坏了，
// 更短则起不到抑制的作用。
const defaultQuietPeriod = 5 * time.Minute

// defaultQueueSize 是投递队列的长度。
//
// 队列满时**丢弃最旧的**而不是阻塞发送方：通知是旁路功能，
// 它绝不能拖慢动态解析或证书续期这些主线任务。
const defaultQueueSize = 128

// Manager 把事件分发给各个通道。
type Manager struct {
	log *slog.Logger

	mu       sync.RWMutex
	channels []Channel
	quiet    time.Duration
	minLevel Severity

	// seen 记录每个去重键的状态。
	seen map[string]*dedupState

	// deliveries 保留最近的投递结果。
	//
	// 保留它是必要的："我的通知到底发出去了没有"是用户配置通道时
	// 问得最多的一个问题，而只写日志的话他得去翻日志文件。
	deliveries []Delivery
	maxHistory int

	queue chan Message
	wg    sync.WaitGroup
}

// dedupState 是一个去重键的当前状态。
type dedupState struct {
	// lastSent 是上次真正发出的时间。
	lastSent time.Time
	// suppressed 是从上次发出之后被抑制的条数。
	suppressed int
	// pending 标记是否已经安排了"补发汇总"。
	pending bool
}

// NewManager 构造通知中心。
func NewManager(log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		log:        log,
		quiet:      defaultQuietPeriod,
		minLevel:   SeverityInfo,
		seen:       make(map[string]*dedupState),
		maxHistory: 100,
		queue:      make(chan Message, defaultQueueSize),
	}
}

// SetQuietPeriod 覆盖静默期。0 表示**关闭去重**。
func (m *Manager) SetQuietPeriod(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.quiet = d
}

// SetMinSeverity 设置最低发送级别。
//
// 低于它的消息直接被丢弃。用户想"只收错误"时不必逐个通道去配。
func (m *Manager) SetMinSeverity(s Severity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.minLevel = s
}

// AddChannel 登记一个通道。
func (m *Manager) AddChannel(ch Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = append(m.channels, ch)
	m.log.Info("已登记通知通道", "name", ch.Name(), "kind", ch.Kind())
}

// Channels 返回已登记的通道。
func (m *Manager) Channels() []Channel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]Channel(nil), m.channels...)
}

// Run 启动投递循环，直到 ctx 被取消。
func (m *Manager) Run(ctx context.Context) {
	m.wg.Add(1)
	defer m.wg.Done()

	// 静默期的补发检查。
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-m.queue:
			m.deliver(ctx, msg)
		case <-ticker.C:
			m.flushSuppressed(ctx)
		}
	}
}

// Notify 提交一条通知。
//
// **非阻塞**：事件循环调用它时不该被网络请求拖住。队列满时丢弃
// 最旧的消息 —— 通知是旁路功能，它绝不能拖慢动态解析或证书续期。
func (m *Manager) Notify(msg Message) {
	if err := msg.Validate(); err != nil {
		m.log.Warn("丢弃不合法的通知", "err", err)
		return
	}
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}

	if !m.passFilter(msg) {
		return
	}
	if !m.shouldSend(msg) {
		return
	}

	select {
	case m.queue <- msg:
	default:
		// 队列满：丢掉最旧的一条再放进去。
		//
		// 丢掉**最旧**而不是**最新**：积压时用户更需要知道刚刚发生了什么，
		// 而不是五分钟前那条。
		select {
		case dropped := <-m.queue:
			m.log.Warn("通知队列已满，丢弃最旧的一条",
				"dropped", dropped.Title, "incoming", msg.Title)
		default:
		}
		select {
		case m.queue <- msg:
		default:
			m.log.Warn("通知队列仍然满，本次通知被丢弃", "title", msg.Title)
		}
	}
}

// passFilter 判断消息是否达到最低级别。
func (m *Manager) passFilter(msg Message) bool {
	m.mu.RLock()
	minLevel := m.minLevel
	m.mu.RUnlock()

	return severityRank(msg.Severity) >= severityRank(minLevel)
}

// shouldSend 做去重判断。
func (m *Manager) shouldSend(msg Message) bool {
	if msg.DedupKey == "" {
		return true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// 关闭去重时直接放行。
	if m.quiet <= 0 {
		return true
	}

	now := time.Now()
	st, ok := m.seen[msg.DedupKey]
	if !ok {
		m.seen[msg.DedupKey] = &dedupState{lastSent: now}
		return true
	}

	if now.Sub(st.lastSent) >= m.quiet {
		// 静默期已过：发出去，并把之前抑制的条数一并带上。
		st.suppressed = 0
		st.pending = false
		st.lastSent = now
		return true
	}

	// 静默期内：抑制并计数。
	st.suppressed++
	return false
}

// flushSuppressed 为"被抑制过消息"的键补发一条汇总。
//
// 只计数不补发是**不够的**：用户会以为那段时间什么都没发生，
// 而实际上是他最关心的那类事件在反复出现。
func (m *Manager) flushSuppressed(ctx context.Context) {
	m.mu.Lock()
	now := time.Now()
	var pending []Message

	for key, st := range m.seen {
		if st.suppressed == 0 || st.pending {
			continue
		}
		if now.Sub(st.lastSent) < m.quiet {
			continue
		}
		// 静默期已过且期间有被抑制的消息：安排一条汇总。
		pending = append(pending, Message{
			Event:    "notify.suppressed_summary",
			Title:    fmt.Sprintf("另有 %d 次同类事件被合并", st.suppressed),
			Body:     "触发键：" + key,
			Severity: SeverityWarning,
			At:       now,
			// 汇总消息本身不参与去重。
			Data: map[string]any{"dedup_key": key, "count": st.suppressed},
		})
		st.suppressed = 0
		st.pending = false
		st.lastSent = now
	}
	m.mu.Unlock()

	for _, msg := range pending {
		m.deliver(ctx, msg)
	}
}

// deliver 把一条消息发给全部通道。
func (m *Manager) deliver(ctx context.Context, msg Message) {
	channels := m.Channels()
	if len(channels) == 0 {
		return
	}

	m.log.Info("发送通知",
		"event", msg.Event, "severity", msg.Severity,
		"title", msg.Title, "channels", len(channels))

	for _, ch := range channels {
		// 单个通道失败不阻断其余的：一个 Webhook 配错了不该让
		// 邮件也收不到。
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := ch.Send(sendCtx, msg)
		cancel()

		d := Delivery{
			Channel: ch.Name(), Kind: ch.Kind(),
			OK: err == nil, At: time.Now().UTC(),
		}
		if err != nil {
			d.Error = err.Error()
			m.log.Warn("通知投递失败",
				"channel", ch.Name(), "kind", ch.Kind(), "err", err)
		}
		m.recordDelivery(d)
	}
}

func (m *Manager) recordDelivery(d Delivery) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.deliveries = append(m.deliveries, d)
	if len(m.deliveries) > m.maxHistory {
		m.deliveries = m.deliveries[len(m.deliveries)-m.maxHistory:]
	}
}

// Deliveries 返回最近的投递结果（最新的在前）。
//
// 它是用户配置通道时最有用的东西："我的通知到底发出去了没有"。
func (m *Manager) Deliveries() []Delivery {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Delivery, 0, len(m.deliveries))
	for i := len(m.deliveries) - 1; i >= 0; i-- {
		out = append(out, m.deliveries[i])
	}
	return out
}

// SendNow 立即向全部通道投递一条消息，绕过去重与队列。
//
// 用于界面上那个"发送测试通知"按钮 —— 用户点了之后期待**立刻**
// 看到结果，而不是等下一个投递循环。
//
// # 它也绕过了级别过滤
//
// 这一点是必须的，而且是真机上发现的：一个配了"仅在 warning 及以上
// 发送"的通道，在测试时会因为测试消息是 info 而被过滤掉 ——
// 而过滤器把"被过滤"报成成功，于是用户看到 ✅，实际什么都没发出去。
//
// 用户点"测试"时的意图是"**现在真的发一条**"，因此级别过滤在这里
// 不该生效。想看通道是否配通，就必须真的打一次目标地址。
func (m *Manager) SendNow(ctx context.Context, msg Message) []Delivery {
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	if err := msg.Validate(); err != nil {
		return []Delivery{{
			Channel: "-", OK: false, Error: err.Error(), At: time.Now().UTC(),
		}}
	}

	var out []Delivery
	for _, ch := range m.Channels() {
		// 拆掉级别过滤，直接打到真正的通道上。
		target := unwrapFilter(ch)

		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := target.Send(sendCtx, msg)
		cancel()

		d := Delivery{
			Channel: ch.Name(), Kind: ch.Kind(),
			OK: err == nil, At: time.Now().UTC(),
		}
		if err != nil {
			d.Error = err.Error()
		}
		m.recordDelivery(d)
		out = append(out, d)
	}
	return out
}

// unwrapFilter 剥掉通道外面的所有包装，露出真正的通道。
//
// 用循环而不是一次断言：通道可能被多层包起来
// （`dynamicMarker` 标记来源、`levelFilter` 做级别过滤），
// 而只剥一层会让"测试通知绕过滤"这个保证在某些组合下失效 ——
// 那种失败是静默的（仍然报成功），正是要避免的。
func unwrapFilter(ch Channel) Channel {
	for i := 0; i < 4; i++ {
		switch v := ch.(type) {
		case levelFilter:
			ch = v.inner
		case dynamicMarker:
			ch = v.Channel
		default:
			return ch
		}
	}
	return ch
}

// Wait 等待投递循环退出（用于测试与优雅关闭）。
func (m *Manager) Wait() { m.wg.Wait() }

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 1
	}
}

// SortedDeliveries 按通道名排序返回投递记录，便于界面稳定展示。
func SortedDeliveries(list []Delivery) []Delivery {
	out := append([]Delivery(nil), list...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Channel < out[j].Channel
	})
	return out
}
