package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/daemon"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/logx"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/store"
	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestMain 让整个测试二进制不碰开发机的系统钥匙串（见 testsupport）。
func TestMain(m *testing.M) { os.Exit(testsupport.IsolateSecretStore(m)) }

// harness 是一次完整的守护进程测试会话。
type harness struct {
	t        *testing.T
	daemon   *daemon.Daemon
	cancel   context.CancelFunc
	done     chan error
	info     runtimeinfo.Info
	endpoint platform.Endpoint
	client   *http.Client
	baseURL  string
	// stopped 记录内核是否已经被 stop() 关闭过。
	//
	// 存在的理由：done 是一个容量 1 的通道，stop() 会把它取空。
	// 清理函数若不知道这件事，就会在一个已经关闭的实例上白等到超时。
	stopped bool
	// paths 让测试能直接检查数据目录里的产物（数据库、密钥文件等）。
	paths paths.Paths
}

// debugLevel 返回一个初值为 Debug 的级别载体。
//
// handler 用的那个与 Options.LogLevelVar 必须是**两个**独立的实例：
// 内核启动时会按 settings.LogLevel 覆盖后者，若共用一块状态，
// "只让事件流里有日志"的意图会被覆盖掉，测试随即失去它的前提。
func debugLevel() *slog.LevelVar {
	v := new(slog.LevelVar)
	v.Set(slog.LevelDebug)
	return v
}

// errorLevel 与 debugLevel 同理，供"只关心生命周期、不看日志"的用例。
func errorLevel() *slog.LevelVar {
	v := new(slog.LevelVar)
	v.Set(slog.LevelError)
	return v
}

// startDaemon 在临时数据目录里启动一个真实的内核实例。
func startDaemon(t *testing.T) *harness {
	t.Helper()
	return startDaemonIn(t, testsupport.ShortTempDir(t))
}

