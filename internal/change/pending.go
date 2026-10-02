package change

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"sync"
	"time"
)

// 本文件解决一个具体问题：**计划里含闭包，而客户端没法把它传回来。**
//
// "预览 → 应用"这个流程要求用户先看到差异、再决定是否执行。但一个
// 计划里的步骤持有 `apply` / `revert` 闭包（它们捕获了平台后端的句柄），
// 那些东西既不能序列化，也不能通过 HTTP 传一个来回。
//
// 于是流程变成：内核生成计划后**留在内存里**，只把可序列化的预览发给
// 客户端；用户确认时凭 plan ID 回来取。
//
// # 为什么不做成"客户端把计划原样传回来"
//
// 那需要把计划序列化成数据，也就是让每个后端把自己要做的事描述成
// 一份可执行的指令清单。那等于在本包之上再造一层脚本引擎 —— 而它的
// 第一个后果就是：客户端可以伪造一份计划，让内核以管理员身份执行
// 任意防火墙操作。留在内存里的方案天然没有这个问题。

// pendingTTL 是待确认计划的有效期。
//
// 10 分钟的依据：用户看完差异、想清楚要不要做，比这更久大概率已经
// 走开了；而计划里的差异是**基于当时的状态**算出来的，放太久之后
// 系统状态可能已经变了，那时再应用会让差异与预期不符
// （例如别人手工加了一条规则）。过期强制重新生成是对的。
const pendingTTL = 10 * time.Minute

// StepPreview 是步骤的可序列化描述。
type StepPreview struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Details string     `json:"details,omitempty"`
	Diff    []DiffLine `json:"diff,omitempty"`
	// Revertable 报告这一步出问题时能否撤销。
	//
	// 界面必须在应用之前显示它 —— 用户有权知道"这一步做错了能不能退"。
	Revertable bool `json:"revertable"`
}

// Preview 是一个待确认计划的完整描述。
//
// 它是本包唯一会被发给客户端的东西，因此**不含任何闭包或后端句柄**。
type Preview struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Risk  Risk   `json:"risk"`

	// Diff 是汇总后的差异，供界面直接渲染。
	Diff []DiffLine `json:"diff"`
	// Steps 是逐步描述。
	Steps []StepPreview `json:"steps"`

	Warnings []string `json:"warnings,omitempty"`
	Notes    []string `json:"notes,omitempty"`

	// Empty 表示无需改动（已是目标状态）。
	//
	// 界面应当把它显示成"无需改动"而不是"操作失败"：用户点
	// "开放 443"而 443 已经开放时，那不是错误。
	Empty bool `json:"empty"`

	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Preview 生成计划的可序列化描述。
