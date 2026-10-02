package console

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件守住控制台前端资源的**结构性一致**。
//
// # 为什么需要它
//
// 控制台是纯静态资源，没有构建链、没有类型检查 —— 因此"加了一个标签
// 却忘了加对应面板"这类疏漏不会有任何编译错误。它的表现是：用户点了
// 标签，什么反应都没有。
//
// 这类问题在手工验证时很容易漏掉（谁会逐个点完 11 个标签），
// 而它一旦发生就是 100% 可复现的用户可见缺陷。

func readAsset(t *testing.T, name string) string {
	t.Helper()

	byt, err := fs.ReadFile(FS(), name)
	if err != nil {
		t.Fatalf("读取资源 %s 失败: %v", name, err)
	}
	return string(byt)
}

var (
	tabButtonRe = regexp.MustCompile(`data-tab="([a-z0-9_-]+)"`)
	tabPanelRe  = regexp.MustCompile(`id="tab-([a-z0-9_-]+)"`)
	scriptSrcRe = regexp.MustCompile(`<script src="([^"]+)"`)
)

// TestTabsAndPanelsMatch 是这里最重要的一条。
//
// 每个标签按钮都必须有一个对应的面板，反之亦然。多一个按钮会让用户
// 点了没反应；多一个面板则意味着有一段界面永远看不到。
func TestTabsAndPanelsMatch(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")

	buttons := map[string]bool{}
	for _, m := range tabButtonRe.FindAllStringSubmatch(html, -1) {
		buttons[m[1]] = true
	}

	panels := map[string]bool{}
	for _, m := range tabPanelRe.FindAllStringSubmatch(html, -1) {
		panels[m[1]] = true
	}

	if len(buttons) == 0 {
		t.Fatal("没有解析出任何标签按钮 —— 选择器可能过时了")
	}
	if len(panels) == 0 {
		t.Fatal("没有解析出任何面板 —— 选择器可能过时了")
	}

	for name := range buttons {
		if !panels[name] {
			t.Errorf("标签 %q 没有对应的面板（<section id=\"tab-%s\">）", name, name)
		}
	}
	for name := range panels {
		if !buttons[name] {
			t.Errorf("面板 %q 没有对应的标签按钮（data-tab=\"%s\"）", name, name)
		}
	}
}

// TestExactlyOnePanelIsActive 验证初始只有一个面板是显示状态。
//
// 两个都 active 会让界面上出现两段内容叠在一起；一个都没有则是白屏。
func TestExactlyOnePanelIsActive(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")

	// 只统计面板（tab-panel）上的 active。
	re := regexp.MustCompile(`<section id="tab-[a-z0-9_-]+" class="tab-panel([^"]*)"`)
	active := 0
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		if strings.Contains(m[1], "active") {
			active++
		}
	}

	if active != 1 {
		t.Errorf("初始应当恰好有 1 个面板是 active，实际 %d 个", active)
	}
}

// TestEveryTabHasALoader 验证每个需要数据的标签都有加载动作。
//
// 缺了加载的话，用户点进那个标签只会看到一个空壳 —— 而空壳看起来
// 和"确实没有数据"一模一样，用户无从分辨。
func TestEveryTabHasALoader(t *testing.T) {
	t.Parallel()

	app := readAsset(t, "app.js")

	// 从 index.html 里取出全部标签名，逐个检查 app.js 里的路由。
	html := readAsset(t, "index.html")
	names := map[string]bool{}
	for _, m := range tabButtonRe.FindAllStringSubmatch(html, -1) {
		names[m[1]] = true
	}

	// 这几个标签不需要主动加载：
	//
	//	events   靠用户点"连接"才开始接收
	//	raw      是手动发请求的工具
	//	records  必须先选凭据与区域，"加载记录"按钮才可用 ——
	//	         没有凭据时自动加载只会得到一个空表，那看起来
	//	         和"这个区域确实没有记录"一模一样
	exempt := map[string]bool{
		"events":  true,
		"raw":     true,
		"records": true,
	}

	var missing []string
	for name := range names {
		if exempt[name] {
			continue
		}
		// 路由形式：if (tab === 'xxx') loadXxx();
		if !strings.Contains(app, "tab === '"+name+"'") {
			missing = append(missing, name)
		}
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("这些标签没有加载动作，点进去会是空壳: %v", missing)
	}
}

// TestExternalScriptsExist 验证 HTML 引用的每个脚本都存在。
//
// 少一个文件时浏览器只在控制台打一行 404 —— 用户看到的是"某个按钮
// 点了没反应"，而不会想到是资源没带进二进制。
func TestExternalScriptsExist(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")
	assets := FS()

	for _, m := range scriptSrcRe.FindAllStringSubmatch(html, -1) {
		src := m[1]
		if strings.HasPrefix(src, "http") {
			continue // 外部资源不在我们控制范围内
		}
		if _, err := fs.Stat(assets, src); err != nil {
			t.Errorf("HTML 引用了不存在的脚本 %q: %v", src, err)
		}
	}
}

