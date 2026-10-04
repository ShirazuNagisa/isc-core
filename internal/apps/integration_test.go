package apps

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 本文件里的测试**真的起进程、真的占端口、真的发 HTTP 请求**，因此它们
// 需要机器上装了解释器，装不上就跳过。
//
// 为什么值得单独写：应用引擎的其余测试都用假的进程控制器，验证的是状态机
// 与协调逻辑；而"一条真实的 npm start 到底能不能把一个站点跑起来"只能由
// 真实进程回答 —— 环境变量有没有传对、PATH 里有没有 npm、端口有没有落到
// 应用手上、日志有没有收到输出，这些都是打桩测不出来的。

// requireTool 在工具不存在时跳过测试。
func requireTool(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not installed; skipping the real-deployment test", name)
	}
	return path
}

// realManager 构造一个使用真实平台进程控制的管理器。
func realManager(t *testing.T) (*Manager, *memoryStore, *LogStore) {
	t.Helper()
	dataRoot := t.TempDir()
	goos, goarch := goruntime.GOOS, goruntime.GOARCH
	bundle := platform.Current(dataRoot)
	if !bundle.Capabilities().Processes.Available {
		t.Skip("this platform has no process control")
	}
	store := newMemoryStore()
	logs := NewLogStore(filepath.Join(dataRoot, "logs"))
	bus := event.NewBus(64)
	t.Cleanup(bus.Close)

	manager := NewManager(Deps{
		Store:     store,
		Runtimes:  runtime.NewManager(dataRoot, goos, goarch),
		Processes: bundle.Processes,
		Logs:      logs,
		Bus:       bus,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataRoot:  dataRoot,
	})
	// 真实进程的启动比假进程慢，健康检查的默认 60 秒对测试太长。
	previous := healthTimeout
	healthTimeout = 30 * time.Second
	t.Cleanup(func() { healthTimeout = previous })
	return manager, store, logs
}

func writeSiteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const nodeServer = `const http = require('http');
const port = Number(process.env.PORT || 3000);
const host = process.env.HOST || '127.0.0.1';
http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'text/plain'});
  res.end('ISC-PHECDA-NODE-OK');
}).listen(port, host, () => console.log('LISTENING ' + host + ':' + port));
`

// 端到端：一份真实的 Node 源码 → 识别 → 登记 → 部署 → 真的能访问 → 停止。
func TestRealNodeSiteDeploysAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-deployment test in short mode")
	}
	requireTool(t, "node")
	requireTool(t, "npm")

	manager, _, logs := realManager(t)
	ctx := context.Background()

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "package.json"),
		`{"name":"isc-e2e","version":"1.0.0","scripts":{"start":"node server.js"}}`)
	writeSiteFile(t, filepath.Join(site, "server.js"), nodeServer)

	// 识别必须认出这是 Node 站点，并且能给出依据。
	inspection, err := inspectForTest(site)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if inspection.Recommended != "node-auto" {
		t.Fatalf("expected node-auto, got %q (evidence %#v)", inspection.Recommended, inspection.Evidence)
	}

	app, err := manager.Create(ctx, CreateSpec{Name: "node-e2e", PresetID: "node-auto", SourcePath: site})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	var progress []string
	if err := manager.Deploy(ctx, app.ID, func(_ float64, message string) {
		if message != "" {
			progress = append(progress, message)
		}
	}); err != nil {
		t.Fatalf("deploy: %v\nlogs:\n%s", err, strings.Join(logs.Tail(app.ID, 50), "\n"))
	}

	// 部署完成的判据是**真的能访问**，而不是状态字段说了什么。
	running, _, err := manager.Get(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("expected running/healthy, got %s/%s (%s)", running.State, running.Health, running.HealthDetail)
	}

	body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
	if !strings.Contains(body, "ISC-PHECDA-NODE-OK") {
		t.Fatalf("the site answered with something unexpected: %q", body)
	}

	// 应用自己的输出必须被采集到 —— 这是用户排查"为什么没起来"的唯一线索。
	tail := logs.Tail(app.ID, 200)
	if !containsSubstring(tail, "LISTENING") {
		t.Fatalf("the app's own output was not captured; got %#v", tail)
	}

	// 运行时解析应当选中本机已装的 node，而不是去下载一份（这台机器上有）。
	if running.Kind != runtime.KindNode {
		t.Fatalf("kind = %q", running.Kind)
	}

	if err := manager.Stop(ctx, app.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 停止必须真的释放端口：否则下一次启动会撞上"端口被占用"。
	if err := probePort(running.LocalPort); err == nil {
		t.Fatalf("port %d should be free after stop", running.LocalPort)
	}
}

// 停止之后必须能把同一个应用再启动起来（端口与计划都要能复用）。
func TestRealNodeSiteRestartsAfterStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-deployment test in short mode")
	}
	requireTool(t, "node")

	manager, _, _ := realManager(t)
	ctx := context.Background()

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "package.json"), `{"name":"x","scripts":{"start":"node server.js"}}`)
	writeSiteFile(t, filepath.Join(site, "server.js"), nodeServer)

	app, err := manager.Create(ctx, CreateSpec{Name: "node-restart", PresetID: "node-auto", SourcePath: site})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Deploy(ctx, app.ID, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if err := manager.Stop(ctx, app.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// 第二次启动走的是 Start（不重新装依赖、不重新构建）。
	if err := manager.Start(ctx, app.ID); err != nil {
		t.Fatalf("restart: %v", err)
	}
	running, _, _ := manager.Get(ctx, app.ID)
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("after restart: %s/%s", running.State, running.Health)
	}
	body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
	if !strings.Contains(body, "ISC-PHECDA-NODE-OK") {
		t.Fatalf("the restarted site answered with something unexpected: %q", body)
	}
}

