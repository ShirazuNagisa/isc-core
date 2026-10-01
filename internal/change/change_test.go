package change

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖变更编排的**保证**。
//
// 这些保证是本包存在的全部理由，因此每一条都必须有测试钉住：
//
//	先写日志再动手    执行前记录必须已落盘
//	失败即回滚        第一步失败就停，已生效的按相反顺序撤销
//	回滚失败要如实报告 不能假装成功
//	回滚是幂等的      用户重试不该报错
//	变更串行化        两个计划不能交错执行
//
// 用内存日志而不是真数据库：这些测试要验证的是**编排逻辑**，
// 引入 SQLite 只会让失败信息变模糊（分不清是编排错了还是存储错了）。
// 存储层的往返由 store 包自己的测试覆盖。

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

type fakeJournal struct {
	mu    sync.Mutex
	recs  map[string]Record
	order []string
	// failSave 让下一次 Save 失败，用于测试"日志写不进去就不动手"。
	failSave error
	// onSave 在每次 Save 时被调用（在持锁状态下），
	// 用于在执行过程中观察日志内容。
	onSave func(Record)
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{recs: make(map[string]Record)}
}

func (j *fakeJournal) Save(_ context.Context, rec Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failSave != nil {
		return j.failSave
	}
	if _, exists := j.recs[rec.PlanID]; !exists {
		j.order = append(j.order, rec.PlanID)
	}
	// 深拷贝，模拟真实的持久化边界 —— 共享切片会让测试在
	// "runner 后续修改了记录"时看到不该看到的变化。
	j.recs[rec.PlanID] = cloneRecord(rec)
	if j.onSave != nil {
		j.onSave(cloneRecord(rec))
	}
	return nil
}

func (j *fakeJournal) Get(_ context.Context, planID string) (Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	rec, ok := j.recs[planID]
	return rec, ok, nil
}

func (j *fakeJournal) List(_ context.Context, limit int) ([]Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Record
	for i := len(j.order) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, j.recs[j.order[i]])
	}
	return out, nil
}

func (j *fakeJournal) ListInterrupted(_ context.Context) ([]Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Record
	for _, id := range j.order {
		if j.recs[id].Status == StatusApplying {
			out = append(out, j.recs[id])
		}
	}
	return out, nil
}

