package logx_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/logx"
	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// TestParseLevelCoversEveryStoredValue 钉住"设置里的四个取值都能正确映射"。
//
// 期望值刻意写成 settings 包的常量而不是 logx 自己的：两处字面量必须
// 一致，任何一边改名（例如把 "warn" 改成 "warning"）都会让这条用例
// 失败，而不是产生"用户改了级别但内核静默按 info 跑"这种谁都不会
// 收到报错的结果。
func TestParseLevelCoversEveryStoredValue(t *testing.T) {
	cases := []struct {
		name string
		want slog.Level
	}{
		{settings.LevelDebug, slog.LevelDebug},
		{settings.LevelInfo, slog.LevelInfo},
		{settings.LevelWarn, slog.LevelWarn},
		{settings.LevelError, slog.LevelError},
	}
	for _, c := range cases {
		if got := logx.ParseLevel(c.name); got != c.want {
			t.Errorf("ParseLevel(%q) = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

// TestParseLevelFallsBackToInfo 钉住非法取值的行为。
//
// 三种坏值都要单独覆盖：
//   - 完全不认识的值；
//   - 空串（数据库里那一行存在但为空）；
//   - 大小写不同（"DEBUG"）——它虽然"看起来对"，但不是契约里的取值。
//
// 大小写这一条特别值得一提：JSON 与界面都可能产出大写，而**静默接受**
// 它意味着设置表里会出现两种拼法，之后任何按字符串比较的地方都会漏。
// 统一退回 info，行为可预期。
func TestParseLevelFallsBackToInfo(t *testing.T) {
	for _, bad := range []string{"", "trace", "verbose", "DEBUG", "Warning", "信息", "0"} {
		if got := logx.ParseLevel(bad); got != slog.LevelInfo {
			t.Errorf("ParseLevel(%q) = %v, 期望退回 %v", bad, got, slog.LevelInfo)
		}
	}
}

// newCapture 构造一个把日志收进内存的处理器，返回它与缓冲区。
func newCapture(level *slog.LevelVar) (*logx.BusHandler, *bytes.Buffer) {
	buf := new(bytes.Buffer)
	return logx.New(buf, level, nil), buf
}

// TestHandlerFollowsLevelVarAtRuntime 是这次修改的核心用例：
// 级别在运行期改动之后，被过滤掉的日志必须**立刻**不再出现，
// 而更高级别的日志必须开始出现。
//
// 用内存缓冲区（slog.NewTextHandler 那一路）而不是文件：判据是
// "处理器到底有没有放行这条记录"，落盘会引入一个与本问题无关的
// 依赖（磁盘、权限、清理）。
func TestHandlerFollowsLevelVarAtRuntime(t *testing.T) {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	h, buf := newCapture(level)
	log := slog.New(h)

	// 起点是 info：debug 被挡下，info 放行。
	log.Debug("调试一")
	log.Info("信息一")
	if got := buf.String(); !strings.Contains(got, "信息一") || strings.Contains(got, "调试一") {
		t.Fatalf("info 级别下输出不对:\n%s", got)
	}

	// 调到 debug：此后 debug 也要出现。
	level.Set(slog.LevelDebug)
	log.Debug("调试二")
	if got := buf.String(); !strings.Contains(got, "调试二") {
		t.Errorf("级别改成 debug 后仍看不到调试日志 —— LevelVar 没被处理器共享:\n%s", got)
	}

	// 调到 error：info 与 warn 都该被挡下，error 仍然放行。
	level.Set(slog.LevelError)
	log.Info("信息三")
	log.Warn("警告三")
	log.Error("错误三")
	got := buf.String()
	if strings.Contains(got, "信息三") {
		t.Errorf("级别改成 error 后仍出现 info 日志:\n%s", got)
	}
	if strings.Contains(got, "警告三") {
		t.Errorf("级别改成 error 后仍出现 warn 日志:\n%s", got)
	}
	if !strings.Contains(got, "错误三") {
		t.Errorf("级别改成 error 后 error 日志被误挡:\n%s", got)
	}
}

// TestEnabledDelegatesToSharedLevelVar 直接钉住 BusHandler.Enabled。
//
// 这是最容易漏的一处：BusHandler 曾经自己存一份 level 副本，于是
// 即便把 stderr 那个 TextHandler 的 level 换成 LevelVar，外围这一层
// 仍会按构造时的旧级别短路掉日志 —— 而它是**先**被调用的那一层，
// 表现为"设置改了、什么都没发生"。这条用例专门盯着它。
func TestEnabledDelegatesToSharedLevelVar(t *testing.T) {
	level := new(slog.LevelVar)
	level.Set(slog.LevelError)
	h, _ := newCapture(level)

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("error 级别下 debug 不该被放行")
	}
	if !h.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("error 级别下 error 本身必须被放行")
	}

	level.Set(slog.LevelDebug)
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("级别改成 debug 后 debug 必须被放行（Enabled 没有读取共享的 LevelVar）")
	}
}

// TestWithAttrsKeepsFollowingLevelVar 覆盖由 WithAttrs 派生出来的处理器。
//
// 派生出来的那一份**共享同一个 LevelVar**：内核里绝大多数日志器都是
// 这样来的（各领域服务各自 With(...)），若派生时把级别快照下来，
// 运行期改级别就只对根处理器生效 —— 症状是"有些模块的调试日志出得来，
// 有些出不来"，比完全不生效更难查。
func TestWithAttrsKeepsFollowingLevelVar(t *testing.T) {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	h, buf := newCapture(level)

	derived := slog.New(h.WithAttrs([]slog.Attr{slog.String("mod", "ddns")}))
	derived.Debug("派生调试一")
	if strings.Contains(buf.String(), "派生调试一") {
		t.Fatal("info 级别下派生处理器不该输出 debug")
	}

	level.Set(slog.LevelDebug)
	derived.Debug("派生调试二")
	got := buf.String()
	if !strings.Contains(got, "派生调试二") {
		t.Errorf("派生处理器没有跟随 LevelVar:\n%s", got)
	}
	// 附加上下文不能因为派生而丢掉。
	if !strings.Contains(got, "mod=ddns") {
		t.Errorf("派生处理器丢了附加属性:\n%s", got)
	}
}

// TestNewAcceptsNilPublisher 确认"只输出、不发布事件"这条路径仍然可用：
// 事件总线要等平台与配置就绪后才建，日志从第一行代码起就要能用。
func TestNewAcceptsNilPublisher(t *testing.T) {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	h, buf := newCapture(level)

	slog.New(h).Info("不接总线也要能输出")
	if !strings.Contains(buf.String(), "不接总线也要能输出") {
		t.Errorf("未接发布者时日志丢了:\n%s", buf.String())
	}
}
