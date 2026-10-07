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

// 级别的名字与 settings 表里的取值一一对应。
//
// 刻意在本包内重新声明一遍字面量，而不是 import internal/settings：
// logx 被所有包引用（包括 settings 的下游），让最底层的日志包反过来
// 依赖领域设置包会把依赖方向倒过来。两边的一致性由
// internal/logx 的单测钉住 —— 那里的用例直接引用 settings.LevelDebug
// 等常量，任何一边改名都会让测试失败，而不是让用户拿到一个
// "设置改了但级别没变"的静默失效。
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// ParseLevel 把设置里的级别名映射为 slog 级别。
//
// 无法识别的值（拼错的、旧版本留下的、被手工改坏的）退回 [slog.LevelInfo]
// 并**不报错也不 panic**：
//
//   - panic 会让内核起不来，而用户此刻连改回设置的界面都没有；
//   - 退回 debug 则是更坏的选择 —— 它会让一个"写错的级别"变成磁盘与
//     事件总线上的一场洪水，用户完全无从预料。
//
// info 是既不吵也不哑的那一个，也与 settings.Default() 一致。
func ParseLevel(name string) slog.Level {
	switch name {
	case LevelDebug:
		return slog.LevelDebug
	case LevelInfo:
		return slog.LevelInfo
	case LevelWarn:
		return slog.LevelWarn
	case LevelError:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
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
	attrs []slog.Attr
	group string
}

// New 构造一个输出到 w 的日志处理器。
//
// pub 可以为 nil（此时只输出，不发布事件）；之后可用 SetPublisher 补上 ——
// 这是必要的，因为事件总线要等平台与配置就绪后才能创建，
// 而日志从进程第一行代码开始就要能用。
//
// # 级别为什么是 *slog.LevelVar 而不是 slog.Level
//
// 级别必须能在**运行期**改（用户在界面上把 log_level 从 info 改成 debug
// 应当立刻生效，而不是等下次重启）。slog 的级别在构造 handler 时就固定
// 写进了 HandlerOptions，只有 *slog.LevelVar 是例外：它是一块被 handler
// 与调用方**共享**的可变状态，Set 之后立刻对所有 Enabled 判定生效。
//
// 别为了"重建 handler"绕过这一点：slog.Logger 是在启动时交给几十个领域
// 服务的，重建只覆盖新拿到的那个引用，早已持有旧 logger 的组件会继续
// 按旧级别过滤 —— 症状是"有些模块的 debug 日志出得来，有些出不来"。
func New(w io.Writer, level *slog.LevelVar, pub Publisher) *BusHandler {
	h := &BusHandler{
		base: slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}),
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
//
// **必须委托给基础 handler**，不能在本类型上再存一份级别副本：
// 存副本就意味着"级别"有两个真相来源，而运行期改了 LevelVar 之后
// 这边仍然按构造时的旧级别短路掉日志 —— 那正是"设置里写着 debug，
// 日志里却没有 debug"这类问题的成因，且它完全静默。
func (h *BusHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
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
		attrs: h.attrs,
		group: name,
	}
	if p := h.pub.Load(); p != nil {
		next.pub.Store(p)
	}
	return next
}
