// Package libisc 是内核的**库接口层**：GUI（Swift / C#）通过 cmd/libisc 那个
// c-shared 产物链接到它。
//
// # 为什么逻辑单独成包
//
// 两个理由，都是被工具链逼出来的、也都是对的：
//
//   - Go 不允许在 _test.go 里 import "C"，因此 cgo 只能留在 cmd/libisc 的薄包装
//     里（C 字符串 ↔ Go 字符串），行为本身在**普通 Go 测试**里验证；
//   - 这一层不依赖 cgo，于是它参与默认的 CI（`CGO_ENABLED=0 go test ./...`），
//     而不是只在那一个需要 cgo 的 job 里才被测到。
package libisc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/daemon"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/version"
)

// 一个进程里只允许一个内核实例：多个实例会抢同一个套接字与数据目录。
var (
	mu      sync.Mutex
	current *kernel
)

type kernel struct {
	daemon *daemon.Daemon
	cancel context.CancelFunc
	done   chan struct{}
	paths  paths.Paths
}

// 错误码：GUI 靠它做分支，而不是去匹配错误文案。
//
// 文案会随语言与措辞变，错误码是契约的一部分。
const (
	CodeBadRequest   = "bad_request"
	CodeNotRunning   = "not_running"
	CodeNotReady     = "not_ready"
	CodeAlreadyUp    = "already_running"
	CodeTimeout      = "timeout"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeInternal     = "internal"
)

// ---------------------------------------------------------------------------
// 响应组装
// ---------------------------------------------------------------------------

func failMap(code string, status int, err error) map[string]any {
	out := map[string]any{"ok": false, "code": code, "error": err.Error()}
	if status != 0 {
		out["status"] = status
	}
	return out
}

// marshal 把响应编成 JSON。编不出来时退回一个**合法**的 JSON ——
// 绝不能让调用方拿到空指针（那是崩溃，而不是错误）。
func Marshal(m map[string]any) string {
	b, err := json.Marshal(m)
	if err != nil {
		return `{"ok":false,"code":"internal","error":"response encoding failed"}`
	}
	return string(b)
}

func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	default:
		if status >= 500 {
			return CodeInternal
		}
		return CodeBadRequest
	}
}

func decodeBody(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// 响应未必是 JSON（例如 404 的纯文本）。
		return strings.TrimSpace(string(raw))
	}
	return decoded
}

// ---------------------------------------------------------------------------
// 版本
// ---------------------------------------------------------------------------

func APIVersion() string { return version.APIVersion }

func VersionInfo() map[string]any {
	return map[string]any{
		"ok":         true,
		"api":        version.APIVersion,
		"version":    version.Version,
		"commit":     version.Commit,
		"build_time": version.BuildTime,
	}
}

// ---------------------------------------------------------------------------
// 生命周期
// ---------------------------------------------------------------------------

// startKernel 在**本进程内**启动内核，就绪后返回。
func Start(dataDir string) map[string]any {
	mu.Lock()
	defer mu.Unlock()

	if current != nil {
		return failMap(CodeAlreadyUp, 0, errors.New(i18n.T("libisc.already_running")))
	}

	if dataDir != "" {
		// 数据目录只能通过环境变量交给内核（paths.Resolve 只认它），
		// 因此必须在 Resolve 之前设好。
		if err := os.Setenv(paths.EnvDataDir, dataDir); err != nil {
			return failMap(CodeInternal, 0, err)
		}
	}

	p, err := paths.Resolve()
	if err != nil {
		return failMap(CodeInternal, 0, err)
	}

	d := daemon.New(daemon.Options{Paths: p, Lang: i18n.Default})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Run(ctx) // Run 阻塞到 ctx 被取消
	}()

	// 等就绪，但要给上限：起不来时必须把原因还给调用方，而不是让它干等。
	select {
	case <-d.Ready():
	case <-done:
		cancel()
		<-done
		return failMap(CodeNotReady, 0, errors.New(i18n.T("libisc.exited_during_start")))
	case <-time.After(15 * time.Second):
		cancel()
		<-done
		return failMap(CodeTimeout, 0, fmt.Errorf(i18n.T("libisc.start_timeout"), 15))
	}

	info, err := runtimeinfo.Read(d.RuntimeFile())
	if err != nil {
		cancel()
		<-done
		return failMap(CodeInternal, 0, fmt.Errorf(i18n.T("libisc.runtime_info_failed"), err))
	}

	current = &kernel{daemon: d, cancel: cancel, done: done, paths: p}
	return map[string]any{
		"ok":           true,
		"endpoint":     info.Endpoint,
		"runtime_file": d.RuntimeFile(),
		"data_dir":     p.Root(),
	}
}

