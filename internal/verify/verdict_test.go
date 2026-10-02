package verify

import (
	"context"
	"testing"
	"time"
)

// 本文件覆盖**外部验证结论的形成与保鲜**。
//
// # 它闭合的那条链路
//
// M3 的验收标准里写着「故意封掉端口后，isc doctor 能明确指出是运营商侧
// 封禁而非本机问题」。而在此之前那条链路是断的：CheckBlocked 这个状态
// 定义了、注释齐全、doctor 里有完整的「上游挡住了」结论分支 ——
// **但没有任何代码会设置它**。
//
// 于是那个分支永远不会被走到，而用户拿到的永远是那句"请用手机确认"。

// ---------------------------------------------------------------------------
// 结论的形成
// ---------------------------------------------------------------------------

// TestVerdictFromReachable 验证收到公网访问时形成"通"的结论。
func TestVerdictFromReachable(t *testing.T) {
	t.Parallel()

	v, ok := verdictFromSession(Session{
		Status: StatusReachable, Port: 8443,
	})
	if !ok {
		t.Fatal("终态应当产生结论")
	}
	if !v.Reachable || v.Blocked {
		t.Errorf("结论不对: %+v", v)
	}
	if v.Port != 8443 {
		t.Errorf("端口 = %d，期望 8443", v.Port)
	}
}

// TestVerdictFromUnreachableIsBlocked 是这里最要紧的一条。
//
// "超时且一次访问都没收到"就是上游挡住的证据 —— 而它与"没验过"
// 是两回事。
func TestVerdictFromUnreachableIsBlocked(t *testing.T) {
	t.Parallel()

	v, ok := verdictFromSession(Session{
		Status: StatusUnreachable, Port: 8443,
	})
	if !ok {
		t.Fatal("终态应当产生结论")
	}
	if !v.Blocked || v.Reachable {
		t.Errorf("超时且无访问应当判定为被上游挡住: %+v", v)
	}
	// 说明里必须点出**为什么**能得出这个结论 ——
	// 那是用户唯一能据此行动的线索。
	if v.Detail == "" {
		t.Error("缺少说明")
	}
}

// TestVerdictFromHairpinOnlyIsNoVerdict 钉住一个刻意的设计。
//
// "只收到本机/内网的访问"说明**不了**上游通不通 —— 用户可能只是用
// WiFi 而不是 4G 打开的。把它当成"被封了"是一个**错误的诊断**，
// 而错误的诊断比没有诊断更糟：它会引导用户去联系运营商，
// 而真正的问题是他自己的验证方式不对。
func TestVerdictFromHairpinOnlyIsNoVerdict(t *testing.T) {
	t.Parallel()

	if _, ok := verdictFromSession(Session{Status: StatusHairpinOnly}); ok {
		t.Error("只收到回环访问时不该产生任何结论")
	}
}

// TestVerdictFromPendingIsNoVerdict 验证未完成的会话不产生结论。
func TestVerdictFromPendingIsNoVerdict(t *testing.T) {
	t.Parallel()

	for _, st := range []Status{StatusWaiting, StatusStopped, ""} {
		if _, ok := verdictFromSession(Session{Status: st}); ok {
			t.Errorf("状态 %q 不该产生结论", st)
		}
	}
}

// ---------------------------------------------------------------------------
// 结论的保鲜
// ---------------------------------------------------------------------------

// TestLastVerdictExpires 钉住结论**不能永不过期**。
//
// 用户今天验出来"被运营商封了"，下周换了宽带或运营商改了策略之后，
// 那个结论就不再成立了。让它长期留着会让 doctor 给出一个基于
// 上周情况的错误结论。
func TestLastVerdictExpires(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)

	// 一个刚好在有效期内、一个已经过期的结论。
	m.SetLastVerdict(Verdict{
		Blocked: true,
		At:      time.Now().Add(-VerdictTTL / 2),
	})
	if _, ok := m.LastVerdict(); !ok {
		t.Error("有效期内的结论应当可用")
	}

	m.SetLastVerdict(Verdict{
		Blocked: true,
		At:      time.Now().Add(-VerdictTTL - time.Minute),
	})
	if _, ok := m.LastVerdict(); ok {
		t.Error("过期的结论不该可用 —— 它会基于上周的情况给出结论")
	}

	// 过期之后应当被清掉，而不是每次都要重新判断。
	m.SetLastVerdict(Verdict{At: time.Now().Add(-VerdictTTL - time.Hour)})
	_, _ = m.LastVerdict()
	m.mu.Lock()
	left := m.lastVerdict
	m.mu.Unlock()
	if left != nil {
		t.Error("过期的结论应当被清掉")
	}
}

func TestLastVerdictWhenNone(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)
	if _, ok := m.LastVerdict(); ok {
		t.Error("没有验过时不该有结论")
	}
}

