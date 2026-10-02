package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖通知中心的**核心价值：不被刷屏**。
//
// 内核产生的很多事件会成串出现（IPv6 前缀抖动、服务商限流、证书续期
// 重试）。原样转发的话用户会在几分钟内收到十几条一样的消息，然后关掉
// 通知 —— 而那之后真正重要的那条他也看不到了。
//
// 因此重点测三件事：去重、静默期、以及**抑制之后的补发汇总**。
// 最后一条最容易被漏掉：只计数不补发会让用户以为那段时间什么都没发生。

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// fakeChannel 记录收到的消息。
type fakeChannel struct {
	mu       sync.Mutex
	received []Message
	err      error
	delay    time.Duration
}

func (f *fakeChannel) Name() string { return "假通道" }
func (f *fakeChannel) Kind() string { return "fake" }

func (f *fakeChannel) Send(_ context.Context, msg Message) error {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.received = append(f.received, msg)
	return nil
}

func (f *fakeChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

func (f *fakeChannel) all() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.received...)
}

// drain 起一个投递循环并等它把队列处理完。
func drain(t *testing.T, m *Manager) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()

	// 等到队列为空再取消。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(m.queue) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// 再给投递一点时间。
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
}

func newTestManager(t *testing.T, ch Channel) *Manager {
	t.Helper()
	m := NewManager(nil)
	m.AddChannel(ch)
	return m
}

// ---------------------------------------------------------------------------
// 去重与静默期
// ---------------------------------------------------------------------------

// TestDedupSuppressesRepeatedMessages 是这里最重要的一条。
func TestDedupSuppressesRepeatedMessages(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(time.Hour)

	// 同一个去重键连发 10 条（模拟前缀抖动）。
	for i := 0; i < 10; i++ {
		m.Notify(Message{
			Event: "ip.changed", Title: "地址变化",
			DedupKey: "ip.changed:wlan",
		})
	}
	drain(t, m)

	if got := ch.count(); got != 1 {
		t.Errorf("同一个去重键应当只发出 1 条，实际 %d 条", got)
	}
}

func TestDifferentDedupKeysBothSend(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(time.Hour)

	m.Notify(Message{Event: "a", Title: "A", DedupKey: "key-a"})
	m.Notify(Message{Event: "b", Title: "B", DedupKey: "key-b"})
	drain(t, m)

	if got := ch.count(); got != 2 {
		t.Errorf("不同的去重键都应当发出，实际 %d 条", got)
	}
}

func TestEmptyDedupKeyAlwaysSends(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(time.Hour)

	for i := 0; i < 5; i++ {
		m.Notify(Message{Event: "x", Title: "X"}) // 没有 DedupKey
	}
	drain(t, m)

	if got := ch.count(); got != 5 {
		t.Errorf("没有去重键的消息应当全部发出，实际 %d 条", got)
	}
}

func TestQuietPeriodZeroDisablesDedup(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(0)

	for i := 0; i < 5; i++ {
		m.Notify(Message{Event: "x", Title: "X", DedupKey: "same"})
	}
	drain(t, m)

	if got := ch.count(); got != 5 {
		t.Errorf("关闭去重后应当全部发出，实际 %d 条", got)
	}
}

func TestQuietPeriodExpiryAllowsResend(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	// 极短的静默期，便于测试。
	m.SetQuietPeriod(60 * time.Millisecond)

	m.Notify(Message{Event: "x", Title: "第一次", DedupKey: "k"})
	drain(t, m)

	time.Sleep(80 * time.Millisecond)

	m.Notify(Message{Event: "x", Title: "第二次", DedupKey: "k"})
	drain(t, m)

	if got := ch.count(); got != 2 {
		t.Errorf("静默期过后应当能再次发出，实际 %d 条", got)
	}
}