// stopKernel 停止内核并等它真的退出。幂等。
func Stop() map[string]any {
	mu.Lock()
	k := current
	current = nil
	mu.Unlock()

	if k == nil {
		return map[string]any{
			"ok": true, "stopped": false,
			"note": i18n.T("libisc.not_running_note"),
		}
	}

	k.cancel()
	select {
	case <-k.done:
	case <-time.After(20 * time.Second):
		return failMap(CodeTimeout, 0, fmt.Errorf(i18n.T("libisc.stop_timeout"), 20))
	}
	return map[string]any{"ok": true, "stopped": true}
}

func reStart(dataDir string) map[string]any {
	if r := Stop(); r["ok"] != true {
		return r
	}
	return Start(dataDir)
}

// Restart 重启内核（等价于 Stop + Start）。
//
// 用途主要是"改了数据目录/设置之后重新来一遍"；失败时返回 Stop 或 Start 的
// 错误，调用方据此判断内核是不是还活着。
func Restart(dataDir string) map[string]any {
	if r := Stop(); r["ok"] != true {
		return r
	}
	return Start(dataDir)
}

// ---------------------------------------------------------------------------
// 进程内派发
// ---------------------------------------------------------------------------

// dispatch 把一次请求直接交给内核自己的 handler（不经过套接字/TCP）。
func dispatch(k *kernel, method, path, body string) (int, []byte, error) {
	handler := k.daemon.Handler()
	if handler == nil {
		return 0, nil, errors.New(i18n.T("libisc.handler_missing"))
	}

	info, err := runtimeinfo.Read(k.daemon.RuntimeFile())
	if err != nil {
		return 0, nil, err
	}

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://127.0.0.1"+path, reader)
	if err != nil {
		return 0, nil, err
	}

	// 令牌由库自己从运行信息里取用，**不外传** —— GUI 不需要也不应该知道它。
	//
	// Host 用 127.0.0.1 而不是任意名字：内核有一层 DNS rebinding 防护会拒绝
	// 非本机 Host 的请求（那层防护挡的是浏览器发起的跨站请求，是对的）。
	req.Header.Set("Authorization", "Bearer "+info.Token)
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes(), nil
}

// callKernel 调用契约里的任意路径。
func Call(method, path, body string) map[string]any {
	mu.Lock()
	k := current
	mu.Unlock()

	if k == nil {
		return failMap(CodeNotRunning, 0, errors.New(i18n.T("libisc.not_running")))
	}

	m := strings.ToUpper(strings.TrimSpace(method))
	if m == "" {
		m = http.MethodGet
	}
	if path == "" || path[0] != '/' {
		return failMap(CodeBadRequest, 0, fmt.Errorf(i18n.T("libisc.bad_path"), path))
	}

	status, raw, err := dispatch(k, m, path, body)
	if err != nil {
		return failMap(CodeInternal, 0, err)
	}
	if status < 200 || status >= 300 {
		return failMap(codeForStatus(status), status,
			fmt.Errorf(i18n.T("libisc.call_failed"), status, readableProblem(raw)))
	}
	return map[string]any{"ok": true, "status": status, "body": decodeBody(raw)}
}

// problemDocument 是内核自己发出的 RFC 7807 错误文档里我们关心的两个字段。
type problemDocument struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

