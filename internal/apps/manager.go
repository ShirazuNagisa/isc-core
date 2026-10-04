package apps

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 错误哨兵。调用方按它们分支，不要匹配文案。
var (
	// ErrNotFound 表示应用不存在。
	ErrNotFound = errors.New("app not found")
	// ErrBusy 表示应用正在状态切换中。
	ErrBusy = errors.New("app is busy")
	// ErrNotRunning 表示应用当前没有在运行。
	ErrNotRunning = errors.New("app is not running")
	// ErrNoRuntime 表示该应用需要的运行时不可用。
	ErrNoRuntime = errors.New("the required runtime is not available")
	// ErrUnhealthy 表示应用起来了但没通过健康检查。
	ErrUnhealthy = errors.New("app did not become healthy")
)

// 时间参数。
//
// 它们是**变量**而不是常量，唯一的理由是可测试性：一个"健康检查超时"
// 或"崩溃重启退避"的测试若必须真等 60 秒或 7 秒，就不会有人愿意跑它。
const (
	// stopGrace 是先 SIGTERM 再 SIGKILL 之间的等待。
	stopGrace = 5 * time.Second
	// healthInterval 是健康探测的间隔。
	healthInterval = 500 * time.Millisecond
)

var (
	// healthTimeout 是启动后等待健康的最长时间。
	healthTimeout = 60 * time.Second
	// restartBackoffBase 是崩溃重启的退避基数（1s、2s、4s…）。
	restartBackoffBase = time.Second
)

// Manager 管理应用的生命周期。
type Manager struct {
	store    Store
	binder   Binder
	runtimes *runtime.Manager
	procs    platform.ProcessController
	logs     *LogStore
	bus      *event.Bus
	log      *slog.Logger
	dataRoot string

	mu      sync.Mutex
	running map[string]*instance

	// 以下三个在测试中替换，避免依赖真实时间与网络栈。
	now        func() time.Time
	allocate   func() (int, error)
	httpClient *http.Client
}

// Deps 是构造 Manager 所需的依赖。
type Deps struct {
	Store Store
	// Binder 可为空：不接公网的应用（纯内网服务）不需要它。
	Binder    Binder
	Runtimes  *runtime.Manager
	Processes platform.ProcessController
	Logs      *LogStore
	Bus       *event.Bus
	Log       *slog.Logger
	DataRoot  string
}