// TestSuppressedSummaryIsFlushed 钉住"只计数不补发是不够的"。
//
// 用户会以为那段时间什么都没发生，而实际上是他最关心的那类事件
// 在反复出现。
func TestSuppressedSummaryIsFlushed(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(80 * time.Millisecond)

	m.Notify(Message{
		Event: "dns.update_failed", Title: "解析失败",
		Severity: SeverityError, DedupKey: "dns.update_failed:task-1",
	})
	drain(t, m)

	// 静默期内再发 4 条，全部被抑制。
	for i := 0; i < 4; i++ {
		m.Notify(Message{
			Event: "dns.update_failed", Title: "解析失败",
			Severity: SeverityError, DedupKey: "dns.update_failed:task-1",
		})
	}
	drain(t, m)

	if got := ch.count(); got != 1 {
		t.Fatalf("静默期内应当只发出 1 条，实际 %d", got)
	}

	// 等静默期过去，然后触发补发检查。
	time.Sleep(120 * time.Millisecond)
	m.flushSuppressed(context.Background())

	msgs := ch.all()
	if len(msgs) != 2 {
		t.Fatalf("应当补发一条汇总，实际共 %d 条", len(msgs))
	}

	summary := msgs[1]
	if summary.Event != "notify.suppressed_summary" {
		t.Errorf("补发的消息类型 = %s", summary.Event)
	}
	if !strings.Contains(summary.Title, "4") {
		t.Errorf("汇总里应当带上被抑制的条数，得到 %q", summary.Title)
	}
}

// TestNoSummaryWhenNothingSuppressed 验证没有抑制时不补发。
//
// 补发一条"另有 0 次同类事件"是无意义的噪音。
func TestNoSummaryWhenNothingSuppressed(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(50 * time.Millisecond)

	m.Notify(Message{Event: "x", Title: "一次", DedupKey: "k"})
	drain(t, m)

	time.Sleep(80 * time.Millisecond)
	m.flushSuppressed(context.Background())

	if got := ch.count(); got != 1 {
		t.Errorf("没有抑制时不该补发，实际 %d 条", got)
	}
}

// ---------------------------------------------------------------------------
// 级别过滤
// ---------------------------------------------------------------------------

func TestMinSeverityFilters(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetMinSeverity(SeverityWarning)

	m.Notify(Message{Event: "i", Title: "信息", Severity: SeverityInfo})
	m.Notify(Message{Event: "w", Title: "警告", Severity: SeverityWarning})
	m.Notify(Message{Event: "e", Title: "错误", Severity: SeverityError})
	drain(t, m)

	got := ch.all()
	if len(got) != 2 {
		t.Fatalf("应当只发出警告与错误，实际 %d 条", len(got))
	}
	for _, msg := range got {
		if msg.Severity == SeverityInfo {
			t.Error("info 级别不该被发出")
		}
	}
}

func TestInvalidMessageIsDropped(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)

	m.Notify(Message{Event: "x", Title: ""}) // 没有标题
	m.Notify(Message{Event: "x", Title: "坏级别", Severity: "catastrophic"})
	drain(t, m)

	if got := ch.count(); got != 0 {
		t.Errorf("不合法的消息应当被丢弃，实际发出 %d 条", got)
	}
}

// ---------------------------------------------------------------------------
// 投递
// ---------------------------------------------------------------------------

// TestChannelFailureDoesNotBlockOthers 验证一个通道失败不影响其它通道。
//
// 一个 Webhook 配错了不该让邮件也收不到。
func TestChannelFailureDoesNotBlockOthers(t *testing.T) {
	t.Parallel()

	bad := &fakeChannel{err: errors.New("连不上")}
	good := &fakeChannel{}

	m := NewManager(nil)
	m.AddChannel(bad)
	m.AddChannel(good)

	m.Notify(Message{Event: "x", Title: "测试"})
	drain(t, m)

	if good.count() != 1 {
		t.Error("一个通道失败不该阻止其它通道")
	}
}

