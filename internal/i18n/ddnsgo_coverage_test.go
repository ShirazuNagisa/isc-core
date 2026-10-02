package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件管的是**移植代码的消息覆盖**。
//
// # 它防的是一个静默的回退
//
// internal/ddnsgo 沿用 ddns-go 的约定：**中文原文即消息 key**。
// `Log(key, args...)` / `LogStr(key, args...)` 把它们交给 i18n 目录，
// 而 `T()` 在找不到 key 时**返回 key 本身**。
//
// 那个回退是对的（漏翻一句不该让内核崩），但代价很具体：
// **一条没有译文的 key 在英文界面上会显示中文，而没有任何东西会抱怨**。
//
// 实测过一次：62 个去重 key 里有 21 个（34%）没有译文。这个缺口在此文件
// 建立之前一直存在，因为"能跑"和"翻好了"在这套机制下长得一模一样。

// ddnsGoCallNames 是移植代码里承载消息的两个函数。
//
// 它们**按设计**接收中文原文当 key，因此那些字符串不是"硬编码文案"，
// 而是**消息标识符**。棘轮（countHardcodedCJK）同样据此排除它们 ——
// 否则它会把 318 个 key 当成 318 处待迁移的文案，那个数字永远降不到 0。
var ddnsGoCallNames = map[string]bool{
	"Log":    true,
	"LogStr": true,
}

// collectDdnsGoKeys 扫出 internal/ddnsgo 里全部消息 key 及其出现位置。
func collectDdnsGoKeys(t *testing.T, root string) map[string][]string {
	t.Helper()

	fset := token.NewFileSet()
	keys := map[string][]string{}

	err := filepath.WalkDir(filepath.Join(root, "internal", "ddnsgo"),
		func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)

			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || !ddnsGoCallNames[id.Name] || len(call.Args) == 0 {
					return true
				}
				// 第一个参数可能是字面量，也可能是编译期拼接的常量。
				var parts []string
				collectStringParts(call.Args[0], &parts)
				if len(parts) == 0 {
					return true
				}
				key := strings.Join(parts, "")
				keys[key] = append(keys[key], filepath.ToSlash(rel))
				return true
			})
			return nil
		})
	if err != nil {
		t.Fatalf("遍历移植代码失败: %v", err)
	}
	return keys
}

// collectStringParts 收集表达式里的字符串字面量片段（支持 `+` 拼接）。
func collectStringParts(e ast.Expr, out *[]string) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			if s, err := strconv.Unquote(v.Value); err == nil {
				*out = append(*out, s)
			}
		}
	case *ast.BinaryExpr:
		collectStringParts(v.X, out)
		collectStringParts(v.Y, out)
	}
}

// ddnsGoUntranslated 是**已知且有意**没有译文的 key。
//
// 目前为空：所有 key 都应当有译文。若将来确实需要留一条中文，
// 在这里写明理由 —— 而不是让它在英文界面上悄悄显示中文。
var ddnsGoUntranslated = map[string]string{}

// TestDdnsGoKeysAreTranslated 是核心断言：每个 key 都要有英文译文。
func TestDdnsGoKeysAreTranslated(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	keys := collectDdnsGoKeys(t, root)
	if len(keys) == 0 {
		t.Fatal("没有扫到任何 key —— 测试没有覆盖到东西（" +
			"是不是 ddnsgo 的调用点改名了？）")
	}

	en := New(En)

	var missing []string
	for key, sites := range keys {
		if _, exempt := ddnsGoUntranslated[key]; exempt {
			continue
		}
		// 中文原文即 key，因此"没有译文"的表现是 T() 返回 key 本身。
		if en.T(key) == key {
			// 用 %q 而不是 %s：key 里可能有**看不见的字符**。
			//
			// 实测过一次：`域名解析 %s 成功! IP: %s` 与
			// `域名解析 %s 成功! IP: %s\n` 被当成同一条打印出来，
			// 因为那个 \n 是行尾。补译文时因此漏了带 \n 的那条。
			missing = append(missing,
				strconv.Quote(key)+"  ←  "+strings.Join(dedupe(sites), ", "))
		}
	}

	if len(missing) > 0 {
		sortStrings(missing)
		t.Errorf("有 %d 个移植代码的消息 key 没有英文译文（共 %d 个）：\n  %s\n\n"+
			"英文界面上它们会显示中文，而机制**不会报错** —— 这正是本测试存在的理由。\n"+
			"译文加进 internal/i18n/messages_ddnsgo_extra.go（那张表是手工维护的；"+
			"messages_ddnsgo.go 由脚本生成，不要改它）。",
			len(missing), len(keys), strings.Join(missing, "\n  "))
	}
}

// TestDdnsGoLogCallsAreExcludedFromRatchet 确认棘轮不把 key 当文案。
//
// 这条测试守的是**计数器与移植约定的边界**：如果哪天有人在
// countHardcodedCJK 里去掉了 ddnsGoCallNames 的判断，ddnsgo 的基线会
// 立刻"暴涨"318 —— 而那 318 是消息标识符，不是待翻译的文案。
func TestDdnsGoLogCallsAreExcludedFromRatchet(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	counts := countHardcodedCJK(t, root)

	keys := collectDdnsGoKeys(t, root)
	if len(keys) < 10 {
		t.Fatalf("扫到的 key 太少（%d），无法说明问题", len(keys))
	}

	if n := counts["internal/ddnsgo"]; n > len(keys) {
		t.Errorf("棘轮在 internal/ddnsgo 里数出 %d 处，而消息 key 只有 %d 个 —— "+
			"说明 Log/LogStr 的参数没有被排除，棘轮在把 key 当文案数",
			n, len(keys))
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
