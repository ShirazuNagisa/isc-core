package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// 本文件是 D21 的**棘轮**：硬编码的用户可见中文字符串不得增加。
//
// # 为什么是棘轮而不是"一律禁止"
//
// D21（docs/DECISIONS.md）写得很明确：
//
//	**禁止硬编码用户可见字符串**。所有面向用户的文案（API 错误、CLI 输出、
//	通知模板、控制台）都必须经由本包的消息 key 获取。
//
// 而实际状况是**离达标还很远**（见下面的基线）。PLAN 里那条 CI 检查
// 一直写着"此阶段先占位" —— 也就是说这个缺口从未被度量过。
//
// 一个"一律禁止"的检查会立刻红掉全仓，而那种检查的下场是被人加
// `//nolint` 或者干脆从 CI 里摘掉。**棘轮**是这类大规模迁移的通行做法：
//
//	不允许**新增**硬编码串（这是最容易发生的）
//	允许逐步减少（每转换一处就调低基线）
//	基线本身就是进度表
//
// # 用户可见的症状
//
// 这不是纯粹的内部整洁问题。真机上确认过：`isc --lang en credential list`
// 输出**仍然是中文** —— 标志存在、有文档、对 CLI 输出毫无作用。
// 而接口层是正确的（`任务不存在` 来自消息目录）。
//
// # 基线怎么调
//
// 转换完一处之后，把对应包的基线改成实际值（**只能调低**）。
// 某个包降到 0 时，从下面的表里删掉它 —— 那时它会受"完全不许有"的约束。

// convertedFiles 是**已经完全转换**的文件。
//
// 按文件而不是按包记录进度：一个包有几十个文件，而迁移是一文件一文件
// 推进的。这个列表让"哪些已经做完"一目了然，而不是只能从总数推断。
var convertedFiles = []string{
	"internal/cli/credential.go",
	"internal/cli/records.go",
	"internal/cli/root.go",
	"internal/cli/ddns.go",
	"internal/cli/cert.go",
	"internal/cli/client.go",
	"internal/cli/ddns_add.go",
	"internal/cli/notify.go",
	"internal/cli/console.go",
	"internal/cli/service.go",
	"internal/cli/daemon.go",
	"internal/cli/verify.go",
	"internal/cli/doctor.go",
	"internal/cli/proxy.go",
	"internal/cli/expose.go",
	"internal/cli/init.go",
}

// hardcodedBaseline 是各包当前硬编码中文串的数量。
//
// 数字由 TestNoNewHardcodedStrings 自己统计并对照，因此它同时是
// **进度表**：改小它是这个迁移唯一的推进方式。
var hardcodedBaseline = map[string]int{
	"internal/ddnsgo":         318,
	"internal/platform":       73,
	"internal/provider/tier1": 158,
	"internal/reach":          90,
	"internal/store":          73,
	"internal/acme":           67,
	"scripts/release":         64,
	"internal/proxy":          55,
	"internal/verify":         43,
	"internal/change":         40,
	"internal/daemon":         37,
	"internal/ddns":           30,
	"internal/notify":         28,
	"internal/provider":       21,
	"internal/credential":     17,
	"internal/secret":         14,
	"internal/paths":          14,
	"internal/dns":            12,
	"internal/job":            9,
	"internal/runtimeinfo":    9,
	"internal/configio":       9,
}

