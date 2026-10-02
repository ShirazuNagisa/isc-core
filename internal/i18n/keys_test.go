package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 本文件验证**代码里用到的每一个 i18n key 都真的在目录里**。
//
// # 它防的是一个静默的失效模式
//
// `i18n.T` 对缺失的 key **不 panic**，而是返回 key 本身：
//
//	"cli.init.step_verify_note_a"
//
// 那个设计是对的（漏翻一条文案不该让内核崩），但它的代价是**拼错 key
// 不会有任何提示**。真机上出现过一次：目录里写的是
// `cli.init.step_verify_note`，而代码用的是 `_a` / `_b` / `_c` ——
// 于是 `isc init` 打出了三个 key 名连在一起的一行乱码。
//
// 而发现它的方式是**跑起来看输出**，不是任何一条测试。这就是要补的洞。

// keyCallRe 匹配 i18n.T("key") 与 T("key") 两种写法。
var keyCallRe = regexp.MustCompile(`\b(?:i18n\.)?T\(\s*"([a-z][a-z0-9_.]*)"`)

// TestUsedKeysExist 扫全部 Go 源码，核对每个 key。
func TestUsedKeysExist(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	used := collectUsedKeys(t, root)

	if len(used) == 0 {
		t.Fatal("没有从源码里解析出任何 key —— 正则可能过时了")
	}

	// 两种语言都必须有 —— 目录一致性由另一条测试保证，这里只做核对。
	zh := New(ZhCN)
	var missing []string
	for key := range used {
		if !zh.Has(key) {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("有 %d 个 key 在代码里被使用，但目录里没有：\n  %s\n\n"+
			"i18n.T 对缺失的 key 会**静默返回 key 本身** —— 界面上会出现\n"+
			"一串 key 名而不是文案。请把它们补进 messages_zh.go 与 messages_en.go。",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestNoUnusedKeys 反向检查：目录里有、但没人用的 key。
//
// 它是**报告**而不是失败：有些 key 是为下游 GUI 或未来的控制台准备的，
// 现在没人用是合理的。但把这个数字打出来，可以让"目录是不是在腐烂"
// 变成一个看得见的问题。
func TestNoUnusedKeys(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	used := collectUsedKeys(t, root)

	var unused []string
	for _, key := range Keys() {
		if !used[key] {
			unused = append(unused, key)
		}
	}
	sort.Strings(unused)

	total := len(Keys())
	t.Logf("目录共 %d 条，其中 %d 条当前没有被任何代码使用",
		total, len(unused))
	if len(unused) > 0 && len(unused) < 40 {
		t.Logf("未被使用的 key：\n  %s", strings.Join(unused, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

// collectUsedKeys 扫出源码里出现过的全部 key。
func collectUsedKeys(t *testing.T, root string) map[string]bool {
	t.Helper()

	used := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "ddns-go-master", "vendor", ".git", ".tmp", "dist", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// i18n 包自己是目录，里面的字符串就是译文。
		if strings.Contains(path, string(filepath.Separator)+"i18n"+string(filepath.Separator)) {
			return nil
		}

		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}

		// 用 AST 而不是纯文本正则：注释里提到一个 key 不该算"被使用"。
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			// 只看 T(...) 或 xxx.T(...) 这种调用。
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name != "T" {
					return true
				}
			case *ast.SelectorExpr:
				if fun.Sel == nil || fun.Sel.Name != "T" {
					return true
				}
			default:
				return true
			}

			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if key, uerr := strconv.Unquote(lit.Value); uerr == nil && key != "" {
				used[key] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("遍历源码失败: %v", err)
	}

	return used

}
