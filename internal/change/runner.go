package change

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"log/slog"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// Status 是一个计划的整体状态。
type Status string

const (
	// StatusApplying 正在执行。
	//
	// **这个状态如果停着不动，说明内核在执行过程中崩了。**
	// 启动时检查它是发现"可能有半成品变更"的唯一手段 ——
	// 见 Runner.RecoverInterrupted。
	StatusApplying Status = "applying"
	// StatusApplied 全部步骤成功。
	StatusApplied Status = "applied"
	// StatusFailed 有步骤失败，但已完成自动回滚。
	StatusFailed Status = "failed"
	// StatusRolledBack 已回滚。
	StatusRolledBack Status = "rolled_back"
	// StatusRollbackFailed 回滚本身失败。
	//
	// 这是最严重的结果：系统处于一个既不是原状、也不是目标状态的
	// 中间态。必须显著提示用户手动处理。
	StatusRollbackFailed Status = "rollback_failed"
)

// StepState 是单个步骤的状态。
type StepState string

const (
	StepPending  StepState = "pending"
	StepApplied  StepState = "applied"
	StepFailed   StepState = "failed"
	StepReverted StepState = "reverted"
	// StepSkipped 表示该步骤没有需要撤销的东西，回滚时被跳过。
	StepSkipped StepState = "skipped"
)

// StepRecord 是步骤在日志里的快照。
//
// 它保存的是**渲染后的文本**而不是重建差异所需的信息：
// 日志的用途是"事后看清发生了什么"，而那时原始状态早已不存在，
// 重新计算差异既不可能也没有意义。
type StepRecord struct {
	ID    string     `json:"id"`
	Title string     `json:"title"`
	State StepState  `json:"state"`
	Diff  []DiffLine `json:"diff,omitempty"`
	Error string     `json:"error,omitempty"`
}

// Record 是一个计划的执行记录。
type Record struct {
	PlanID string       `json:"plan_id"`
	Kind   string       `json:"kind"`
	Title  string       `json:"title"`
	Risk   Risk         `json:"risk"`
	Status Status       `json:"status"`
	Steps  []StepRecord `json:"steps"`

	// Warnings / Notes 一并存下来：回滚时界面上要能重新展示
	// "当初提示过什么"，否则用户无法判断该不该退。
	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`

	// Payload 是后端私有的回滚数据，本包原样保存与交还。
	//
	// 见 Plan.Payload 的说明：它是跨进程撤销得以成立的唯一途径。
	Payload []byte `json:"payload,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AppliedSteps 返回已生效的步骤 ID。
func (r Record) AppliedSteps() []string {
	var out []string
	for _, s := range r.Steps {
		if s.State == StepApplied {
			out = append(out, s.ID)
		}
	}
	return out
}

// Journal 持久化变更记录。
//
// 它必须是**先写后做**（write-ahead）的：记录在执行任何步骤之前
// 就要落盘。只在成功后落盘的话，一次执行中的崩溃会留下一个
// 无人知晓的系统变更 —— 而那正是回滚最需要的信息。
type Journal interface {
	// Save 写入或更新一条记录（按 PlanID upsert）。
	Save(ctx context.Context, rec Record) error
	// Get 取出一条记录。
	Get(ctx context.Context, planID string) (Record, bool, error)
	// List 按创建时间倒序列出记录。
	List(ctx context.Context, limit int) ([]Record, error)
	// ListInterrupted 列出停留在"执行中"状态的记录。
	//
	// 用于内核启动时的恢复检查：这些记录意味着上次执行没有走完。
	ListInterrupted(ctx context.Context) ([]Record, error)
}

// Reverter 能撤销某一类已经生效的变更。
//
// # 为什么需要它，而不是直接存下 revert 闭包
//
// 闭包无法持久化。用户上午应用了一次防火墙变更，下午重启了内核，
// 晚上想撤销 —— 那时进程里早已没有当初的闭包。唯一可行的办法是
// 让**制造这次变更的后端**依据日志记录重新推导撤销动作。
//
// 它因此是本包中"跨进程回滚"的唯一途径；同一次运行内的自动回滚
// 仍然走闭包（更快、更精确）。
type Reverter interface {
	// Kind 返回它负责的变更类型，与 Plan.Kind 对应。
	Kind() string
	// Revert 撤销一条已生效的记录。
	//
	// 它必须是**幂等**的：目标状态已经不存在时应当返回成功。
	// 理由见 Apply 的说明 —— 用户重试撤销是常见操作。
	Revert(ctx context.Context, rec Record) error
}

// Runner 执行计划并记录日志。
type Runner struct {
	journal Journal
	bus     *event.Bus
	log     *slog.Logger

	// reverters 按变更类型登记撤销器。
	reverters map[string]Reverter

	// mu 串行化全部变更。
	//
	// 这是必须的：两个防火墙计划同时执行会互相干扰 ——
	// A 读到的当前状态里看不到 B 刚要加的规则，于是 A 的差异
	// 计算是错的，而它的"撤销"会把 B 的规则也一起抹掉。
	mu sync.Mutex

	// live / liveID 保存最近一次执行时的步骤表，供失败时的自动回滚
	// 取用 revert 闭包。见 liveSteps 的说明。
	liveMu sync.Mutex
	live   map[string]Step
	liveID string

	// pending 保存"已生成但尚未应用"的计划。
	//
	// 用独立的锁：pending 的读写比执行频繁得多（用户每次刷新预览都会读），
	// 与 mu 共用会让一次正在执行的变更把预览请求全部挡住。
	pendingMu sync.Mutex
	pending   *Pending
}

// NewRunner 构造执行器。
func NewRunner(journal Journal, bus *event.Bus, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		journal:   journal,
		bus:       bus,
		log:       log,
		reverters: make(map[string]Reverter),
	}
}

