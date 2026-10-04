package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件实现事件流的**游标长轮询**变体。
//
// # 为什么除了 WebSocket 还要有它
//
// WebSocket 是给浏览器与本机 GUI 的：它们长驻、网络稳定、没有系统会
// 把它们的进程挂起。手机不是 —— iOS 会在切到后台时冻结进程、在切换
// 网络时掐断所有连接，而恢复一个 WebSocket 需要重建握手、重新对齐游标、
// 并处理"断线期间到达的事件"。
//
// 长轮询把同一件事变成"再发一个请求"：没有握手状态、没有半开连接、
// 中间有代理也无所谓。代价是每次没有事件时的往返开销 —— 而 25 秒挂起
// 一次的成本远低于一条时刻要维护的持久连接。
//
// 语义与 `isc_events_json`（libisc 的进程内长轮询）**逐字对齐**：
// 两者是同一个契约的两个传输，行为不同会让"本机看到的"与"手机上看到的"
// 在同一时刻不一致。

// 长轮询的取值边界。
const (
	pollDefaultTimeout = 25000
	// pollMaxTimeout 是 55 秒。再长会被常见的反向代理与运营商网关
	// 按空闲连接掐断，而那种断开在客户端看来是"网络错误"，
	// 没有任何线索指向"是你的超时设大了"。
	pollMaxTimeout   = 55000
	pollDefaultLimit = 200
	pollMaxLimit     = 500
)

// PollEvents 实现 GET /v1/events/poll。
func (s *Server) PollEvents(w http.ResponseWriter, r *http.Request, params gen.PollEventsParams) {
	if s.Bus == nil {
		s.internalError(w, r, i18n.T("api.events_bus_missing"), errors.New(i18n.T("libisc.bus_missing")))
		return
	}

	timeout := pollDefaultTimeout
	if params.TimeoutMs != nil {
		timeout = *params.TimeoutMs
	}
	limit := pollDefaultLimit
	if params.Limit != nil {
		limit = *params.Limit
	}
	if timeout < 0 || timeout > pollMaxTimeout {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.FromContext(r.Context()).T("remote.api.poll_timeout"))
		return
	}
	if limit < 1 || limit > pollMaxLimit {
		writeProblem(w, r, s.Log, http.StatusBadRequest,
			CodeInvalidRequest, "error.invalid_request", i18n.FromContext(r.Context()).T("remote.api.poll_limit"))
		return
	}

	since := int64(0)
	if params.Since != nil {
		since = *params.Since
	}
	if since <= 0 {
		// since=0 表示"从现在开始"，而不是"从头开始"。
		//
		// 从头开始会把环形缓冲里的一千条历史一次性倒给刚连上的客户端，
		// 而它一条都不需要 —— 那些事件描述的是它没看到的状态变化，
		// 而它接下来会自己拉一次全量。
		since = s.Bus.LatestSeq()
	}

	sub, err := s.Bus.Subscribe(since)
	if err != nil {
		s.internalError(w, r, i18n.T("api.events_subscribe_failed"), err)
		return
	}
	defer sub.Close()

	events := make([]gen.KernelEvent, 0, limit)
	next := since

	appendEvent := func(ev event.Event) bool {
		if len(events) >= limit {
			return false
		}
		events = append(events, toGenKernelEvent(ev))
		next = ev.Seq
		return true
	}

	// 先按需阻塞等一条。
	//
	// 循环等待是必要的：订阅建立到这一刻之间的空档里可能已经来了事件，
	// 而那种情况下 `<-sub.C()` 会**立刻**返回；真正的空等只在真的事件
	// 稀疏时发生。
	if timeout > 0 {
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		select {
		case ev, ok := <-sub.C():
			if ok {
				appendEvent(ev)
			}
		case <-timer.C:
		case <-r.Context().Done():
			// 客户端先走了（切后台、切网络、用户退出）。
			//
			// 这不是错误，也不值得记一笔：长轮询被中断是它的**正常**
			// 生命周期的一部分。返回一个已取消的 context 写入的结果
			// 同样没有意义。
		}
		timer.Stop()
	}

drain:
	for len(events) < limit {
		select {
		case ev, ok := <-sub.C():
			if !ok {
				break drain
			}
			if !appendEvent(ev) {
				break drain
			}
		default:
			break drain
		}
	}

	writeJSON(w, s.Log, http.StatusOK, "application/json", gen.EventPoll{
		Events: events,
		Next:   next,
		Gap:    eventsGap(sub, events, since),
	})
}

// eventsGap 判断这一批事件之前是否丢过。
//
// 两种情形都算丢：
//
//  1. 订阅本身报告了 ErrGap（游标已跌出环形缓冲，或积压超过订阅缓冲）；
//  2. 第一条事件的序号**没有紧接**在游标之后。
//
// 第二种是必要的补充：环形缓冲只保留固定条数，而订阅缓冲是 256 条 ——
// 一次"前缀变化触发的几十条记录更新"就足以让某一次拉取跨越一段空洞，
// 而那次订阅本身可能完全没有报错。
func eventsGap(sub *event.Subscription, events []gen.KernelEvent, since int64) bool {
	var gap *event.ErrGap
	if errors.As(sub.Err(), &gap) {
		return true
	}
	return len(events) > 0 && events[0].Seq > since+1
}

// toGenKernelEvent 把内核事件转成契约类型。
func toGenKernelEvent(ev event.Event) gen.KernelEvent {
	out := gen.KernelEvent{
		Seq:  ev.Seq,
		Ts:   ev.TS,
		Type: ev.Type,
	}
	if len(ev.Payload) > 0 {
		var payload map[string]any
		// 载荷解不出来就**不带载荷**返回，而不是让整个请求失败：
		// 事件的类型与序号仍然有用，而客户端拿到一个它不认识的
		// 载荷时本来就会忽略它。
		if err := json.Unmarshal(ev.Payload, &payload); err == nil {
			out.Payload = &payload
		}
	}
	return out
}
