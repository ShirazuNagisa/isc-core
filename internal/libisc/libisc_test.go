package libisc

// 这一层不依赖 cgo，因此这些用例在默认 CI（CGO_ENABLED=0）里就会跑。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ShirazuNagisa/isc-core/internal/settings"
	"github.com/ShirazuNagisa/isc-core/internal/store"
	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestMain 把凭据存储固定成文件后端：测试**不许碰开发机的系统钥匙串**
// （见 docs/DECISIONS.md D23 与 internal/testsupport）。
func TestMain(m *testing.M) { testsupport.IsolateSecretStore(m) }

// startTemp 在短路径的临时数据目录上启动内核。
//
// 必须用 ShortTempDir：macOS 的 Unix 套接字路径上限是 104 字节，而
// t.TempDir() 给出的路径通常就超了（那个坑已经踩过一次）。
func startTemp(t *testing.T) string {
	t.Helper()

	_ = Stop() // 保证每个用例从干净状态开始
	dir := testsupport.ShortTempDir(t)
	res := Start(dir)
	if res["ok"] != true {
		t.Fatalf("启动失败: %v", res)
	}
	t.Cleanup(func() { _ = Stop() })
	return dir
}

func TestAPIVersionIsPlainString(t *testing.T) {
	if got := APIVersion(); len(got) < 2 || got[0] != 'v' {
		t.Errorf("接口版本不像 vN: %q", got)
	}
}

func TestVersionInfo(t *testing.T) {
	v := VersionInfo()
	for _, k := range []string{"ok", "api", "version", "commit"} {
		if _, present := v[k]; !present {
			t.Errorf("版本信息里缺少 %s: %v", k, v)
		}
	}
}

func TestStartStatusStop(t *testing.T) {
	startTemp(t)

	status := Status()
	if status["ok"] != true {
		t.Fatalf("状态查询失败: %v", status)
	}
	health, _ := status["health"].(map[string]any)
	if health == nil || health["status"] != "ok" {
		t.Errorf("health 不对: %v", status["health"])
	}

	// 六个平台后端必须都在。
	meta, _ := status["meta"].(map[string]any)
	caps, _ := meta["capabilities"].(map[string]any)
	for _, name := range []string{
		"firewall", "ip_monitor", "low_port_binder",
		"secret_store", "service_manager", "transport",
	} {
		if _, ok := caps[name]; !ok {
			t.Errorf("能力清单里缺少 %s（实际：%v）", name, caps)
		}
	}

	// 重复启动必须被挡住，而不是起第二个内核。
	dup := Start("/tmp/isc-libisc-dup")
	if dup["code"] != CodeAlreadyUp {
		t.Errorf("重复启动的错误码是 %v，期望 %s", dup["code"], CodeAlreadyUp)
	}

	if stop := Stop(); stop["ok"] != true || stop["stopped"] != true {
		t.Errorf("停止失败: %v", stop)
	}
	// 幂等
	if again := Stop(); again["ok"] != true || again["stopped"] != false {
		t.Errorf("重复停止应当是 ok 且 stopped=false: %v", again)
	}
}

func TestRestart(t *testing.T) {
	dir := startTemp(t)

	res := reStart(dir)
	if res["ok"] != true {
		t.Fatalf("重启失败: %v", res)
	}
	// 重启之后仍然可用
	if status := Status(); status["ok"] != true {
		t.Errorf("重启之后状态查询失败: %v", status)
	}
}

func TestCallDispatchesInProcess(t *testing.T) {
	startTemp(t)

	res := Call("GET", "/v1/health", "")
	if res["ok"] != true {
		t.Fatalf("GET /v1/health 失败: %v", res)
	}
	body, _ := res["body"].(map[string]any)
	if body["status"] != "ok" {
		t.Errorf("health 响应不对: %v", res["body"])
	}

	// 未知路径 → not_found（错误码要能被 GUI 判别）
	if nf := Call("GET", "/v1/nope", ""); nf["code"] != CodeNotFound {
		t.Errorf("未知路径的错误码是 %v，期望 %s", nf["code"], CodeNotFound)
	}

	// 路径不合法 → bad_request
	if bad := Call("GET", "v1/health", ""); bad["code"] != CodeBadRequest {
		t.Errorf("非法路径的错误码是 %v，期望 %s", bad["code"], CodeBadRequest)
	}

	// 方法为空时按 GET 处理
	if empty := Call("", "/v1/health", ""); empty["ok"] != true {
		t.Errorf("空方法应当按 GET 处理: %v", empty)
	}
}