// startDaemonIn 在指定数据目录里启动内核。
//
// 数据目录由调用方给定，因此可以在同一个测试里先后启动两次
// （模拟内核重启），而不是每次都用新的临时目录。
//
// 刻意不做任何 mock：验收标准就是"真实进程 + 真实传输 + 真实 HTTP +
// 真实数据库"，mock 掉任何一层都会让这些测试失去意义。
func startDaemonIn(t *testing.T, dir string) *harness {
	t.Helper()

	t.Setenv(paths.EnvDataDir, dir)

	p, err := paths.Resolve()
	if err != nil {
		t.Fatalf("解析数据目录失败: %v", err)
	}

	d := daemon.New(daemon.Options{
		Paths: p,
		// 日志写进黑洞：真实日志会淹没测试输出。
		// 级别刻意设为 Debug 而非 Error —— 日志同时会被桥接成
		// log.appended 事件，Debug 级别保证事件流里有日志可断言
		// （见 TestJobLifecycleOverEventStream）。
		//
		// handler 与 LogLevelVar 刻意用**两个独立**的 LevelVar，
		// 并显式传 LogLevel=debug：内核启动时会按 settings.LogLevel
		// 覆盖 LogLevelVar，若共用一块状态、又不显式指定级别，
		// 新建数据库里那份默认的 info 会把 Debug 悄悄降回去 ——
		// 事件流里于是再没有一条日志，而那看起来像是"桥接坏了"。
		LogHandler:  logx.New(io.Discard, debugLevel(), nil),
		LogLevelVar: debugLevel(),
		LogLevel:    settings.LevelDebug,
		Lang:        i18n.ZhCN,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	h := &harness{t: t, daemon: d, cancel: cancel, done: done, paths: p}

	// 启动失败时要保证测试不会留下一个还在跑的 goroutine。
	//
	// **必须等到 done**，不能只 cancel 就返回：t.TempDir 的清理在本函数
	// 的清理之后执行（LIFO），而内核关闭过程中仍持有 SQLite 句柄 ——
	// 于是临时目录删除会间歇性失败，报"文件被另一个进程占用"。
	// 那是一个只在某些机器上偶现、看起来与测试内容完全无关的失败。
	//
	// 但已经 stop 过的实例不能再等：stop() 已经把 done 里的值取走了，
	// 再等只会白等到超时。用 stopped 标记区分这两种情况 ——
	// "停掉再启动"类测试（验证持久化的那几个）正好会走到这条路径上。
	t.Cleanup(func() {
		if h.stopped {
			return
		}
		h.stopped = true
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("内核关闭超时，测试资源可能未被释放")
		}
	})

	select {
	case <-d.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("内核启动失败: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("等待内核就绪超时")
	}

	// 读取运行时文件 —— 这是客户端发现内核的唯一入口。
	info, err := runtimeinfo.Read(p.RuntimeFile())
	if err != nil {
		t.Fatalf("读取 runtime.json 失败: %v", err)
	}
	h.info = info

	if info.Token == "" {
		t.Fatal("runtime.json 中缺少访问令牌")
	}
	if info.Endpoint == "" {
		t.Fatal("runtime.json 中缺少首选通道地址")
	}

	// 客户端优先使用首选通道（命名管道 / Unix 套接字）——
	// 这条路径能通，才算真正验证了本地通道。
	ep := platform.Endpoint(info.Endpoint)
	hc, err := ep.HTTPClient(10 * time.Second)
	if err != nil {
		t.Fatalf("构造 HTTP 客户端失败: %v", err)
	}
	h.endpoint = ep
	h.client = hc
	h.baseURL = ep.HTTPBaseURL()

	return h
}

// stop 关闭内核并等待它退出。
//
// 与 t.Cleanup 的区别：它让同一个测试可以"停掉再启动"，
// 这正是验证持久化的方式。
//
// 幂等：重复调用直接返回。清理函数依赖 stopped 标记来判断
// 是否需要再等一次 —— done 通道只会有一个值，等第二次必然超时。
func (h *harness) stop() {
	h.t.Helper()
	if h.stopped {
		return
	}
	h.stopped = true
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("内核关闭时返回错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		h.t.Error("内核关闭超时")
	}
}

// get 发起一次带鉴权的 GET 请求。
func (h *harness) get(path string) (*http.Response, error) {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, h.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.info.Token)
	return h.client.Do(req)
}

// getJSON 发起请求并解码 JSON。
func (h *harness) getJSON(path string, out any) {
	h.t.Helper()
	resp, err := h.get(path)
	if err != nil {
		h.t.Fatalf("GET %s 失败: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("GET %s 返回 %d: %s", path, resp.StatusCode, body)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		h.t.Fatalf("解码 %s 响应失败: %v", path, err)
	}
}

// postJSON 发起一次带鉴权的 POST 并解码 JSON。
func (h *harness) postJSON(path string, body io.Reader, out any) *http.Response {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.baseURL+path, body)
	if err != nil {
		h.t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+h.info.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("POST %s 失败: %v", path, err)
	}
	if out != nil {
		defer resp.Body.Close() //nolint:errcheck // 测试清理
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			h.t.Fatalf("解码 %s 响应失败: %v", path, err)
		}
	}
	return resp
}

// wsURL 把 HTTP base URL 换成 WebSocket URL。
func (h *harness) wsURL(path string) string {
	u := h.baseURL
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	default:
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + path
}

// ---------------------------------------------------------------------------
// 测试
// ---------------------------------------------------------------------------

