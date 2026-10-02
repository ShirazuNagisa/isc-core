package i18n

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件测**注释剥离**。
//
// 它值得单独测，因为它是这个仓库里唯一一段自己写的词法处理 ——
// 而它错了的代价很隐蔽：剥多了会让真正的文案不被计数（棘轮失效），
// 剥少了会让注释被当成文案（数字降不到 0）。

func TestStripHTMLComments(t *testing.T) {
	t.Parallel()

	src := "<p>一</p><!-- 这是注释 -->\n<p>二</p>\n<!--\n多行\n注释\n-->\n<p>三</p>"

	got := stripHTMLComments(src)

	if strings.Contains(got, "这是注释") || strings.Contains(got, "多行") {
		t.Errorf("注释没有被剥掉:\n%s", got)
	}
	for _, want := range []string{"一", "二", "三"} {
		if !strings.Contains(got, want) {
			t.Errorf("正文里的 %q 被误剥了:\n%s", want, got)
		}
	}
	// 行号必须对齐：棘轮按行比对，错位会让报告指向错误的行。
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Errorf("换行数从 %d 变成 %d —— 行号会错位",
			strings.Count(src, "\n"), strings.Count(got, "\n"))
	}
}

// TestStripJSCommentsKeepsURLs 是这一组里最关键的一条。
//
// 朴素的"按行找 //"会把 `https://example.com` 后面的内容一起吃掉 ——
// 而那正是需要计数的东西（提示文案里经常有 URL）。
func TestStripJSCommentsKeepsURLs(t *testing.T) {
	t.Parallel()

	src := "// 注释里的中文\n" +
		"const url = 'https://example.com/路径';  // 行尾注释里的中文\n" +
		"const s = \"中文双引号\";\n" +
		"const tpl = `模板 ${x} 里的中文`;\n" +
		"/* 块注释\n   里的中文 */\n" +
		"const s2 = 'a\\'b // 中文';\n"

	got := stripJSComments(src)

	// 该留下的
	for _, want := range []string{"https://example.com/路径", "中文双引号", "模板 ${x} 里的中文", "a\\'b // 中文"} {
		if !strings.Contains(got, want) {
			t.Errorf("字符串里的 %q 被误剥了:\n%s", want, got)
		}
	}
	// 该剥掉的
	for _, unwanted := range []string{"注释里的中文", "行尾注释里的中文", "块注释"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("注释里的 %q 没有被剥掉:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Errorf("换行数变了 —— 行号会错位")
	}
}

// TestStripUnterminatedCommentDoesNotPanic 钉住未闭合注释的处理。
//
// 它不该 panic，也不该把后面的内容当成代码 —— 未闭合意味着"剩下的全是注释"。
func TestStripUnterminatedCommentDoesNotPanic(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		"中文 <!-- 没闭合",
		"中文 /* 没闭合",
		"中文 // 没换行",
		"a\\",
		"`未闭合的模板串",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("输入 %q 让剥离逻辑 panic 了: %v", src, r)
				}
			}()
			_ = stripConsoleComments("x.js", src)
			_ = stripConsoleComments("x.html", src)
		}()
	}
}

// TestStripLeavesNoCommentInRealAssets 确认四个真实资源都被处理到了。
func TestStripLeavesNoCommentInRealAssets(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	var stripped int

	for _, rel := range consoleAssets {
		byt, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		body := string(byt)
		got := stripConsoleComments(rel, body)
		if got != body {
			stripped++
		}
		if strings.Count(got, "\n") != strings.Count(body, "\n") {
			t.Errorf("%s 的行号在剥离后错位了", rel)
		}
	}

	if stripped == 0 {
		t.Error("四个资源里一个都没被剥离 —— 说明规则没匹配上任何注释，" +
			"而它们每个都有中文注释")
	}
}