// TestDeliveryResultsAreRecorded 验证投递结果被保留。
//
// "我的通知到底发出去了没有"是用户配置通道时问得最多的一个问题，
// 而只写日志的话他得去翻日志文件。
func TestDeliveryResultsAreRecorded(t *testing.T) {
	t.Parallel()

	bad := &fakeChannel{err: errors.New("目标返回 HTTP 500")}
	good := &fakeChannel{}

	m := NewManager(nil)
	m.AddChannel(bad)
	m.AddChannel(good)

	m.Notify(Message{Event: "x", Title: "测试"})
	drain(t, m)

	records := m.Deliveries()
	if len(records) != 2 {
		t.Fatalf("应当记录 2 条投递结果，实际 %d", len(records))
	}

	var sawFailure, sawSuccess bool
	for _, d := range records {
		if d.OK {
			sawSuccess = true
		} else {
			sawFailure = true
			if !strings.Contains(d.Error, "500") {
				t.Errorf("失败原因应当被记录下来，得到 %q", d.Error)
			}
		}
	}
	if !sawFailure || !sawSuccess {
		t.Errorf("应当同时记录成功与失败，实际 %+v", records)
	}
}

// TestNotifyIsNonBlocking 验证提交通知不会阻塞调用方。
//
// 通知是旁路功能，它绝不能拖慢动态解析或证书续期这些主线任务。
func TestNotifyIsNonBlocking(t *testing.T) {
	t.Parallel()

	// 一个很慢的通道。
	slow := &fakeChannel{delay: 200 * time.Millisecond}
	m := newTestManager(t, slow)

	// 填满队列（远超容量），且不启动投递循环。
	start := time.Now()
	for i := 0; i < defaultQueueSize*3; i++ {
		m.Notify(Message{Event: "x", Title: fmt.Sprintf("消息 %d", i)})
	}
	elapsed := time.Since(start)

	// 全部调用应当立刻返回 —— 慢通道在另一个 goroutine 里跑。
	if elapsed > 100*time.Millisecond {
		t.Errorf("提交 %d 条消息花了 %v，通知不该阻塞调用方",
			defaultQueueSize*3, elapsed)
	}
}

// TestQueueOverflowDropsOldest 验证队列满时丢最旧的。
//
// 积压时用户更需要知道刚刚发生了什么，而不是五分钟前那条。
func TestQueueOverflowDropsOldest(t *testing.T) {
	t.Parallel()

	m := NewManager(nil) // 不启动投递循环，队列会一直积压

	// 塞满并超额。
	for i := 0; i < defaultQueueSize+10; i++ {
		m.Notify(Message{Event: "x", Title: fmt.Sprintf("消息 %d", i)})
	}

	if len(m.queue) != defaultQueueSize {
		t.Fatalf("队列长度 = %d，期望 %d", len(m.queue), defaultQueueSize)
	}

	// 队首应当不是最早那条 —— 它已经被丢掉了。
	first := <-m.queue
	if strings.Contains(first.Title, "消息 0") {
		t.Error("队列满时应当丢弃最旧的消息")
	}
}

// TestSendNowBypassesDedup 验证"发送测试通知"绕过去重与队列。
//
// 用户点了按钮之后期待**立刻**看到结果，而不是等下一个投递循环。
func TestSendNowBypassesDedup(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)
	m.SetQuietPeriod(time.Hour)

	// 先占住去重键。
	m.Notify(Message{Event: "x", Title: "第一次", DedupKey: "k"})
	m.SendNow(context.Background(), Message{Event: "x", Title: "测试", DedupKey: "k"})
	m.SendNow(context.Background(), Message{Event: "x", Title: "测试2", DedupKey: "k"})

	// SendNow 是同步的，因此不需要 drain。
	if got := ch.count(); got != 2 {
		t.Errorf("SendNow 应当绕过去重，实际发出 %d 条", got)
	}
}

func TestSendNowRejectsInvalidMessage(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := newTestManager(t, ch)

	results := m.SendNow(context.Background(), Message{Event: "x", Title: ""})
	if len(results) != 1 || results[0].OK {
		t.Error("不合法的消息应当返回失败结果")
	}
	if ch.count() != 0 {
		t.Error("不合法的消息不该被真正发出")
	}
}

