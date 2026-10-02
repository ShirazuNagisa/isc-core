package daemon_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
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
)

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

// startDaemon 在临时数据目录里启动一个真实的内核实例。
func startDaemon(t *testing.T) *harness {
	t.Helper()
	return startDaemonIn(t, t.TempDir())
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
		LogHandler: logx.New(io.Discard, slog.LevelDebug, nil),
		Lang:       i18n.ZhCN,
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
	dir := t.TempDir()
	t.Setenv(paths.EnvDataDir, dir)

	p, err := paths.Resolve()
	if err != nil {
		t.Fatal(err)
	}

	d := daemon.New(daemon.Options{
		Paths:      p,
		LogHandler: logx.New(io.Discard, slog.LevelError, nil),
		Lang:       i18n.ZhCN,
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
		Paths:      p,
		LogHandler: logx.New(io.Discard, slog.LevelError, nil),
		Lang:       i18n.ZhCN,
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

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