// SetBus 设置事件总线。
//
// 单独一步而不是构造参数：总线的缓冲容量来自设置，而设置在本包之前
// 就要加载完（语言与日志级别都靠它）。与其把构造顺序扭成一个环，
// 不如留一个显式的回填点。
func (r *Runner) SetBus(bus *event.Bus) { r.bus = bus }

// Register 登记一个撤销器。
//
// 通常在装配阶段调用（daemon 启动时）。重复登记同一个 Kind 会以后者
// 覆盖前者，并在日志里留一条警告 —— 那几乎总是装配时的复制粘贴错误。
func (r *Runner) Register(rev Reverter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.reverters[rev.Kind()]; dup {
		r.log.Warn("变更类型被重复登记撤销器，后者生效", "kind", rev.Kind())
	}
	r.reverters[rev.Kind()] = rev
}

// Result 是一次执行的结果。
type Result struct {
	PlanID string
	Status Status

	// Applied 是成功生效的步骤 ID（按执行顺序）。
	Applied []string
	// FailedStep 是失败的步骤 ID；没有失败时为空串。
	FailedStep string
	// Err 是失败原因。
	Err error

	// RolledBack 是因失败而**自动撤销**的步骤 ID。
	RolledBack []string
	// RollbackErr 是自动回滚本身失败的原因。
	//
	// 与 Err 分开：Err 是"原操作失败"，通常无害；
	// RollbackErr 是"系统现在处于中间态"，严重得多。
	RollbackErr error
}

// OK 报告执行是否完全成功。
func (r Result) OK() bool { return r.Status == StatusApplied }

// Problem 返回一个适合展示给用户的错误。
//
// 有回滚失败时优先返回它 —— 用户需要知道的是"现在机器处于什么状态"，
// 而不是"最初哪一步失败了"。
func (r Result) Problem() error {
	if r.RollbackErr != nil {
		return fmt.Errorf(i18n.T("change.err.autorollback"), r.RollbackErr)
	}
	return r.Err
}