// TestNoInlineHandlers 验证没有内联事件处理器。
//
// 内联处理器（onclick="..."）会让"打开控制台 CSP"变成一件需要先
// 重构前端的事。现在全部走 addEventListener，这条测试守住它。
func TestNoInlineHandlers(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")

	re := regexp.MustCompile(`\son(click|change|input|submit|load|error)\s*=`)
	if hits := re.FindAllString(html, -1); len(hits) > 0 {
		t.Errorf("HTML 里有内联事件处理器 %v —— 它们会让 CSP 无法开启", hits)
	}
}

// TestElementIDsAreUnique 验证 ID 不重复。
//
// 重复的 ID 不会报错：getElementById 返回第一个，而第二个元素
// 永远拿不到事件绑定 —— 表现是"那个按钮点了没反应"。
func TestElementIDsAreUnique(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")

	re := regexp.MustCompile(`\sid="([^"]+)"`)
	seen := map[string]int{}
	for _, m := range re.FindAllStringSubmatch(html, -1) {
		seen[m[1]]++
	}

	for id, n := range seen {
		if n > 1 {
			t.Errorf("ID %q 出现了 %d 次 —— 重复的 ID 会让后一个元素拿不到事件绑定",
				id, n)
		}
	}
}

// TestReferencedIDsExist 验证 JS 里取的元素在 HTML 里存在。
//
// 这是最容易出错的一类：JS 里写 $('pxSave')，而 HTML 里写的是
// id="pxSave2" —— 结果是 null.onclick 抛异常，整个初始化中断，
// **所有**按钮都失效。而浏览器控制台里那一行错误很容易被忽略。
func TestReferencedIDsExist(t *testing.T) {
	t.Parallel()

	html := readAsset(t, "index.html")

	// 收集 HTML 里定义的全部 ID。
	defined := map[string]bool{}
	for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).
		FindAllStringSubmatch(html, -1) {
		defined[m[1]] = true
	}

	// JS 里通过 $('xxx') 取用的 ID。
	// $ 是本控制台的 getElementById 简写（见 app.js）。
	getterRe := regexp.MustCompile(`\$\('([A-Za-z][A-Za-z0-9_-]*)'\)`)

	for _, file := range []string{"app.js", "panels.js"} {
		js := readAsset(t, file)

		// 有些 ID 是**动态渲染出来的**（表单在渲染时才拼进 DOM），
		// 因此先收集 JS 里定义过的 id="..."。
		dynamic := map[string]bool{}
		for _, m := range regexp.MustCompile(`id="([A-Za-z][A-Za-z0-9_-]*)"`).
			FindAllStringSubmatch(js, -1) {
			dynamic[m[1]] = true
		}
		for _, m := range regexp.MustCompile(`id=\\?"([A-Za-z][A-Za-z0-9_-]*)`).
			FindAllStringSubmatch(js, -1) {
			dynamic[m[1]] = true
		}

		var missing []string
		for _, m := range getterRe.FindAllStringSubmatch(js, -1) {
			id := m[1]
			if defined[id] || dynamic[id] {
				continue
			}
			missing = append(missing, id)
		}

		sort.Strings(missing)
		if len(missing) > 0 {
			t.Errorf("%s 引用了 HTML 与自身都没有定义的 ID: %v"+
				"（症状是 null.onclick 抛异常，而它会让整个初始化中断）",
				file, missing)
		}
	}
}

// TestPanelsUseEscaping 验证渲染用户数据时用了 esc()。
//
// 控制台里显示的是**用户自己的**数据（域名、备注、错误信息），
// 而它们会被拼进 innerHTML。不转义的话，一个域名里的 `<` 就能
// 破坏布局，而更糟的情况是注入脚本。
func TestPanelsUseEscaping(t *testing.T) {
	t.Parallel()

	js := readAsset(t, "panels.js")

	// 找所有拼进 HTML 的表达式里的用户数据字段。
	// 这里只做粗粒度检查：出现这些字段的插值必须带 esc(。
	fields := []string{
		"c.name", "c.url", "c.reason", "c.error",
		"d.channel", "d.kind", "d.error",
		"r.upstream", "s.error",
	}

	for _, f := range fields {
		// 形如 `+ f +` 或 `+ f)` 的直接拼接（没有 esc 包裹）。
		unescaped := strings.Contains(js, "+ "+f+" +") ||
			strings.Contains(js, "+ "+f+" }")
		if unescaped {
			t.Errorf("字段 %s 被直接拼进了 HTML —— 应当用 esc() 包裹", f)
		}
	}
}
