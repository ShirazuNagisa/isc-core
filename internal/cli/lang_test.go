package cli

import (
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// TestFlagValue 覆盖那个"在 cobra 之前"的参数解析器。
//
// # 它为什么必须存在
//
// 语言要在**构造命令树之前**定好（Short / Long / 标志说明在 New() 里就
// 被求值了），而 cobra 处理 --help 又发生在 PersistentPreRunE 之前 ——
// 所以那一步只能自己扫 os.Args。
//
// 代价是这里有一份**独立于 cobra 的解析逻辑**。两份解析必须对同一种
// 写法给出一致的答案，否则会出现"`isc --lang en` 能用、`isc --lang=en`
// 却不生效"这种极难归因的问题。
func TestFlagValue(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		// 两种写法都必须认 —— cobra 两种都认。
		{"空格形式", []string{"--lang", "en"}, "en"},
		{"等号形式", []string{"--lang=en"}, "en"},
		{"等号形式带空值", []string{"--lang="}, ""},

		// 混在其它参数里。
		{"前面有别的参数", []string{"credential", "list", "--lang", "en"}, "en"},
		{"后面有别的参数", []string{"--lang", "en", "credential"}, "en"},
		{"等号形式在中间", []string{"credential", "--lang=en", "list"}, "en"},

		// 子命令自己的同名标志不该被误取。
		{"取第一个匹配", []string{"--lang", "en", "--lang", "zh-CN"}, "en"},

		// 不存在时返回空串。
		{"没有该标志", []string{"credential", "list"}, ""},
		{"空参数", nil, ""},

		// **值不能与标志名混淆**。
		//
		// `--lang --json` 里 `--json` 是下一个标志而不是 lang 的值。
		// 这里不特判：cobra 会把 "--json" 当成 lang 的值并随后报错，
		// 而提前解析只要不 panic、不取到别的标志名就够了。
		{"值看起来像标志", []string{"--lang", "--json"}, "--json"},

		// 前缀不能误匹配。
		{"langx 不该匹配 lang", []string{"--langx", "en"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flagValue(tc.args, "lang"); got != tc.want {
				t.Errorf("flagValue(%v, lang) = %q，期望 %q",
					tc.args, got, tc.want)
			}
		})
	}
}

// TestHelpTextFollowsAmbientLanguage 钉住那条**顺序**约束。
//
// # 它防的是什么
//
// Short / Long / 标志说明都是 `i18n.T(...)` 的结果，在构造命令树时就被
// 求值了。于是"先建树、再设语言"会让帮助文本永远停在旧语言 —— 而
// 命令行输出却是对的，症状极具迷惑性。
//
// 真机上就是这样发现的：`isc --lang en credential add --help` 的输出
// 已经是英文，帮助正文却还是中文。
//
// 这条测试把"语言必须先设、树必须后建"这个顺序固定下来：它**先**设
// 语言，**再**建树，然后断言帮助文本跟着变。
func TestHelpTextFollowsAmbientLanguage(t *testing.T) {
	// 不用 t.Parallel：它改的是全局默认语言。
	prev := i18n.Default
	t.Cleanup(func() { i18n.SetDefault(prev) })

	i18n.SetDefault(i18n.En)
	root := New()

	// 根命令自己的说明。
	if strings.Contains(root.Short, "电脑") {
		t.Errorf("根命令的 Short 仍是中文：%q", root.Short)
	}

	// 子命令与更深一层的。
	cred := findSubcommand(root, "credential")
	if cred == nil {
		t.Fatal("找不到 credential 子命令")
	}
	if strings.Contains(cred.Short, "凭据") {
		t.Errorf("credential 的 Short 仍是中文：%q", cred.Short)
	}

	add := findSubcommand(cred, "add")
	if add == nil {
		t.Fatal("找不到 credential add 子命令")
	}
	if strings.Contains(add.Long, "字段") {
		t.Errorf("credential add 的 Long 仍是中文：%.40q", add.Long)
	}

	// 标志说明也要跟着变 —— 它同样在构造时求值。
	if f := add.Flags().Lookup("label"); f != nil {
		if strings.Contains(f.Usage, "凭据") {
			t.Errorf("--label 的说明仍是中文：%q", f.Usage)
		}
	}
}

// TestHelpTextDefaultsToChinese 确认上一条不是"永远英文"。
func TestHelpTextDefaultsToChinese(t *testing.T) {
	prev := i18n.Default
	t.Cleanup(func() { i18n.SetDefault(prev) })

	i18n.SetDefault(i18n.ZhCN)
	root := New()

	if !strings.Contains(root.Short, "电脑") {
		t.Errorf("默认语言下根命令的 Short 应当是中文：%q", root.Short)
	}
}
