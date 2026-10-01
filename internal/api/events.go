package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/event"
)

const (
	// wsWriteTimeout 是单次写入 WebSocket 的超时。
	//
	// 必须有：客户端半死不活（例如笔记本休眠后网络切换）时，
	// 没有超时的写会永久阻塞住 goroutine。
	wsWriteTimeout = 10 * time.Second

	// wsPingInterval 是心跳间隔，用于及时探测断开的客户端。
	wsPingInterval = 30 * time.Second

	// wsReadLimit 是允许客户端发来的最大消息字节数。
	//
	// 事件流是**服务端单向推送**，客户端不需要发任何数据。
	// 设一个很小的上限，避免恶意客户端用超大消息消耗内存。
	wsReadLimit = 1024
)

// gapPayload 是 events.gap 事件的载荷。
type gapPayload struct {
	Requested int64  `json:"requested"`
	Earliest  int64  `json:"earliest"`
	Latest    int64  `json:"latest"`
	Message   string `json:"message"`
}

// SubscribeEvents 实现 GET /v1/events。
//
// 流程要点：
//
//  1. **先订阅、后升级**。反过来的话，"订阅成功"与"开始推送"之间产生的
//     事件会永久丢失，而客户端以为自己已经连上了 —— 这类丢失最难排查。
//  2. 若客户端请求的 lastEventId 已滑出保留窗口，仍然完成升级，然后推一条
//     events.gap 再关闭。不直接返回 4xx，是因为客户端此时期望的是
//     WebSocket 握手成功，一个 HTTP 错误会让它无法区分"鉴权失败"与"需要重新同步"。
//  3. 订阅因消费过慢被总线断开时，用 StatusTryAgainLater 关闭连接，
//     明确告诉客户端"重连并补发"，而不是假装正常结束。
func (s *Server) SubscribeEvents(w http.ResponseWriter, r *http.Request, params gen.SubscribeEventsParams) {
	var after int64
	if params.LastEventId != nil {
		after = *params.LastEventId
	}

	sub, subErr := s.Bus.Subscribe(after)
	if sub != nil {
		defer sub.Close()
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 同源检查由库完成：浏览器从控制台页面发起时 Origin 与 Host 一致；
		// CLI 与原生 GUI 不带 Origin，直接放行。
		// 注意鉴权已经在中间件里完成，这里是第二道防线而非唯一防线。
		OriginPatterns: s.AllowedOrigins,
		// 回显浏览器通过子协议携带的令牌子协议。不回显的话，
		// 浏览器会判定握手失败并立刻关闭连接。
		Subprotocols:    selectedTokenSubprotocol(r),
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		// Accept 已经接管了响应写入，此时无法再写 problem+json，
		// 只能记日志。库自身会向客户端发送合适的握手失败响应。
		s.Log.Warn("事件流握手失败", "err", err, "remote", r.RemoteAddr)
		return
	}
	defer conn.CloseNow() //nolint:errcheck // 关闭失败无可挽回

	conn.SetReadLimit(wsReadLimit)

	if subErr != nil {
		s.sendGapAndClose(conn, subErr)
		return
	}

	s.pumpEvents(conn, sub)
}

// sendGapAndClose 推送一条 events.gap 事件后关闭连接。
func (s *Server) sendGapAndClose(conn *websocket.Conn, subErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()

	var gapErr *event.ErrGap
	payload := gapPayload{Message: subErr.Error()}
	if errors.As(subErr, &gapErr) {
		payload.Requested = gapErr.Requested
		payload.Earliest = gapErr.Earliest
		payload.Latest = gapErr.Latest
	}

	// Seq 刻意留 0：gap 事件不属于带序号的事件流本身，
	// 给它一个真实序号会让客户端误以为"已经收到到 seq 了"。
	// 客户端应当依据 payload.latest 重新拉取全量状态。
	ev := event.Event{
		Type: event.TypeEventsGap,
		TS:   time.Now().UTC(),
	}
	if raw, err := json.Marshal(payload); err == nil {
		ev.Payload = raw
	}

	if err := wsjson.Write(ctx, conn, ev); err != nil {
		s.Log.Debug("推送 events.gap 失败", "err", err)
	}
	_ = conn.Close(websocket.StatusPolicyViolation, "event stream gap")
}

// pumpEvents 把订阅到的事件逐条推给客户端，直到任一方断开。
func (s *Server) pumpEvents(conn *websocket.Conn, sub *event.Subscription) {
	// CloseRead 返回的 ctx 会在客户端关闭连接（或被探测为断开）时取消。
	// 事件流是单向的，因此读侧的唯一用途就是探测断开。
	ctx := conn.CloseRead(context.Background())

	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			err := conn.Ping(pingCtx)
			cancel()
			if err != nil {
				s.Log.Debug("事件流心跳失败，关闭连接", "err", err)
				return
			}

		case ev, ok := <-sub.C():
			if !ok {
				s.closeOnSubscriptionEnd(conn, sub)
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, wsWriteTimeout)
			err := wsjson.Write(writeCtx, conn, ev)
			cancel()
			if err != nil {
				// 客户端断开是最常见的原因，不值得记为错误。
				s.Log.Debug("事件流写入失败，关闭连接", "err", err, "seq", ev.Seq)
				return
			}
		}
	}
}

// closeOnSubscriptionEnd 在订阅结束时给出恰当的关闭码。
func (s *Server) closeOnSubscriptionEnd(conn *websocket.Conn, sub *event.Subscription) {
	if err := sub.Err(); err != nil {
		// 消费过慢被断开：告诉客户端"稍后重试"并带上 lastEventId 重连，
		// 它会走补发路径。用 TryAgainLater 而不是 NormalClosure，
		// 是为了让客户端的前端逻辑能区分"正常结束"与"需要重连"。
		s.Log.Warn("事件流订阅被断开", "err", err)
		_ = conn.Close(websocket.StatusTryAgainLater, err.Error())
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}
