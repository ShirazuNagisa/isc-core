package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件守的是**页面标记与消息目录之间的一致性**。
//
// # 它防的是什么
//
// 提取文案时，改动是成对的：index.html 里加 `data-i18n="web.x"`，
// 目录里加一条 `"web.x": "…"`。漏掉后一半的表现是**页面上那一块空白** ——
// 而它不会报错、不会 panic，只是少了一行字。翻遍整个页面去找"哪一块空了"
// 是件很费眼睛的事。
//
// 反过来的漂移（目录里有、页面上没用）会让目录里堆死条目，
// 而译者会以为它们还要维护。

var consoleMarkerRe = regexp.MustCompile(`data-i18n(?:-html)?="(web\.[a-z_.0-9]+)"`)

// consoleMarkerKeys 取出 index.html 里被标记的全部 key。
func consoleMarkerKeys(t *testing.T, root string) []string {
	t.Helper()

	byt, err := os.ReadFile(filepath.Join(root, "internal", "console", "assets", "index.html"))
	if err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{}
	for _, m := range consoleMarkerRe.FindAllStringSubmatch(string(byt), -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestConsoleMarkersHaveCatalogEntries 是这一组里最重要的一条：
// 页面上每个标记都要有对应的目录条目。
func TestConsoleMarkersHaveCatalogEntries(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	keys := consoleMarkerKeys(t, root)
	if len(keys) == 0 {
		t.Fatal("index.html 里一个 data-i18n 都没有 —— " +
			"测试没覆盖到东西（属性名改了吗？）")
	}

	zh := ConsoleMessages(ZhCN)
	en := ConsoleMessages(En)

	var missingZh, missingEn []string
	for _, k := range keys {
		if _, ok := zh[k]; !ok {
			missingZh = append(missingZh, k)
		}
		if _, ok := en[k]; !ok {
			missingEn = append(missingEn, k)
		}
	}

	if len(missingZh) > 0 {
		t.Errorf("页面标记了 %d 个 key，中文目录里缺 %d 个：\n  %s\n\n"+
			"表现是页面上那一块**空白** —— 不报错、不 panic，只是少了一行字。",
			len(keys), len(missingZh), strings.Join(missingZh, "\n  "))
	}
	if len(missingEn) > 0 {
		t.Errorf("英文目录里缺 %d 个：\n  %s", len(missingEn), strings.Join(missingEn, "\n  "))
	}
}

// TestConsoleCatalogHasNoDeadEntries 检查目录里没有**已经不再被标记**的条目。
//
// 它现在是**报告**而非失败项：提取是分批做的，目录里先放几条还没用上的
// 条目是正常的（比如 web.lang_tag 由 i18n.js 用，页面上没有标记）。
// 但它会打印出来，好让"提取做完了、目录里却还堆着死条目"这件事看得见。
func TestConsoleCatalogHasNoDeadEntries(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	used := map[string]bool{}
	for _, k := range consoleMarkerKeys(t, root) {
		used[k] = true
	}
	// 这几个由前端脚本用，不来自 HTML 标记。
	used["web.lang_tag"] = true

	var dead []string
	for k := range ConsoleMessages(ZhCN) {
		if !used[k] {
			dead = append(dead, k)
		}
	}
	sort.Strings(dead)

	if len(dead) > 0 {
		t.Logf("目录里有 %d 条尚未被页面标记使用（提取进行中时正常）：\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
}
