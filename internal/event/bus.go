// Package event 是内核的事件总线。
//
// 设计见 docs/DECISIONS.md D08 与 docs/ARCHITECTURE.md §6：
//
//   - 事件带**单调递增**的序号 seq；
//   - 最近 N 条保留在环形缓冲中，客户端断线重连时可凭 lastEventId 补发；
//   - 若请求的序号已滑出缓冲窗口，返回 gap 让客户端重新拉取全量状态，
//     而不是假装什么都没发生；
//   - 订阅者消费不过来时**主动断开**该订阅者，逼迫其重连并走补发路径。
//     静默丢事件会让界面上的状态永久错位，比断开连接危险得多。
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 事件类型常量。
//
// 命名约定：<领域>.<过去式动作>。领域与 API 的资源命名保持一致，
// 便于客户端按前缀批量处理（例如所有 "job." 事件）。
const (
	// TypeJobProgress 任务进度变化。
	TypeJobProgress = "job.progress"
	// TypeJobFinished 任务结束（成功 / 失败 / 取消）。
	TypeJobFinished = "job.finished"

	// TypeIPChanged 本机地址变化。
	TypeIPChanged = "ip.changed"
	// TypeIPPrefixChanged IPv6 委派前缀变化。
	//
	// 这是本项目的核心事件：前缀变化意味着该前缀下**所有** AAAA 记录
	// 都需要重写，而不是单个地址更新。
	TypeIPPrefixChanged = "ip.prefix_changed"

	// TypeEventsGap 告知客户端请求的序号已滑出保留窗口，需要重新拉取全量状态。
	TypeEventsGap = "events.gap"

	// TypeLogAppended 新日志。
	TypeLogAppended = "log.appended"
	// TypeConfigChanged 配置变化。
	TypeConfigChanged = "config.changed"
)

// DefaultCapacity 是环形缓冲的默认容量。
//
// 1000 条足以覆盖客户端一次常规断线重连（例如笔记本合盖再打开）期间的
// 事件量，同时内存占用可忽略。
const DefaultCapacity = 1000

// subscriberBuffer 是单个订阅者的通道缓冲。
//
// 取值依据：事件流是"突发型"的（例如一次前缀变化会触发数十条记录更新事件），
// 缓冲太小会让正常突发被误判为消费不过来。
const subscriberBuffer = 256

// Event 是一条事件。
type Event struct {
	// Seq 是单调递增的序号，从 1 开始。
	Seq int64 `json:"seq"`
	// TS 是事件产生时间（UTC）。
	TS time.Time `json:"ts"`
	// Type 是事件类型，见本包的 Type* 常量。
	Type string `json:"type"`
	// Payload 是事件载荷，结构由 Type 决定。
	Payload json.RawMessage `json:"payload,omitempty"`
}

// ErrGap 表示请求的起始序号已滑出环形缓冲窗口。
//
// 调用方应当据此推送一条 TypeEventsGap 事件，并让客户端重新拉取全量状态。
type ErrGap struct {
	// Requested 是客户端请求的起始序号。
	Requested int64
	// Earliest 是当前缓冲中最早可用的事件序号（0 表示缓冲为空）。
	Earliest int64
	// Latest 是当前最新事件序号。
	Latest int64
}

// Error 实现 error。
func (e *ErrGap) Error() string {
	return i18n.T("events.gap", e.Requested, e.Earliest)
}

// Bus 是事件总线。
//
// 并发安全。Publish 不会被慢订阅者阻塞：缓冲满时直接断开该订阅者。
type Bus struct {
	mu       sync.Mutex
	ring     []Event
	capacity int
	nextSeq  int64
	subs     map[uint64]*Subscription
	nextSub  uint64
	closed   bool
}

// NewBus 构造一个容量为 capacity 的总线。
//
// capacity <= 0 时使用 DefaultCapacity。
func NewBus(capacity int) *Bus {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Bus{
		ring:     make([]Event, 0, capacity),
		capacity: capacity,
		nextSeq:  1,
		subs:     make(map[uint64]*Subscription),
	}
}

// Publish 发布一条事件并返回它。
//
// payload 会被序列化为 JSON；序列化失败时 payload 置空而不是丢弃整条事件 ——
// "事件发生过"这个事实比它的细节更重要。
//
// 总线已关闭时返回零值 Event。
func (b *Bus) Publish(typ string, payload any) Event {
	var raw json.RawMessage
	if payload != nil {
		if byt, err := json.Marshal(payload); err == nil {
			raw = byt
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return Event{}
	}

	ev := Event{
		Seq:     b.nextSeq,
		TS:      time.Now().UTC(),
		Type:    typ,
		Payload: raw,
	}
	b.nextSeq++

	b.ring = append(b.ring, ev)
	if len(b.ring) > b.capacity {
		// 丢弃最旧的一条。先把头部元素置零再整体左移，
		// 否则底层数组会一直持有已丢弃事件的 Payload，阻碍 GC。
		n := copy(b.ring, b.ring[1:])
		b.ring[n] = Event{}
		b.ring = b.ring[:n]
	}

	for id, sub := range b.subs {
		if !sub.deliver(ev) {
			// 订阅者消费不过来：断开它，让它重连后凭 lastEventId 补发。
			sub.terminate(fmt.Errorf(
				"event: 订阅者 %d 消费过慢，已断开以触发补发", id))
			delete(b.subs, id)
		}
	}

	return ev
}

// LatestSeq 返回当前最新事件序号；无事件时为 0。
func (b *Bus) LatestSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq - 1
}

