package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// WebhookChannel 把通知 POST 到一个 HTTP 地址。
//
// # 为什么它是最重要的一种通道
//
// 它是**通用**的：用户想接飞书、钉钉、企业微信、Slack、自建机器人，
// 都可以通过它做到，而不需要内核为每一家单独写一个通道 ——
// 那些服务的 API 会变，而内核发版跟不上。
//
// 它也覆盖了"我只想让自己写的脚本收到通知"这类需求。
type WebhookChannel struct {
	name    string
	url     string
	method  string
	headers map[string]string
	client  *http.Client

	// bodyTemplate 是请求体模板。
	//
	// 留空时发送一个默认的 JSON 结构。有模板时可以用
	// {{.Title}} / {{.Body}} / {{.Event}} / {{.Severity}} 等变量 ——
	// 各家的消息格式差异很大（飞书要 {"msg_type":"text","content":{...}}，
	// Slack 要 {"text":"..."}），模板让用户不必等内核适配。
	bodyTemplate *template.Template
	// rawTemplate 是模板原文，用于诊断时展示。
	rawTemplate string
}

// WebhookOptions 是 Webhook 通道的配置。
type WebhookOptions struct {
	// Name 是通道名称。
	Name string
	// URL 是目标地址。
	URL string
	// Method 是 HTTP 方法，默认 POST。
	Method string
	// Headers 是附加请求头，常用于鉴权。
	Headers map[string]string
	// BodyTemplate 是请求体模板，留空用默认 JSON。
	BodyTemplate string
	// Timeout 是单次请求超时，默认 10 秒。
	Timeout time.Duration
}

// NewWebhookChannel 构造 Webhook 通道。
func NewWebhookChannel(opts WebhookOptions) (*WebhookChannel, error) {
	if strings.TrimSpace(opts.URL) == "" {
		return nil, fmt.Errorf("notify: Webhook 通道需要目标地址")
	}

	// 只允许 http/https：`file://` 之类的 scheme 会让这个功能变成
	// 一个"能读写本机任意文件"的入口。
	lower := strings.ToLower(opts.URL)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return nil, fmt.Errorf(
			"notify: Webhook 地址必须以 http:// 或 https:// 开头，得到 %q", opts.URL)
	}

	name := opts.Name
	if name == "" {
		name = "webhook"
	}
	method := strings.ToUpper(opts.Method)
	if method == "" {
		method = http.MethodPost
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	ch := &WebhookChannel{
		name:    name,
		url:     opts.URL,
		method:  method,
		headers: opts.Headers,
		client:  &http.Client{Timeout: timeout},
	}

	if opts.BodyTemplate != "" {
		tmpl, err := template.New("body").Option("missingkey=zero").
			Parse(opts.BodyTemplate)
		if err != nil {
			// 模板语法错误必须在**保存配置时**就暴露。
			//
			// 放到发送时才发现的话，用户会看到"通知发不出去"，
			// 而真正的问题是模板里少了一个括号 —— 那是两件很难
			// 联系起来的事。
			return nil, fmt.Errorf("notify: Webhook 请求体模板语法错误: %w", err)
		}
		ch.bodyTemplate = tmpl
		ch.rawTemplate = opts.BodyTemplate
	}

	return ch, nil
}

// Name 实现 Channel。
func (w *WebhookChannel) Name() string { return w.name }

// Kind 实现 Channel。
func (w *WebhookChannel) Kind() string { return "webhook" }

// Send 实现 Channel。
func (w *WebhookChannel) Send(ctx context.Context, msg Message) error {
	body, contentType, err := w.renderBody(msg)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, w.method, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "ISC-Core")
	for k, v := range w.headers {
		req.Header.Set(k, v)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("notify: 请求失败: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 只读响应，关闭失败无影响

	// 读一点点响应体用于错误信息。
	//
	// **限制长度**：某些服务在出错时返回一整页 HTML，
	// 把它塞进日志与通知记录里既没用又占地方。
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notify: 目标返回 HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	return nil
}

// renderBody 生成请求体。
func (w *WebhookChannel) renderBody(msg Message) (body []byte, contentType string, err error) {
	if w.bodyTemplate == nil {
		// 默认结构：直接是消息本身。
		//
		// 保持简单而不是学某一家服务的格式：默认值只是"能用"，
		// 真正对接具体服务时用户会写模板。
		byt, err := json.Marshal(msg)
		if err != nil {
			return nil, "", fmt.Errorf("notify: 序列化消息失败: %w", err)
		}
		return byt, "application/json; charset=utf-8", nil
	}

	var buf bytes.Buffer
	if err := w.bodyTemplate.Execute(&buf, templateData(msg)); err != nil {
		return nil, "", fmt.Errorf("notify: 渲染请求体失败: %w", err)
	}

	ct := "application/json; charset=utf-8"
	trimmed := strings.TrimSpace(buf.String())
	// 模板产出不是 JSON 时改用 text/plain ——
	// 有些服务（以及用户自己的脚本）收的就是纯文本。
	if !json.Valid([]byte(trimmed)) {
		ct = "text/plain; charset=utf-8"
	}
	return []byte(trimmed), ct, nil
}

// templateData 是模板可用的变量。
//
// 单独一个函数而不是直接把 Message 交给模板：这样模板里能用的东西
// 是**明确列出**的（含格式化好的时间），而不是 Message 的全部字段
// 随实现变化而变。
func templateData(msg Message) map[string]any {
	data := map[string]any{
		"Event":    msg.Event,
		"Title":    msg.Title,
		"Body":     msg.Body,
		"Severity": string(msg.Severity),
		// 用 RFC3339 而不是 Go 的默认时间格式：前者几乎所有服务
		// 都能直接解析，而后者不能。
		"At": msg.At.Format(time.RFC3339),
	}
	for k, v := range msg.Data {
		// 消息自带的字段不覆盖内置变量 —— 否则一个叫 "Title" 的
		// data 键会让模板莫名其妙地输出错的东西。
		if _, exists := data[k]; !exists {
			data[k] = v
		}
	}
	return data
}

// ---------------------------------------------------------------------------
// 其它通道
// ---------------------------------------------------------------------------

// LogChannel 把通知写进日志。
//
// 它存在的意义是"总有一个可用的通道"：用户还没配任何外部通道时，
// 通知至少会出现在日志与事件流里，而不是无声无息地消失。
type LogChannel struct {
	sink func(msg Message)
}

// NewLogChannel 构造日志通道。
func NewLogChannel(sink func(Message)) *LogChannel {
	if sink == nil {
		sink = func(Message) {}
	}
	return &LogChannel{sink: sink}
}

// Name 实现 Channel。
func (l *LogChannel) Name() string { return "日志" }

// Kind 实现 Channel。
func (l *LogChannel) Kind() string { return "log" }

// Send 实现 Channel。
//
// 它**永不失败**：日志通道没有"投递失败"这回事，而让一个不可能
// 失败的通道报错只会污染投递记录。
func (l *LogChannel) Send(_ context.Context, msg Message) error {
	l.sink(msg)
	return nil
}