// TestRuntimeInfoAndHealth 验证 M0 的核心验收：
// 内核启动后写出 runtime.json，客户端凭它能连上并拿到版本信息。
func TestRuntimeInfoAndHealth(t *testing.T) {
	h := startDaemon(t)

	// runtime.json 的内容必须完整可用。
	if h.info.PID != os.Getpid() {
		t.Errorf("PID = %d, 期望 %d", h.info.PID, os.Getpid())
	}
	if h.info.FallbackEndpoint == "" {
		t.Error("应当同时提供回环回退通道（浏览器需要它）")
	}
	if !strings.HasPrefix(h.info.FallbackEndpoint, "tcp://127.0.0.1:") {
		t.Errorf("回退通道必须是回环 TCP，得到 %q", h.info.FallbackEndpoint)
	}
	if h.info.StartedAt.IsZero() {
		t.Error("缺少启动时间")
	}
	if len(h.info.Token) < 32 {
		t.Errorf("令牌长度不足，随机性可疑: %d", len(h.info.Token))
	}

	var health gen.Health
	h.getJSON("/v1/health", &health)
	if health.Status != gen.Ok {
		t.Errorf("健康状态 = %q, 期望 ok", health.Status)
	}
	if health.Version == nil || *health.Version == "" {
		t.Error("健康检查应当返回版本号 —— 这正是 isc status 的验收内容")
	}
}

// TestMetaReportsCapabilities 验证平台能力矩阵被正确暴露。
//
// 下游 GUI 要靠它决定"哪些按钮该置灰"，因此字段不能缺。
func TestMetaReportsCapabilities(t *testing.T) {
	h := startDaemon(t)

	var meta gen.Meta
	h.getJSON("/v1/meta", &meta)

	if meta.Os == "" || meta.Arch == "" {
		t.Error("meta 应当包含运行平台与架构")
	}
	if meta.ApiVersion == "" {
		t.Error("meta 应当包含接口版本")
	}
	if !meta.Capabilities.Transport.Available {
		t.Error("传输后端必须可用 —— 否则内核根本无法被管理")
	}
	if meta.Capabilities.Transport.Backend == "" {
		t.Error("应当报告传输后端的具体实现名称")
	}
	// 密钥存储必须可用且必须自报后端 —— 凭据的机密性完全依赖它，
	// 而"用了哪种保护"是用户有权知道的事（文件兜底与系统密钥库
	// 的保护级别差别很大）。
	if !meta.Capabilities.SecretStore.Available {
		t.Error("密钥存储后端必须可用 —— 否则凭据无法安全保存")
	}
	if meta.Capabilities.SecretStore.Backend == "" {
		t.Error("密钥存储必须报告具体后端（如 windows-dpapi / file）")
	}
	// 服务管理后端现在三个平台都已实现。
	//
	// 于是断言从"M1 阶段尚未实现"改成"必须报告具体后端与说明" ——
	// 后者才是真正要保证的事：下游 GUI 要靠 Backend 决定显示什么，
	// 靠 Note 告诉用户需要什么权限。
	if meta.Capabilities.ServiceManager.Backend == "" {
		t.Error("服务管理后端必须报告具体实现（如 windows-scm / systemd / launchd）")
	}
	if meta.Capabilities.ServiceManager.Note == nil ||
		*meta.Capabilities.ServiceManager.Note == "" {
		t.Error("服务管理后端必须给出说明 —— " +
			"用户需要知道安装服务要不要管理员权限")
	}
}

// TestUnauthorizedRequestsAreRejected 验证令牌是硬门槛。
//
// 回环不等于可信：本机任何进程、以及浏览器里的任意页面都能向
// 回环端口发请求。令牌是唯一把"能连上"和"能操作"分开的东西。
func TestUnauthorizedRequestsAreRejected(t *testing.T) {
	h := startDaemon(t)

	cases := []struct {
		name   string
		header string
	}{
		{"完全不带令牌", ""},
		{"空 Bearer", "Bearer "},
		{"错误的令牌", "Bearer wrong-token-value"},
		{"令牌多一个字符", "Bearer " + h.info.Token + "x"},
		{"缺少 Bearer 前缀", h.info.Token},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(),
				http.MethodGet, h.baseURL+"/v1/meta", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := h.client.Do(req)
			if err != nil {
				t.Fatalf("请求失败: %v", err)
			}
			defer resp.Body.Close() //nolint:errcheck // 测试清理

			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("期望 401，得到 %d", resp.StatusCode)
			}

			var p gen.Problem
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatalf("错误响应应当是 problem+json: %v", err)
			}
			if p.Code == nil || *p.Code != "unauthorized" {
				t.Errorf("错误码应当是稳定的机器可读值，得到 %v", p.Code)
			}
		})
	}
}