// Apply 执行一个计划。
//
// 行为约定（这些约定本身就是本包存在的理由）：
//
//  1. 执行前先把记录落盘（write-ahead），因此中途崩溃不会留下
//     无人知晓的变更。
//  2. 步骤按顺序执行，**第一个失败就停下**。继续执行后续步骤
//     没有意义 —— 后续步骤很可能依赖前面那一步的结果。
//  3. 失败时**自动按相反顺序撤销已生效的步骤**。这一步不做的话，
//     用户会得到一个"改了一半"的系统，而他还得自己搞清楚
//     哪一半生效了。
//  4. 自动回滚失败会如实报告（StatusRollbackFailed），
//     而不是假装成功。
func (r *Runner) Apply(ctx context.Context, plan Plan) (Result, error) {
	if err := plan.Validate(); err != nil {
		return Result{PlanID: plan.ID, Status: StatusFailed, Err: err}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	res := Result{PlanID: plan.ID}

	// 无需改动的情形不是错误 —— 用户点"开放 443"而 443 已经开放时，
	// 正确的回应是"无需改动"，而不是一个报错。
	//
	// 但它**仍然落一条记录**。这是一个刻意的选择：记录的价值不只是
	// "改了什么"，还包括"用户在那个时刻想做什么"。事后排查"我明明
	// 点过开放端口，怎么还是不通"时，这条记录能立刻排除掉一半可能。
	// 早先的版本为了"不产生噪音"跳过了它，代价是接口层拿不到任何
	// 可返回的东西。
	if plan.Empty() {
		rec := Record{
			PlanID:    plan.ID,
			Kind:      plan.Kind,
			Title:     plan.Title,
			Risk:      plan.Risk,
			Status:    StatusApplied,
			Steps:     []StepRecord{},
			Warnings:  plan.Warnings,
			Notes:     plan.Notes,
			Payload:   plan.Payload,
			CreatedAt: plan.CreatedAt,
			UpdatedAt: time.Now().UTC(),
		}
		if rec.CreatedAt.IsZero() {
			rec.CreatedAt = rec.UpdatedAt
		}
		if err := r.journal.Save(ctx, rec); err != nil {
			r.log.Warn("无需改动，但记录写入失败", "plan", plan.ID, "err", err)
		}

		res.Status = StatusApplied
		r.log.Info("变更无需执行（已是目标状态）", "plan", plan.ID, "kind", plan.Kind)
		return res, nil
	}

	rec := Record{
		PlanID:    plan.ID,
		Kind:      plan.Kind,
		Title:     plan.Title,
		Risk:      plan.Risk,
		Status:    StatusApplying,
		Steps:     snapshotSteps(plan, StepPending),
		Warnings:  plan.Warnings,
		Notes:     plan.Notes,
		Payload:   plan.Payload,
		CreatedAt: plan.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = rec.UpdatedAt
	}

	// 先落盘再动手。
	if err := r.journal.Save(ctx, rec); err != nil {
		return res, fmt.Errorf(i18n.T("change.err.no_journal"), err)
	}

	r.log.Info("开始执行变更",
		"plan", plan.ID, "kind", plan.Kind, "risk", plan.Risk, "steps", len(plan.Steps))

	// 记下步骤表，失败时的自动回滚要从这里取 revert 闭包。
	r.remember(plan)

	for i := range plan.Steps {
		step := plan.Steps[i]

		if err := ctx.Err(); err != nil {
			// 上下文被取消：把它当成一次普通失败处理，走回滚路径。
			// 直接返回会让已生效的步骤留在系统里。
			res.Err = fmt.Errorf(i18n.T("change.err.cancelled"), err)
			res.FailedStep = step.ID
			return r.fail(ctx, rec, res)
		}

		r.log.Debug("执行变更步骤", "plan", plan.ID, "step", step.ID, "title", step.Title)

		if err := step.apply(ctx); err != nil {
			res.Err = fmt.Errorf(i18n.T("change.err.step_failed"), step.Title, err)
			res.FailedStep = step.ID
			setStepState(&rec, step.ID, StepFailed, err.Error())
			return r.fail(ctx, rec, res)
		}

		res.Applied = append(res.Applied, step.ID)
		setStepState(&rec, step.ID, StepApplied, "")
		rec.UpdatedAt = time.Now().UTC()

		// 每步之后都落盘：崩在第三步时，日志里必须已经有前两步的结果。
		if err := r.journal.Save(ctx, rec); err != nil {
			r.log.Warn("写入变更日志失败，继续执行", "plan", plan.ID, "step", step.ID, "err", err)
		}
	}

	rec.Status = StatusApplied
	rec.UpdatedAt = time.Now().UTC()
	if err := r.journal.Save(ctx, rec); err != nil {
		// 变更已经生效了，日志写失败不能反过来让操作算失败 ——
		// 那会让用户以为没生效而重复执行一次。
		r.log.Error("变更已生效但日志写入失败", "plan", plan.ID, "err", err)
	}

	res.Status = StatusApplied
	r.publish(event.TypeChangeApplied, plan, res)
	r.log.Info("变更执行完成", "plan", plan.ID, "applied", len(res.Applied))
	return res, nil
}

// fail 处理执行失败：自动回滚已生效的步骤。
func (r *Runner) fail(ctx context.Context, rec Record, res Result) (Result, error) {
	r.log.Warn("变更步骤失败，开始自动回滚",
		"plan", rec.PlanID, "failed_step", res.FailedStep, "applied", len(res.Applied))

	// 回滚要用一个**未被取消的**上下文。
	//
	// 失败常见的原因就是上下文被取消（用户点了取消、进程要退出）。
	// 用同一个 ctx 去回滚会让回滚一步都做不成，于是系统停在半成品状态 ——
	// 那恰恰是回滚机制要避免的事情。
	revertCtx := context.WithoutCancel(ctx)

	// 从日志里重新取回步骤，才能拿到 revert 闭包。
	// 这里用的是刚传进来的 plan —— 回滚与执行必须在同一个进程内完成，
	// 跨进程的回滚走 Rollback()（它依赖的是记录里渲染好的差异，
	// 由各后端自行实现）。
	rolledBack, rbErr := r.rollbackSteps(revertCtx, rec.PlanID, res.Applied)

	res.RolledBack = rolledBack

	now := time.Now().UTC()
	rec.UpdatedAt = now
	for _, id := range rolledBack {
		setStepState(&rec, id, StepReverted, "")
	}

	switch {
	case rbErr != nil:
		rec.Status = StatusRollbackFailed
		res.Status = StatusRollbackFailed
		res.RollbackErr = rbErr
	case len(res.Applied) > 0:
		rec.Status = StatusFailed
		res.Status = StatusFailed
	default:
		// 第一步就失败了，没有东西需要撤销。
		rec.Status = StatusFailed
		res.Status = StatusFailed
	}

	if err := r.journal.Save(revertCtx, rec); err != nil {
		r.log.Error("回滚结果写入日志失败", "plan", rec.PlanID, "err", err)
	}

	r.publish(event.TypeChangeFailed, Plan{
		ID: rec.PlanID, Kind: rec.Kind, Title: rec.Title, Risk: rec.Risk,
	}, res)

	return res, res.Err
}

// rollbackSteps 按相反顺序撤销一组步骤。
//
// 它需要拿到步骤的 revert 闭包，因此从最近一次 Apply 的内存状态里取。
// 这里刻意不把它做成"任意时刻都能调用"：跨进程回滚需要后端自己
// 重新推导撤销动作（见 Rollback），因为闭包无法持久化。
func (r *Runner) rollbackSteps(ctx context.Context, planID string, applied []string) ([]string, error) {
	live := r.liveSteps(planID)
	if live == nil {
		return nil, fmt.Errorf(i18n.T("change.err.no_context"), planID)
	}

	var done []string
	// 逆序：后做的先撤。
	for i := len(applied) - 1; i >= 0; i-- {
		id := applied[i]
		step, ok := live[id]
		if !ok {
			return done, fmt.Errorf(i18n.T("change.err.no_undo"), id)
		}
		if !step.Revertable() {
			// 无需撤销的步骤跳过，不算失败。
			done = append(done, id)
			continue
		}
		if err := step.revert(ctx); err != nil {
			return done, fmt.Errorf(i18n.T("change.err.undo_failed"), step.Title, err)
		}
		done = append(done, id)
		r.log.Info("已撤销变更步骤", "plan", planID, "step", id, "title", step.Title)
	}
	return done, nil
}

// ---------------------------------------------------------------------------
// 跨进程回滚
// ---------------------------------------------------------------------------

// Rollback 撤销一条此前已生效的变更。
//
// 与 Apply 内部那个自动回滚的区别：它走 Reverter，因此**跨越进程重启**
// 依然可用。用户在控制台上点"撤销"时走的就是这条路径。
//
// 幂等：已经撤销过的计划再撤一次会直接返回成功，而不是报错。
// 用户重试是常见操作（网络卡了、按钮点重了），把重试当失败会让他
// 以为撤销没生效，进而做出更激进的处置。
func (r *Runner) Rollback(ctx context.Context, planID string) (Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	res := Result{PlanID: planID}

	rec, found, err := r.journal.Get(ctx, planID)
	if err != nil {
		return res, fmt.Errorf(i18n.T("change.err.read_record"), err)
	}
	if !found {
		return res, ErrNotFound
	}

	switch rec.Status {
	case StatusRolledBack:
		res.Status = StatusRolledBack
		return res, nil
	case StatusApplying:
		// 执行中的记录不该被撤销：那个进程可能还在写系统状态，
		// 两边同时动手会得到谁也没预料到的结果。
		return res, fmt.Errorf(i18n.T("change.err.still_running"), planID)
	}

	rev, ok := r.reverters[rec.Kind]
	if !ok {
		return res, fmt.Errorf(i18n.T("change.err.no_reverter"), rec.Kind)
	}

	applied := rec.AppliedSteps()
	if len(applied) == 0 {
		// 没有生效的步骤：把记录标成已撤销，避免它一直挂在"可撤销"列表里。
		rec.Status = StatusRolledBack
		rec.UpdatedAt = time.Now().UTC()
		if err := r.journal.Save(ctx, rec); err != nil {
			r.log.Warn("更新变更记录失败", "plan", planID, "err", err)
		}
		res.Status = StatusRolledBack
		return res, nil
	}

	r.log.Info("开始撤销变更", "plan", planID, "kind", rec.Kind, "steps", len(applied))

	if err := rev.Revert(ctx, rec); err != nil {
		rec.Status = StatusRollbackFailed
		rec.UpdatedAt = time.Now().UTC()
		if saveErr := r.journal.Save(ctx, rec); saveErr != nil {
			r.log.Error("回滚失败结果写入日志失败", "plan", planID, "err", saveErr)
		}
		res.Status = StatusRollbackFailed
		res.RollbackErr = err
		r.publish(event.TypeChangeFailed, Plan{
			ID: rec.PlanID, Kind: rec.Kind, Title: rec.Title, Risk: rec.Risk,
		}, res)
		return res, err
	}

	rec.Status = StatusRolledBack
	rec.UpdatedAt = time.Now().UTC()
	for i := range rec.Steps {
		if rec.Steps[i].State == StepApplied {
			rec.Steps[i].State = StepReverted
		}
	}
	if err := r.journal.Save(ctx, rec); err != nil {
		r.log.Error("撤销结果写入日志失败", "plan", planID, "err", err)
	}

	res.Status = StatusRolledBack
	res.RolledBack = applied
	r.publish(event.TypeChangeRolledBack, Plan{
		ID: rec.PlanID, Kind: rec.Kind, Title: rec.Title, Risk: rec.Risk,
	}, res)

	r.log.Info("变更已撤销", "plan", planID, "steps", len(applied))
	return res, nil
}

// Interrupted 报告一次被中断的变更。
type Interrupted struct {
	Record Record
	// Reason 解释为什么它被判定为中断。
	Reason string
}

// RecoverInterrupted 找出停留在"执行中"状态的变更。
//
// # 为什么只是报告，不自动撤销
//
// 自动撤销听起来更"安全"，实际相反：一次执行中的变更可能已经
// 部分生效，而用户**可能正依赖那部分**（例如他已经通过新开的端口
// 连上了服务）。内核在启动时擅自把它撤掉，会把用户正在用的东西拿走，
// 而他完全不知道发生了什么。
//
// 因此这里只做两件事：把它找出来、如实报告，并让用户一键决定。
func (r *Runner) RecoverInterrupted(ctx context.Context) ([]Interrupted, error) {
	records, err := r.journal.ListInterrupted(ctx)
	if err != nil {
		return nil, fmt.Errorf(i18n.T("change.err.check_interrupt"), err)
	}

	out := make([]Interrupted, 0, len(records))
	for _, rec := range records {
		applied := rec.AppliedSteps()
		reason := i18n.T("change.msg.interrupted")
		if len(applied) > 0 {
			reason = fmt.Sprintf(
				i18n.T("change.msg.interrupted_detail"),
				len(applied), len(applied))
		}
		out = append(out, Interrupted{Record: rec, Reason: reason})

		r.log.Warn("发现未完成的变更",
			"plan", rec.PlanID, "kind", rec.Kind,
			"applied", len(applied), "reason", reason)
	}
	return out, nil
}

// List 列出历史变更记录。
func (r *Runner) List(ctx context.Context, limit int) ([]Record, error) {
	return r.journal.List(ctx, limit)
}

// Get 取出一条变更记录。
func (r *Runner) Get(ctx context.Context, planID string) (Record, bool, error) {
	return r.journal.Get(ctx, planID)
}

// ---------------------------------------------------------------------------
// 执行上下文
// ---------------------------------------------------------------------------

// liveSteps 返回某计划最近一次执行时的步骤表。
//
// 存在的理由：回滚需要 revert 闭包，而闭包无法持久化。
// 因此自动回滚只在"同一次运行内"可用；跨进程回滚走 Rollback()，
// 由各后端依据记录重新推导撤销动作。
func (r *Runner) liveSteps(planID string) map[string]Step {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	if r.live == nil {
		return nil
	}
	if r.liveID != planID {
		return nil
	}
	return r.live
}

// remember 记下当前计划的步骤表，供失败时的自动回滚使用。
func (r *Runner) remember(plan Plan) {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	r.liveID = plan.ID
	r.live = make(map[string]Step, len(plan.Steps))
	for _, s := range plan.Steps {
		r.live[s.ID] = s
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func snapshotSteps(plan Plan, state StepState) []StepRecord {
	out := make([]StepRecord, 0, len(plan.Steps))
	for _, s := range plan.Steps {
		out = append(out, StepRecord{
			ID: s.ID, Title: s.Title, State: state, Diff: s.Diff,
		})
	}
	return out
}

func setStepState(rec *Record, stepID string, state StepState, errMsg string) {
	for i := range rec.Steps {
		if rec.Steps[i].ID == stepID {
			rec.Steps[i].State = state
			rec.Steps[i].Error = errMsg
			return
		}
	}
}

func (r *Runner) publish(typ string, plan Plan, res Result) {
	if r.bus == nil {
		return
	}
	payload := map[string]any{
		"plan_id": plan.ID,
		"kind":    plan.Kind,
		"title":   plan.Title,
		"risk":    string(plan.Risk),
		"status":  string(res.Status),
	}
	if res.FailedStep != "" {
		payload["failed_step"] = res.FailedStep
	}
	if res.Err != nil {
		payload["error"] = res.Err.Error()
	}
	if res.RollbackErr != nil {
		payload["rollback_error"] = res.RollbackErr.Error()
	}
	r.bus.Publish(typ, payload)
}

// ErrNotFound 表示找不到指定的变更记录。
var ErrNotFound = errors.New(i18n.T("change.err.not_found"))