// TestVerdictSinkReceivesResult 验证结论被送出。
//
// 落点做成回调而不是让 verify 直接依赖 reach：依赖方向应当是
// "上层把它们接起来"，那样任何一个模块都能单独测试。
func TestVerdictSinkReceivesResult(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)

	var got []Verdict
	m.SetVerdictSink(func(v Verdict) { got = append(got, v) })

	// 手工触发一次发布（正常路径由会话状态变更触发）。
	sess := &Session{Status: StatusUnreachable, Port: 443}
	m.publishVerdict(sess)

	if len(got) != 1 {
		t.Fatalf("应当收到 1 个结论，得到 %d", len(got))
	}
	if !got[0].Blocked {
		t.Errorf("结论不对: %+v", got[0])
	}
}

// TestPublishVerdictSkipsNonTerminal 验证非终态不送出结论。
func TestPublishVerdictSkipsNonTerminal(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)

	var count int
	m.SetVerdictSink(func(Verdict) { count++ })

	for _, st := range []Status{StatusWaiting, StatusStopped, StatusHairpinOnly, ""} {
		m.publishVerdict(&Session{Status: st})
	}
	if count != 0 {
		t.Errorf("非终态不该送出结论，送出了 %d 次", count)
	}
}

// TestPublishVerdictWithoutSink 验证没有落点时不会崩。
//
// 内核在某些装配路径下可能不设落点（例如只跑 verify 的单元测试），
// 而那时 panic 会让整个验证流程挂掉。
func TestPublishVerdictWithoutSink(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)
	// 不设 sink，直接调用。
	m.publishVerdict(&Session{Status: StatusUnreachable})
}

func TestVerdictTTLIsBounded(t *testing.T) {
	t.Parallel()

	// 太短会让"验一次、反复跑 doctor"这个用法失效；
	// 太长会让过期的结论长期误导人。
	if VerdictTTL < time.Hour {
		t.Errorf("结论有效期 %v 太短", VerdictTTL)
	}
	if VerdictTTL > 7*24*time.Hour {
		t.Errorf("结论有效期 %v 太长 —— 过期的结论会误导人", VerdictTTL)
	}
}

// ---------------------------------------------------------------------------
// 死锁回归
// ---------------------------------------------------------------------------

// TestVerdictSinkCanCallBackIntoManager 钉住一个真机上才暴露的死锁。
//
// # 它抓的是什么
//
// expire 曾经在**持有 m.mu** 的情况下 defer 了一个 publishVerdict。
// 而落点回调到管理器自己（这正是守护进程里的装配方式：
// SetVerdictSink(func(v){ mgr.SetLastVerdict(v) })）时要拿同一把锁 ——
// Go 的 sync.Mutex 不可重入，于是**整个守护进程死锁**。
//
// 症状出现得很晚：验证会话**到期的那一刻**（默认五分钟后）。
// 在那之前一切正常，而到点之后内核既不写日志也不响应任何请求。
//
// 这个测试用一把带超时的锁来检测它 —— 死锁时它会失败而不是挂住
// 整个测试进程。
func TestVerdictSinkCanCallBackIntoManager(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)

	// 与守护进程里完全一样的装配方式：落点回调到管理器自己。
	m.SetVerdictSink(func(v Verdict) {
		m.SetLastVerdict(v)
	})

	m.mu.Lock()
	m.sessions["s1"] = &Session{
		ID: "s1", Port: 8443, Status: StatusWaiting,
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.expire("s1")
		close(done)
	}()

	select {
	case <-done:
		// 正常返回。
	case <-time.After(3 * time.Second):
		t.Fatal("expire 死锁了 —— 落点在持锁时被调用。" +
			"守护进程会因此整个卡住，且不写任何日志")
	}

	// 结论应当已经落到管理器上。
	v, ok := m.LastVerdict()
	if !ok {
		t.Fatal("到期后应当产生结论")
	}
	if !v.Blocked {
		t.Errorf("到期且无访问应当判定为被上游挡住: %+v", v)
	}
}

// TestSweepDoesNotDeadlock 验证清理路径上的同一类问题。
//
// sweepLocked 是在 Start 的**开头**、持有锁时被调用的，因此它同样
// 不能在持锁时发布结论。
func TestSweepDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	m := NewManager(func(context.Context) string { return "" }, nil)
	m.SetVerdictSink(func(v Verdict) { m.SetLastVerdict(v) })

	// 造一个已经过期的等待中会话。
	m.mu.Lock()
	m.sessions["old"] = &Session{
		ID: "old", Port: 8443, Status: StatusWaiting,
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		m.mu.Lock()
		m.sweepLocked()
		m.mu.Unlock()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("sweepLocked 死锁了")
	}

	// 结论是通过 goroutine 发布的，给它一点时间。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := m.LastVerdict(); ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("清理过期会话时应当产生结论")
}