// TestHealthIsReachableWithoutToken 验证健康检查的例外是有意的。
//
// 刚安装完、还没读到 runtime.json 的客户端需要能探测内核是否在跑。
// 它只暴露"活着"这一个事实，不构成信息泄露。
func TestHealthIsReachableWithoutToken(t *testing.T) {
	h := startDaemon(t)

	req, err := http.NewRequestWithContext(context.Background(),
		http.MethodGet, h.baseURL+"/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("健康检查应无需鉴权，得到 %d", resp.StatusCode)
	}
}

// TestOpenAPISpecIsServed 验证契约原文可获取。
//
// 验证控制台与 downstream GUI 都依赖它，而且它是"文档 == 实现"的保证。
func TestOpenAPISpecIsServed(t *testing.T) {
	h := startDaemon(t)

	resp, err := h.get("/v1/openapi.yaml")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "openapi: 3.1.0") {
		t.Errorf("返回内容不像 OpenAPI 3.1 契约:\n%s", firstLines(string(body), 5))
	}
	if !strings.Contains(string(body), "/v1/events") {
		t.Error("契约应当包含事件流端点")
	}
}

// TestJobLifecycleOverEventStream 是 M0-7 的核心验收：
// 提交任务 → 通过 WebSocket 收到进度与结束事件 → 任务详情可查。
func TestJobLifecycleOverEventStream(t *testing.T) {
	h := startDaemon(t)

	// 先建立事件流连接，再提交任务 —— 避免竞态导致漏掉早期事件。
	conn := h.dialEvents(t, 0)
	defer conn.CloseNow() //nolint:errcheck // 测试清理

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	accepted := gen.JobAccepted{}
	h.postJSON("/v1/debug/noop", strings.NewReader(`{"steps":4,"step_ms":20}`), &accepted)
	if accepted.JobId == "" {
		t.Fatal("提交任务后应返回 job_id")
	}

	var (
		sawProgress bool
		sawFinished bool
		sawLogEvent bool
	)
	for !sawFinished {
		var ev event.Event
		if err := wsjson.Read(ctx, conn, &ev); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if ev.Seq == 0 {
			t.Fatalf("收到序号为 0 的事件，说明序号分配有问题: %+v", ev)
		}

		switch ev.Type {
		case event.TypeJobProgress:
			var p struct {
				JobID    string  `json:"job_id"`
				Progress float64 `json:"progress"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("解析 job.progress 载荷失败: %v", err)
			}
			if p.JobID == accepted.JobId {
				sawProgress = true
				if p.Progress < 0 || p.Progress > 1 {
					t.Errorf("进度应在 [0,1]，得到 %v", p.Progress)
				}
			}
		case event.TypeJobFinished:
			var p struct {
				JobID  string `json:"job_id"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("解析 job.finished 载荷失败: %v", err)
			}
			if p.JobID != accepted.JobId {
				continue
			}
			if p.Status != string(gen.JobStatusSucceeded) {
				t.Fatalf("任务状态 = %q, 期望 succeeded", p.Status)
			}
			sawFinished = true
		case event.TypeLogAppended:
			// 日志被桥接进事件流，控制台因此不需要轮询日志接口。
			sawLogEvent = true
		}
	}

	if !sawProgress {
		t.Error("未收到该任务的进度事件")
	}
	if !sawLogEvent {
		t.Error("未收到日志事件 —— 日志与事件总线的桥接没生效")
	}

	// 任务详情必须可查，且结果已落库。
	var job gen.Job
	h.getJSON("/v1/jobs/"+accepted.JobId, &job)
	if job.Status != gen.JobStatusSucceeded {
		t.Errorf("任务详情状态 = %q", job.Status)
	}
	if job.Progress != 1 {
		t.Errorf("成功任务进度应为 1，得到 %v", job.Progress)
	}
	if job.Result == nil {
		t.Error("成功任务应当返回结果")
	}
}