// EarliestSeq 返回环形缓冲中最早事件的序号；缓冲为空时为 0。
func (b *Bus) EarliestSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.ring) == 0 {
		return 0
	}
	return b.ring[0].Seq
}

// Close 关闭总线，断开全部订阅者。
func (b *Bus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := make([]*Subscription, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.subs = make(map[uint64]*Subscription)
	b.mu.Unlock()

	// 在锁外终止：terminate 不触碰总线状态，无需持锁。
	for _, s := range subs {
		s.terminate(nil)
	}
}

// ---------------------------------------------------------------------------
// 订阅
// ---------------------------------------------------------------------------

// Subscription 是一次事件订阅。
//
// 生命周期由 terminate 唯一决定：它恰好一次地关闭 done 与 ch。
// 消费方应当 range C()，通道关闭即表示订阅结束；结束原因用 Err 查询。
type Subscription struct {
	id   uint64
	ch   chan Event
	done chan struct{}
	bus  *Bus

	once  sync.Once
	errMu sync.Mutex
	err   error
}

// C 返回事件通道。通道被关闭表示订阅已结束。
func (s *Subscription) C() <-chan Event { return s.ch }

// Err 返回订阅结束的原因；正常关闭返回 nil。
//
// 只有 C() 已关闭后调用才有确定意义。
func (s *Subscription) Err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

// Close 结束订阅。可重复调用。
func (s *Subscription) Close() {
	s.bus.removeSubscriber(s.id)
}

// terminate 结束订阅并记录原因，保证只执行一次。
//
// 这是唯一关闭 s.ch 的地方 —— 通道被关闭后任何发送都会 panic，
// 因此必须让所有结束路径都汇聚到这里，并由 once 保证幂等。
func (s *Subscription) terminate(err error) {
	s.once.Do(func() {
		s.errMu.Lock()
		s.err = err
		s.errMu.Unlock()
		close(s.done)
		close(s.ch)
	})
}

// deliver 尝试投递一条事件。
//
// 返回 false 表示订阅者消费不过来，调用方应当断开它。
//
// 并发前提：调用方（Publish）持有总线锁，因此不会有并发的 terminate ——
// Subscription.Close 与 Bus.Close 都需要先取得总线锁。
func (s *Subscription) deliver(ev Event) bool {
	select {
	case <-s.done:
		// 已结束，无需投递，也不算消费过慢。
		return true
	default:
	}
	select {
	case s.ch <- ev:
		return true
	default:
		return false
	}
}

func (b *Bus) removeSubscriber(id uint64) {
	b.mu.Lock()
	sub, ok := b.subs[id]
	delete(b.subs, id)
	b.mu.Unlock()
	if ok {
		sub.terminate(nil)
	}
}

// Subscribe 从 afterSeq 之后开始订阅。
//
// afterSeq == 0 表示"只要新事件"，不回放历史 —— 客户端应当自行拉取全量状态
// 后再订阅，这样语义最清晰。
//
// afterSeq > 0 时先回放环形缓冲中序号大于 afterSeq 的事件。若这些事件已被
// 挤出窗口，或回放量超出订阅者缓冲容量（此时分片投递会破坏与实时事件的
// 顺序交错），返回 *ErrGap，调用方应当推送 events.gap 并让客户端重新拉取。
func (b *Bus) Subscribe(afterSeq int64) (*Subscription, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil, errors.New("event: 总线已关闭")
	}

	var replay []Event
	if afterSeq > 0 {
		earliest := int64(0)
		if len(b.ring) > 0 {
			earliest = b.ring[0].Seq
		}
		latest := b.nextSeq - 1

		// 客户端声称收到过尚未产生的事件：它的状态与内核不一致。
		// 这种情况必须报 gap —— 放行的话客户端会带着一份错误的状态
		// 继续接收增量，此后所有显示都是错的。
		if afterSeq > latest {
			return nil, &ErrGap{Requested: afterSeq, Earliest: earliest, Latest: latest}
		}

		// 缓冲非空但客户端要的起点早于窗口：补发链已断。
		if earliest > 0 && afterSeq+1 < earliest {
			return nil, &ErrGap{Requested: afterSeq, Earliest: earliest, Latest: latest}
		}

		for _, ev := range b.ring {
			if ev.Seq > afterSeq {
				replay = append(replay, ev)
			}
		}
		if len(replay) > subscriberBuffer {
			return nil, &ErrGap{
				Requested: afterSeq,
				Earliest:  replay[0].Seq,
				Latest:    b.nextSeq - 1,
			}
		}
	}

	b.nextSub++
	sub := &Subscription{
		id:   b.nextSub,
		ch:   make(chan Event, subscriberBuffer),
		done: make(chan struct{}),
		bus:  b,
	}
	b.subs[sub.id] = sub

	// 回放必须在锁内完成，否则实时事件可能插到回放事件之前，
	// 破坏订阅者看到的序号单调性。
	for _, ev := range replay {
		sub.ch <- ev
	}

	return sub, nil
}
