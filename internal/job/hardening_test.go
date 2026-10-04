package job

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// newTestBus 构造一个测试用事件总线。
func newTestBus(t *testing.T) *event.Bus {
	t.Helper()
	bus := event.NewBus(1000)
	t.Cleanup(bus.Close)
	return bus
}

// testLogger 返回一个丢弃全部输出的 logger。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- 任务类型登记 -----------------------------------------------------------

func TestRegisterKindsRejectsUnknownKinds(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	e.RegisterKinds("known.one", "known.two")

	if _, err := e.Submit(context.Background(), "typo.onee", func(context.Context, Reporter) (any, error) {
		return nil, nil
	}); err == nil {
		t.Fatalf("an unregistered kind must be rejected once kinds are registered")
	}

	if _, err := e.Submit(context.Background(), "known.one", func(context.Context, Reporter) (any, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("a registered kind must be accepted: %v", err)
	}
}

// 未登记任何 kind 时保持宽松：库的使用者与单元测试不必先注册。
func TestSubmitWithoutRegistrationAcceptsAnyKind(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	if _, err := e.Submit(context.Background(), "anything", func(context.Context, Reporter) (any, error) {
		return nil, nil
	}); err != nil {
		t.Fatalf("submission must stay permissive when nothing is registered: %v", err)
	}
}

// --- 并发上限 ---------------------------------------------------------------

// 一次给多个预设准备运行时会同时提交好几个重任务；没有上限就是
// 几份并发的大流量下载加编译，结果是谁都慢。
//
// 断言的是"同时最多跑一个"，**不是**"先提交的先跑"：抢槽位靠通道发送，
// 而 goroutine 的调度顺序不是提交顺序（见 Engine.sem 的说明）。
func TestMaxConcurrentSerialisesExecution(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	e.SetMaxConcurrent(1)

	var running int32
	var peak int32
	release := make(chan struct{})

	body := func(ctx context.Context, _ Reporter) (any, error) {
		now := atomic.AddInt32(&running, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if now <= old || atomic.CompareAndSwapInt32(&peak, old, now) {
				break
			}
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		atomic.AddInt32(&running, -1)
		return nil, nil
	}

	first, err := e.Submit(context.Background(), "test.serial", body)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.Submit(context.Background(), "test.serial", body)
	if err != nil {
		t.Fatal(err)
	}

	// 等到其中一个真的在跑为止。
	waitForEitherRunning(t, e, first.ID, second.ID)

	// 另一个必须仍在排队 —— 而不是也开始执行。
	time.Sleep(120 * time.Millisecond)
	if got := atomic.LoadInt32(&peak); got != 1 {
		t.Fatalf("expected at most one job executing, observed %d", got)
	}
	queuedID, runningID := second.ID, first.ID
	if statusOf(t, e, first.ID) != StatusRunning {
		queuedID, runningID = first.ID, second.ID
	}
	if got := statusOf(t, e, queuedID); got != StatusPending {
		t.Fatalf("the queued job should be pending, got %s", got)
	}
	if got := statusOf(t, e, runningID); got != StatusRunning {
		t.Fatalf("exactly one job should be running, got %s", got)
	}

	close(release)
	waitStatus(t, e, first.ID, StatusSucceeded)
	waitStatus(t, e, second.ID, StatusSucceeded)
}

func statusOf(t *testing.T, e *Engine, id string) Status {
	t.Helper()
	j, found, err := e.Get(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("get %s: found=%v err=%v", id, found, err)
	}
	return j.Status
}

// waitForEitherRunning 阻塞到两个任务之一进入 running。
func waitForEitherRunning(t *testing.T, e *Engine, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, id := range ids {
			if statusOf(t, e, id) == StatusRunning {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no job started running within the deadline")
}

// 排队期间被取消的任务不该真的跑起来：用户取消的是"还没开始的任务"，
// 让任务体执行一遍再做副作用是白做。
func TestCancelWhileQueuedNeverRunsTheBody(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	e.SetMaxConcurrent(1)

	var secondRan atomic.Bool
	block := make(chan struct{})
	first, err := e.Submit(context.Background(), "test.block", func(ctx context.Context, _ Reporter) (any, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, first.ID, StatusRunning)

	second, err := e.Submit(context.Background(), "test.queued", func(context.Context, Reporter) (any, error) {
		secondRan.Store(true)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Cancel(context.Background(), second.ID); err != nil {
		t.Fatalf("cancel a queued job: %v", err)
	}
	waitStatus(t, e, second.ID, StatusCanceled)

	close(block)
	waitStatus(t, e, first.ID, StatusSucceeded)
	time.Sleep(80 * time.Millisecond)
	if secondRan.Load() {
		t.Fatalf("a job cancelled while queued must never execute its body")
	}
}

// --- 重启归位 ---------------------------------------------------------------

// 任务状态在 SQLite 里，任务体是进程内的 goroutine。内核退出后那些行会
// 永远停在 running —— 界面显示"正在执行"，而实际什么都没在跑。
func TestRecoverInterruptedMarksStaleJobs(t *testing.T) {
	t.Parallel()
	store := NewMemoryStore()
	bus := newTestBus(t)
	ctx := context.Background()

	// 造出"上次运行遗留"的行：一个 running、一个 pending。
	stale := []Job{
		{ID: "stale-running", Kind: "app.deploy", Status: StatusRunning, Progress: 0.4, CreatedAt: time.Now().UTC()},
		{ID: "stale-pending", Kind: "runtime.provision", Status: StatusPending, CreatedAt: time.Now().UTC()},
		{ID: "done", Kind: "app.deploy", Status: StatusSucceeded, Progress: 1, CreatedAt: time.Now().UTC()},
	}
	for _, j := range stale {
		if err := store.Save(ctx, j); err != nil {
			t.Fatal(err)
		}
	}

	e := NewEngine(ctx, bus, store, testLogger())
	sub, err := bus.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()

	recovered, err := e.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 2 {
		t.Fatalf("expected 2 jobs recovered, got %d", recovered)
	}

	for _, id := range []string{"stale-running", "stale-pending"} {
		j, found, err := e.Get(ctx, id)
		if err != nil || !found {
			t.Fatalf("get %s: found=%v err=%v", id, found, err)
		}
		if j.Status != StatusFailed {
			t.Fatalf("%s should be failed, got %s", id, j.Status)
		}
		if j.Err == nil || j.Err.Code != CodeInterrupted {
			t.Fatalf("%s should carry %s, got %#v", id, CodeInterrupted, j.Err)
		}
		if j.FinishedAt == nil {
			t.Fatalf("%s must get a finish time so it stops looking in-flight", id)
		}
	}

	// 已结束的任务不受影响。
	done, _, err := e.Get(ctx, "done")
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusSucceeded {
		t.Fatalf("a finished job must not be rewritten, got %s", done.Status)
	}

	// 订阅者要能像对待任何一次失败那样收到通知。
	deadline := time.After(2 * time.Second)
	seen := 0
	for seen < 2 {
		select {
		case ev := <-sub.C():
			if ev.Type == "job.finished" {
				seen++
			}
		case <-deadline:
			t.Fatalf("expected 2 job.finished events, saw %d", seen)
		}
	}

	// 再跑一次是幂等的：已经没有滞留行了。
	again, err := e.RecoverInterrupted(ctx)
	if err != nil || again != 0 {
		t.Fatalf("recovery must be idempotent, got %d err=%v", again, err)
	}
}

func TestRecoverInterruptedOnACleanStoreIsANoOp(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	n, err := e.RecoverInterrupted(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("expected a no-op, got %d err=%v", n, err)
	}
}

// SetMaxConcurrent 必须在第一个 Submit 之前调用；这里顺便锁住
// "小于 1 会被夹到 1"这条边界，否则 0 会让信号量容量为 0、任务永远排队。
func TestSetMaxConcurrentClampsToAtLeastOne(t *testing.T) {
	t.Parallel()
	e, _ := newTestEngine(t, context.Background())
	e.SetMaxConcurrent(0)
	j, err := e.Submit(context.Background(), "test.clamp", func(context.Context, Reporter) (any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, j.ID, StatusSucceeded)
}