// ---------------------------------------------------------------------------
// Webhook 通道
// ---------------------------------------------------------------------------

func TestWebhookSendsJSON(t *testing.T) {
	t.Parallel()

	var (
		mu      sync.Mutex
		gotBody []byte
		gotCT   string
		gotUA   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotCT, gotUA = body, r.Header.Get("Content-Type"), r.Header.Get("User-Agent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := NewWebhookChannel(WebhookOptions{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	err = ch.Send(context.Background(), Message{
		Event: "dns.update_failed", Title: "解析失败",
		Body: "服务商返回 500", Severity: SeverityError,
		At: time.Now(),
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var decoded Message
	if err := json.Unmarshal(gotBody, &decoded); err != nil {
		t.Fatalf("请求体不是合法 JSON: %v\n%s", err, gotBody)
	}
	if decoded.Title != "解析失败" {
		t.Errorf("标题 = %q", decoded.Title)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Errorf("Content-Type = %q", gotCT)
	}
	// User-Agent 里带项目名：目标服务出错时用户能看出是谁发的。
	if !strings.Contains(gotUA, "ISC") {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

// TestWebhookTemplate 验证自定义模板。
//
// 各家的消息格式差异很大（飞书要 {"msg_type":"text","content":{...}}，
// Slack 要 {"text":"..."}），模板让用户不必等内核适配。
func TestWebhookTemplate(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		body string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		byt, _ := io.ReadAll(r.Body)
		mu.Lock()
		body = string(byt)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := NewWebhookChannel(WebhookOptions{
		URL: srv.URL,
		// 飞书那种嵌套结构。
		BodyTemplate: `{"msg_type":"text","content":{"text":"[{{.Severity}}] {{.Title}}\n{{.Body}}"}}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = ch.Send(context.Background(), Message{
		Event: "x", Title: "地址已更新", Body: "203.0.113.7",
		Severity: SeverityInfo,
	})
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if !strings.Contains(body, "[info] 地址已更新") {
		t.Errorf("模板没有正确渲染:\n%s", body)
	}
	if !strings.Contains(body, "203.0.113.7") {
		t.Errorf("Body 没有被渲染:\n%s", body)
	}
	// 必须是合法 JSON（模板本身就是 JSON）。
	if !json.Valid([]byte(body)) {
		t.Errorf("渲染结果不是合法 JSON:\n%s", body)
	}
}

// TestWebhookBadTemplateFailsAtConstruction 验证模板错误在**保存配置时**暴露。
//
// 放到发送时才发现的话，用户会看到"通知发不出去"，而真正的问题是
// 模板里少了一个括号 —— 那是两件很难联系起来的事。
func TestWebhookBadTemplateFailsAtConstruction(t *testing.T) {
	t.Parallel()

	_, err := NewWebhookChannel(WebhookOptions{
		URL:          "http://127.0.0.1:1/hook",
		BodyTemplate: `{"text":"{{.Title"`, // 少了一个括号
	})
	if err == nil {
		t.Fatal("模板语法错误应当在构造时就报错")
	}
	if !strings.Contains(err.Error(), "模板") {
		t.Errorf("错误信息应当指出是模板问题: %v", err)
	}
}

func TestWebhookRejectsBadURL(t *testing.T) {
	t.Parallel()

	bad := []string{
		"",
		"   ",
		"ftp://example.com/hook",
		// `file://` 之类的 scheme 会让这个功能变成一个
		// "能读写本机任意文件"的入口。
		"file:///etc/passwd",
		"example.com/hook",
	}
	for _, u := range bad {
		if _, err := NewWebhookChannel(WebhookOptions{URL: u}); err == nil {
			t.Errorf("%q 应当被拒绝", u)
		}
	}
}

// TestWebhookReportsUpstreamError 验证目标返回错误时的提示。
func TestWebhookReportsUpstreamError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid token"}`)
	}))
	t.Cleanup(srv.Close)

	ch, err := NewWebhookChannel(WebhookOptions{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	err = ch.Send(context.Background(), Message{Event: "x", Title: "测试"})
	if err == nil {
		t.Fatal("目标返回 401 时应当报错")
	}

	msg := err.Error()
	if !strings.Contains(msg, "401") {
		t.Errorf("错误信息应当带上状态码: %s", msg)
	}
	// 目标返回的说明对排查很关键（"invalid token" 直接指向鉴权配置）。
	if !strings.Contains(msg, "invalid token") {
		t.Errorf("错误信息应当带上目标的说明: %s", msg)
	}
}

// TestWebhookTruncatesResponseBody 验证响应体被截断。
//
// 某些服务在出错时返回一整页 HTML，把它塞进日志与通知记录里
// 既没用又占地方。
func TestWebhookTruncatesResponseBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, strings.Repeat("<html>很长的一页</html>", 500))
	}))
	t.Cleanup(srv.Close)

	ch, _ := NewWebhookChannel(WebhookOptions{URL: srv.URL})
	err := ch.Send(context.Background(), Message{Event: "x", Title: "测试"})
	if err == nil {
		t.Fatal("应当报错")
	}
	if len(err.Error()) > 800 {
		t.Errorf("错误信息过长（%d 字符），响应体没有被截断", len(err.Error()))
	}
}