func TestCallCanWrite(t *testing.T) {
	startTemp(t)

	// 写路径也要能走通：改一次设置再读回来。
	// 用设置接口是因为它幂等、可复原，不会在开发机上留下系统级副作用。
	before := Call("GET", "/v1/settings", "")
	if before["ok"] != true {
		t.Fatalf("读设置失败: %v", before)
	}

	// 方法是 **PATCH**（不是我凭印象写的 PUT）—— 接口面一律以
	// api/openapi.yaml 的契约为准。这类"猜方法/猜路径"的错今天已经犯过两次。
	// 字段名与取值也照契约：是 `lang`，取值是 `en` / `zh-CN`
	//（设置结构里**没有** language 这个字段 —— 又是凭印象写错的）。
	body, _ := json.Marshal(map[string]any{"lang": "en"})
	after := Call("PATCH", "/v1/settings", string(body))
	if after["ok"] != true {
		t.Fatalf("写设置失败: %v", after)
	}

	// 读回来确认真的改了，再复原成中文（别在开发机上留下副作用）。
	read := Call("GET", "/v1/settings", "")
	if read["ok"] != true {
		t.Fatalf("读设置失败: %v", read)
	}
	if settings, _ := read["body"].(map[string]any); settings["lang"] != "en" {
		t.Errorf("设置没生效: %v", read["body"])
	}
	if restore := Call("PATCH", "/v1/settings", `{"lang":"zh-CN"}`); restore["ok"] != true {
		t.Errorf("恢复语言设置失败: %v", restore)
	}
}

// seedLogLevel 在库还没启动内核时，先把 log_level 写进数据目录里的库。
//
// 必须趁内核没起来的时候写：级别是**启动时**读出来的，这也是这条用例
// 真正要验的那一步（运行期改级别走的是另一条路径，见下面的 PATCH）。
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
	if err := st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
}

// TestLogLevelAppliesOnLibraryPath 验证**库（c-shared）这条路**上
// log_level 同样是真的在生效。
//
// # 为什么这条用例非有不可
//
// Phecda 是把内核当 c-shared 库嵌进同一个进程用的（cmd/libisc），
// 它与 CLI 走的不是同一个入口：库调用 daemon.New 时**不传 handler**
// （见 internal/libisc.Start），由内核自己建一个写 stderr 的。
// 也就是说，"CLI 那条路上级别生效"并不能推出"库这条路上也生效" ——
// 而用户在界面上看到的恰恰是库这一条路。
//
// # 为什么捕获 stderr
//
// 库没有、也不该有"把日志处理器交给我"的注入点（那会把内部结构
// 暴露给 GUI）。而用户看到的日志就是 stderr 上那一份，因此这里直接
// 接管 stderr 的文件描述符来观察**真实产物**，而不是去问某个内部
// 变量"你觉得级别是多少"。
func TestLogLevelAppliesOnLibraryPath(t *testing.T) {
	_ = Stop()
	dir := testsupport.ShortTempDir(t)

	// 设置里写 debug，但内核自己的临时初值是 info ——
	// 因此"看到 DEBUG 行"只可能来自"启动时读了设置"。
	seedLogLevel(t, dir, settings.LevelDebug)

	capture := captureStderr(t)

	if res := Start(dir); res["ok"] != true {
		t.Fatalf("启动失败: %v", res)
	}
	t.Cleanup(func() { _ = Stop() })

	// 一次真实调用。中间件给每个请求都记一条 debug 日志
	// （见 internal/api.LogRequests），它进的就是捕获到的 stderr。
	if res := Call("GET", "/v1/health", ""); res["ok"] != true {
		t.Fatalf("GET /v1/health 失败: %v", res)
	}

	// 等在途的写落下来。stderr 是文件描述符，写入与读取之间没有
	// happens-before 关系，因此这里只能等一小会儿 —— 给的是宽裕的
	// 余量，不是在赌某个精确的时序。
	time.Sleep(300 * time.Millisecond)

	got := capture.text()
	if !strings.Contains(got, "level=DEBUG") {
		t.Fatalf("库路径上没有按设置输出 debug 日志 —— log_level 没生效:\n%s", got)
	}
	if !strings.Contains(got, "path=/v1/health") {
		t.Errorf("debug 日志里没有这次请求的记录:\n%s", got)
	}

	// 运行期改级别：库的调用入口就是 GUI 用的那个（契约里的 PATCH）。
	patched := Call("PATCH", "/v1/settings", `{"log_level":"error"}`)
	if patched["ok"] != true {
		t.Fatalf("改 log_level 失败: %v", patched)
	}

	// 切换那一刻之后新写进来的那一段：不该再有 DEBUG 行。
	mark := capture.len()
	if res := Call("GET", "/v1/health", ""); res["ok"] != true {
		t.Fatalf("第二次 GET /v1/health 失败: %v", res)
	}
	time.Sleep(300 * time.Millisecond)

	if after := capture.text()[mark:]; strings.Contains(after, "level=DEBUG") {
		t.Errorf("改成 error 之后库路径上仍在输出 DEBUG 日志:\n%s", after)
	}
}

