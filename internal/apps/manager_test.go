package apps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// memoryStore 是内存实现的应用仓储。
type memoryStore struct {
	mu   sync.Mutex
	apps map[string]App
}

func newMemoryStore() *memoryStore { return &memoryStore{apps: map[string]App{}} }

func (s *memoryStore) SaveApp(_ context.Context, app App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[app.ID] = app
	return nil
}

func (s *memoryStore) GetApp(_ context.Context, id string) (App, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[id]
	return app, ok, nil
}

func (s *memoryStore) ListApps(context.Context) ([]App, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]App, 0, len(s.apps))
	for _, app := range s.apps {
		out = append(out, app)
	}
	return out, nil
}

func (s *memoryStore) DeleteApp(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.apps[id]
	delete(s.apps, id)
	return ok, nil
}

// fakeProcess 模拟一个被守护的子进程。
type fakeProcess struct {
	pid      int
	done     chan struct{}
	once     sync.Once
	status   platform.ExitStatus
	onSignal func()
}

func (p *fakeProcess) PID() int              { return p.pid }
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func (p *fakeProcess) Wait() (platform.ExitStatus, error) {
	<-p.done
	return p.status, nil
}

func (p *fakeProcess) Signal(platform.Signal) error {
	if p.onSignal != nil {
		p.onSignal()
	}
	p.finish(platform.ExitStatus{Signaled: true, Signal: "terminated", Code: -1})
	return nil
}

func (p *fakeProcess) finish(status platform.ExitStatus) {
	p.once.Do(func() {
		p.status = status
		close(p.done)
	})
}

// fakeController 模拟平台的进程控制。
//
// 它让测试不依赖机器上装了什么命令，同时仍然走真实的 TCP 监听 ——
// 健康检查因此测的是真的探测逻辑，而不是被打桩的判定。
type fakeController struct {
	mu       sync.Mutex
	started  []platform.ProcessSpec
	procs    []*fakeProcess
	listener net.Listener
	// exitImmediately 让 Run 步骤启动后立刻以非零码退出，用于测崩溃重启。
	exitImmediately bool
	// listenOnPort 让启动的进程在 PORT 环境变量指定的端口上监听。
	listenOnPort bool
	failNext     int
}

func (c *fakeController) Start(spec platform.ProcessSpec) (platform.Process, error) {
	c.mu.Lock()
	c.started = append(c.started, spec)
	if c.failNext > 0 {
		c.failNext--
		c.mu.Unlock()
		return nil, errors.New("simulated start failure")
	}
	c.mu.Unlock()

	proc := &fakeProcess{pid: 1000 + len(c.started), done: make(chan struct{})}
	c.mu.Lock()
	c.procs = append(c.procs, proc)
	listen := c.listenOnPort
	c.mu.Unlock()

	if listen {
		if port := portFromEnv(spec.Env); port > 0 {
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			c.listener = listener
			c.mu.Unlock()
			proc.onSignal = func() { _ = listener.Close() }
		}
	}
	if c.exitImmediately {
		go func() {
			time.Sleep(20 * time.Millisecond)
			proc.finish(platform.ExitStatus{Code: 1})
		}()
	}
	return proc, nil
}

func (c *fakeController) Describe() platform.ImplState {
	return platform.ImplState{Available: true, Backend: "fake"}
}

// killLast 让最后一个进程以非零码退出，模拟运行期崩溃。
func (c *fakeController) killLast() {
	c.mu.Lock()
	var proc *fakeProcess
	if len(c.procs) > 0 {
		proc = c.procs[len(c.procs)-1]
	}
	listener := c.listener
	c.mu.Unlock()
	if listener != nil {
		_ = listener.Close()
	}
	if proc != nil {
		proc.finish(platform.ExitStatus{Code: 137})
	}
}

func (c *fakeController) specs() []platform.ProcessSpec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]platform.ProcessSpec{}, c.started...)
}

