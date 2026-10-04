// Package job 是内核的异步任务引擎。
//
// 存在的理由见 docs/DECISIONS.md D08：证书签发这类操作要等几十秒到几分钟，
// 做成同步 REST 会让客户端超时或假死。因此所有可能慢的操作都返回 job_id，
// 进度通过事件流推送。
//
// 关键约束：
//
//   - 任务状态必须**可持久化**（进程重启后仍能查询），因此引擎只依赖
//     Store 接口，不直接依赖数据库；
//   - 取消是**协作式**的：任务实现必须尊重传入 ctx；
//   - 每个状态变化都同时落库并发布事件，二者不可偏废 —— 只发事件会让
//     重连的客户端丢失中间状态，只落库则界面不会动。
package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// Status 是任务状态。
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// Finished 报告该状态是否为终态。
func (s Status) Finished() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// Error 是任务的失败信息。
//
// 之所以不直接用 HTTP 层的 Problem 类型：引擎不应依赖接口层，
// 否则以后换传输协议（gRPC / 消息队列）就要动领域代码。
type Error struct {
	// Code 是稳定的机器可读错误码，不随语言变化。
	Code string `json:"code"`
	// Title 是简短摘要（已本地化）。
	Title string `json:"title"`
	// Detail 是具体说明（已本地化）。
	Detail string `json:"detail,omitempty"`
}

// Error 实现 error，便于在任务实现里直接返回。
func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code + ": " + e.Title
	}
	return e.Code + ": " + e.Title + " (" + e.Detail + ")"
}

