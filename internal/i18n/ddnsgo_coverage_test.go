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
	// Errorf 是移植代码里**返回给上层**的错误（见 internal/ddnsgo/errorf.go）。
	// 它与 Log/LogStr 共用"中文原文即 key"的约定，因此同样要在棘轮里排除、
	// 同样要被译文覆盖测试检查。
	"Errorf": true,
}

// ddnsGoSite 是一个消息调用点。
type ddnsGoSite struct {
	file string // 相对仓库根的路径
	line int
}

func (s ddnsGoSite) String() string { return s.file + ":" + strconv.Itoa(s.line) }

// ddnsGoScan 是扫描结果。
type ddnsGoScan struct {
	// keys 是能**静态确定**的 key → 出现位置。
	keys map[string][]ddnsGoSite
	// dynamic 是第一个参数含变量的调用点 —— 它们的运行时 key 静态算不出来。
	dynamic map[ddnsGoSite][]string
}

// collectDdnsGoKeys 扫出 internal/ddnsgo 里全部消息调用点。
func collectDdnsGoKeys(t *testing.T, root string) ddnsGoScan {
	t.Helper()

	fset := token.NewFileSet()
	scan := ddnsGoScan{
		keys:    map[string][]ddnsGoSite{},
		dynamic: map[ddnsGoSite][]string{},
	}

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
			rel = filepath.ToSlash(rel)

			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || !ddnsGoCallNames[id.Name] || len(call.Args) == 0 {
					return true
				}
				site := ddnsGoSite{file: rel, line: fset.Position(call.Pos()).Line}

				var parts []string
				collectStringParts(call.Args[0], &parts)
				if len(parts) == 0 {
					return true
				}
				// 第一个参数里除了字面量还有别的东西 → 运行时 key 算不出来。
				if !isPureStringExpr(call.Args[0]) {
					scan.dynamic[site] = parts
					return true
				}
				key := strings.Join(parts, "")
				scan.keys[key] = append(scan.keys[key], site)
				return true
			})
			return nil
		})
	if err != nil {
		t.Fatalf("遍历移植代码失败: %v", err)
	}
	return scan
}

// isPureStringExpr 报告表达式是否只由字符串字面量与 `+` 组成。
func isPureStringExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.BinaryExpr:
		return v.Op == token.ADD && isPureStringExpr(v.X) && isPureStringExpr(v.Y)
	default:
		return false
	}
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

// ddnsGoDynamicKeys 登记"key 由变量拼接而成"的调用点。
//
// # 为什么需要这张表
//
// 静态扫描算不出 `Log(requestType+"域名解析 %s 成功! IP: %s\n", ...)` 的
// 运行时 key —— `requestType` 是变量。而**测试看不见的东西就会漏**：
//
// 这张表建立之前，上面那两处（namesilo）与 vercel 的两处**实际产生的
// 运行时 key 完全不在目录里**，却没有被任何断言发现。原因是当时的扫描
// 只拼接字面量片段，于是它检查的是 `"域名解析 %s 成功! IP: %s\n"` ——
// 一个**从来不会出现**的 key。
//
// 因此这里要求把每一处显式登记出来，并列出它**可能产生的全部运行时 key**。
// 没登记的新拼接点会让测试失败。
var ddnsGoDynamicKeys = map[string][]string{
	"internal/ddnsgo/provider_namesilo.go:140": {
		"新增域名解析 %s 成功! IP: %s\n",
		"更新域名解析 %s 成功! IP: %s\n",
	},
	"internal/ddnsgo/provider_namesilo.go:143": {
		"新增域名解析 %s 失败! 异常信息: %s",
		"更新域名解析 %s 失败! 异常信息: %s",
	},
	"internal/ddnsgo/provider_vercel.go:107": {
		"新增域名解析 %s 成功! IP: %s",
		"更新域名解析 %s 成功! IP: %s",
	},
	"internal/ddnsgo/provider_vercel.go:110": {
		"新增域名解析 %s 失败! 异常信息: %s",
		"更新域名解析 %s 失败! 异常信息: %s",
	},
}

// TestDdnsGoKeysAreTranslated 是核心断言：每个 key 都要有英文译文。
func TestDdnsGoKeysAreTranslated(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	scan := collectDdnsGoKeys(t, root)
	if len(scan.keys) == 0 {
		t.Fatal("没有扫到任何 key —— 测试没有覆盖到东西（" +
			"是不是 ddnsgo 的调用点改名了？）")
	}

	en := New(En)

	// 静态能确定的 key + 登记过的动态 key，一起检查。
	all := map[string][]string{}
	for key, sites := range scan.keys {
		for _, s := range sites {
			all[key] = append(all[key], s.String())
		}
	}
	for site, keys := range ddnsGoDynamicKeys {
		for _, key := range keys {
			all[key] = append(all[key], site+"（拼接）")
		}
	}

	var missing []string
	for key, sites := range all {
		if _, exempt := ddnsGoUntranslated[key]; exempt {
			continue
		}
		if en.T(key) == key {
			// 用 %q 而不是 %s：key 里可能有**看不见的字符**。
			//
			// 实测过一次：`域名解析 %s 成功! IP: %s` 与
			// `域名解析 %s 成功! IP: %s\n` 在报告里长得一模一样，
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
			len(missing), len(all), strings.Join(missing, "\n  "))
	}
}

// TestDynamicKeySitesAreRegistered 要求每一处拼接点都被显式登记。
//
// 这条比上一条更重要：上一条只能检查**已经登记**的 key，而这一条管的是
// "有没有拼接点根本没被登记"。没有它，新写一处
// `Log(kind+"...", ...)` 就会重新落进那个静默的盲区。
func TestDynamicKeySitesAreRegistered(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	scan := collectDdnsGoKeys(t, root)

	var unregistered []string
	for site := range scan.dynamic {
		if _, ok := ddnsGoDynamicKeys[site.String()]; !ok {
			unregistered = append(unregistered, site.String())
		}
	}
	if len(unregistered) > 0 {
		sortStrings(unregistered)
		t.Errorf("有 %d 处消息调用的 key 由变量拼接而成，但没有登记：\n  %s\n\n"+
			"静态扫描算不出它们的运行时 key，因此它们**不会被译文检查覆盖**。\n"+
			"请把该处可能产生的全部 key 加进 ddnsGoDynamicKeys。",
			len(unregistered), strings.Join(unregistered, "\n  "))
	}

	// 反向：登记了但已经不存在的调用点。
	for site := range ddnsGoDynamicKeys {
		found := false
		for s := range scan.dynamic {
			if s.String() == site {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ddnsGoDynamicKeys 里的 %s 已经不存在了（调用点被改过或删掉）——"+
				"请同步这张表，否则它会慢慢变成一份不准确的清单", site)
		}
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

	scan := collectDdnsGoKeys(t, root)
	if len(scan.keys) < 10 {
		t.Fatalf("扫到的 key 太少（%d），无法说明问题", len(scan.keys))
	}

	if n := counts["internal/ddnsgo"]; n > len(scan.keys) {
		t.Errorf("棘轮在 internal/ddnsgo 里数出 %d 处，而消息 key 只有 %d 个 —— "+
			"说明 Log/LogStr 的参数没有被排除，棘轮在把 key 当文案数",
			n, len(scan.keys))
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
