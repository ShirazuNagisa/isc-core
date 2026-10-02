package cli

import (
	"sync"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// langMu 串行化"会改全局语言"与"依赖全局语言"的测试。
//
// # 它为什么必需
//
// 把 CLI 文案搬进消息目录之后，`i18n.T(...)` 的结果取决于一个**全局**的
// 默认语言。而 Go 的并行测试会让这两类测试同时跑：
//
//	一个测试把语言设成 en 来验证帮助文本会跟着变
//	另一个测试断言某段输出里含某个中文词
//
// 两者交错时后者会间歇性失败 —— 真机上就是这样出现的：
// TestNextStepsIncludesVerify 断言"公网"两个字，而语言恰好被另一个
// 测试改成了英文。
//
// 这不是"测试写得不好"，而是**被测对象确实有全局状态**。既然它存在，
// 测试就必须显式地串行化，而不是靠运气。
var langMu sync.Mutex

// withLang 在测试期间把默认语言固定为 lang，并在结束时恢复。
//
// 它会把 t 标记为**非并行**（通过持有互斥量），因此调用它的测试不该
// 再调用 t.Parallel()。
func withLang(t *testing.T, lang i18n.Lang) {
	t.Helper()

	langMu.Lock()
	prev := i18n.Default
	i18n.SetDefault(lang)
	t.Cleanup(func() {
		i18n.SetDefault(prev)
		langMu.Unlock()
	})
}