// Job 是一个任务。
type Job struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Status   Status          `json:"status"`
	Progress float64         `json:"progress"`
	Message  string          `json:"message,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Err      *Error          `json:"error,omitempty"`

	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Reporter 供任务实现上报进度。
type Reporter interface {
	// Progress 上报进度。fraction 会被夹到 [0, 1]。
	//
	// message 应当已经本地化，且描述**当前正在做什么**而不是"完成了百分之几"。
	Progress(fraction float64, message string)
}

// Func 是任务体。
//
// 返回值会被序列化为 Result；返回 *Error 或 error 都会让任务进入 failed。
// 若 ctx 被取消，任务应当尽快返回，引擎会把它标记为 canceled。
type Func func(ctx context.Context, r Reporter) (any, error)

// Filter 是任务列表的查询条件。
type Filter struct {
	// Statuses 为空表示不过滤。
	Statuses []Status
	// Kind 为空表示不过滤。
	Kind string
	// Limit 为 0 时使用默认值。
	Limit int
	// Cursor 取自上一次响应的 next_cursor。
	Cursor string
}

// Store 是任务状态的持久化后端。
//
// M0 阶段使用 MemoryStore；M1 接入 SQLite 后换实现，
// 引擎本身不需要改动 —— 这就是把它抽成接口的目的。
type Store interface {
	Save(ctx context.Context, j Job) error
	Get(ctx context.Context, id string) (Job, bool, error)
	List(ctx context.Context, f Filter) (jobs []Job, nextCursor string, err error)
}

// DefaultLimit 是任务列表的默认页大小。
const DefaultLimit = 50

// maxLimit 是任务列表的最大页大小。
const maxLimit = 200

// DefaultMaxConcurrent 是同时执行的任务数上限。
//
// 取 2 是因为 v0.2.0 的任务体都很重（下载数百 MB 的运行时、npm install、
// 构建），而目标机器是家用 Mac。不设上限的话，"一次给三个预设准备环境"
// 会变成三份并发的大流量下载加三份并发编译 —— 结果是谁都慢，
// 而且在低配机器上会把内存和磁盘 IO 一起打满。
const DefaultMaxConcurrent = 2

// Engine 是任务引擎。
type Engine struct {
	bus   *event.Bus
	store Store
	log   *slog.Logger

	// rootCtx 是全部任务的父上下文：引擎关闭时统一取消。
	rootCtx context.Context

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
	wg      sync.WaitGroup
	closed  bool

	// sem 限制**同时执行**的任务数。
	//
	// 提交仍然是立即返回的：任务先落库为 pending 并占一个 goroutine，
	// 由该 goroutine 去抢槽位。这样"排队"对调用方是透明的 ——
	// Submit 的语义不变，而执行被限流。
	//
	// 注意：**不保证先进先出**。抢槽位靠的是通道发送，而 goroutine 的
	// 调度顺序不是提交顺序。要严格 FIFO 就得引入票据队列，而那会带来
	// "取消与转交位置同时发生"的竞态，收益（几个互相独立的重任务之间
	// 的先后）不值这个复杂度。需要顺序的场景应当由调用方串行提交。
	sem chan struct{}

	// kinds 是已登记的任务类型。
	//
	// 为空表示**不做校验**：库的使用者与单元测试可以自由提交。
	// daemon 在装配时登记生产用的那几个，此后拼错的 kind 会在提交时
	// 就被拒绝，而不是变成一个永远查不出原因的任务。
	kinds map[string]bool
}

// NewEngine 构造任务引擎。
//
// rootCtx 取消时，全部在途任务会被取消；Shutdown 会等待它们收尾。
func NewEngine(rootCtx context.Context, bus *event.Bus, store Store, log *slog.Logger) *Engine {
	if store == nil {
		store = NewMemoryStore()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		bus:     bus,
		store:   store,
		log:     log,
		rootCtx: rootCtx,
		cancels: make(map[string]context.CancelFunc),
		sem:     make(chan struct{}, DefaultMaxConcurrent),
		kinds:   make(map[string]bool),
	}
}

// SetMaxConcurrent 调整同时执行的任务数上限。
//
// **必须在第一个 Submit 之前调用**：它会替换信号量，而已经在排队或执行
// 的任务持有的是替换前那个通道。daemon 在装配阶段调用，符合这个前提。
func (e *Engine) SetMaxConcurrent(n int) {
	if n < 1 {
		n = 1
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sem = make(chan struct{}, n)
}

// RegisterKinds 登记内核支持的任务类型。
//
// 登记之后，Submit 会拒绝未登记的 kind —— 拼错一个字母会立刻报错，
// 而不是留下一个永远失败的任务。**未登记任何 kind 时不做校验**，
// 这样库的使用者与单元测试不必先注册。
func (e *Engine) RegisterKinds(kinds ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, kind := range kinds {
		e.kinds[kind] = true
	}
}

// CodeInterrupted 是"内核在任务执行期间退出"的错误码。
//
// 复用 failed 状态而不新增一个 interrupted 状态：状态枚举是接口契约
// （GUI 与 CLI 都按它分支），而"上次运行没跑完"与"跑完但失败"对调用方
// 的处置是同一件事 —— 看错误码就够了。
const CodeInterrupted = "job_interrupted"

// RecoverInterrupted 把上一次内核运行遗留的 pending / running 任务归位。
//
// # 为什么必须做
//
// 任务状态存在 SQLite 里，而任务体是进程内的 goroutine：内核退出后，
// 那些行会永远停留在 running。界面于是显示一堆"正在执行"的任务，
// 而实际上什么都没有在跑 —— 没有任何东西会去修正它们。
//
// 归位为 failed + CodeInterrupted，并照常发 job.finished 事件：
// 订阅者因此能像对待任何一次失败那样更新自己的状态。
//
// 必须在接受新的 Submit 之前调用（daemon 在装配阶段调用）。
func (e *Engine) RecoverInterrupted(ctx context.Context) (int, error) {
	const maxRounds = 100
	total := 0
	for round := 0; round < maxRounds; round++ {
		// 每轮都从头取：被归位的行不再匹配 status 过滤条件，
		// 因此游标会自然收敛（用游标反而会跳过它们）。
		jobs, _, err := e.store.List(ctx, Filter{
			Statuses: []Status{StatusPending, StatusRunning},
			Limit:    maxLimit,
		})
		if err != nil {
			return total, err
		}
		if len(jobs) == 0 {
			return total, nil
		}
		for _, j := range jobs {
			finished := time.Now().UTC()
			j.Status = StatusFailed
			j.Err = &Error{
				Code:   CodeInterrupted,
				Title:  i18n.T("job.err.interrupted"),
				Detail: i18n.T("job.err.interrupted_detail"),
			}
			j.FinishedAt = &finished
			if err := e.store.Save(ctx, j); err != nil {
				return total, err
			}
			total++
			if e.bus != nil {
				e.bus.Publish(event.TypeJobFinished, jobFinishedPayload{JobID: j.ID, Kind: j.Kind, Status: StatusFailed, Error: j.Err})
			}
		}
	}
	return total, nil
}

// jobProgressPayload 是 job.progress 事件的载荷。
type jobProgressPayload struct {
	JobID    string  `json:"job_id"`
	Kind     string  `json:"kind"`
	Progress float64 `json:"progress"`
	Message  string  `json:"message,omitempty"`
}

// jobFinishedPayload 是 job.finished 事件的载荷。
type jobFinishedPayload struct {
	JobID  string `json:"job_id"`
	Kind   string `json:"kind"`
	Status Status `json:"status"`
	Error  *Error `json:"error,omitempty"`
}

// Submit 提交一个任务并立即返回。
//
// 返回的 Job 处于 pending 状态；实际执行在后台 goroutine 中进行。
func (e *Engine) Submit(ctx context.Context, kind string, fn Func) (Job, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return Job{}, errors.New(i18n.T("job.err.closed"))
	}
	// 只有登记过 kind 之后才校验：未登记时（库的使用者、单元测试）
	// 保持"什么 kind 都能提交"的宽松语义。
	if len(e.kinds) > 0 && !e.kinds[kind] {
		e.mu.Unlock()
		return Job{}, fmt.Errorf(i18n.T("job.err.unknown_kind"), kind)
	}
	e.mu.Unlock()

	id, err := newID()
	if err != nil {
		return Job{}, err
	}

	j := Job{
		ID:        id,
		Kind:      kind,
		Status:    StatusPending,
		Progress:  0,
		CreatedAt: time.Now().UTC(),
	}
	if err := e.store.Save(ctx, j); err != nil {
		return Job{}, fmt.Errorf(i18n.T("job.err.persist"), err)
	}

	// 任务上下文派生自 rootCtx，这样引擎关闭能统一取消在途任务。
	jobCtx, cancel := context.WithCancel(e.rootCtx)

	e.mu.Lock()
	e.cancels[id] = cancel
	e.wg.Add(1)
	e.mu.Unlock()

	go e.run(jobCtx, cancel, j, fn)

	return j, nil
}

// run 在后台执行任务。
func (e *Engine) run(ctx context.Context, cancel context.CancelFunc, j Job, fn Func) {
	defer e.wg.Done()
	defer cancel()
	defer func() {
		e.mu.Lock()
		delete(e.cancels, j.ID)
		e.mu.Unlock()
	}()

	// 先抢执行槽位；抢不到就在队列里等着，任务保持 pending。
	//
	// 排队期间被取消（Cancel 或引擎关闭）时直接以 canceled 收尾 ——
	// 注意此时**不能**调用 fn：用户取消的是"还没开始的任务"，
	// 让任务体跑起来再取消会白白做一遍副作用。
	e.mu.Lock()
	sem := e.sem
	e.mu.Unlock()
	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-ctx.Done():
		e.finish(ctx, j, StatusCanceled, nil, nil)
		return
	}

	// panic 必须被拦住：一个任务实现里的空指针不应该带崩整个内核，
	// 而应该表现为"这个任务失败了"。
	defer func() {
		if r := recover(); r != nil {
			e.log.Error("任务 panic", "job_id", j.ID, "kind", j.Kind, "panic", r)
			e.finish(context.WithoutCancel(ctx), j, StatusFailed, nil, &Error{
				Code:   "job_panic",
				Title:  i18n.T("error.internal"),
				Detail: fmt.Sprintf("%v", r),
			})
		}
	}()

	started := time.Now().UTC()
	j.Status = StatusRunning
	j.StartedAt = &started
	e.persist(ctx, j)

	rep := &reporter{engine: e, job: j}
	result, err := fn(ctx, rep)

	// 用最新的任务快照收尾：rep 在过程中改过 Progress / Message。
	e.finish(ctx, rep.snapshot(), statusFor(ctx, err), result, toJobError(err))
}

// statusFor 根据 ctx 与 err 判定终态。
//
// ctx 被取消优先于 err：任务在收到取消信号后返回的任何错误
// 都不应该被当成"业务失败"，否则通知与审计会误报。
func statusFor(ctx context.Context, err error) Status {
	if ctx.Err() != nil {
		return StatusCanceled
	}
	if err != nil {
		return StatusFailed
	}
	return StatusSucceeded
}

// toJobError 把任意 error 归一化为 *Error。
func toJobError(err error) *Error {
	if err == nil {
		return nil
	}
	var je *Error
	if errors.As(err, &je) {
		return je
	}
	return &Error{Code: "job_failed", Title: i18n.T("error.internal"), Detail: err.Error()}
}

// finish 写入终态、落库并发布事件。
func (e *Engine) finish(ctx context.Context, j Job, status Status, result any, jobErr *Error) {
	now := time.Now().UTC()
	j.Status = status
	j.FinishedAt = &now
	j.Err = jobErr
	if status == StatusSucceeded {
		j.Progress = 1
		if result != nil {
			if byt, err := json.Marshal(result); err == nil {
				j.Result = byt
			} else {
				e.log.Warn("任务结果序列化失败", "job_id", j.ID, "err", err)
			}
		}
	}
	if status == StatusCanceled && j.Message == "" {
		j.Message = i18n.T("job.canceled")
	}

	e.persist(ctx, j)

	e.bus.Publish(event.TypeJobFinished, jobFinishedPayload{
		JobID:  j.ID,
		Kind:   j.Kind,
		Status: status,
		Error:  jobErr,
	})
}

// persist 落库，失败只记日志不中断任务。
//
// 取舍说明：落库失败意味着重启后查不到这个任务的历史，是**可接受的降级**；
// 而因此中断任务则会让用户的证书签发半途而废，代价大得多。
func (e *Engine) persist(ctx context.Context, j Job) {
	// 用 WithoutCancel：任务被取消时仍要把它"已取消"的状态写进去。
	if err := e.store.Save(context.WithoutCancel(ctx), j); err != nil {
		e.log.Error("任务状态落库失败", "job_id", j.ID, "err", err)
	}
}

// Get 查询单个任务。
func (e *Engine) Get(ctx context.Context, id string) (Job, bool, error) {
	return e.store.Get(ctx, id)
}

// List 查询任务列表。
func (e *Engine) List(ctx context.Context, f Filter) ([]Job, string, error) {
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > maxLimit {
		f.Limit = maxLimit
	}
	return e.store.List(ctx, f)
}

// Cancel 请求取消一个任务。
//
// 返回更新后的任务；任务已处于终态时返回 ErrNotCancelable。
func (e *Engine) Cancel(ctx context.Context, id string) (Job, error) {
	e.mu.Lock()
	cancel, ok := e.cancels[id]
	e.mu.Unlock()

	j, found, err := e.store.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if !found {
		return Job{}, ErrNotFound
	}

	// **终态的任务不可取消，即使它还挂在在途表里。**
	//
	// # 这道判断为什么必需
	//
	// 任务的收尾顺序是：任务体返回前先把状态落库（succeeded / failed），
	// 之后才轮到 run 的 defer 把它从 e.cancels 里摘掉 —— 中间还夹着
	// 日志与事件发布。
	//
	// 于是存在一道真实的缝隙：状态已经是终态，但在途表里还有它。
	// 只看在途表会得出"可以取消"的结论，然后 cancel() 一个**早就做完**
	// 的任务，并向客户端报告成功。
	//
	// 这个缺陷最初是以"测试偶发失败"的形式出现的（
	// TestCancelFinishedJobRejected 在并发负载下挂掉、单独跑却稳定通过）。
	// 它不只是测试问题：客户端会以为它取消掉了一件事，而那件事其实
	// 已经完成，并且可能已经产生了副作用。
	if j.Status.Finished() {
		return j, ErrNotCancelable
	}

	if !ok {
		return j, ErrNotCancelable
	}

	cancel()

	// 取消是**异步**的：这里只发出信号，任务体随后才会观察到
	// 上下文被取消并转入 canceled。因此返回的是此刻的状态
	//（仍是 running），客户端应当通过事件流或轮询看最终结果。
	return j, nil
}

// Shutdown 取消全部在途任务并等待它们收尾。
//
// 超时返回错误，但不会强杀 goroutine —— Go 没有安全的强杀手段，
// 强行退出进程由调用方决定。
func (e *Engine) Shutdown(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	cancels := make([]context.CancelFunc, 0, len(e.cancels))
	for _, c := range e.cancels {
		cancels = append(cancels, c)
	}
	e.mu.Unlock()

	for _, c := range cancels {
		c()
	}

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf(i18n.T("job.err.drain_timeout"), ctx.Err())
	}
}

// 哨兵错误。
var (
	// ErrNotFound 表示任务不存在。
	ErrNotFound = errors.New(i18n.T("job.err.not_found"))
	// ErrNotCancelable 表示任务已处于终态，无法取消。
	ErrNotCancelable = errors.New(i18n.T("job.err.finished"))
)

// ---------------------------------------------------------------------------
// Reporter
// ---------------------------------------------------------------------------

type reporter struct {
	engine *Engine
	mu     sync.Mutex
	job    Job
}

// Progress 实现 Reporter。
func (r *reporter) Progress(fraction float64, message string) {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}

	r.mu.Lock()
	r.job.Progress = fraction
	if message != "" {
		r.job.Message = message
	}
	snapshot := r.job
	r.mu.Unlock()

	r.engine.persist(context.Background(), snapshot)
	r.engine.bus.Publish(event.TypeJobProgress, jobProgressPayload{
		JobID:    snapshot.ID,
		Kind:     snapshot.Kind,
		Progress: fraction,
		Message:  snapshot.Message,
	})
}

// snapshot 返回当前任务快照。
func (r *reporter) snapshot() Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}
