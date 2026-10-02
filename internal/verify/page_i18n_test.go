package verify

import (
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件测的是**发给手机的验证页**。
//
// 它是整个项目里唯一会被**外部设备**看到的界面 —— 用户在移动网络上打开它，
// 而他要据此判断"到底通没通"。因此有三条不能退化：
//
//   - 结论必须在第一屏（headline 与 body 都得在）；
//   - 页面语言跟随**内核**设置，而不是手机的 Accept-Language；
//   - 页面里不能出现任何关于本机的内部信息（版本、路径、内部地址）。

// TestPageFollowsKernelLanguage 钉住 `lang` 与 `<title>` 跟随内核语言。
//
// 这条测试存在的原因很具体：`pageHead` 原本是**常量**，里面写死了
// `<html lang="zh-CN">` 与中文标题。迁移到 i18n 时编译器当场拦下
// （常量里放不了函数调用），它才变成函数。没有这条测试，将来有人
// 图省事把它改回常量，页面就会在英文设置下退回中文 —— 而那**不会报错**。
func TestPageFollowsKernelLanguage(t *testing.T) {
	// 不能用 t.Parallel：它改的是包级默认语言。
	defer i18n.SetDefault(i18n.ZhCN)

	for _, tc := range []struct {
		lang    i18n.Lang
		wantTag string
		wantIn  string
		notWant string
	}{
		{i18n.ZhCN, `lang="zh-CN"`, "链路是通的", "The path is open"},
		{i18n.En, `lang="en"`, "The path is open", "链路是通的"},
	} {
		i18n.SetDefault(tc.lang)

		page := renderPage(SourcePublic, &Session{})

		if !strings.Contains(page, tc.wantTag) {
			t.Errorf("%v: 页面里应当有 %s —— "+
				"拿手机的是同一个用户，语言要跟随内核而不是手机",
				tc.lang, tc.wantTag)
		}
		if !strings.Contains(page, tc.wantIn) {
			t.Errorf("%v: 页面里应当含 %q", tc.lang, tc.wantIn)
		}
		if strings.Contains(page, tc.notWant) {
			t.Errorf("%v: 页面里不该出现 %q（那是另一种语言的文案）",
				tc.lang, tc.notWant)
		}
	}
}

// TestPageLeaksNothingAboutThisMachine 确认页面不含内部信息。
//
// 它是唯一会被**外部设备**看到的输出 —— 一个版本号加上一个 CVE，
// 比多写一行字危险得多。
func TestPageLeaksNothingAboutThisMachine(t *testing.T) {
	t.Parallel()

	page := renderPage(SourcePublic, &Session{})

	// 这些词一个都不该出现在发给外部的页面上。
	for _, banned := range []string{
		"ISC-Core", "go1.", "commit", "v0.", "/internal/", "runtime.json",
	} {
		if strings.Contains(page, banned) {
			t.Errorf("验证页里出现了 %q —— 它是发给外部设备的，"+
				"不该泄漏本机的版本或路径信息", banned)
		}
	}
}

// TestPageStatesTheConclusionFirst 确认结论在页面里，且两种结论不同。
func TestPageStatesTheConclusionFirst(t *testing.T) {
	t.Parallel()

	ok := renderPage(SourcePublic, &Session{})
	bad := renderPage(SourceLoopback, &Session{})

	if !strings.Contains(ok, i18n.T("verify.page.headline_ok")) {
		t.Error("公网来源的页面里应当给出正面结论")
	}
	if !strings.Contains(bad, i18n.T("verify.page.headline_fail")) {
		t.Error("回环来源的页面里应当说明这次访问不算数")
	}
	if ok == bad {
		t.Error("两种来源的页面不该完全一样 —— 那意味着结论没有随判定变化")
	}
}