// TestJobCancellation 验证取消是协作式且状态正确落库。
func TestJobCancellation(t *testing.T) {
	h := startDaemon(t)

	accepted := gen.JobAccepted{}
	// 100 步 × 50ms 足够长，保证取消请求到达时任务还在跑。
	h.postJSON("/v1/debug/noop", strings.NewReader(`{"steps":100,"step_ms":50}`), &accepted)

	time.Sleep(150 * time.Millisecond)

	canceled := gen.Job{}
	h.postJSON("/v1/jobs/"+accepted.JobId+"/cancel", nil, &canceled)

	// 取消是异步的，轮询等待终态。
	deadline := time.Now().Add(10 * time.Second)
	var final gen.Job
	for time.Now().Before(deadline) {
		h.getJSON("/v1/jobs/"+accepted.JobId, &final)
		if final.Status == gen.JobStatusCanceled {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != gen.JobStatusCanceled {
		t.Fatalf("任务状态 = %q, 期望 canceled", final.Status)
	}
	if final.FinishedAt == nil {
		t.Error("被取消的任务应当有结束时间")
	}
}

// TestCancelFinishedJobReturns409 验证状态机不允许非法操作。
func TestCancelFinishedJobReturns409(t *testing.T) {
	h := startDaemon(t)

	accepted := gen.JobAccepted{}
	h.postJSON("/v1/debug/noop", strings.NewReader(`{"steps":1,"step_ms":0}`), &accepted)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var j gen.Job
		h.getJSON("/v1/jobs/"+accepted.JobId, &j)
		if j.Status == gen.JobStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp := h.postJSON("/v1/jobs/"+accepted.JobId+"/cancel", nil, nil)
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("取消已完成的任务应返回 409，得到 %d", resp.StatusCode)
	}
	var p gen.Problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("错误响应应当是 problem+json: %v", err)
	}
	if p.Code == nil || *p.Code != "job_not_cancelable" {
		t.Errorf("错误码 = %v, 期望 job_not_cancelable", p.Code)
	}
}

// TestUnknownJobReturns404 验证 404 语义。
func TestUnknownJobReturns404(t *testing.T) {
	h := startDaemon(t)

	resp, err := h.get("/v1/jobs/does-not-exist")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404，得到 %d", resp.StatusCode)
	}
	var p gen.Problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatal(err)
	}
	if p.Code == nil || *p.Code != "job_not_found" {
		t.Errorf("错误码 = %v", p.Code)
	}
}

// TestEventStreamGapSignalsResync 验证补发链断裂时客户端被明确告知。
//
// 静默地"尽力补一部分"会让客户端长期显示错误状态，
// 明确报 gap 才能让它重新拉全量。
func TestEventStreamGapSignalsResync(t *testing.T) {
	h := startDaemon(t)

	// 声称收到过一个远超实际进度的事件序号。
	conn := h.dialEvents(t, 1_000_000)
	defer conn.CloseNow() //nolint:errcheck // 测试清理

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var ev event.Event
	if err := wsjson.Read(ctx, conn, &ev); err != nil {
		t.Fatalf("应当先收到一条 events.gap 事件，读取失败: %v", err)
	}
	if ev.Type != event.TypeEventsGap {
		t.Fatalf("事件类型 = %q, 期望 events.gap", ev.Type)
	}
	var p struct {
		Latest int64 `json:"latest"`
	}
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("解析 gap 载荷失败: %v", err)
	}
	if p.Latest < 0 {
		t.Errorf("gap 载荷应当带上最新的序号，得到 %d", p.Latest)
	}
}