// NewManager 构造应用管理器。
func NewManager(d Deps) *Manager {
	logger := d.Log
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		store:    d.Store,
		binder:   d.Binder,
		runtimes: d.Runtimes,
		procs:    d.Processes,
		logs:     d.Logs,
		bus:      d.Bus,
		log:      logger,
		dataRoot: d.DataRoot,
		running:  make(map[string]*instance),
		now:      time.Now,
		allocate: allocatePort,
		httpClient: &http.Client{
			Timeout: 3 * time.Second,
			// 健康探测只打本机，不要走系统代理。
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

// instance 是一个正在运行的应用（运行期状态，不落库）。
type instance struct {
	app      App
	process  platform.Process
	server   *http.Server
	listener net.Listener

	// stopRequested 区分"我们让它停的"与"它自己崩了"。
	// 不区分的话，正常停止会被当成崩溃而触发重启。
	//
	// 用原子量而不是普通字段：它由 Stop/Delete/Shutdown 写、由 supervise
	// 的 goroutine 读，而两者不在同一把锁的保护范围内（-race 抓到过）。
	stopRequested atomic.Bool

	// restarts 只由该实例自己的 supervise goroutine 读写。
	restarts int

	// startedAt 是本次运行开始的时间，用于汇报运行时长。
	startedAt time.Time
}

// RunningApp 描述一个正在运行的站点。
//
// 指标采样需要知道"有哪些站点在跑、各自的进程号是多少"。静态站点没有
// 独立进程，PID 为 0 —— 采样器据此跳过它，而不是去测一个不存在的进程。
type RunningApp struct {
	ID        string
	PID       int
	StartedAt time.Time
}

// Running 返回当前正在运行的站点。
func (m *Manager) Running() []RunningApp {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RunningApp, 0, len(m.running))
	for id, inst := range m.running {
		entry := RunningApp{ID: id, StartedAt: inst.startedAt}
		if inst.process != nil {
			entry.PID = inst.process.PID()
		}
		out = append(out, entry)
	}
	return out
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

// List 返回全部应用。
func (m *Manager) List(ctx context.Context) ([]App, error) {
	apps, err := m.store.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	// 状态以内存里的运行期状态为准：库里的行可能落后于刚刚发生的
	// 状态变化，而界面看到的东西必须和实际在跑的东西一致。
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range apps {
		if inst, ok := m.running[apps[i].ID]; ok {
			apps[i].State = inst.app.State
			apps[i].Health = inst.app.Health
			apps[i].HealthDetail = inst.app.HealthDetail
		}
	}
	return apps, nil
}

// Get 返回单个应用。
func (m *Manager) Get(ctx context.Context, id string) (App, bool, error) {
	app, ok, err := m.store.GetApp(ctx, id)
	if err != nil || !ok {
		return App{}, ok, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if inst, running := m.running[id]; running {
		app.State = inst.app.State
		app.Health = inst.app.Health
		app.HealthDetail = inst.app.HealthDetail
	}
	return app, true, nil
}

// Logs 返回应用的近期输出。
func (m *Manager) Logs(id string, tail int) []string {
	if m.logs == nil {
		return []string{}
	}
	return m.logs.Tail(id, tail)
}

// ---------------------------------------------------------------------------
// 创建
// ---------------------------------------------------------------------------

// Create 登记一个应用：识别源码、生成构建方案、分配端口，然后落库。
//
// 它**不**执行任何构建步骤；部署是 Deploy 的事。分开是为了让"登记"
// 保持即时（界面能立刻显示出来），而把耗时动作放进可取消的任务。
func (m *Manager) Create(ctx context.Context, spec CreateSpec) (App, error) {
	if spec.Name == "" {
		return App{}, errors.New("app name is required")
	}
	resolved, err := filepath.EvalSymlinks(spec.SourcePath)
	if err != nil {
		return App{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return App{}, err
	}
	if !info.IsDir() {
		return App{}, fmt.Errorf("%w: %s", presets.ErrNotADirectory, spec.SourcePath)
	}

	preset, err := presets.Lookup(spec.PresetID)
	if err != nil {
		return App{}, err
	}

	port := spec.Port
	if port == 0 {
		if m.allocate == nil {
			return App{}, errors.New("port allocation is unavailable")
		}
		if port, err = m.allocate(); err != nil {
			return App{}, err
		}
	}
	if port < 1 || port > 65535 {
		return App{}, fmt.Errorf("port %d is out of range", port)
	}

	inspection, err := presets.Inspect(resolved)
	if err != nil {
		return App{}, err
	}

	plan, err := presets.BuildPlan(resolved, preset, inspection.Facts, port)
	if err != nil {
		return App{}, err
	}
	if spec.CustomRun != nil {
		// 自定义服务器：命令由用户提供，替换掉目录里的占位。
		plan.Run = *spec.CustomRun
	}
	if plan.Run.Executable == "" && preset.Kind != runtime.KindNone {
		return App{}, fmt.Errorf("%w: the plan has no start command", presets.ErrNotRunnable)
	}

	app := App{
		ID:          newAppID(),
		Name:        spec.Name,
		PresetID:    preset.ID,
		Kind:        preset.Kind,
		SourcePath:  resolved,
		LocalPort:   port,
		State:       StateDraft,
		Health:      HealthUnknown,
		AutoStart:   spec.AutoStart == nil || *spec.AutoStart,
		MaxRestarts: DefaultMaxRestarts,
		Plan:        plan,
		Domains:     spec.Domains,
		CreatedAt:   m.now().UTC(),
		UpdatedAt:   m.now().UTC(),
	}
	if spec.MaxRestarts != nil {
		app.MaxRestarts = *spec.MaxRestarts
	}
	if err := m.store.SaveApp(ctx, app); err != nil {
		return App{}, err
	}
	m.publish(event.TypeAppStateChanged, app)
	return app, nil
}

// ---------------------------------------------------------------------------
// 部署
// ---------------------------------------------------------------------------

// Deploy 执行完整流程：准备运行时 → 装依赖 → 构建 → 启动 → 等健康。
//
// report 可为 nil；取值 0..1，消息已本地化。
func (m *Manager) Deploy(ctx context.Context, id string, report func(float64, string)) error {
	app, ok, err := m.store.GetApp(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if app.State.Busy() {
		return fmt.Errorf("%w: state is %s", ErrBusy, app.State)
	}

	// 阶段权重：准备运行时最重（可能是几百 MB 下载），其余各占一小部分。
	stage := func(from, to float64) func(float64, string) {
		return func(fraction float64, message string) {
			if report == nil {
				return
			}
			report(from+fraction*(to-from), message)
		}
	}

	if err := m.runInstall(ctx, &app, stage(0.6, 0.75), report); err != nil {
		return m.fail(ctx, app, err)
	}
	if err := m.runBuild(ctx, &app, stage(0.75, 0.9)); err != nil {
		return m.fail(ctx, app, err)
	}
	if err := m.setState(ctx, &app, StateStarting); err != nil {
		return err
	}
	if err := m.start(ctx, &app, stage(0.9, 1)); err != nil {
		return m.fail(ctx, app, err)
	}
	// 公网绑定放在本地可用**之后**并且是尽力而为：站点已经跑起来了，
	// 不该因为证书或 DNS 一时没就绪就把整个部署判成失败。
	m.bind(ctx, &app)
	return nil
}

// bind 把应用的域名接到反向代理上，并触发证书申请。
//
// # 为什么失败不算部署失败
//
// 绑定依赖外部条件（域名是否已解析到本机、DNS 凭据是否可用、CA 是否
// 可达）。把"本地已经能访问"的站点判成"部署失败"，会让用户在面对一个
// 真正可用的服务时以为一切都白做了。因此这里只记录，不升级为失败 ——
// 公网侧的状态由 /v1/apps/{id} 如实报出，原因由建议引擎解释。
func (m *Manager) bind(ctx context.Context, app *App) {
	if m.binder == nil || len(app.Domains) == 0 {
		return
	}
	routeID := app.RouteID
	for _, domain := range app.Domains {
		id, err := m.binder.EnsureRoute(ctx, *app, domain)
		if err != nil {
			m.log.Warn("could not bind a domain to the reverse proxy",
				"app_id", app.ID, "domain", domain, "err", err)
			continue
		}
		if id != "" {
			routeID = id
		}
	}
	app.RouteID = routeID
	_ = m.persist(ctx, *app)
}

// runInstall 准备运行时并执行安装步骤。
func (m *Manager) runInstall(ctx context.Context, app *App, progress func(float64, string), report func(float64, string)) error {
	if app.Kind != runtime.KindNone && app.Kind != runtime.KindDocker {
		if m.runtimes == nil {
			return fmt.Errorf("%w: %s", ErrNoRuntime, app.Kind)
		}
		if err := m.setState(ctx, app, StateProvisioning); err != nil {
			return err
		}
		// 用预设声明的最低版本，而不是"随便什么版本都行"：一个需要
		// Node 18 的项目装在 Node 16 上，失败会以"语法错误 / 不支持的特性"
		// 的形式出现在应用日志里，离真正的原因很远。
		minVersion := ""
		if preset, err := presets.Lookup(app.PresetID); err == nil {
			minVersion = preset.MinVersion
		}
		installed, err := m.runtimes.Provision(ctx, app.Kind, minVersion, progress)
		if err != nil {
			return err
		}
		app.toolchain = installed
	}
	if len(app.Plan.Install) == 0 {
		return nil
	}
	if err := m.setState(ctx, app, StateInstalling); err != nil {
		return err
	}
	for _, step := range app.Plan.Install {
		if err := m.runStep(ctx, *app, step, progress); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) runBuild(ctx context.Context, app *App, progress func(float64, string)) error {
	if len(app.Plan.Build) == 0 {
		return nil
	}
	if err := m.setState(ctx, app, StateBuilding); err != nil {
		return err
	}
	for _, step := range app.Plan.Build {
		if err := m.runStep(ctx, *app, step, progress); err != nil {
			return err
		}
	}
	return nil
}

// runStep 执行一个构建步骤并等待它结束。
func (m *Manager) runStep(ctx context.Context, app App, step presets.Step, progress func(float64, string)) error {
	if m.procs == nil {
		return fmt.Errorf("%w: process control is unavailable on this platform", platform.ErrNotImplemented)
	}
	// 执行前渲染端口占位符 —— 端口可能刚被重新分配过。
	step = presets.RenderStep(step, app.LocalPort)
	dir := app.SourcePath
	if step.Dir != "" {
		dir = filepath.Join(app.SourcePath, step.Dir)
	}
	executable := step.Executable
	if !filepath.IsAbs(executable) {
		// 相对路径是"运行自己构建出来的产物"，按源码根解析。
		if candidate := filepath.Join(app.SourcePath, executable); fileExists(candidate) {
			executable = candidate
		}
	}
	process, err := m.procs.Start(platform.ProcessSpec{
		Executable: executable,
		Args:       step.Args,
		Dir:        dir,
		Env:        m.envFor(app, step.Env),
		OnOutput: func(chunk string) {
			if m.logs != nil {
				m.logs.Append(app.ID, chunk)
			}
		},
	})
	if err != nil {
		return fmt.Errorf("start %s: %w", step.Executable, err)
	}
	status, err := process.Wait()
	if err != nil {
		return err
	}
	if status.Code != 0 || status.Signaled {
		return fmt.Errorf("%s failed (%s)", step.Executable, status)
	}
	return nil
}

// envFor 组装子进程环境。
//
// 三段：最小默认集（D30：不继承内核环境）+ 运行时工具链目录 + 计划里
// 声明的变量。把运行时的 bin 放进 PATH 是关键 —— 否则托管运行时里的
// npm/pip 会因为"找不到命令"失败，而用户看到的只是"装依赖失败"。
func (m *Manager) envFor(app App, extra []string) []string {
	env := platform.MinimalEnv()
	path := ""
	for _, kv := range env {
		if len(kv) > 5 && kv[:5] == "PATH=" {
			path = kv[5:]
		}
	}
	if app.toolchain.Executable != "" {
		binDir := filepath.Dir(app.toolchain.Executable)
		path = binDir + string(os.PathListSeparator) + path
		env = append(env, "ISC_RUNTIME_ROOT="+app.toolchain.Root)
	}
	env = append(env, "PATH="+path)
	if app.toolchain.Kind == runtime.KindJava && app.toolchain.Root != "" {
		// Maven/Gradle 靠 JAVA_HOME 找 JDK。
		env = append(env, "JAVA_HOME="+filepath.Join(app.toolchain.Root, "Contents", "Home"))
	}
	if app.toolchain.Kind == runtime.KindDotNet && app.toolchain.Root != "" {
		env = append(env, "DOTNET_ROOT="+app.toolchain.Root)
	}
	return append(env, extra...)
}

// ---------------------------------------------------------------------------
// 启动与停止
// ---------------------------------------------------------------------------

// Start 启动一个已经部署过的应用（不重新装依赖或构建）。
func (m *Manager) Start(ctx context.Context, id string) error {
	app, ok, err := m.store.GetApp(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if app.State.Busy() {
		return fmt.Errorf("%w: state is %s", ErrBusy, app.State)
	}
	if m.isRunning(id) {
		return nil
	}
	return m.start(ctx, &app, nil)
}

// start 真正把应用跑起来（外部触发的启动，重启计数归零）。
func (m *Manager) start(ctx context.Context, app *App, progress func(float64, string)) error {
	return m.startWithRestarts(ctx, app, progress, 0)
}

// startWithRestarts 是 start 的实现，额外接收"本次运行已经重启过几次"。
//
// 这个参数必须显式传进来：每次重启都会创建一个新的实例，若计数跟着
// 新实例从 0 开始，"重启到上限就停下"这条规则永远不会触发 —— 崩溃循环
// 会无限重试，而界面上一片"正在重启"。
func (m *Manager) startWithRestarts(ctx context.Context, app *App, progress func(float64, string), restarts int) error {
	if err := m.ensurePortFree(app); err != nil {
		return err
	}

	// 渲染后的计划才是真正要执行的东西（见 presets.BuildPlan 的说明）。
	plan := presets.Render(app.Plan, app.LocalPort)
	inst := &instance{app: *app, restarts: restarts}
	if app.Kind == runtime.KindNone && plan.Run.Executable == "" {
		// 静态站点：内核自己托管，不需要外部进程，也不需要运行时。
		if err := m.startStatic(inst); err != nil {
			return err
		}
	} else {
		if m.procs == nil {
			return fmt.Errorf("%w: process control is unavailable on this platform", platform.ErrNotImplemented)
		}
		process, err := m.procs.Start(platform.ProcessSpec{
			Executable: plan.Run.Executable,
			Args:       plan.Run.Args,
			Dir:        filepath.Join(app.SourcePath, plan.Run.Dir),
			Env:        m.envFor(*app, plan.Run.Env),
			OnOutput: func(chunk string) {
				if m.logs != nil {
					m.logs.Append(app.ID, chunk)
				}
			},
		})
		if err != nil {
			return err
		}
		inst.process = process
	}

	inst.startedAt = m.now()
	m.mu.Lock()
	m.running[app.ID] = inst
	m.mu.Unlock()

	app.State = StateRunning
	app.Health = HealthStarting
	app.HealthDetail = ""
	app.RestartCount = restarts
	m.updateInstance(app.ID, *app)
	if err := m.persist(ctx, *app); err != nil {
		return err
	}
	m.publish(event.TypeAppStateChanged, *app)

	if err := m.waitHealthy(ctx, *app); err != nil {
		// 起来了但没通过健康检查。
		//
		// **启动阶段崩溃不重启**：进程在还没健康时就退出，几乎总是配置或
		// 构建问题，重启只是把同一个失败重放一遍。这里清理干净并如实报失败，
		// 让用户去看日志。运行期崩溃才走 supervise 的退避重启。
		m.teardown(app.ID)
		return m.fail(ctx, *app, err)
	}

	app.Health = HealthHealthy
	m.updateInstance(app.ID, *app)
	if err := m.persist(ctx, *app); err != nil {
		return err
	}
	m.publish(event.TypeAppHealthChanged, *app)

	if inst.process != nil {
		go m.supervise(*app, inst)
	}
	if progress != nil {
		progress(1, "")
	}
	return nil
}

// startStatic 用内核自己的文件服务器托管静态站点。
//
// 静态站点不该为了"能打开"而先下载一个解释器：那是最常见的建站场景，
// 而它其实一个字节都不需要。因此静态站点由内核直接托管。
func (m *Manager) startStatic(inst *instance) error {
	address := fmt.Sprintf("127.0.0.1:%d", inst.app.LocalPort)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	handler := http.FileServer(http.Dir(inst.app.SourcePath))
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	inst.listener = listener
	inst.server = server
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Warn("static server stopped", "app_id", inst.app.ID, "err", err)
		}
	}()
	return nil
}

// Stop 停止应用。
func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	inst, ok := m.running[id]
	var snapshot App
	if ok {
		inst.stopRequested.Store(true)
		snapshot = inst.app
		delete(m.running, id)
	}
	m.mu.Unlock()
	if !ok {
		// 没在跑也算成功：停止的语义是"让它不跑"。
		app, found, err := m.store.GetApp(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		if app.State != StateStopped {
			app.State = StateStopped
			app.Health = HealthUnknown
			return m.persist(ctx, app)
		}
		return nil
	}
	m.stopInstance(inst)

	app := snapshot
	app.State = StateStopped
	app.Health = HealthUnknown
	app.HealthDetail = ""
	if err := m.persist(ctx, app); err != nil {
		return err
	}
	m.publish(event.TypeAppStateChanged, app)
	return nil
}

// stopInstance 停掉一个实例：先给收尾机会，超时再强杀。
func (m *Manager) stopInstance(inst *instance) {
	if inst.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
		_ = inst.server.Shutdown(shutdownCtx)
	}
	if inst.process != nil {
		_ = inst.process.Signal(platform.SignalTerminate)
		select {
		case <-inst.process.Done():
		case <-time.After(stopGrace):
			// 优雅退出没有兑现：强杀整组，否则它会一直占着端口。
			_ = inst.process.Signal(platform.SignalKill)
			<-inst.process.Done()
		}
	}
}

// updateInstance 在锁内刷新实例上的应用快照。
//
// `inst.app` 是 Get/List 用来报告**实时**状态的副本，因此它的每一次写入都
// 必须与那些读取用同一把锁 —— 否则 -race 会（也确实）在这里报出竞争。
func (m *Manager) updateInstance(id string, app App) {
	m.mu.Lock()
	if inst, ok := m.running[id]; ok {
		inst.app = app
	}
	m.mu.Unlock()
}

// instanceSnapshot 在锁内取一份实例上的应用快照。
func (m *Manager) instanceSnapshot(id string) (App, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inst, ok := m.running[id]
	if !ok {
		return App{}, false
	}
	return inst.app, true
}

// teardown 停掉一个实例的进程/服务器并把它从运行表里摘掉。
func (m *Manager) teardown(id string) {
	m.mu.Lock()
	inst, ok := m.running[id]
	if ok {
		inst.stopRequested.Store(true)
		delete(m.running, id)
	}
	m.mu.Unlock()
	if ok {
		m.stopInstance(inst)
	}
}

// supervise 在进程意外退出时按退避重启。
//
// 它是"崩溃循环"的唯一防线：没有它，一个启动即退出的应用会永远重启下去；
// 有了上限与退避，用户最终会看到明确的失败与原因。
func (m *Manager) supervise(app App, inst *instance) {
	status, err := inst.process.Wait()

	m.mu.Lock()
	current, still := m.running[app.ID]
	m.mu.Unlock()
	if !still || current != inst || inst.stopRequested.Load() {
		return // 我们自己停的，或者是被替换掉的旧实例。
	}

	detail := status.String()
	if err != nil {
		detail = err.Error()
	}
	reason := fmt.Sprintf("process exited unexpectedly (%s)", detail)

	if inst.restarts >= app.MaxRestarts {
		m.mu.Lock()
		delete(m.running, app.ID)
		m.mu.Unlock()
		failed, ok := m.instanceSnapshot(app.ID)
		if !ok {
			// 已经被替换掉（例如重启期间又被停了一次）：不该把它置为失败。
			failed = app
		}
		failed.State = StateFailed
		failed.Health = HealthUnhealthy
		failed.LastError = reason
		failed.HealthDetail = reason
		failed.RestartCount = inst.restarts
		_ = m.persist(context.Background(), failed)
		m.publish(event.TypeAppStateChanged, failed)
		return
	}

	inst.restarts++
	backoff := restartBackoffBase << (inst.restarts - 1)
	m.log.Info("restarting app after unexpected exit",
		"app_id", app.ID, "attempt", inst.restarts, "backoff", backoff.String(), "reason", reason)
	time.Sleep(backoff)

	m.mu.Lock()
	_, still = m.running[app.ID]
	m.mu.Unlock()
	if !still || inst.stopRequested.Load() {
		return
	}

	restarting, ok := m.instanceSnapshot(app.ID)
	if !ok {
		restarting = app
	}
	restarting.State = StateStarting
	_ = m.persist(context.Background(), restarting)

	if err := m.startWithRestarts(context.Background(), &restarting, nil, inst.restarts); err != nil {
		m.log.Warn("restart failed", "app_id", app.ID, "err", err)
	}
}

// ---------------------------------------------------------------------------
// 删除与恢复
// ---------------------------------------------------------------------------

// Delete 停止并删除一个应用。
//
// keepLogs 为 true 时保留磁盘日志：用户往往正是在"删掉它"之前想看一眼
// 它为什么一直起不来。
func (m *Manager) Delete(ctx context.Context, id string) error {
	app, ok, err := m.store.GetApp(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if err := m.Stop(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	// 先撤公网绑定再删记录：反过来的话，域名会继续指向一个已经不存在
	// 的本地端口，而用户看到的是 502。
	if m.binder != nil {
		for _, domain := range app.Domains {
			if err := m.binder.RemoveRoute(ctx, app, domain); err != nil {
				m.log.Warn("could not remove a public binding", "app_id", id, "domain", domain, "err", err)
			}
		}
	}
	if _, err := m.store.DeleteApp(ctx, id); err != nil {
		return err
	}
	if m.logs != nil {
		m.logs.Forget(id)
	}
	return nil
}

// Recover 在内核启动时把状态归位，并按需拉起应当自动启动的应用。
//
// # 为什么必须做
//
// 进程句柄活不过重启，但库里的行还写着 running。不归位的话，界面会显示
// 一堆"运行中"的应用，而实际上什么都没有在跑，且没有任何东西会修正它们。
func (m *Manager) Recover(ctx context.Context) (started int, err error) {
	apps, err := m.store.ListApps(ctx)
	if err != nil {
		return 0, err
	}
	for _, app := range apps {
		if !app.State.Active() {
			continue
		}
		if app.AutoStart {
			if startErr := m.Start(ctx, app.ID); startErr == nil {
				started++
				continue
			} else {
				m.log.Warn("could not restart app after kernel restart",
					"app_id", app.ID, "err", startErr)
			}
		}
		app.State = StateStopped
		app.Health = HealthUnknown
		app.HealthDetail = ""
		if err := m.persist(ctx, app); err != nil {
			return started, err
		}
		m.publish(event.TypeAppStateChanged, app)
	}
	return started, nil
}

// Shutdown 停止全部应用。
//
// 它必须自带超时并且**不依赖** daemon 的 drain 预算：逐个停止
// 可能要等好几秒，而 daemon 的总预算是 10 秒。
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	instances := make([]*instance, 0, len(m.running))
	ids := make([]string, 0, len(m.running))
	for id, inst := range m.running {
		// 在锁内先置标志：supervise 的 goroutine 会读它，
		// 放到并发段里写就又变成一次数据竞争。
		inst.stopRequested.Store(true)
		instances = append(instances, inst)
		ids = append(ids, id)
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, inst := range instances {
		wg.Add(1)
		go func(inst *instance) {
			defer wg.Done()
			m.stopInstance(inst)
		}(inst)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		m.log.Warn("giving up on stopping applications in time", "count", len(instances))
	}
	m.mu.Lock()
	for _, id := range ids {
		delete(m.running, id)
	}
	m.mu.Unlock()
}

// ---------------------------------------------------------------------------
// 健康检查与端口
// ---------------------------------------------------------------------------

// waitHealthy 等待应用真的能提供服务。
//
// 只等"端口在监听"是不够的：很多框架是先绑定端口再做初始化，此时
// 请求会 500 或挂住。因此声明了 HealthPath 的预设会做一次 HTTP 探测。
func (m *Manager) waitHealthy(ctx context.Context, app App) error {
	deadline := time.Now().Add(healthTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.probe(app) {
			return nil
		}
		// 进程已经退出了就不必再等到超时。
		m.mu.Lock()
		inst, ok := m.running[app.ID]
		m.mu.Unlock()
		if ok && inst.process != nil {
			select {
			case <-inst.process.Done():
				return fmt.Errorf("%w: the process exited during startup", ErrUnhealthy)
			default:
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: nothing answered on 127.0.0.1:%d within %s", ErrUnhealthy, app.LocalPort, healthTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(healthInterval):
		}
	}
}

func (m *Manager) probe(app App) bool {
	address := fmt.Sprintf("127.0.0.1:%d", app.LocalPort)
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	if app.Plan.HealthPath == "" {
		return true
	}
	url := fmt.Sprintf("http://%s%s", address, app.Plan.HealthPath)
	client := m.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Get(url)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	// 2xx/3xx 都算健康：一个把自己重定向到首页的站点是完全正常的。
	return response.StatusCode < 400
}

// ensurePortFree 在启动前确认端口可用，被占用时重新分配。
//
// 规划时分配的端口到真正启动之间可能被别的进程抢走（包括用户自己
// 起的别的东西）。不复查的话，失败会以"bind: address already in use"
// 的形式出现在应用日志里，而用户不知道那是端口冲突。
func (m *Manager) ensurePortFree(app *App) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", app.LocalPort))
	if err == nil {
		_ = listener.Close()
		return nil
	}
	if m.allocate == nil {
		return err
	}
	port, allocErr := m.allocate()
	if allocErr != nil {
		return allocErr
	}
	// 只改本地端口：计划里存的是 {port} 占位符，渲染时才变成具体数字，
	// 因此重新分配不需要改写任何已有参数。
	app.LocalPort = port
	return m.persist(context.Background(), *app)
}

// ---------------------------------------------------------------------------
// 内部工具
// ---------------------------------------------------------------------------

func (m *Manager) isRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.running[id]
	return ok
}

func (m *Manager) setState(ctx context.Context, app *App, state State) error {
	app.State = state
	app.UpdatedAt = m.now().UTC()
	if err := m.persist(ctx, *app); err != nil {
		return err
	}
	m.mu.Lock()
	if inst, ok := m.running[app.ID]; ok {
		inst.app.State = state
	}
	m.mu.Unlock()
	m.publish(event.TypeAppStateChanged, *app)
	return nil
}

// fail 把应用置为失败并保留原因。
func (m *Manager) fail(ctx context.Context, app App, cause error) error {
	app.State = StateFailed
	app.Health = HealthUnhealthy
	app.LastError = cause.Error()
	app.HealthDetail = cause.Error()
	if err := m.persist(ctx, app); err != nil {
		return cause
	}
	m.publish(event.TypeAppStateChanged, app)
	return cause
}

func (m *Manager) persist(ctx context.Context, app App) error {
	app.UpdatedAt = m.now().UTC()
	return m.store.SaveApp(context.WithoutCancel(ctx), app)
}

func (m *Manager) publish(eventType string, app App) {
	if m.bus == nil {
		return
	}
	m.bus.Publish(eventType, map[string]any{
		"id":            app.ID,
		"name":          app.Name,
		"state":         string(app.State),
		"health":        string(app.Health),
		"health_detail": app.HealthDetail,
		"local_port":    app.LocalPort,
	})
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// allocatePort 向系统要一个空闲端口。
//
// 拿到之后立刻关闭，因此存在"被别的进程抢走"的窗口 —— 这一点由
// ensurePortFree 在启动前复查来兜底。
func allocatePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = listener.Close() }()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("unexpected listener address type")
	}
	return address.Port, nil
}