func (p Plan) Preview(expiresAt time.Time) Preview {
	out := Preview{
		ID:        p.ID,
		Kind:      p.Kind,
		Title:     p.Title,
		Risk:      p.Risk,
		Diff:      p.Diff(),
		Warnings:  p.Warnings,
		Notes:     p.Notes,
		Empty:     p.Empty(),
		CreatedAt: p.CreatedAt,
		ExpiresAt: expiresAt,
		Steps:     make([]StepPreview, 0, len(p.Steps)),
	}
	for _, s := range p.Steps {
		out.Steps = append(out.Steps, StepPreview{
			ID:         s.ID,
			Title:      s.Title,
			Details:    s.Details,
			Diff:       s.Diff,
			Revertable: s.Revertable(),
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// 待确认计划的登记表
// ---------------------------------------------------------------------------

// pendingEntry 是一个待确认的计划。
type pendingEntry struct {
	plan      Plan
	preview   Preview
	createdAt time.Time
	expiresAt time.Time
}

// Pending 保存"已生成但尚未应用"的计划。
//
// 并发安全。它是 Runner 的一部分而不是独立组件 —— 计划的生命周期
// 完全属于变更流程。
type Pending struct {
	mu      sync.Mutex
	entries map[string]pendingEntry
	order   []string
	ttl     time.Duration
	now     func() time.Time
}

// NewPending 构造登记表。
func NewPending() *Pending {
	return &Pending{
		entries: make(map[string]pendingEntry),
		ttl:     pendingTTL,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

// SetTTL 覆盖有效期，供测试使用。
func (p *Pending) SetTTL(d time.Duration) {
	if d > 0 {
		p.ttl = d
	}
}

// Put 登记一个计划并返回它的预览。
func (p *Pending) Put(plan Plan) Preview {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := p.now()
	p.sweepLocked(now)

	expires := now.Add(p.ttl)
	preview := plan.Preview(expires)

	p.entries[plan.ID] = pendingEntry{
		plan: plan, preview: preview, createdAt: now, expiresAt: expires,
	}
	p.order = append(p.order, plan.ID)

	return preview
}

// Get 取出预览（不移除）。
//
// 用于客户端刷新页面后重新读取差异 —— 那不该让它失效。
func (p *Pending) Get(id string) (Preview, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sweepLocked(p.now())
	entry, ok := p.entries[id]
	if !ok {
		return Preview{}, false
	}
	return entry.preview, true
}

// Take 取出计划并**移除**它。
//
// 取出即消费：应用一个计划只能成功一次。
// 不做这件事的话，用户双击"应用"会执行两次 —— 对幂等的操作无害，
// 但对"新增一条规则"这类操作会留下重复的东西。
func (p *Pending) Take(id string) (Plan, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sweepLocked(p.now())
	entry, ok := p.entries[id]
	if !ok {
		return Plan{}, false
	}
	delete(p.entries, id)
	p.removeFromOrderLocked(id)
	return entry.plan, true
}

// Discard 丢弃一个待确认的计划。
func (p *Pending) Discard(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.entries, id)
	p.removeFromOrderLocked(id)
}

// List 返回全部待确认的预览（最近的在前）。
func (p *Pending) List() []Preview {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sweepLocked(p.now())
	out := make([]Preview, 0, len(p.order))
	for i := len(p.order) - 1; i >= 0; i-- {
		if entry, ok := p.entries[p.order[i]]; ok {
			out = append(out, entry.preview)
		}
	}
	return out
}

// Len 返回待确认的数量。
func (p *Pending) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked(p.now())
	return len(p.entries)
}

// sweepLocked 清理过期的计划。
//
// 过期是必须的：预览里的差异是**基于当时的状态**算出来的。放太久之后
// 系统状态可能已经变了（别人手工加了规则、另一个计划改了同一处），
// 那时再应用会让实际效果与用户看到的差异不符 —— 而"确认时看到的东西
// 和实际执行的东西不一样"是这套机制最不能接受的失败。
func (p *Pending) sweepLocked(now time.Time) {
	var kept []string
	for _, id := range p.order {
		entry, ok := p.entries[id]
		if !ok {
			continue
		}
		if now.After(entry.expiresAt) {
			delete(p.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	p.order = kept
}

func (p *Pending) removeFromOrderLocked(id string) {
	for i, existing := range p.order {
		if existing == id {
			p.order = append(p.order[:i], p.order[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Runner 的对外流程
// ---------------------------------------------------------------------------

// ErrPlanExpired 表示待确认的计划不存在或已过期。
var ErrPlanExpired = errors.New(i18n.T("change.err.plan_expired"))

// Prepare 生成一个待确认的计划。
//
// 它是"预览"这一步：**不产生任何系统变更**，只登记计划并返回描述。
// 用户确认后再调用 ApplyPending。
func (r *Runner) Prepare(_ context.Context, plan Plan) (Preview, error) {
	if err := plan.Validate(); err != nil {
		// 空计划没有步骤，Validate 会因为缺步骤而通过，但它的
		// ID/标题仍然必须齐全 —— 那是界面渲染的依据。
		return Preview{}, err
	}

	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pending == nil {
		r.pending = NewPending()
	}
	return r.pending.Put(plan), nil
}

// ApplyPending 应用一个此前 Prepare 过的计划。
//
// 计划在取出时即被消费，因此重复调用会得到 ErrPlanExpired ——
// 用户双击"应用"不会执行两次。
func (r *Runner) ApplyPending(ctx context.Context, planID string) (Result, error) {
	r.pendingMu.Lock()
	pending := r.pending
	r.pendingMu.Unlock()

	if pending == nil {
		return Result{PlanID: planID, Status: StatusFailed}, ErrPlanExpired
	}

	plan, ok := pending.Take(planID)
	if !ok {
		return Result{PlanID: planID, Status: StatusFailed}, ErrPlanExpired
	}

	r.log.Info("用户确认执行变更", "plan", plan.ID, "kind", plan.Kind, "risk", plan.Risk)
	return r.Apply(ctx, plan)
}

// DiscardPending 丢弃一个待确认的计划。
func (r *Runner) DiscardPending(planID string) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pending != nil {
		r.pending.Discard(planID)
	}
}

// Pending 返回待确认的计划列表。
func (r *Runner) PendingList() []Preview {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pending == nil {
		return nil
	}
	return r.pending.List()
}

// GetPending 取出一条待确认的预览。
func (r *Runner) GetPending(planID string) (Preview, bool) {
	r.pendingMu.Lock()
	defer r.pendingMu.Unlock()
	if r.pending == nil {
		return Preview{}, false
	}
	return r.pending.Get(planID)
}

// NextPlanID 生成一个计划 ID。
//
// 由本包统一生成而不是让调用方自己拼：ID 会进入日志、事件与客户端，
// 格式不一致会让"从日志里找到那个计划"变得麻烦。
func NextPlanID(kind string) string {
	return fmt.Sprintf("%s-%d", kind, time.Now().UTC().UnixNano())
}