// readableProblem 把错误响应的正文压缩成一句给人看的话。
//
// # 为什么要做这一步
//
// 错误正文是完整的 problem+json，直接塞进 error 的结果是：界面上出现
// 一屏转义过的花括号（\"code\":\"upstream_error\",\"detail\":…），用户读不出
// 重点，而且那串没有任何空格的文本会把布局撑坏 —— 曾经把整个左侧栏
// 挤没。GUI 拿到的应该是"服务商拒绝了这次操作：列出区域失败：…"。
//
// 解析不出来就退回原始正文，但要截断：错误信息是给人看的，不是日志转储。
func readableProblem(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return ""
	}
	var doc problemDocument
	if json.Unmarshal([]byte(trimmed), &doc) == nil {
		switch {
		case doc.Title != "" && doc.Detail != "":
			return fmt.Sprintf(i18n.T("libisc.problem_title_detail"), doc.Title, doc.Detail)
		case doc.Detail != "":
			return doc.Detail
		case doc.Title != "":
			return doc.Title
		}
	}
	return truncateProblem(trimmed)
}

// maxProblemBytes 是退回原始正文时的长度上限。
const maxProblemBytes = 400

// truncateProblem 按字节截断，并保证不切碎多字节字符。
func truncateProblem(text string) string {
	if len(text) <= maxProblemBytes {
		return text
	}
	runes := []rune(text)
	kept := 0
	for i, r := range runes {
		kept += len(string(r))
		if kept > maxProblemBytes {
			return string(runes[:i]) + "…"
		}
	}
	return text
}

// statusInfo 把 health 与 meta 合成一份，GUI 一次调用就能拿全。
func Status() map[string]any {
	mu.Lock()
	k := current
	mu.Unlock()

	if k == nil {
		return failMap(CodeNotRunning, 0, errors.New(i18n.T("libisc.not_running")))
	}

	get := func(path string) (any, error) {
		status, raw, err := dispatch(k, http.MethodGet, path, "")
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf(i18n.T("libisc.call_failed"), status, readableProblem(raw))
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
		return out, nil
	}

	health, err := get("/v1/health")
	if err != nil {
		return failMap(CodeInternal, 0, fmt.Errorf(i18n.T("libisc.health_decode_failed"), err))
	}
	meta, err := get("/v1/meta")
	if err != nil {
		return failMap(CodeInternal, 0, fmt.Errorf(i18n.T("libisc.meta_decode_failed"), err))
	}
	return map[string]any{"ok": true, "health": health, "meta": meta}
}

// eventsSince 拉取 since 之后的事件（游标式长轮询）。
func Events(since int64, timeoutMs int) map[string]any {
	mu.Lock()
	k := current
	mu.Unlock()

	if k == nil {
		return failMap(CodeNotRunning, 0, errors.New(i18n.T("libisc.not_running")))
	}
	bus := k.daemon.Events()
	if bus == nil {
		return failMap(CodeNotReady, 0, errors.New(i18n.T("libisc.bus_missing")))
	}

	if since <= 0 {
		// 首次调用从当前时刻开始，避免把历史事件全倒给 GUI。
		since = bus.LatestSeq()
	}

	sub, err := bus.Subscribe(since)
	if err != nil {
		return failMap(CodeInternal, 0, err)
	}
	defer sub.Close()

	events := []event.Event{}
	next := since
	append_ := func(ev event.Event) {
		events = append(events, ev)
		next = ev.Seq
	}

	// 先按需阻塞等一条（timeoutMs=0 时跳过），再把此刻已到达的都取走 ——
	// 否则 GUI 每轮只能拿到一条事件。
	if timeoutMs > 0 {
		timer := time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		select {
		case ev, ok := <-sub.C():
			if ok {
				append_(ev)
			}
		case <-timer.C:
		}
		timer.Stop()
	}
drain:
	for {
		select {
		case ev, ok := <-sub.C():
			if !ok {
				break drain
			}
			append_(ev)
		default:
			break drain
		}
	}

	gap := false
	var eg *event.ErrGap
	if errors.As(sub.Err(), &eg) {
		gap = true
	}
	// 游标不连续也说明丢过事件：下一条的序号应当紧接上一条。
	if len(events) > 0 && events[0].Seq > since+1 {
		gap = true
	}

	return map[string]any{"ok": true, "events": events, "next": next, "gap": gap}
}
