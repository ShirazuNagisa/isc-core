package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件测试**计数器本身**。
//
// 为什么计数器的行为也需要测试：棘轮挡住了"新增一条面向用户的中文文案"，
// 而它挡不住的东西同样重要 —— 如果计数器把**日志行**也算进来，它就在测量
// 一个 D21 不关心的东西，那个数字永远降不到 0，于是"达标"变成不可能。
//
// 这不是假想的：internal/change 与 internal/daemon 的用户可见错误与 slog
// 日志混在同一个包里，此前只能靠 logOnlyPackages 逐个包手工豁免 ——
// 那个机制对"整包只剩日志"够用，对混在一起的包就失效了。
//
// 改成按**调用**排除之后，daemon 的数字从 37 降到 12，change 从 40 降到 22。

// countIn 把一段源码放进临时目录并计数。
func countIn(t *testing.T, src string) int {
	t.Helper()

	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "sample")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "sample.go"),
		[]byte("package sample\n\n"+src), 0o600); err != nil {
		t.Fatal(err)
	}

	counts := countHardcodedCJK(t, dir)
	return counts["sample"]
}

// TestLoggerArgumentsAreNotCounted 是核心的一条：日志参数不计入。
func TestLoggerArgumentsAreNotCounted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "字段风格的结构化日志",
			src: `func f() {
	s.log.Info("开始执行变更", "plan", "p1")
	s.log.Warn("写入日志失败，继续执行")
	s.log.Error("回滚结果写入日志失败")
	s.log.Debug("发现未完成的变更")
}`,
			want: 0,
		},
		{
			name: "直接叫 logger 的接收者",
			src: `func f() {
	logger.Info("总线已关闭")
}`,
			want: 0,
		},
		{
			name: "slog 包级函数",
			src: `func f() {
	slog.Warn("订阅者过慢")
}`,
			want: 0,
		},
		{
			name: "嵌套接收者",
			src: `func f() {
	a.b.logger.Error("审计写入失败")
}`,
			want: 0,
		},
		{
			name: "返回给调用方的错误要计入",
			src: `func f() error {
	return errors.New("change: 变更记录不存在")
}`,
			want: 1,
		},
		{
			name: "fmt.Errorf 要计入",
			src: `func f() error {
	return fmt.Errorf("撤销步骤 %q 失败: %w", "s", nil)
}`,
			want: 1,
		},
		{
			name: "两者混在一起时只数后者",
			src: `func f() error {
	s.log.Info("开始撤销变更")
	s.log.Info("变更已撤销")
	return errors.New("change: 没有登记撤销器，无法撤销")
}`,
			want: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := countIn(t, tc.src); got != tc.want {
				t.Errorf("计数 = %d，期望 %d\n源码:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

// TestNonLoggerErrorCallsAreCounted 钉住**保守性**。
//
// `isLogCall` 只看方法名会被 `x.Error("用户可见文案")` 骗过去，因此它
// 还要看接收者。这一条确认"接收者不像 logger"时**不会**被排除 ——
// 宁可漏掉一条日志（那它会被要求迁移），也不要把用户可见的错误放过。
func TestNonLoggerErrorCallsAreCounted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		src  string
	}{
		{"接收者是普通标识符", `func f() { problem.Error("操作失败") }`},
		{"接收者是 resp", `func f() { resp.Error("记录不存在") }`},
		{"接收者是 e", `func f() { e.Error("写入失败") }`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := countIn(t, tc.src); got != 1 {
				t.Errorf("%s 的计数 = %d，期望 1 —— "+
					"接收者不像 logger 时不该被排除", tc.name, got)
			}
		})
	}
}

// TestLoggerDetectionIsNotNameBased 确认判断不是"方法名里有 Error 就排除"。
func TestLoggerDetectionIsNotNameBased(t *testing.T) {
	t.Parallel()

	// errors.New 的方法名是 New，不在日志级别里；即便在也不该被排除，
	// 因为它的接收者 `errors` 不含 log。
	if got := countIn(t, `func f() { _ = errors.New("出错了") }`); got != 1 {
		t.Errorf("errors.New 的计数 = %d，期望 1", got)
	}
	// fmt.Errorf 同理。
	if got := countIn(t, `func f() { _ = fmt.Errorf("失败了") }`); got != 1 {
		t.Errorf("fmt.Errorf 的计数 = %d，期望 1", got)
	}
}
