package console

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeTempAsset 把内嵌资源写到临时文件，供外部工具检查。
//
// `node --check` 需要一个**文件路径**，而资源是 go:embed 进来的。
// 写到临时目录而不是解压整个资源树：只有被检查的那一个文件需要落盘。
func writeTempAsset(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(readAsset(t, name)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// 本文件是控制台前端的**语法检查**。
//
// # 它为什么存在
//
// 这个仓库的 Go 侧有 24 个包的测试，但它们读 JS 的方式是**当文本读** ——
// 查 ID 是否唯一、查有没有内联事件处理器、查路径是否在契约里。
// 没有一条会解析它。
//
// 于是发生过这件事：一次批量替换把 40 多处字符串改成 t('key', '兜底')，
// 其中两处替换串末尾多带了一个引号，与原文的 `+ esc(...)` 拼成了
// `+ ' + esc(...)`。**24 个包全部通过**，而那两个文件根本跑不起来。
//
// 空白处不在测试的数量，而在它们的**种类**：那一类检查一个都没有。
//
// # 为什么用外部工具而不是 Go 里解析
//
// 项目禁止引入新的编译期依赖（见 docs/PLAN.md D06 附近），而一个完整的
// JS 语法分析器不是几十行能写完的。`node --check` 做的是**纯语法解析**，
// 不执行任何代码 —— 对一个只做语法检查的需求刚好够用，也没有副作用。
//
// 没有 node 的机器上**跳过**而不是失败：这条检查是加固，不是构建前置条件。
func TestConsoleJavaScriptParses(t *testing.T) {
	t.Parallel()

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("没有找到 node；跳过 JS 语法检查（这条是加固，不是前置条件）")
	}

	for _, name := range []string{"app.js", "panels.js", "i18n.js"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := writeTempAsset(t, name)

			// --check 只解析，不执行。因此它不会碰到这个文件里的任何逻辑。
			out, err := exec.Command(node, "--check", path).CombinedOutput()
			if err != nil {
				t.Errorf("%s 有语法错误 —— 这类问题在 Go 测试里"+
					"**一个都抓不到**，而它会让整个控制台白屏：\n%s",
					name, out)
			}
		})
	}
}

// TestConsoleJavaScriptHasNoStrayQuotedPlus 是上面那条的**廉价补充**。
//
// 批量替换最常犯的错是把一个 `+` 关进字符串里（`+ ' + esc(...)`）。
// 它产生的语法错误 node 能抓到，而这条在**没有 node 的机器上**也能抓到 ——
// 同一类缺陷有两道网，其中一道不依赖外部工具。
func TestConsoleJavaScriptHasNoStrayQuotedPlus(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"app.js", "panels.js", "i18n.js"} {
		src := readAsset(t, name)
		for i, line := range strings.Split(src, "\n") {
			// `+ ' + ` 或行尾的 `+ ' +`：一个加号被引号包住了。
			if strings.Contains(line, "+ ' + ") || strings.HasSuffix(strings.TrimSpace(line), "+ ' +") {
				t.Errorf("%s:%d 有一个被引号包住的加号：\n  %s\n\n"+
					"这是批量替换最常见的失误，而它会让整个控制台白屏。",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}