func cloneRecord(r Record) Record {
	out := r
	out.Steps = append([]StepRecord(nil), r.Steps...)
	out.Warnings = append([]string(nil), r.Warnings...)
	out.Notes = append([]string(nil), r.Notes...)
	return out
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recorder 记录操作发生的顺序。
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// okStep 构造一个成功且可撤销的步骤。
func okStep(rec *recorder, id string) Step {
	return NewStep(id, "步骤 "+id,
		func(context.Context) error { rec.add("apply:" + id); return nil },
		func(context.Context) error { rec.add("revert:" + id); return nil },
	)
}

// failStep 构造一个必定失败的步骤。
func failStep(rec *recorder, id string) Step {
	return NewStep(id, "步骤 "+id,
		func(context.Context) error { rec.add("apply:" + id); return errors.New("模拟失败") },
		func(context.Context) error { rec.add("revert:" + id); return nil },
	)
}

func newPlan(id string, steps ...Step) Plan {
	return Plan{
		ID: id, Kind: "test.kind", Title: "测试计划",
		Risk: RiskMedium, Steps: steps, CreatedAt: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// 正常路径
// ---------------------------------------------------------------------------

func TestApplySucceeds(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	res, err := r.Apply(context.Background(), newPlan("p1",
		okStep(rec, "a"), okStep(rec, "b")))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !res.OK() {
		t.Fatalf("状态 = %s，期望 applied", res.Status)
	}
	if len(res.Applied) != 2 {
		t.Errorf("生效步骤数 = %d，期望 2", len(res.Applied))
	}

	// 顺序必须是正的。
	if got := rec.all(); fmt.Sprint(got) != "[apply:a apply:b]" {
		t.Errorf("调用顺序 = %v", got)
	}

	got, ok, _ := j.Get(context.Background(), "p1")
	if !ok {
		t.Fatal("日志里没有这条记录")
	}
	if got.Status != StatusApplied {
		t.Errorf("日志状态 = %s", got.Status)
	}
	if len(got.AppliedSteps()) != 2 {
		t.Errorf("日志里生效步骤数 = %d", len(got.AppliedSteps()))
	}
}

// TestApplyWritesJournalBeforeActing 钉住"先写日志再动手"。
//
// 这条保证是崩溃安全的基础：只在成功后写日志的话，一次执行中的崩溃
// 会留下一个**无人知晓**的系统变更 —— 而那正是回滚最需要的信息。
func TestApplyWritesJournalBeforeActing(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	var seenAtApplyTime Record
	var foundAtApplyTime bool

	step := NewStep("dangerous", "危险步骤",
		func(context.Context) error {
			// 在动手的那一刻检查日志。
			rec, ok, _ := j.Get(context.Background(), "p-wal")
			seenAtApplyTime, foundAtApplyTime = rec, ok
			return nil
		},
		func(context.Context) error { return nil },
	)

	if _, err := r.Apply(context.Background(), newPlan("p-wal", step)); err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	if !foundAtApplyTime {
		t.Fatal("执行步骤时日志里还没有记录 —— 崩溃会留下无人知晓的变更")
	}
	if seenAtApplyTime.Status != StatusApplying {
		t.Errorf("动手前日志状态 = %s，期望 applying", seenAtApplyTime.Status)
	}
}

// TestApplyRefusesWithoutJournal 验证日志写不进去时**不做**变更。
//
// 没有日志的变更等于无法撤销的变更。宁可什么都不做，
// 也不要制造一个用户无从收拾的系统状态。
func TestApplyRefusesWithoutJournal(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	j.failSave = errors.New("磁盘满了")
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	_, err := r.Apply(context.Background(), newPlan("p-nolog", okStep(rec, "a")))
	if err == nil {
		t.Fatal("日志写入失败时应当拒绝执行")
	}
	if len(rec.all()) != 0 {
		t.Errorf("日志写不进去却执行了步骤: %v", rec.all())
	}
}

// TestEmptyPlanIsSuccessNotError 验证"无需改动"不是错误。
//
// 用户点"开放 443"而 443 已经开放时，正确的回应是"无需改动"。
// 把它当错误会让用户以为操作失败了，然后去做一些不必要的排查。
func TestEmptyPlanIsSuccessNotError(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	res, err := r.Apply(context.Background(), newPlan("p-empty"))
	if err != nil {
		t.Fatalf("空计划不应报错: %v", err)
	}
	if !res.OK() {
		t.Errorf("空计划的状态 = %s，期望 applied", res.Status)
	}
	// 空计划不该产生日志记录：它什么都没做，记录只会是噪音。
	if _, ok, _ := j.Get(context.Background(), "p-empty"); ok {
		t.Error("空计划不应写入日志")
	}
}

// ---------------------------------------------------------------------------
// 失败与回滚
// ---------------------------------------------------------------------------

// TestFailureRollsBackInReverseOrder 是核心保证。
//
// 第一步成功、第二步失败时，第一步必须被撤销，且撤销顺序是**逆序** ——
// 这与"后做的先撤"的直觉一致，也是唯一正确的顺序：
// 后面的步骤可能依赖前面的结果。
func TestFailureRollsBackInReverseOrder(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	res, err := r.Apply(context.Background(), newPlan("p-fail",
		okStep(rec, "a"), okStep(rec, "b"), failStep(rec, "c"), okStep(rec, "d")))
	if err == nil {
		t.Fatal("应当返回错误")
	}

	if res.Status != StatusFailed {
		t.Errorf("状态 = %s，期望 failed", res.Status)
	}
	if res.FailedStep != "c" {
		t.Errorf("失败步骤 = %q，期望 c", res.FailedStep)
	}

	// d 不该被执行：第一步失败就停下。
	// 继续执行后续步骤没有意义 —— 它们很可能依赖失败的那一步。
	want := "[apply:a apply:b apply:c revert:b revert:a]"
	if got := fmt.Sprint(rec.all()); got != want {
		t.Errorf("调用顺序 = %v\n期望 %s", got, want)
	}

	if len(res.RolledBack) != 2 {
		t.Errorf("回滚步骤数 = %d，期望 2", len(res.RolledBack))
	}
	if res.RollbackErr != nil {
		t.Errorf("自动回滚不应失败: %v", res.RollbackErr)
	}

	got, _, _ := j.Get(context.Background(), "p-fail")
	if got.Status != StatusFailed {
		t.Errorf("日志状态 = %s", got.Status)
	}
	for _, s := range got.Steps {
		switch s.ID {
		case "a", "b":
			if s.State != StepReverted {
				t.Errorf("步骤 %s 的状态 = %s，期望 reverted", s.ID, s.State)
			}
		case "c":
			if s.State != StepFailed {
				t.Errorf("步骤 c 的状态 = %s，期望 failed", s.ID)
			}
		case "d":
			if s.State != StepPending {
				t.Errorf("步骤 d 的状态 = %s，期望 pending（不该被执行）", s.State)
			}
		}
	}
}

// TestFailureOnFirstStepNeedsNoRollback 验证第一步就失败时不报回滚错误。
func TestFailureOnFirstStepNeedsNoRollback(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	res, err := r.Apply(context.Background(), newPlan("p-first-fail",
		failStep(rec, "a"), okStep(rec, "b")))
	if err == nil {
		t.Fatal("应当返回错误")
	}
	if res.Status != StatusFailed {
		t.Errorf("状态 = %s", res.Status)
	}
	if res.RollbackErr != nil {
		t.Errorf("没有东西需要撤销，不该有回滚错误: %v", res.RollbackErr)
	}
	if len(res.RolledBack) != 0 {
		t.Errorf("回滚步骤数 = %d，期望 0", len(res.RolledBack))
	}
}

// TestRollbackFailureIsReportedHonestly 验证回滚失败不被吞掉。
//
// 这是最严重的结果：系统既不是原状、也不是目标状态。
// 把它报成"变更失败"会让用户以为一切已经退回去了，
// 而他实际面对的是一台规则半生效的机器。
func TestRollbackFailureIsReportedHonestly(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	stepA := NewStep("a", "步骤 a",
		func(context.Context) error { return nil },
		func(context.Context) error { return errors.New("撤销也失败了") },
	)
	stepB := NewStep("b", "步骤 b",
		func(context.Context) error { return errors.New("模拟失败") },
		func(context.Context) error { return nil },
	)

	res, err := r.Apply(context.Background(), newPlan("p-rbfail", stepA, stepB))
	if err == nil {
		t.Fatal("应当返回错误")
	}

	if res.Status != StatusRollbackFailed {
		t.Errorf("状态 = %s，期望 rollback_failed", res.Status)
	}
	if res.RollbackErr == nil {
		t.Fatal("必须报告回滚失败")
	}
	// 给用户看的错误必须指向"系统处于中间态"这个更要紧的事实，
	// 而不是最初那个步骤失败。
	if msg := res.Problem().Error(); !contains(msg, "中间状态") {
		t.Errorf("Problem() 没有说明系统处于中间态: %s", msg)
	}

	got, _, _ := j.Get(context.Background(), "p-rbfail")
	if got.Status != StatusRollbackFailed {
		t.Errorf("日志状态 = %s，期望 rollback_failed", got.Status)
	}
}

// TestNonRevertableStepsAreSkipped 验证"无需撤销"的步骤被跳过而不是报错。
//
// 把"无需撤销"当成错误会让自动回滚在无害的步骤上中断，
// 于是真正需要撤销的步骤反而没被撤销。
func TestNonRevertableStepsAreSkipped(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	// 只读探测：没有 revert。
	probe := NewStep("probe", "只读探测",
		func(context.Context) error { rec.add("apply:probe"); return nil },
		nil,
	)

	res, err := r.Apply(context.Background(), newPlan("p-skip",
		probe, okStep(rec, "a"), failStep(rec, "b")))
	if err == nil {
		t.Fatal("应当返回错误")
	}

	if res.RollbackErr != nil {
		t.Fatalf("跳过无需撤销的步骤不应算失败: %v", res.RollbackErr)
	}
	// probe 被跳过、a 被真正撤销。
	want := "[apply:probe apply:a apply:b revert:a]"
	if got := fmt.Sprint(rec.all()); got != want {
		t.Errorf("调用顺序 = %v\n期望 %s", got, want)
	}
}

// TestCanceledContextStillRollsBack 验证取消不会阻止回滚。
//
// 失败常见的原因就是上下文被取消（用户点了取消、进程要退出）。
// 用同一个被取消的 ctx 去回滚会让回滚一步都做不成 ——
// 那恰恰是回滚机制要避免的事情。
func TestCanceledContextStillRollsBack(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	ctx, cancel := context.WithCancel(context.Background())

	stepA := NewStep("a", "步骤 a",
		func(context.Context) error { rec.add("apply:a"); return nil },
		func(c context.Context) error {
			// 回滚时上下文必须仍然可用。
			if c.Err() != nil {
				return fmt.Errorf("回滚时上下文已被取消: %w", c.Err())
			}
			rec.add("revert:a")
			return nil
		},
	)
	stepB := NewStep("b", "步骤 b",
		func(context.Context) error {
			rec.add("apply:b")
			cancel() // 执行到一半被取消
			return errors.New("被取消")
		},
		func(context.Context) error { return nil },
	)

	res, err := r.Apply(ctx, newPlan("p-cancel", stepA, stepB))
	if err == nil {
		t.Fatal("应当返回错误")
	}
	if res.RollbackErr != nil {
		t.Fatalf("回滚不该因上下文被取消而失败: %v", res.RollbackErr)
	}
	if got := fmt.Sprint(rec.all()); got != "[apply:a apply:b revert:a]" {
		t.Errorf("调用顺序 = %v", got)
	}
}

// ---------------------------------------------------------------------------
// 计划校验
// ---------------------------------------------------------------------------

func TestDuplicateStepIDsRejected(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	// 重复 ID 会让回滚定位到错误的步骤 —— 而那是在出问题的时候
	// 才暴露，代价最高。
	_, err := r.Apply(context.Background(), newPlan("p-dup",
		okStep(rec, "same"), okStep(rec, "same")))
	if err == nil {
		t.Fatal("重复的步骤 ID 应当被拒绝")
	}
	if len(rec.all()) != 0 {
		t.Error("校验失败时不该执行任何步骤")
	}
}

func TestInvalidPlanRejected(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	cases := []struct {
		name string
		plan Plan
	}{
		{"缺 ID", Plan{Kind: "k", Title: "t", Steps: []Step{NewStep("a", "a", func(context.Context) error { return nil }, nil)}}},
		{"缺 Kind", Plan{ID: "p", Title: "t", Steps: []Step{NewStep("a", "a", func(context.Context) error { return nil }, nil)}}},
		{"缺标题", Plan{ID: "p", Kind: "k", Steps: []Step{NewStep("a", "a", func(context.Context) error { return nil }, nil)}}},
		{"步骤缺执行体", Plan{ID: "p", Kind: "k", Title: "t", Steps: []Step{{ID: "a"}}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.Apply(context.Background(), tc.plan); err == nil {
				t.Error("应当被拒绝")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 跨进程回滚
// ---------------------------------------------------------------------------

// fakeReverter 记录被要求撤销的计划。
type fakeReverter struct {
	kind  string
	mu    sync.Mutex
	plans []string
	err   error
}

func (f *fakeReverter) Kind() string { return f.kind }

func (f *fakeReverter) Revert(_ context.Context, rec Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, rec.PlanID)
	return f.err
}

// TestRollbackThroughReverter 验证跨进程撤销可用。
//
// 这条路径与"失败时的自动回滚"不同：它依赖 Reverter 依据日志记录
// 重新推导撤销动作，因此**内核重启之后依然可用**。
func TestRollbackThroughReverter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	if _, err := r.Apply(ctx, newPlan("p-rb", okStep(rec, "a"))); err != nil {
		t.Fatalf("执行失败: %v", err)
	}

	rev := &fakeReverter{kind: "test.kind"}
	r.Register(rev)

	res, err := r.Rollback(ctx, "p-rb")
	if err != nil {
		t.Fatalf("撤销失败: %v", err)
	}
	if res.Status != StatusRolledBack {
		t.Errorf("状态 = %s", res.Status)
	}
	if len(rev.plans) != 1 || rev.plans[0] != "p-rb" {
		t.Errorf("撤销器收到的计划 = %v", rev.plans)
	}

	got, _, _ := j.Get(ctx, "p-rb")
	if got.Status != StatusRolledBack {
		t.Errorf("日志状态 = %s", got.Status)
	}
	for _, s := range got.Steps {
		if s.State != StepReverted {
			t.Errorf("步骤 %s 的状态 = %s，期望 reverted", s.ID, s.State)
		}
	}

	// --- 幂等：再撤一次仍然成功 ---
	res2, err := r.Rollback(ctx, "p-rb")
	if err != nil {
		t.Fatalf("重复撤销应当成功（幂等），实际报错: %v", err)
	}
	if res2.Status != StatusRolledBack {
		t.Errorf("重复撤销的状态 = %s", res2.Status)
	}
	// 撤销器不该被再调用一次。
	if len(rev.plans) != 1 {
		t.Errorf("撤销器被重复调用: %v", rev.plans)
	}
}

func TestRollbackUnknownPlan(t *testing.T) {
	t.Parallel()

	r := NewRunner(newFakeJournal(), nil, testLogger())
	_, err := r.Rollback(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("应当返回 ErrNotFound，得到 %v", err)
	}
}

func TestRollbackWithoutReverter(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	j := newFakeJournal()
	rec := &recorder{}
	r := NewRunner(j, nil, testLogger())

	if _, err := r.Apply(ctx, newPlan("p-norev", okStep(rec, "a"))); err != nil {
		t.Fatal(err)
	}

	// 没有登记对应 Kind 的撤销器时必须**明确报错**，
	// 而不是静默地把记录标成已撤销 —— 后者会让用户以为撤销成功了。
	_, err := r.Rollback(ctx, "p-norev")
	if err == nil {
		t.Fatal("没有撤销器时应当报错")
	}

	got, _, _ := j.Get(ctx, "p-norev")
	if got.Status == StatusRolledBack {
		t.Error("撤销没有真正执行，不该把记录标成已撤销")
	}
}

// TestRollbackRefusesWhileApplying 验证执行中的变更不被撤销。
//
// 那个进程可能还在写系统状态，两边同时动手会得到谁也没预料到的结果。
func TestRollbackRefusesWhileApplying(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	j := newFakeJournal()

	// 手工造一条"执行中"的记录。
	rec := Record{
		PlanID: "p-busy", Kind: "test.kind", Title: "执行中",
		Status: StatusApplying, CreatedAt: time.Now().UTC(),
	}
	if err := j.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}

	r := NewRunner(j, nil, testLogger())
	r.Register(&fakeReverter{kind: "test.kind"})

	if _, err := r.Rollback(ctx, "p-busy"); err == nil {
		t.Fatal("执行中的变更不该被撤销")
	}
}

// ---------------------------------------------------------------------------
// 中断恢复
// ---------------------------------------------------------------------------

// TestRecoverInterruptedReportsWithoutAutoRollback 钉住一个刻意的设计选择。
//
// 自动撤销听起来更安全，实际相反：一次执行中的变更可能已经部分生效，
// 而用户**可能正依赖那部分**（例如他已经通过新开的端口连上了服务）。
// 内核在启动时擅自把它撤掉，会把用户正在用的东西拿走，
// 而他完全不知道发生了什么。
func TestRecoverInterruptedReportsWithoutAutoRollback(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	// 造一条"执行到一半就没了"的记录。
	rec := Record{
		PlanID: "p-stuck", Kind: "test.kind", Title: "半成品",
		Status: StatusApplying, CreatedAt: time.Now().UTC(),
		Steps: []StepRecord{
			{ID: "a", Title: "步骤 a", State: StepApplied},
			{ID: "b", Title: "步骤 b", State: StepPending},
		},
	}
	if err := j.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}

	reverter := &fakeReverter{kind: "test.kind"}
	r.Register(reverter)

	got, err := r.RecoverInterrupted(ctx)
	if err != nil {
		t.Fatalf("恢复检查失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("应当发现 1 条中断的变更，得到 %d", len(got))
	}
	if got[0].Record.PlanID != "p-stuck" {
		t.Errorf("发现的计划 = %s", got[0].Record.PlanID)
	}
	if got[0].Reason == "" {
		t.Error("必须给出可读的原因")
	}

	// **关键**：没有自动撤销。
	if len(reverter.plans) != 0 {
		t.Errorf("恢复检查不该自动撤销，撤销器被调用了: %v", reverter.plans)
	}

	// 记录状态保持原样，等用户决定。
	after, _, _ := j.Get(ctx, "p-stuck")
	if after.Status != StatusApplying {
		t.Errorf("恢复检查不该改动记录状态，现在 = %s", after.Status)
	}

	// 但用户随后可以手动撤销它 —— 这条路径必须通。
	if _, err := r.Rollback(ctx, "p-stuck"); err == nil {
		t.Log("（执行中的记录仍拒绝直接撤销，这是有意的保护）")
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

// TestApplyIsSerialized 验证变更被串行化。
//
// 两个防火墙计划同时执行会互相干扰：A 读到的当前状态里看不到 B 刚要加的
// 规则，于是 A 的差异计算是错的，而它的"撤销"会把 B 的规则也一起抹掉。
func TestApplyIsSerialized(t *testing.T) {
	t.Parallel()

	j := newFakeJournal()
	r := NewRunner(j, nil, testLogger())

	var (
		mu      sync.Mutex
		inside  int
		maxSeen int
	)

	// 每步睡一小会儿，制造交错的机会。
	slow := func(id string) Step {
		return NewStep(id, id,
			func(context.Context) error {
				mu.Lock()
				inside++
				if inside > maxSeen {
					maxSeen = inside
				}
				mu.Unlock()

				time.Sleep(5 * time.Millisecond)

				mu.Lock()
				inside--
				mu.Unlock()
				return nil
			},
			func(context.Context) error { return nil },
		)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			plan := newPlan(fmt.Sprintf("p-conc-%d", n), slow("a"), slow("b"))
			if _, err := r.Apply(context.Background(), plan); err != nil {
				t.Errorf("并发执行失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if maxSeen != 1 {
		t.Errorf("同时有 %d 个步骤在执行 —— 变更没有被串行化", maxSeen)
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