// captureStderr 把 stderr 重定向到一个管道，返回可读取其内容的句柄。
//
// 必须在 Start **之前**调用：内核的 handler 是在启动时建的，
// 它捕获的是当时的 os.Stderr。
type stderrCapture struct {
	mu  sync.Mutex
	buf strings.Builder
	// orig 是原来的 stderr，清理时还回去 —— 不还的话测试失败信息
	// 就再也打不出来了，排查时看到的是"没有任何输出"。
	orig *os.File
	pipe *os.File
}

func captureStderr(t *testing.T) *stderrCapture {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("创建管道失败: %v", err)
	}

	c := &stderrCapture{orig: os.Stderr, pipe: r}
	os.Stderr = w

	// 后台把管道抽干：内核的日志量会超过管道的缓冲，
	// 不抽干会让写日志的那一方**阻塞**，表现成内核卡住。
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				c.mu.Lock()
				c.buf.Write(buf[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	t.Cleanup(func() {
		_ = Stop()
		os.Stderr = c.orig
		_ = w.Close()
		<-done
		_ = r.Close()
	})
	return c
}

func (c *stderrCapture) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func (c *stderrCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Len()
}

func TestCallBeforeStartIsNotRunning(t *testing.T) {
	_ = Stop()

	if res := Call("GET", "/v1/health", ""); res["code"] != CodeNotRunning {
		t.Errorf("未启动时的错误码是 %v，期望 %s", res["code"], CodeNotRunning)
	}
	if st := Status(); st["code"] != CodeNotRunning {
		t.Errorf("未启动时查状态的错误码是 %v，期望 %s", st["code"], CodeNotRunning)
	}
	if ev := Events(0, 0); ev["code"] != CodeNotRunning {
		t.Errorf("未启动时拉事件的错误码是 %v，期望 %s", ev["code"], CodeNotRunning)
	}
}

func TestEventsLongPoll(t *testing.T) {
	startTemp(t)

	first := Events(0, 0)
	if first["ok"] != true {
		t.Fatalf("事件拉取失败: %v", first)
	}
	if _, ok := first["next"]; !ok {
		t.Error("返回里缺少游标 next")
	}

	// 带超时的长轮询也要正常返回（没有新事件时等到超时）。
	second := Events(0, 100)
	if second["ok"] != true {
		t.Fatalf("长轮询失败: %v", second)
	}
	if second["gap"] != false {
		t.Errorf("不该报 gap: %v", second)
	}
}

func TestStopWithoutStart(t *testing.T) {
	_ = Stop()
	res := Stop()
	if res["ok"] != true || res["stopped"] != false {
		t.Errorf("未启动时停止应当是 ok 且 stopped=false: %v", res)
	}
}

// 错误正文必须是给人看的一句话。
//
// 这里守的是一个真实事故：内核把整个 problem+json 塞进 error，界面上
// 出现一屏转义过的花括号；那串没有任何空格的文本还把布局撑坏，DNS 服务商
// 一出错，整个左侧栏就被挤没了。GUI 拿到的应该是可读的原因。
func TestCallReportsReadableProblem(t *testing.T) {
	startTemp(t)

	// /v1/nope 会得到内核自己生成的 problem+json。
	res := Call("GET", "/v1/nope", "")
	if res["ok"] != false {
		t.Fatalf("未知路径应当失败: %v", res)
	}
	msg, _ := res["error"].(string)
	if msg == "" {
		t.Fatal("错误信息不该为空")
	}
	if strings.Contains(msg, `{"`) || strings.Contains(msg, `"type"`) {
		t.Fatalf("错误信息里不该出现原始 JSON：%s", msg)
	}
	if len([]rune(msg)) > 400 {
		t.Fatalf("错误信息过长（%d 字符），界面会被撑坏：%s", len([]rune(msg)), msg)
	}
}

func TestReadableProblemPrefersTitleAndDetail(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "标题与详情都有",
			raw:  `{"code":"upstream_error","status":400,"title":"服务商拒绝了这次操作","detail":"列出区域失败：证书不匹配"}`,
			want: "服务商拒绝了这次操作：列出区域失败：证书不匹配",
		},
		{
			name: "只有详情",
			raw:  `{"detail":"磁盘写满了"}`,
			want: "磁盘写满了",
		},
		{
			name: "只有标题",
			raw:  `{"title":"没有找到"}`,
			want: "没有找到",
		},
		{
			name: "不是 JSON 时原样返回",
			raw:  "plain text failure",
			want: "plain text failure",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := readableProblem([]byte(tc.raw)); got != tc.want {
				t.Errorf("readableProblem = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 退回原始正文时必须截断，且不能把多字节字符切碎。
func TestTruncateProblemKeepsRunesIntact(t *testing.T) {
	long := strings.Repeat("错误信息", 400)
	got := truncateProblem(long)
	if len([]rune(got)) >= len([]rune(long)) {
		t.Fatal("超长正文没有被截断")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("截断后应有省略号: %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断切碎了多字节字符: %q", got)
	}
}
