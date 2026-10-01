// Package logx 提供内核的结构化日志。
//
// 它做两件事：
//
//   - 基于标准库 log/slog，输出结构化日志到 stderr（服务场景下由
//     systemd / Windows 事件日志 / launchd 收集）；
//   - 把每条日志同时以 event.TypeLogAppended 事件发布到事件总线，
//     使验证控制台与下游 GUI 能实时看到日志，而不需要轮询
//     `GET /v1/logs`。
//
// 刻意不引入第三方日志库：slog 已覆盖需求，而本项目对依赖数量有硬约束
// （见 docs/PLAN.md §0）。
package logx

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// Publisher 是日志事件的最小发布接口。
//
// 之所以不直接依赖 *event.Bus：日志包被所有包引用，若它依赖事件总线，
// 会形成"日志 → 总线 → 日志"的隐式耦合。这里只保留一个窄接口。
type Publisher interface {
	Publish(typ string, payload any) event.Event
}

// logPayload 是 log.appended 事件的载荷。
type logPayload struct {
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Attrs   map[string]any `json:"attrs,omitempty"`
}

// BusHandler 是把日志桥接到事件总线的 slog.Handler。
//
// 它包装一个基础 handler（通常是 stderr 上的 TextHandler / JSONHandler），
// 先交给基础 handler 输出，再发布事件。
type BusHandler struct {
	base  slog.Handler
	pub   atomic.Pointer[Publisher]
	level slog.Level
	attrs []slog.Attr
	group string
}

// New 构造一个输出到 w 的日志处理器。
//
// pub 可以为 nil（此时只输出，不发布事件）；之后可用 SetPublisher 补上 ——
// 这是必要的，因为事件总线要等平台与配置就绪后才能创建，
// 而日志从进程第一行代码开始就要能用。
func New(w io.Writer, level slog.Level, pub Publisher) *BusHandler {
	h := &BusHandler{
		base:  slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}),
		level: level,
	}
	if pub != nil {
		h.pub.Store(&pub)
	}
	return h
}

// SetPublisher 设置（或替换）事件发布者。
func (h *BusHandler) SetPublisher(p Publisher) {
	if p == nil {
		return
	}
	h.pub.Store(&p)
}

// Enabled 实现 slog.Handler。
func (h *BusHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// Handle 实现 slog.Handler。
func (h *BusHandler) Handle(ctx context.Context, r slog.Record) error {
	// 先做基础输出：即使事件发布路径出问题，日志本身也不能丢。
	if err := h.base.Handle(ctx, r); err != nil {
		return err
	}

	pub := h.pub.Load()
	if pub == nil {
		return nil
	}

	attrs := make(map[string]any, r.NumAttrs()+len(h.attrs))
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	if len(attrs) == 0 {
		attrs = nil
	}

	// 尽力而为：事件总线已关闭时 Publish 返回零值，不视为错误。
	// 日志绝不该因为"订阅者没了"而失败。
	(*pub).Publish(event.TypeLogAppended, logPayload{
		Level:   r.Level.String(),
		Message: r.Message,
		Attrs:   attrs,
	})
	return nil
}

// WithAttrs 实现 slog.Handler。
func (h *BusHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)

	next := &BusHandler{
		base:  h.base.WithAttrs(attrs),
		level: h.level,
		attrs: merged,
	}
	if p := h.pub.Load(); p != nil {
		next.pub.Store(p)
	}
	return next
}

// WithGroup 实现 slog.Handler。
func (h *BusHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := &BusHandler{
		base:  h.base.WithGroup(name),
		level: h.level,
		attrs: h.attrs,
		group: name,
	}
	if p := h.pub.Load(); p != nil {
		next.pub.Store(p)
	}
	return next
}
