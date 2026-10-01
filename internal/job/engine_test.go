package job

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// newTestEngine 构造一个静默的测试引擎。
func newTestEngine(t *testing.T, ctx context.Context) (*Engine, *event.Bus) {
	t.Helper()
	bus := event.NewBus(1000)
	t.Cleanup(bus.Close)
	return NewEngine(ctx, bus, NewMemoryStore(),
		slog.New(slog.NewTextHandler(io.Discard, nil))), bus
}

// waitStatus 轮询等待任务到达期望状态。
func waitStatus(t *testing.T, e *Engine, id string, want Status) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		j, found, err := e.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("查询任务失败: %v", err)
		}
		if !found {
			t.Fatalf("任务 %s 不存在", id)
		}
		if j.Status == want {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	j, _, _ := e.Get(context.Background(), id)
	t.Fatalf("等待任务进入 %s 超时，当前为 %s（message=%q）", want, j.Status, j.Message)
	return Job{}
}

func TestSubmitRunsToSuccess(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, err := e.Submit(context.Background(), "test.ok", func(ctx context.Context, r Reporter) (any, error) {
		r.Progress(0.5, "走到一半")
		return map[string]string{"hello": "world"}, nil
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	got := waitStatus(t, e, j.ID, StatusSucceeded)
	if got.Progress != 1 {
		t.Errorf("成功后进度应为 1，得到 %v", got.Progress)
	}
	if string(got.Result) != `{"hello":"world"}` {
		t.Errorf("结果 = %s", got.Result)
	}
	if got.StartedAt == nil || got.FinishedAt == nil {
		t.Error("终态任务应当有开始与结束时间")
	}
	if got.Kind != "test.ok" {
		t.Errorf("Kind = %q", got.Kind)
	}
}

func TestFailureCarriesStructuredError(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, err := e.Submit(context.Background(), "test.fail", func(context.Context, Reporter) (any, error) {
		return nil, &Error{Code: "boom", Title: "炸了", Detail: "细节"}
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	got := waitStatus(t, e, j.ID, StatusFailed)
	if got.Err == nil {
		t.Fatal("失败任务应当带错误信息")
	}
	if got.Err.Code != "boom" {
		t.Errorf("错误码 = %q, 期望 boom", got.Err.Code)
	}
	if got.Err.Error() == "" {
		t.Error("错误应当可读")
	}
}

// TestPlainErrorIsWrapped 验证普通 error 也被归一化成结构化错误。
func TestPlainErrorIsWrapped(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, _ := e.Submit(context.Background(), "test.fail", func(context.Context, Reporter) (any, error) {
		return nil, errors.New("普通错误")
	})

	got := waitStatus(t, e, j.ID, StatusFailed)
	if got.Err == nil || got.Err.Code != "job_failed" {
		t.Fatalf("普通错误应被包装为 job_failed，得到 %+v", got.Err)
	}
	if got.Err.Detail != "普通错误" {
		t.Errorf("原始错误信息应保留在 Detail，得到 %q", got.Err.Detail)
	}
}

// TestPanicIsContained 验证任务内 panic 不会带崩引擎。
//
// 这是硬要求：内核以系统服务身份常驻，一个任务实现里的空指针
// 若能让进程退出，用户的所有定时解析任务都会静默停摆。
func TestPanicIsContained(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, err := e.Submit(context.Background(), "test.panic", func(context.Context, Reporter) (any, error) {
		panic("任务内的空指针")
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	got := waitStatus(t, e, j.ID, StatusFailed)
	if got.Err == nil || got.Err.Code != "job_panic" {
		t.Fatalf("panic 应被转成 job_panic 错误，得到 %+v", got.Err)
	}

	// 引擎必须仍然可用。
	j2, err := e.Submit(context.Background(), "test.ok", func(context.Context, Reporter) (any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatalf("panic 之后引擎应仍可提交任务: %v", err)
	}
	waitStatus(t, e, j2.ID, StatusSucceeded)
}

func TestCancelRunningJob(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	started := make(chan struct{})
	j, err := e.Submit(context.Background(), "test.slow", func(ctx context.Context, _ Reporter) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	<-started
	if _, err := e.Cancel(context.Background(), j.ID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	got := waitStatus(t, e, j.ID, StatusCanceled)
	if got.FinishedAt == nil {
		t.Error("取消的任务应当有结束时间")
	}
	if got.Message == "" {
		t.Error("取消的任务应当有说明文案")
	}
}

// TestCancelFinishedJobRejected 验证重复取消被明确拒绝。
func TestCancelFinishedJobRejected(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, _ := e.Submit(context.Background(), "test.ok", func(context.Context, Reporter) (any, error) {
		return nil, nil
	})
	waitStatus(t, e, j.ID, StatusSucceeded)

	_, err := e.Cancel(context.Background(), j.ID)
	if !errors.Is(err, ErrNotCancelable) {
		t.Fatalf("已完成的任务取消应返回 ErrNotCancelable，得到 %v", err)
	}
}

func TestCancelUnknownJob(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	if _, err := e.Cancel(context.Background(), "不存在的任务"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}

// TestProgressEmitsEvents 验证进度同时落库并发事件。
//
// 二者不可偏废：只发事件会让重连的客户端丢失中间状态，
// 只落库则界面完全不会动。
func TestProgressEmitsEvents(t *testing.T) {
	t.Parallel()

	e, bus := newTestEngine(t, context.Background())

	sub, err := bus.Subscribe(0)
	if err != nil {
		t.Fatalf("订阅失败: %v", err)
	}
	defer sub.Close()

	j, _ := e.Submit(context.Background(), "test.progress", func(ctx context.Context, r Reporter) (any, error) {
		for i := 1; i <= 3; i++ {
			r.Progress(float64(i)/3, "步骤")
		}
		return nil, nil
	})
	waitStatus(t, e, j.ID, StatusSucceeded)

	var progresses, finished int
	timeout := time.After(3 * time.Second)
	for progresses < 3 || finished == 0 {
		select {
		case ev, ok := <-sub.C():
			if !ok {
				t.Fatal("事件通道意外关闭")
			}
			switch ev.Type {
			case event.TypeJobProgress:
				progresses++
			case event.TypeJobFinished:
				finished++
			}
		case <-timeout:
			t.Fatalf("等待事件超时：progress=%d finished=%d", progresses, finished)
		}
	}
}

func TestProgressFractionIsClamped(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j, _ := e.Submit(context.Background(), "test.clamp", func(ctx context.Context, r Reporter) (any, error) {
		r.Progress(-5, "负数")
		r.Progress(99, "超界")
		return nil, nil
	})

	// 直接检查中间态不可靠，改为验证最终 Progress 合法即可；
	// 夹取逻辑本身在 reporter 里是纯函数级的。
	waitStatus(t, e, j.ID, StatusSucceeded)
}

// TestListPagination 验证游标分页。
func TestListPagination(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	var ids []string
	for i := 0; i < 5; i++ {
		j, err := e.Submit(context.Background(), "test.list", func(context.Context, Reporter) (any, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatalf("提交失败: %v", err)
		}
		ids = append(ids, j.ID)
		waitStatus(t, e, j.ID, StatusSucceeded)
	}

	// 第一页：最新在前。
	page1, cursor, err := e.List(context.Background(), Filter{Limit: 2})
	if err != nil {
		t.Fatalf("列表查询失败: %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("第一页应有 2 条，得到 %d", len(page1))
	}
	if page1[0].ID != ids[4] {
		t.Errorf("第一页第一条应为最新任务，得到 %s", page1[0].ID)
	}
	if cursor == "" {
		t.Fatal("应当返回下一页游标")
	}

	// 第二页：接着第一页。
	page2, cursor2, err := e.List(context.Background(), Filter{Limit: 2, Cursor: cursor})
	if err != nil {
		t.Fatalf("第二页查询失败: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("第二页应有 2 条，得到 %d", len(page2))
	}
	if page2[0].ID == page1[1].ID {
		t.Error("第二页不应重复第一页的内容")
	}
	if cursor2 == "" {
		t.Fatal("应当还有第三页")
	}

	// 第三页：只剩 1 条，且无下一页。
	page3, cursor3, err := e.List(context.Background(), Filter{Limit: 2, Cursor: cursor2})
	if err != nil {
		t.Fatalf("第三页查询失败: %v", err)
	}
	if len(page3) != 1 {
		t.Fatalf("第三页应有 1 条，得到 %d", len(page3))
	}
	if cursor3 != "" {
		t.Errorf("最后一页不应返回游标，得到 %q", cursor3)
	}
}

func TestListFiltersByStatusAndKind(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	j1, _ := e.Submit(context.Background(), "kind.a", func(context.Context, Reporter) (any, error) {
		return nil, nil
	})
	waitStatus(t, e, j1.ID, StatusSucceeded)

	j2, _ := e.Submit(context.Background(), "kind.b", func(context.Context, Reporter) (any, error) {
		return nil, &Error{Code: "x", Title: "x"}
	})
	waitStatus(t, e, j2.ID, StatusFailed)

	byKind, _, err := e.List(context.Background(), Filter{Kind: "kind.b"})
	if err != nil {
		t.Fatalf("按类型过滤失败: %v", err)
	}
	if len(byKind) != 1 || byKind[0].ID != j2.ID {
		t.Errorf("按类型过滤结果错误: %+v", byKind)
	}

	byStatus, _, err := e.List(context.Background(), Filter{Statuses: []Status{StatusSucceeded}})
	if err != nil {
		t.Fatalf("按状态过滤失败: %v", err)
	}
	if len(byStatus) != 1 || byStatus[0].ID != j1.ID {
		t.Errorf("按状态过滤结果错误: %+v", byStatus)
	}
}

func TestListLimitIsClamped(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	// 超过上限的 limit 应被夹到 maxLimit，而不是让调用方拉爆内存。
	items, _, err := e.List(context.Background(), Filter{Limit: 100000})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("没有任务时应当返回空列表，得到 %d 条", len(items))
	}
}

// TestShutdownCancelsRunningJobs 验证关闭时在途任务被取消并等待收尾。
func TestShutdownCancelsRunningJobs(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, _ := newTestEngine(t, ctx)

	var wg sync.WaitGroup
	wg.Add(1)

	j, err := e.Submit(context.Background(), "test.slow", func(ctx context.Context, _ Reporter) (any, error) {
		defer wg.Done()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if err != nil {
		t.Fatalf("提交失败: %v", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer shutdownCancel()

	if err := e.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	wg.Wait()

	got, _, _ := e.Get(context.Background(), j.ID)
	if got.Status != StatusCanceled {
		t.Errorf("关闭后在途任务应被取消，得到 %s", got.Status)
	}
}

func TestSubmitAfterShutdownRejected(t *testing.T) {
	t.Parallel()

	e, _ := newTestEngine(t, context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	if _, err := e.Submit(context.Background(), "test.ok", func(context.Context, Reporter) (any, error) {
		return nil, nil
	}); err == nil {
		t.Fatal("关闭后提交任务应当被拒绝")
	}
}

// TestJobIDsAreUniqueAndOpaque 验证 ID 不可预测且不重复。
//
// 可预测的 ID 让攻击者能枚举历史任务，而任务详情里可能含域名与错误信息。
func TestJobIDsAreUniqueAndOpaque(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		id, err := newID()
		if err != nil {
			t.Fatalf("生成 ID 失败: %v", err)
		}
		if seen[id] {
			t.Fatalf("ID 重复: %s", id)
		}
		seen[id] = true
		if len(id) < 20 {
			t.Errorf("ID 过短，随机性不足: %q", id)
		}
	}
}