// TestNoNewHardcodedStrings 统计各包的硬编码中文串，与基线对照。
//
// 超出基线即失败 —— 那就意味着**新增了**未经消息目录的用户可见文案。
func TestNoNewHardcodedStrings(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	counts := countHardcodedCJK(t, root)

	// 日志包不参与：它们的理由写在 logOnlyPackages 里。
	for pkg := range logOnlyPackages {
		delete(counts, pkg)
	}

	for pkg, n := range counts {
		limit, known := hardcodedBaseline[pkg]
		if !known {
			// 新出现的包：要么它没有硬编码串（那就不该出现），
			// 要么它需要一条基线（那就把它加进表里并说明）。
			t.Errorf("包 %s 有 %d 处硬编码中文串，但基线表里没有它。\n"+
				"如果这是新增的包，请把它的基线加进 hardcodedBaseline。",
				pkg, n)
			continue
		}
		if n > limit {
			t.Errorf("包 %s 的硬编码中文串从 %d 涨到了 %d。\n"+
				"D21 要求所有面向用户的文案经由消息目录（internal/i18n）。\n"+
				"新增文案请用 i18n.T(\"some.key\") 并同时补 zh-CN 与 en 两份译文。",
				pkg, limit, n)
		}
	}

	// 减少了也要报出来 —— 那时基线该跟着调低，否则棘轮会松掉。
	for pkg, limit := range hardcodedBaseline {
		if n, ok := counts[pkg]; !ok {
			t.Errorf("包 %s 已经没有任何硬编码中文串（或已不存在），"+
				"请把它从 hardcodedBaseline 里删掉", pkg)
		} else if n < limit {
			t.Logf("包 %s 的硬编码中文串已从 %d 降到 %d —— "+
				"请把基线调低到 %d，让棘轮跟上", pkg, limit, n, n)
		}
	}
}

// TestUserFacingPackagesHaveI18n 记录哪些包**已经**完全合规。
//
// 这个列表只增不减：一个包进了这里就不该再退出去。
var i18nComplete = []string{
	// 第一个达标的包。
	//
	// 443 处 → 0，用了七轮。它的每一轮都同时在做两件事：把文案搬进目录，
	// 以及**发现那些"搬进去之后才显形"的问题** —— 帮助文本的求值时机、
	// 用了不存在的 key、并行测试与全局语言的冲突。
	"internal/cli",

	// 接口层。近百条错误说明与操作名，全部搬进 api_zh.go / api_en.go。
	//
	// 它比 cli 更容易被忽略：那些文案不会直接打在终端上，而是藏在
	// HTTP 响应的 detail 字段里 —— 但 GUI 与脚本读的正是它。
	"internal/api",

	// 设置项的校验错误。它们会作为 API 的 detail 返回给调用方，
	// 因此是面向用户的 —— 与 event/console/audit 那三个纯日志包不同，
	// 后者的理由见 logOnlyPackages。
	"internal/settings",
}

func TestUserFacingPackagesHaveI18n(t *testing.T) {
	t.Parallel()

	if len(i18nComplete) == 0 {
		t.Skip("尚无完全合规的包 —— 迁移未开始")
	}

	root := repoRoot(t)
	counts := countHardcodedCJK(t, root)

	for _, pkg := range i18nComplete {
		if n := counts[pkg]; n > 0 {
			t.Errorf("包 %s 曾被标记为完全合规，现在有 %d 处硬编码中文串",
				pkg, n)
		}
	}
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// 控制台前端的文案**不在 Go 源码里**，因此早先的统计完全看不见它。
//
// 这是 D21 明确列出的四个面之一（API 错误、CLI 输出、通知模板、控制台），
// 而一个"只数 Go 文件"的棘轮会给出**虚假的安心**：数字在降，而四个面里
// 有一个根本没被数过。
//
// 这里把前端资源也纳入统计。粒度按"文件"而不是"包" —— 前端只有四个
// 文件，而它们的体积差异很大。
var consoleAssets = []string{
	"internal/console/assets/index.html",
	"internal/console/assets/app.js",
	"internal/console/assets/panels.js",
}

// TestConsoleAssetsAreCounted 记录控制台前端的硬编码文案数。
//
// 它现在是**报告**而非失败项：前端本地化需要一套与 Go 侧不同的机制
// （静态资源在浏览器里运行，拿不到 i18n 目录），而那是一件独立的工作。
//
// 但数字必须被看见。在这之前它连数字都没有 —— 而"没有被测量的东西
// 不会被改进"。
func TestConsoleAssetsAreCounted(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var lines, chars int
	broken := map[string]int{}

	for _, rel := range consoleAssets {
		byt, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("读不到 %s: %v", rel, err)
			continue
		}
		n := 0
		for _, line := range strings.Split(string(byt), "\n") {
			if containsHan(line) {
				n++
				chars += len([]rune(line))
			}
		}
		if n > consoleHardcodedLines[rel] {
			broken[rel] = n
		}
		lines += n
	}

	t.Logf("控制台前端：%d 行含中文（%d 个字符）—— 尚未迁移到消息目录",
		lines, chars)

	for rel, n := range broken {
		t.Errorf("%s 的含中文行数从 %d 涨到了 %d。\n"+
			"控制台是 D21 列出的四个面之一。前端本地化尚未开始，"+
			"因此这里只拦住**新增**。",
			rel, consoleHardcodedLines[rel], n)
	}
}