// 运行期崩溃必须被自动拉起：这是"日常稳定"的核心承诺。
func TestRealNodeSiteIsRestartedAfterBeingKilled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-deployment test in short mode")
	}
	requireTool(t, "node")

	manager, _, _ := realManager(t)
	ctx := context.Background()

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "package.json"), `{"name":"x","scripts":{"start":"node server.js"}}`)
	writeSiteFile(t, filepath.Join(site, "server.js"), nodeServer)

	app, err := manager.Create(ctx, CreateSpec{Name: "node-crash", PresetID: "node-auto", SourcePath: site})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Deploy(ctx, app.ID, nil); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	running, _, _ := manager.Get(ctx, app.ID)

	// 把进程**整组**杀掉，模拟真实的崩溃。
	manager.mu.Lock()
	inst := manager.running[app.ID]
	manager.mu.Unlock()
	if inst == nil || inst.process == nil {
		t.Fatalf("no process to kill")
	}
	_ = inst.process.Signal(platform.SignalKill)

	// 退避基数是 1 秒，因此给它几秒。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		current, _, _ := manager.Get(ctx, app.ID)
		if current.State == StateRunning && current.Health == HealthHealthy && current.RestartCount > 0 {
			// 端口上必须真的有东西在响应，而不只是一个好听的状态。
			body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
			if !strings.Contains(body, "ISC-PHECDA-NODE-OK") {
				t.Fatalf("after restart the port is not serving: %q", body)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	current, _, _ := manager.Get(ctx, app.ID)
	t.Fatalf("a crash should be recovered from, still %s/%s (restarts %d, error %q)",
		current.State, current.Health, current.RestartCount, current.LastError)
}

// --- 辅助 -------------------------------------------------------------------

type inspectionResult struct {
	Recommended string
	Evidence    []string
}

func inspectForTest(root string) (inspectionResult, error) {
	result, err := presets.Inspect(root)
	if err != nil {
		return inspectionResult{}, err
	}
	out := inspectionResult{Recommended: result.Recommended}
	for _, item := range result.Evidence {
		out.Evidence = append(out.Evidence, item.File+" "+item.Signal)
	}
	return out, nil
}

func probePort(port int) error {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func containsSubstring(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

const goServer = `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ISC-PHECDA-GO-OK")
	})
	fmt.Println("LISTENING 127.0.0.1:" + port)
	_ = http.ListenAndServe("127.0.0.1:"+port, nil)
}
`

// Go 站点端到端：这条路径会真的执行**安装与构建**两步（Node 那条只跑了
// 启动），因此它是"构建流水线"唯一被真实验证的地方。
func TestRealGoSiteBuildsAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-deployment test in short mode")
	}
	requireTool(t, "go")

	manager, _, logs := realManager(t)
	ctx := context.Background()

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "go.mod"), "module isce2e\n\ngo 1.21\n")
	writeSiteFile(t, filepath.Join(site, "main.go"), goServer)

	app, err := manager.Create(ctx, CreateSpec{Name: "go-e2e", PresetID: "go-module", SourcePath: site})
	if err != nil {
		// 识别不出来时宁可明确失败，也不要悄悄退回别的预设。
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Deploy(ctx, app.ID, nil); err != nil {
		t.Fatalf("deploy: %v\nlogs:\n%s", err, strings.Join(logs.Tail(app.ID, 80), "\n"))
	}

	// 构建产物必须真的在，而且是可执行的 —— 这是"构建那一步真的跑了"的
	// 唯一证据（只看状态字段的话，一个空计划也会显示成功）。
	binary := filepath.Join(site, ".isc", "bin", "app")
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatalf("the build step did not produce a binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("the built binary is not executable: %v", info.Mode())
	}

	running, _, _ := manager.Get(ctx, app.ID)
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("expected running/healthy, got %s/%s (%s)", running.State, running.Health, running.HealthDetail)
	}
	body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
	if !strings.Contains(body, "ISC-PHECDA-GO-OK") {
		t.Fatalf("unexpected body: %q", body)
	}
}

// 构建失败必须**如实报失败**，并且把编译器的输出留在日志里。
//
// 把它当成"部署成功"是最糟的一类故障：用户看到"运行中"，访问却是 404。
func TestRealGoSiteWithABrokenBuildFailsAndKeepsTheCompilerOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the real-deployment test in short mode")
	}
	requireTool(t, "go")

	manager, _, logs := realManager(t)
	ctx := context.Background()

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "go.mod"), "module iscbad\n\ngo 1.21\n")
	// 故意写一段编译不过的代码。
	writeSiteFile(t, filepath.Join(site, "main.go"), "package main\n\nfunc main() { this is not go }\n")

	app, err := manager.Create(ctx, CreateSpec{Name: "go-bad", PresetID: "go-module", SourcePath: site})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	err = manager.Deploy(ctx, app.ID, nil)
	if err == nil {
		t.Fatalf("a broken build must not be reported as a successful deploy")
	}

	got, _, _ := manager.Get(ctx, app.ID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
	if got.LastError == "" {
		t.Fatalf("the failure must be recorded with a reason")
	}
	// 编译器说了什么必须看得到 —— 那是用户唯一能据以修代码的东西。
	tail := strings.Join(logs.Tail(app.ID, 200), "\n")
	if !strings.Contains(tail, "main.go") && !strings.Contains(tail, "syntax error") {
		t.Fatalf("the compiler output was not kept in the app log; got:\n%s", tail)
	}
}