func portFromEnv(env []string) int {
	for _, kv := range env {
		if strings.HasPrefix(kv, "PORT=") {
			port, err := strconv.Atoi(strings.TrimPrefix(kv, "PORT="))
			if err == nil {
				return port
			}
		}
	}
	return 0
}

// newTestManager 构造一个完全离线的管理器。
func newTestManager(t *testing.T, controller *fakeController) (*Manager, *memoryStore, *LogStore) {
	t.Helper()
	store := newMemoryStore()
	logs := NewLogStore(filepath.Join(t.TempDir(), "logs"))
	bus := event.NewBus(64)
	t.Cleanup(bus.Close)
	manager := NewManager(Deps{
		Store:     store,
		Logs:      logs,
		Bus:       bus,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataRoot:  t.TempDir(),
		Processes: controller,
	})
	return manager, store, logs
}

// staticSite 造一个最小的静态站点目录。
func staticSite(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>hello</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// ---------------------------------------------------------------------------
// 创建
// ---------------------------------------------------------------------------

func TestCreateAllocatesAPortAndPersistsThePlan(t *testing.T) {
	manager, store, _ := newTestManager(t, &fakeController{})
	app, err := manager.Create(context.Background(), CreateSpec{
		Name: "站点", PresetID: "static-html", SourcePath: staticSite(t),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if app.State != StateDraft {
		t.Fatalf("a new app should be draft, got %s", app.State)
	}
	if app.LocalPort < 1024 {
		t.Fatalf("expected an allocated high port, got %d", app.LocalPort)
	}
	if !app.AutoStart {
		t.Fatalf("auto start should default on")
	}
	if app.MaxRestarts != DefaultMaxRestarts {
		t.Fatalf("max restarts default = %d", app.MaxRestarts)
	}
	// 计划里的端口是占位符，不是具体端口：这样重新分配只是换一个数字。
	processApp, err := manager.Create(context.Background(), processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(processApp.Plan.Run.Env, " "), presets.PortPlaceholder) {
		t.Fatalf("the stored plan should keep the placeholder: %v", processApp.Plan.Run.Env)
	}
	stored, ok, err := store.GetApp(context.Background(), app.ID)
	if err != nil || !ok {
		t.Fatalf("app was not persisted: ok=%v err=%v", ok, err)
	}
	if stored.ID != app.ID || stored.PresetID != "static-html" {
		t.Fatalf("unexpected stored app: %#v", stored)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	manager, _, _ := newTestManager(t, &fakeController{})
	cases := map[string]CreateSpec{
		"no name":      {PresetID: "static-html", SourcePath: staticSite(t)},
		"bad preset":   {Name: "x", PresetID: "nope", SourcePath: staticSite(t)},
		"missing path": {Name: "x", PresetID: "static-html", SourcePath: "/nonexistent/isc-test"},
		"path is file": {Name: "x", PresetID: "static-html", SourcePath: writeFile(t)},
		"bad port":     {Name: "x", PresetID: "static-html", SourcePath: staticSite(t), Port: 70000},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := manager.Create(context.Background(), spec); err == nil {
				t.Fatalf("expected an error")
			}
		})
	}
}

// 识别不出入口时**不猜**，而是让创建失败并说明原因。
func TestCreateRefusesWhenThePlanHasNoStartCommand(t *testing.T) {
	manager, _, _ := newTestManager(t, &fakeController{})
	// 一个只有 requirements.txt 的目录：Python 预设给不出启动命令。
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "requirements.txt"), []byte("requests"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Create(context.Background(), CreateSpec{
		Name: "py", PresetID: "python-auto", SourcePath: root,
	})
	if !errors.Is(err, presets.ErrNotRunnable) {
		t.Fatalf("want ErrNotRunnable, got %v", err)
	}
}

func writeFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---------------------------------------------------------------------------
// 静态站点：内核自己托管，不需要任何运行时
// ---------------------------------------------------------------------------

func TestStaticSiteIsServedWithoutARuntime(t *testing.T) {
	manager, _, _ := newTestManager(t, &fakeController{})
	ctx := context.Background()
	app, err := manager.Create(ctx, CreateSpec{Name: "站点", PresetID: "static-html", SourcePath: staticSite(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Deploy(ctx, app.ID, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	running, _, err := manager.Get(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("expected a healthy running app, got %s/%s (%s)", running.State, running.Health, running.HealthDetail)
	}

	body, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
	if err != nil {
		t.Fatalf("the site should be reachable: %v", err)
	}
	defer func() { _ = body.Body.Close() }()
	content, _ := io.ReadAll(body.Body)
	if !strings.Contains(string(content), "hello") {
		t.Fatalf("unexpected body: %q", content)
	}

	if err := manager.Stop(ctx, app.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 停止之后端口必须真的释放，否则下一次启动会撞上"端口被占用"。
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort)); err == nil {
		t.Fatalf("the site should be unreachable after stop")
	}
}

// ---------------------------------------------------------------------------
// 进程应用
// ---------------------------------------------------------------------------

// processSpec 是一个自定义服务器（Run 命令由用户给出）。
func processSpec(t *testing.T) CreateSpec {
	t.Helper()
	return CreateSpec{
		Name: "自定义", PresetID: presets.CustomPresetID, SourcePath: t.TempDir(),
		CustomRun: &presets.Step{Executable: "/bin/true", Env: []string{"PORT=" + presets.PortPlaceholder}},
	}
}

func TestProcessAppStartsAndReportsHealthy(t *testing.T) {
	controller := &fakeController{listenOnPort: true}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()

	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatalf("start: %v", err)
	}
	running, _, _ := manager.Get(ctx, app.ID)
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("expected healthy/running, got %s/%s (%s)", running.State, running.Health, running.HealthDetail)
	}

	// 启动参数里必须是**真实端口**，而不是占位符。
	specs := controller.specs()
	if len(specs) == 0 {
		t.Fatalf("no process was started")
	}
	if portFromEnv(specs[0].Env) != running.LocalPort {
		t.Fatalf("the port must reach the process: env=%v local_port=%d", specs[0].Env, running.LocalPort)
	}
}

// 启动参数必须是渲染过的：占位符直接传给应用等于让它监听一个不存在的端口。
func TestProcessAppNeverReceivesTheRawPlaceholder(t *testing.T) {
	controller := &fakeController{listenOnPort: true}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	for _, spec := range controller.specs() {
		for _, value := range append(append([]string{}, spec.Args...), spec.Env...) {
			if strings.Contains(value, presets.PortPlaceholder) {
				t.Fatalf("an unrendered placeholder reached the process: %q", value)
			}
		}
	}
}

// 起来了但没人监听：必须报不健康，而不是"部署成功"。
func TestAppThatNeverListensIsReportedUnhealthy(t *testing.T) {
	previous := healthTimeout
	healthTimeout = 600 * time.Millisecond
	t.Cleanup(func() { healthTimeout = previous })

	controller := &fakeController{} // 不监听端口
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(ctx, app.ID); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("want ErrUnhealthy, got %v", err)
	}
	got, _, _ := manager.Get(ctx, app.ID)
	if got.Health != HealthUnhealthy && got.Health != HealthStarting {
		t.Fatalf("unexpected health %s", got.Health)
	}
}

// ---------------------------------------------------------------------------
// 崩溃重启与上限
// ---------------------------------------------------------------------------

func TestCrashWhileRunningIsRestartedUpToTheLimitThenFails(t *testing.T) {
	previousTimeout, previousBackoff := healthTimeout, restartBackoffBase
	healthTimeout = 2 * time.Second
	restartBackoffBase = 10 * time.Millisecond
	t.Cleanup(func() { healthTimeout, restartBackoffBase = previousTimeout, previousBackoff })

	controller := &fakeController{listenOnPort: true}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()

	spec := processSpec(t)
	limit := 2
	spec.MaxRestarts = &limit
	app, err := manager.Create(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatalf("start: %v", err)
	}

	// 反复制造运行期崩溃。上限是 2，因此第三次崩溃必须让它停下 ——
	// 而不是永远重启下去（重启计数若不跨实例保留，这里会无限循环）。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _, _ := manager.Get(ctx, app.ID)
		if got.State == StateFailed {
			if got.LastError == "" {
				t.Fatalf("a failed app must explain why")
			}
			if got.RestartCount != limit {
				t.Fatalf("expected %d restarts before giving up, got %d", limit, got.RestartCount)
			}
			return
		}
		controller.killLast()
		time.Sleep(40 * time.Millisecond)
	}
	got, _, _ := manager.Get(ctx, app.ID)
	t.Fatalf("a crash loop must end in failed, still %s after %d restarts", got.State, got.RestartCount)
}

// 启动阶段就退出**不重启**：那几乎总是配置或构建问题，重启只是把同一个
// 失败重放一遍。要如实报失败并留下日志。
func TestCrashDuringStartupIsNotRestarted(t *testing.T) {
	previousTimeout, previousBackoff := healthTimeout, restartBackoffBase
	healthTimeout = 2 * time.Second
	restartBackoffBase = 10 * time.Millisecond
	t.Cleanup(func() { healthTimeout, restartBackoffBase = previousTimeout, previousBackoff })

	controller := &fakeController{exitImmediately: true}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Start(ctx, app.ID); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("want ErrUnhealthy, got %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	// 只应启动过一次：没有重启风暴。
	if specs := controller.specs(); len(specs) != 1 {
		t.Fatalf("a startup crash must not be retried, %d starts observed", len(specs))
	}
	got, _, _ := manager.Get(ctx, app.ID)
	if got.State == StateRunning {
		t.Fatalf("a failed startup must not leave the app looking running")
	}
}

// 我们主动停止时不该被当成崩溃而重启。
func TestStopIsNotTreatedAsACrash(t *testing.T) {
	previous := healthTimeout
	healthTimeout = 500 * time.Millisecond
	t.Cleanup(func() { healthTimeout = previous })

	controller := &fakeController{}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx, app.ID); !errors.Is(err, ErrUnhealthy) {
		t.Fatalf("want ErrUnhealthy, got %v", err)
	}
	if err := manager.Stop(ctx, app.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 给监督 goroutine 一点时间去（错误地）重启。
	time.Sleep(150 * time.Millisecond)
	got, _, _ := manager.Get(ctx, app.ID)
	if got.State != StateStopped {
		t.Fatalf("stop must win over the crash path, got %s", got.State)
	}
}

// ---------------------------------------------------------------------------
// 恢复
// ---------------------------------------------------------------------------

// 内核重启后，库里写着 running 的行必须被归位 —— 否则界面显示一堆
// "运行中"的应用，而实际上什么都没有在跑。
func TestRecoverRestartsAutoStartAppsAndStopsTheRest(t *testing.T) {
	controller := &fakeController{listenOnPort: true}
	manager, store, _ := newTestManager(t, controller)
	ctx := context.Background()

	// 模拟"上次运行时留下的行"：两个都在 running，一个自启、一个不自启。
	autoApp, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{autoApp.ID, manual.ID} {
		app, _, _ := store.GetApp(ctx, id)
		app.State = StateRunning
		if err := store.SaveApp(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	manualApp, _, _ := store.GetApp(ctx, manual.ID)
	manualApp.AutoStart = false
	if err := store.SaveApp(ctx, manualApp); err != nil {
		t.Fatal(err)
	}

	started, err := manager.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	if started != 1 {
		t.Fatalf("expected exactly the auto-start app to be restarted, got %d", started)
	}
	gotAuto, _, _ := manager.Get(ctx, autoApp.ID)
	if gotAuto.State != StateRunning {
		t.Fatalf("the auto-start app should be running, got %s", gotAuto.State)
	}
	gotManual, _, _ := manager.Get(ctx, manual.ID)
	if gotManual.State != StateStopped {
		t.Fatalf("an app that is not auto-start should be reconciled to stopped, got %s", gotManual.State)
	}
}

func TestRecoverLeavesFinishedAppsAlone(t *testing.T) {
	manager, store, _ := newTestManager(t, &fakeController{})
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := store.GetApp(ctx, app.ID)
	stored.State = StateFailed
	if err := store.SaveApp(ctx, stored); err != nil {
		t.Fatal(err)
	}
	started, err := manager.Recover(ctx)
	if err != nil || started != 0 {
		t.Fatalf("a failed app must not be restarted by recovery: started=%d err=%v", started, err)
	}
}

// ---------------------------------------------------------------------------
// 日志
// ---------------------------------------------------------------------------

func TestLogsAreBoundedAndTailable(t *testing.T) {
	logs := NewLogStore(t.TempDir())
	for i := 0; i < maxLogLines+50; i++ {
		logs.Append("app", fmt.Sprintf("line-%d\n", i))
	}
	tail := logs.Tail("app", 10)
	if len(tail) != 10 {
		t.Fatalf("expected 10 lines, got %d", len(tail))
	}
	if tail[len(tail)-1] != fmt.Sprintf("line-%d", maxLogLines+49) {
		t.Fatalf("tail should end with the newest line, got %q", tail[len(tail)-1])
	}
	// 环形缓冲不能无限增长：这是"用户程序写日志把内核吃光"的唯一防线。
	if got := len(logs.Tail("app", maxLogLines+500)); got != maxLogLines {
		t.Fatalf("buffer should be capped at %d lines, got %d", maxLogLines, got)
	}
	// 落盘也应当在。
	if _, err := os.Stat(logs.FilePath("app")); err != nil {
		t.Fatalf("logs should also be written to disk: %v", err)
	}
}

func TestLogsSplitPartialLinesAndTruncateGiants(t *testing.T) {
	logs := NewLogStore(t.TempDir())
	// 没有换行的片段（进度条）也必须能看到。
	logs.Append("app", "progress 50%")
	if got := logs.Tail("app", 5); len(got) != 1 || got[0] != "progress 50%" {
		t.Fatalf("a partial line must still be visible: %v", got)
	}
	// 一个只写不换行的进程不能把内存吃光。
	giant := strings.Repeat("x", maxLogLineBytes*2)
	logs.Append("app", giant)
	got := logs.Tail("app", 1)
	if len(got[0]) > maxLogLineBytes+32 {
		t.Fatalf("a giant line should be truncated, got %d bytes", len(got[0]))
	}
}

// ---------------------------------------------------------------------------
// 端口
// ---------------------------------------------------------------------------

// 规划时分配的端口到真正启动之间可能被别人抢走；必须复查并重新分配，
// 否则失败会以"address already in use"的形式出现在应用日志里。
func TestPortIsReallocatedWhenItWasTakenMeanwhile(t *testing.T) {
	controller := &fakeController{listenOnPort: true}
	manager, _, _ := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	// 抢走它规划到的端口。
	squatter, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", app.LocalPort))
	if err != nil {
		t.Fatalf("could not occupy the port: %v", err)
	}
	defer func() { _ = squatter.Close() }()

	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatalf("start should re-allocate instead of failing: %v", err)
	}
	got, _, _ := manager.Get(ctx, app.ID)
	if got.LocalPort == app.LocalPort {
		t.Fatalf("the port was taken, so it must have been re-allocated")
	}
	// 而且新端口必须真的传给了应用。
	specs := controller.specs()
	if portFromEnv(specs[len(specs)-1].Env) != got.LocalPort {
		t.Fatalf("the re-allocated port must reach the process")
	}
}

// ---------------------------------------------------------------------------
// 删除
// ---------------------------------------------------------------------------

func TestDeleteStopsAndRemovesTheApp(t *testing.T) {
	controller := &fakeController{listenOnPort: true}
	manager, store, logs := newTestManager(t, controller)
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	logs.Append(app.ID, "something happened")

	if err := manager.Delete(ctx, app.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, _ := store.GetApp(ctx, app.ID); ok {
		t.Fatalf("the app should be gone")
	}
	if len(logs.Tail(app.ID, 10)) != 0 {
		t.Fatalf("in-memory logs should be dropped")
	}
	// 但磁盘日志要留着：用户往往正是在删掉它之前想看一眼它为什么起不来。
	if _, err := os.Stat(logs.FilePath(app.ID)); err != nil {
		t.Fatalf("the on-disk log should be kept: %v", err)
	}
	if err := manager.Delete(ctx, app.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting twice should report not found, got %v", err)
	}
}

func TestConcurrentDeployIsRefused(t *testing.T) {
	manager, store, _ := newTestManager(t, &fakeController{})
	ctx := context.Background()
	app, err := manager.Create(ctx, processSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _ := store.GetApp(ctx, app.ID)
	stored.State = StateBuilding
	if err := store.SaveApp(ctx, stored); err != nil {
		t.Fatal(err)
	}
	// 两个并发的部署会争同一个端口与同一个工作目录，结果是两者都失败。
	if err := manager.Deploy(ctx, app.ID, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("want ErrBusy, got %v", err)
	}
}

func TestRuntimeKindIsPreservedThroughPersistence(t *testing.T) {
	manager, store, _ := newTestManager(t, &fakeController{})
	ctx := context.Background()
	app, err := manager.Create(ctx, CreateSpec{Name: "站点", PresetID: "static-html", SourcePath: staticSite(t)})
	if err != nil {
		t.Fatal(err)
	}
	if app.Kind != runtime.KindNone {
		t.Fatalf("a static site needs no runtime, got %q", app.Kind)
	}
	stored, _, _ := store.GetApp(ctx, app.ID)
	if stored.Kind != runtime.KindNone {
		t.Fatalf("kind must survive persistence, got %q", stored.Kind)
	}
}

// 需要 Docker 的站点在缺 Docker 时要**立刻**给出可读的原因。
//
// 不挡的话，用户会在部署的最后一步看到
// `start docker: exec: "docker": executable file not found in $PATH` ——
// 那是一句实现细节，而真正该说的是"这个方式需要 Docker，本机没有"。
func TestDockerAppFailsFastWithoutDocker(t *testing.T) {
	manager, _, _ := newTestManager(t, &fakeController{})
	// 一个没有 runtime 管理器的 Manager：Docker 探测因此必然失败。
	ctx := context.Background()

	site := t.TempDir()
	if err := os.WriteFile(filepath.Join(site, "compose.yaml"), []byte("services:\n  web:\n    image: nginx\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, err := manager.Create(ctx, CreateSpec{Name: "容器", PresetID: "docker-compose", SourcePath: site})
	if err != nil {
		t.Fatalf("creating a docker app should work (the check happens at deploy): %v", err)
	}
	if app.Kind != runtime.KindDocker {
		t.Fatalf("kind = %q", app.Kind)
	}

	err = manager.Deploy(ctx, app.ID, nil)
	if !errors.Is(err, ErrDockerRequired) {
		t.Fatalf("want ErrDockerRequired, got %v", err)
	}
	// 失败要被记录下来并说明原因，而不是让应用停在"部署中"。
	stored, _, _ := manager.Get(ctx, app.ID)
	if stored.State != StateFailed {
		t.Fatalf("state = %s", stored.State)
	}
	if !strings.Contains(stored.LastError, "Docker") {
		t.Fatalf("the recorded reason should mention Docker: %q", stored.LastError)
	}
}