// TestDaemonShutdownRemovesRuntimeFile 验证优雅关闭会清理运行时文件。
//
// 残留的 runtime.json 会让客户端以为内核还在跑，进而得到
// "连接被拒绝"这种难以归因的报错。
func TestDaemonShutdownRemovesRuntimeFile(t *testing.T) {
	dir := testsupport.ShortTempDir(t)
	t.Setenv(paths.EnvDataDir, dir)

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	d := daemon.New(daemon.Options{
		Paths:       p,
		LogHandler:  logx.New(io.Discard, errorLevel(), nil),
		LogLevelVar: errorLevel(),
		Lang:        i18n.ZhCN,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case <-d.Ready():
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("等待内核就绪超时")
	}

	if _, err := os.Stat(p.RuntimeFile()); err != nil {
		t.Fatalf("启动后应当存在 runtime.json: %v", err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("关闭时返回错误: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("关闭超时")
	}

	if _, err := os.Stat(p.RuntimeFile()); !os.IsNotExist(err) {
		t.Errorf("关闭后 runtime.json 应被删除，stat 得到 err=%v", err)
	}
}

// TestSecondInstanceRefusesToStart 验证不会出现两个内核抢同一份数据。
func TestSecondInstanceRefusesToStart(t *testing.T) {
	h := startDaemon(t)
	_ = h

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	// 复用同一个数据目录启动第二个实例。
	d2 := daemon.New(daemon.Options{
		Paths:       p,
		LogHandler:  logx.New(io.Discard, errorLevel(), nil),
		LogLevelVar: errorLevel(),
		Lang:        i18n.ZhCN,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = d2.Run(ctx)
	if err == nil {
		t.Fatal("第二个实例应当拒绝启动")
	}
	// 断言的是"用户能否看懂"，因此只检查关键短语而不是完整文案，
	// 避免翻译微调就让测试变红。
	if !strings.Contains(err.Error(), "已在运行") {
		t.Errorf("错误信息应当说明已有实例在运行，得到: %v", err)
	}
}

// dialEvents 建立事件流连接。
func (h *harness) dialEvents(t *testing.T, lastEventID int64) *websocket.Conn {
	t.Helper()

	url := h.wsURL("/v1/events")
	if lastEventID > 0 {
		url += "?lastEventId=" + itoa(lastEventID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{
		HTTPClient: h.client,
		// 与 CLI / 原生 GUI 一致：用请求头携带令牌。
		HTTPHeader: http.Header{
			"Authorization": []string{"Bearer " + h.info.Token},
		},
	}
	conn, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		t.Fatalf("建立事件流失败: %v", err)
	}
	return conn
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// ---------------------------------------------------------------------------
// 日志级别
// ---------------------------------------------------------------------------

// seedLogLevel 在一个还没被内核打开的数据目录里写下 log_level。
//
// 必须在 daemon.New 之前调用：级别是**启动时**从数据库里读出来的，
// 而"启动时读对了"正是这几条用例要验证的事。内核跑起来之后再改，
// 验证的就是另一条路径了（见 TestLogLevelChangeTakesEffectImmediately）。
func seedLogLevel(t *testing.T, dir, level string) {
	t.Helper()

	st, err := store.Open(context.Background(), filepath.Join(dir, "isc.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	if err := st.SaveSettings(context.Background(), map[string]string{
		settings.KeyLogLevel: level,
	}); err != nil {
		_ = st.Close()
		t.Fatalf("写入 log_level 失败: %v", err)
	}
	// 必须关掉：SQLite 的连接不共享，留着会让随后启动的内核拿不到写锁。
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
}

// TestLogLevelFromSettingsAppliesAtStartup 验证启动时会**真的按设置**定级。
//
// 这条用例之所以能证伪原来那个 bug，靠的是两件事：
//
//   - handler 自带的初值是 Debug，而设置里是 error。若内核启动时**不**
//     去读设置（改之前的样子），级别会停在 Debug，用例立刻失败；
//   - 设置取 error 而不是 debug，是为了让"没生效"与"生效了"两个方向
//     都能被观察到：既断言 debug 被挡下，也断言 error 仍放行。
//
// 日志不写文件也不读文件，而是收进内存缓冲区 —— 判据是处理器有没有
// 放行记录，落盘会引入磁盘与权限这些与本问题无关的变量。
func TestLogLevelFromSettingsAppliesAtStartup(t *testing.T) {
	dir := testsupport.ShortTempDir(t)
	seedLogLevel(t, dir, settings.LevelError)

	t.Setenv(paths.EnvDataDir, dir)
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	buf := new(strings.Builder)
	var mu sync.Mutex
	// 初值刻意设成 Debug：内核应当在读出设置之后把它收紧到 error。
	// 反过来（初值 error、设置 debug）在这里分不出"没生效"与"生效了"，
	// 因为两者都不会让 debug 日志多出来。
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelDebug)

	d := daemon.New(daemon.Options{
		Paths:       p,
		LogHandler:  logx.New(syncWriter{w: buf, mu: &mu}, levelVar, nil),
		LogLevelVar: levelVar,
		Lang:        i18n.ZhCN,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case <-d.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("内核启动失败: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("等待内核就绪超时")
	}

	// 拿一个**真实的**日志器来问"现在放行到哪一级"：它由内核自己创建，
	// 与各领域服务用的是同一个 handler。这比直接看 levelVar 更有说服力 ——
	// 后者只证明那个变量被 Set 了，不证明 handler 真的跟着它走。
	effective := slog.New(d.LogHandler())

	// 级别来自设置（error），因此不该有 debug 日志。
	if effective.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("设置的 log_level=error 没有生效：debug 仍被放行")
	}
	// 而 error 本身必须还在 —— 否则"看日志排查"这条路就断了。
	if !effective.Enabled(context.Background(), slog.LevelError) {
		t.Error("error 级别下 error 日志被误挡")
	}

	// 停稳之后再检查输出：从"按设置定级"到"写下那一行"是同步的，
	// 因此不需要 sleep 去赌。
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("内核关闭超时")
	}

	mu.Lock()
	got := buf.String()
	mu.Unlock()

	if !strings.Contains(got, "正在应用日志级别") {
		t.Fatalf("启动日志里没有级别确认那一行:\n%s", firstLines(got, 20))
	}
	if want := "from_settings=error"; !strings.Contains(got, want) {
		t.Errorf("级别确认那一行不对，期望 %q：\n%s", want, firstLines(got, 20))
	}
	// 反向判据：如果级别被读成 debug，这里一定会出现 DEBUG 行 ——
	// 只有 level >= 生效级别的记录才会被写进缓冲区。
	if strings.Contains(got, "level=DEBUG") {
		t.Errorf("日志里出现了 DEBUG 行 —— 级别没有按设置收紧:\n%s", firstLines(got, 20))
	}
}

// TestLogLevelChangeTakesEffectImmediately 验证运行期改级别**立刻**生效，
// 不必重启内核。
//
// 判据是一个真实的 HTTP 请求：中间件给每个请求都记一条 debug 日志
// （见 api.LogRequests），因此"改成 error 之后还有没有请求日志"
// 直接回答了"级别有没有改到"。全程不重启内核 —— 重启就不是热更新了。
func TestLogLevelChangeTakesEffectImmediately(t *testing.T) {
	dir := testsupport.ShortTempDir(t)
	t.Setenv(paths.EnvDataDir, dir)
	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	// 数据库里不写 log_level：走 Default()，也就是 info。
	// 这同时钉住了"默认不是 debug"——需求要求缺失/非法时退回 info，
	// 而不是静默变成最吵的那一档。
	buf := new(strings.Builder)
	var mu sync.Mutex
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)

	d := daemon.New(daemon.Options{
		Paths:       p,
		LogHandler:  logx.New(syncWriter{w: buf, mu: &mu}, levelVar, nil),
		LogLevelVar: levelVar,
		Lang:        i18n.ZhCN,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case <-d.Ready():
	case err := <-done:
		cancel()
		t.Fatalf("内核启动失败: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("等待内核就绪超时")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("内核关闭超时")
		}
	})

	h := &harness{t: t, daemon: d, paths: p, stopped: true}

	// 客户端要拿运行时文件里的令牌才能调本地接口。
	info, err := runtimeinfo.Read(p.RuntimeFile())
	if err != nil {
		t.Fatalf("读取 runtime.json 失败: %v", err)
	}
	ep := platform.Endpoint(info.Endpoint)
	hc, err := ep.HTTPClient(10 * time.Second)
	if err != nil {
		t.Fatalf("构造 HTTP 客户端失败: %v", err)
	}
	h.info = info
	h.endpoint = ep
	h.client = hc
	h.baseURL = ep.HTTPBaseURL()

	// 拿内核自己的日志器来问级别。它取自内核持有的那份 handler ——
	// 级别由内核按设置接管，这里要验的正是后者。
	log := slog.New(d.LogHandler())

	// 默认（info）：debug 必须被挡下。
	if log.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("没有设置过 log_level 时按默认应当只到 info，debug 不该被放行")
	}

	// 改设置：这是界面上那一步真正走的路径（PATCH /v1/settings）。
	resp := h.do(http.MethodPatch, "/v1/settings", []byte(`{"log_level":"debug"}`))
	_ = resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改 log_level 失败：HTTP %d", resp.StatusCode)
	}

	// 回调是**同步**跑的（见 settings.SetOnChange 的说明），因此
	// POST 返回之后级别必须已经改好，不需要等待、也不需要重试。
	if !log.Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("改成 debug 之后 debug 仍被挡下 —— 设置没有传播到日志级别")
	}

	// 不止问 Enabled，还真的发一次请求，看中间件那条 debug 日志
	// 有没有被写出来。
	//
	// 这是"级别真的生效"最直接的证据：中间件用的是与各领域服务同一个
	// handler，日志进的是这里给的缓冲区。只断言 Enabled 的话，
	// 一个"Enabled 说放行、Handle 却仍丢弃"的实现会漏过去。
	got, err := h.get("/v1/health")
	if err != nil {
		t.Fatalf("请求 /v1/health 失败: %v", err)
	}
	_ = got.Body.Close() //nolint:errcheck // 测试清理

	mu.Lock()
	out := buf.String()
	mu.Unlock()
	if !strings.Contains(out, "path=/v1/health") {
		t.Fatalf("改成 debug 之后没有看到请求日志 —— 级别没有真的作用到输出上:\n%s",
			firstLines(out, 20))
	}

	// 再改回 error：反方向同样要立刻生效。只测一个方向的话，
	// "级别只会变宽、不会收紧"这种半对的实现会漏过去。
	//
	// 判据取"切换那一刻之后新写进来的那一段"：上面的请求已经完整返回
	// （中间件在响应写完才记日志），因此这一段里除了设置变更本身的
	// 副作用之外，不该再有任何 DEBUG 行。
	mu.Lock()
	mark := buf.Len()
	mu.Unlock()

	resp = h.do(http.MethodPatch, "/v1/settings", []byte(`{"log_level":"error"}`))
	_ = resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("改 log_level 失败：HTTP %d", resp.StatusCode)
	}

	if log.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("改成 error 之后 info 仍被放行 —— 级别只放宽不收紧")
	}
	if !log.Enabled(context.Background(), slog.LevelError) {
		t.Error("改成 error 之后 error 本身被误挡")
	}

	// 再打一次接口：这次不该再留下请求日志。级别是同步改好的，
	// 因此这一条也是确定性的，不靠 sleep 去赌。
	got, err = h.get("/v1/health")
	if err != nil {
		t.Fatalf("请求 /v1/health 失败: %v", err)
	}
	_ = got.Body.Close() //nolint:errcheck // 测试清理

	mu.Lock()
	after := buf.String()[mark:]
	mu.Unlock()
	if strings.Contains(after, "level=DEBUG") {
		t.Errorf("改成 error 之后仍在输出 DEBUG 日志 —— 级别只放宽不收紧:\n%s",
			firstLines(after, 20))
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// syncWriter 让内存缓冲区可以被内核的后台 goroutine 安全地写。
//
// strings.Builder 本身不是并发安全的，而内核会在多个 goroutine 里记日志；
// 不加锁的话 -race 下会报数据竞争，而那种失败看起来与本用例要验证的
// 东西毫无关系。
type syncWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