// consoleHardcodedLines 是前端各文件当前的含中文行数。
//
// 与 Go 侧的基线同理：它是一张进度表，只降不升。
var consoleHardcodedLines = map[string]int{
	"internal/console/assets/index.html": 129,
	"internal/console/assets/app.js":     191,
	"internal/console/assets/panels.js":  94,
}

// logOnlyPackages 是**只剩下日志文案**的包，不再要求迁移。
//
// # 为什么"把日志也翻译了"不是目标
//
// D21 要求的是"面向用户的文案"。而日志行是**给运维看的**，不是给用户看的。
// 把它们翻译了反而有害：
//
//   - 运维靠 grep 稳定的字符串来定位问题。日志随语言设置变来变去，
//     会让"我上周见过这条错误"变成一件做不到的事；
//   - 报错时用户贴出来的日志，会与文档、issue 里的英文/中文原文对不上；
//   - 而这些行本来就带 `pkg: ` 前缀（如 `event: `），是明确的内部信号。
//
// 所以这里的做法是：**显式地**把这类包列出来并写明理由，而不是机械地
// 一条条翻译过去。棘轮因此仍然有意义 —— 它挡住的是"新加了一条面向用户的
// 中文文案"，而不是"日志里出现了中文"。
//
// 判定标准：该包剩余的中文串**全部**出现在 slog / errors.New 的内部错误里，
// 且不会作为 API 的 detail 返回给调用方。
var logOnlyPackages = map[string]string{
	"internal/event": "仅剩总线自身的日志（订阅者过慢、总线已关闭）",
	"internal/console": "仅剩内嵌资源结构异常这一条开发者错误，" +
		"它表示二进制被破坏，用户看到的会是 500",
	"internal/audit": "仅剩写入审计失败这一条日志 —— 它是**刻意**只记日志的：" +
		"审计写不进去不该让业务操作失败",
}

// countHardcodedCJK 统计各包里的中文字符串字面量。
//
// 只统计**字符串字面量**，不统计注释 —— 注释里的中文是好的
// （它们解释了"为什么"），而 D21 管的是面向用户的文案。
func countHardcodedCJK(t *testing.T, root string) map[string]int {
	t.Helper()

	fset := token.NewFileSet()
	counts := map[string]int{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "ddns-go-master", "vendor", ".git", ".tmp", "dist", "bin", "gen":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// i18n 包自己是消息目录，它的字符串就是译文本身。
		if strings.Contains(path, string(filepath.Separator)+"i18n"+string(filepath.Separator)) {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		// 统一成斜杠，让基线表在三个平台上一致。
		pkg := filepath.ToSlash(filepath.Dir(rel))

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if containsHan(s) {
				counts[pkg]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码失败: %v", err)
	}
	return counts
}

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// repoRoot 向上找到含 go.mod 的目录。
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("找不到仓库根目录")
	return ""
}
