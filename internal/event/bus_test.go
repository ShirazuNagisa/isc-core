package event

import (
	"errors"
	"testing"
	"time"
)

// TestPublishAssignsMonotonicSeq 钉住序号语义。
//
// 序号是断线补发的唯一依据，一旦不单调或多播共享计数，
// 客户端的补发逻辑会静默错位 —— 这类 bug 在界面上表现为
// "偶尔少一条更新"，极难排查。
func TestPublishAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	for i := 1; i <= 5; i++ {
		ev := bus.Publish("test.event", map[string]int{"i": i})
		if ev.Seq != int64(i) {
			t.Fatalf("第 %d 条事件的 Seq = %d, 期望 %d", i, ev.Seq, i)
		}
	}
	if got := bus.LatestSeq(); got != 5 {
		t.Errorf("LatestSeq = %d, 期望 5", got)
	}
	if bus.EarliestSeq() != 1 {
		t.Errorf("EarliestSeq = %d, 期望 1", bus.EarliestSeq())
	}
}

// TestRingEvictsOldest 验证环形缓冲按容量淘汰最旧事件。
func TestRingEvictsOldest(t *testing.T) {
	t.Parallel()

	bus := NewBus(3)
	defer bus.Close()

	for i := 0; i < 5; i++ {
		bus.Publish("test.event", nil)
	}

	if got := bus.EarliestSeq(); got != 3 {
		t.Errorf("EarliestSeq = %d, 期望 3（前两条已被淘汰）", got)
	}
	if got := bus.LatestSeq(); got != 5 {
		t.Errorf("LatestSeq = %d, 期望 5", got)
	}
}