func TestWebhookSendsCustomHeaders(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		auth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := NewWebhookChannel(WebhookOptions{
		URL:     srv.URL,
		Headers: map[string]string{"Authorization": "Bearer secret-token"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := ch.Send(context.Background(), Message{Event: "x", Title: "测试"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer secret-token" {
		t.Errorf("自定义头没有发出: %q", auth)
	}
}

func TestWebhookMethodOverride(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		method string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method = r.Method
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, _ := NewWebhookChannel(WebhookOptions{URL: srv.URL, Method: "put"})
	if err := ch.Send(context.Background(), Message{Event: "x", Title: "测试"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if method != http.MethodPut {
		t.Errorf("方法 = %s，期望 PUT", method)
	}
}

func TestWebhookContextCancellation(t *testing.T) {
	t.Parallel()

	// 一个不响应的服务。
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	ch, _ := NewWebhookChannel(WebhookOptions{URL: srv.URL})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := ch.Send(ctx, Message{Event: "x", Title: "测试"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("上下文超时时应当报错")
	}
	if elapsed > 2*time.Second {
		t.Errorf("等了 %v 才返回 —— 没有响应上下文取消", elapsed)
	}
}

// ---------------------------------------------------------------------------
// 日志通道
// ---------------------------------------------------------------------------

// TestLogChannelNeverFails 验证日志通道永不失败。
//
// 让一个不可能失败的通道报错只会污染投递记录，而用户看到"日志通道
// 投递失败"会去查一个根本不存在的问题。
func TestLogChannelNeverFails(t *testing.T) {
	t.Parallel()

	var (
		mu  sync.Mutex
		got []Message
	)
	ch := NewLogChannel(func(msg Message) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, msg)
	})

	if err := ch.Send(context.Background(), Message{Event: "x", Title: "测试"}); err != nil {
		t.Errorf("日志通道不该失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Errorf("消息没有被交给日志: %d 条", len(got))
	}
}

func TestLogChannelNilSink(t *testing.T) {
	t.Parallel()

	// 传 nil 不该 panic —— 那会让"总有一个可用通道"这个保证失效。
	ch := NewLogChannel(nil)
	if err := ch.Send(context.Background(), Message{Event: "x", Title: "测试"}); err != nil {
		t.Errorf("不该失败: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 模板数据
// ---------------------------------------------------------------------------

// TestTemplateDataDoesNotAllowOverridingBuiltins 验证内置变量不被覆盖。
//
// 一个叫 "Title" 的 data 键会让模板莫名其妙地输出错的东西。
func TestTemplateDataDoesNotAllowOverridingBuiltins(t *testing.T) {
	t.Parallel()

	msg := Message{
		Event: "x", Title: "真标题", Body: "正文",
		Severity: SeverityError,
		At:       time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC),
		Data:     map[string]any{"Title": "假标题", "custom": "自定义值"},
	}

	data := templateData(msg)

	if data["Title"] != "真标题" {
		t.Errorf("Title 被 data 覆盖了: %v", data["Title"])
	}
	if data["custom"] != "自定义值" {
		t.Error("自定义字段应当可用")
	}
	// 时间用 RFC3339：几乎所有服务都能直接解析，而 Go 的默认格式不能。
	if data["At"] != "2026-03-15T12:00:00Z" {
		t.Errorf("At = %v，期望 RFC3339", data["At"])
	}
}

// TestWebhookTemplatePlainText 验证非 JSON 的模板产出。
//
// 有些服务（以及用户自己的脚本）收的就是纯文本。
func TestWebhookTemplatePlainText(t *testing.T) {
	t.Parallel()

	var (
		mu   sync.Mutex
		body string
		ct   string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		byt, _ := io.ReadAll(r.Body)
		mu.Lock()
		body, ct = string(byt), r.Header.Get("Content-Type")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ch, err := NewWebhookChannel(WebhookOptions{
		URL:          srv.URL,
		BodyTemplate: `{{.Title}}: {{.Body}}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), Message{
		Event: "x", Title: "标题", Body: "正文",
	}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if body != "标题: 正文" {
		t.Errorf("请求体 = %q", body)
	}
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("纯文本模板的 Content-Type = %q", ct)
	}
}

// TestSendNowBypassesSeverityFilter 来自一次真机发现。
//
// 一个配了"仅在 warning 及以上发送"的通道，在测试时会因为测试消息是
// info 而被过滤掉 —— 而过滤器把"被过滤"报成成功，于是用户看到 ✅，
// 实际什么都没发出去。那个 Webhook 指向的端口当时**根本没有服务在监听**。
//
// 用户点"测试"时的意图是"现在真的发一条"，因此级别过滤在这里不该生效。
func TestSendNowBypassesSeverityFilter(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := NewManager(nil)

	// 一个只会收到 error 的通道。
	m.SetDynamicChannels([]Channel{
		levelFilter{inner: ch, min: SeverityError},
	})

	// 测试消息是 info —— 正常情况下会被过滤掉。
	results := m.SendNow(context.Background(), Message{
		Event: "notify.test", Title: "测试", Severity: SeverityInfo,
	})

	if len(results) != 1 {
		t.Fatalf("应当有 1 条投递结果，得到 %d", len(results))
	}
	if !results[0].OK {
		t.Errorf("测试通知应当成功发出，得到失败: %s", results[0].Error)
	}
	if ch.count() != 1 {
		t.Error("测试通知必须真的打到通道上 —— " +
			"被级别过滤挡掉却报成功，会让用户以为通道配好了")
	}
}

// TestNormalNotificationStillHonorsSeverityFilter 验证常规通知仍受过滤。
//
// 测试绕过过滤是特例；事件通知必须遵守用户设的阈值。
func TestNormalNotificationStillHonorsSeverityFilter(t *testing.T) {
	t.Parallel()

	ch := &fakeChannel{}
	m := NewManager(nil)
	m.SetDynamicChannels([]Channel{
		levelFilter{inner: ch, min: SeverityError},
	})

	m.Notify(Message{Event: "x", Title: "信息", Severity: SeverityInfo})
	drain(t, m)

	if ch.count() != 0 {
		t.Error("info 级别的常规通知应当被 error 阈值挡掉")
	}

	m.Notify(Message{Event: "x", Title: "错误", Severity: SeverityError})
	drain(t, m)

	if ch.count() != 1 {
		t.Error("error 级别的通知应当通过")
	}
}