// TestSubscribeReplaysFromSeq 验证断线补发。
func TestSubscribeReplaysFromSeq(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	for i := 0; i < 4; i++ {
		bus.Publish("test.event", nil)
	}

	sub, err := bus.Subscribe(2)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer sub.Close()

	// 期望补发 seq 3、4
	for _, want := range []int64{3, 4} {
		select {
		case ev := <-sub.C():
			if ev.Seq != want {
				t.Fatalf("补发事件的 Seq = %d, 期望 %d", ev.Seq, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("等待补发 seq=%d 超时", want)
		}
	}

	// 之后新发布的事件应当继续送达，序号保持连续。
	bus.Publish("test.event", nil)
	select {
	case ev := <-sub.C():
		if ev.Seq != 5 {
			t.Errorf("实时事件的 Seq = %d, 期望 5", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("等待实时事件超时")
	}
}

// TestSubscribeAfterZeroDoesNotReplay 验证 afterSeq=0 只收新事件。
//
// 语义设计见 Subscribe 的文档：客户端应当先拉全量状态再订阅，
// 回放历史会让"我刚看到的状态"与"回放出来的旧事件"互相打架。
func TestSubscribeAfterZeroDoesNotReplay(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	for i := 0; i < 3; i++ {
		bus.Publish("test.event", nil)
	}

	sub, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer sub.Close()

	select {
	case ev := <-sub.C():
		t.Fatalf("afterSeq=0 不应回放历史，却收到 seq=%d", ev.Seq)
	case <-time.After(50 * time.Millisecond):
	}

	bus.Publish("test.event", nil)
	select {
	case ev := <-sub.C():
		if ev.Seq != 4 {
			t.Errorf("实时事件 Seq = %d, 期望 4", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("等待实时事件超时")
	}
}

// TestSubscribeReturnsGapWhenSeqEvicted 验证补发链断裂时报 gap。
//
// 报 gap 而不是"尽力而为地补一部分"，是因为后者会让客户端
// 以为自己拥有完整状态，从而长期显示错误的数据。
func TestSubscribeReturnsGapWhenSeqEvicted(t *testing.T) {
	t.Parallel()

	bus := NewBus(3)
	defer bus.Close()

	for i := 0; i < 10; i++ {
		bus.Publish("test.event", nil)
	}

	_, err := bus.Subscribe(2)
	if err == nil {
		t.Fatal("期望返回 gap 错误，实际成功")
	}

	var gap *ErrGap
	if !errors.As(err, &gap) {
		t.Fatalf("错误类型应为 *ErrGap，得到 %T: %v", err, err)
	}
	if gap.Requested != 2 {
		t.Errorf("Requested = %d, 期望 2", gap.Requested)
	}
	if gap.Earliest != 8 {
		t.Errorf("Earliest = %d, 期望 8", gap.Earliest)
	}
	if gap.Latest != 10 {
		t.Errorf("Latest = %d, 期望 10", gap.Latest)
	}
	if gap.Error() == "" {
		t.Error("gap 错误应当有本地化消息")
	}
}

// TestSubscribeRejectsFutureSeq 验证不一致的客户端状态被识别。
func TestSubscribeRejectsFutureSeq(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	bus.Publish("test.event", nil)

	if _, err := bus.Subscribe(999); err == nil {
		t.Fatal("声称收到过不存在的事件序号，应当返回 gap")
	}
}

// TestSlowSubscriberIsDisconnected 验证慢订阅者被断开而不是拖住发布方。
//
// 这是本包最重要的行为保证：一个卡住的控制台页面
// 绝不能阻塞内核里其它子系统的事件发布。
func TestSlowSubscriberIsDisconnected(t *testing.T) {
	t.Parallel()

	bus := NewBus(1000)
	defer bus.Close()

	sub, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	// 不消费，持续发布直到超过订阅者缓冲。
	// 缓冲为 subscriberBuffer，多打一些确保溢出。
	for i := 0; i < subscriberBuffer+10; i++ {
		bus.Publish("test.event", nil)
	}

	// 通道应当已被关闭。
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-sub.C():
			if !ok {
				if sub.Err() == nil {
					t.Error("因消费过慢被断开时应当记录原因")
				}
				return
			}
		case <-deadline:
			t.Fatal("订阅者消费过慢却未被断开")
		}
	}
}

// TestCloseIsIdempotent 验证重复关闭不 panic。
func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	sub, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}

	sub.Close()
	sub.Close()
	bus.Close()
	bus.Close()

	// 关闭后通道应当已关闭，而不是永久阻塞。
	select {
	case _, ok := <-sub.C():
		if ok {
			t.Error("关闭后不应还能读到事件")
		}
	case <-time.After(time.Second):
		t.Fatal("关闭后通道未关闭，消费者会永久阻塞")
	}
}

// TestPublishAfterCloseIsSafe 验证关闭后发布不 panic。
func TestPublishAfterCloseIsSafe(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	bus.Close()

	ev := bus.Publish("test.event", nil)
	if ev.Seq != 0 {
		t.Errorf("关闭后发布应返回零值事件，得到 seq=%d", ev.Seq)
	}
}

// TestSubscribeAfterCloseFails 验证关闭后订阅被拒绝。
func TestSubscribeAfterCloseFails(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	bus.Close()

	if _, err := bus.Subscribe(0); err == nil {
		t.Fatal("总线已关闭时订阅应当报错")
	}
}

// TestPayloadIsJSON 验证载荷经过 JSON 序列化。
func TestPayloadIsJSON(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	ev := bus.Publish("test.event", struct {
		Name string `json:"name"`
	}{Name: "isc"})

	if string(ev.Payload) != `{"name":"isc"}` {
		t.Errorf("载荷 = %s", ev.Payload)
	}
}

// TestUnmarshalablePayloadKeepsEvent 验证载荷序列化失败时不丢事件。
//
// "事件发生过"这个事实比它的细节更重要：丢了整条事件，
// 客户端会以为什么都没发生。
func TestUnmarshalablePayloadKeepsEvent(t *testing.T) {
	t.Parallel()

	bus := NewBus(10)
	defer bus.Close()

	ev := bus.Publish("test.event", make(chan int))
	if ev.Seq == 0 {
		t.Error("载荷无法序列化时仍应产生事件")
	}
	if ev.Payload != nil {
		t.Errorf("无法序列化的载荷应为空，得到 %s", ev.Payload)
	}
}
