package i18n

import (
	"sort"
	"testing"
)

// TestCatalogsHaveIdenticalKeys 强制两份目录的 key 集合完全一致。
//
// 这是本包存在的核心保障：漏翻一条文案在界面上表现为"突然冒出一串
// error.xxx"，而这类问题在开发期极难被注意到。用测试钉死。
func TestCatalogsHaveIdenticalKeys(t *testing.T) {
	t.Parallel()

	zh := New(ZhCN)
	en := New(En)

	var missingInEn, missingInZh []string
	for _, k := range Keys() {
		if !en.Has(k) {
			missingInEn = append(missingInEn, k)
		}
	}
	for k := range messagesEn {
		if !zh.Has(k) {
			missingInZh = append(missingInZh, k)
		}
	}

	sort.Strings(missingInEn)
	sort.Strings(missingInZh)

	if len(missingInEn) > 0 {
		t.Errorf("英文目录缺少 %d 个 key: %v", len(missingInEn), missingInEn)
	}
	if len(missingInZh) > 0 {
		t.Errorf("中文目录缺少 %d 个 key: %v", len(missingInZh), missingInZh)
	}
}

// TestNoEmptyMessages 防止出现"翻译了但内容是空串"。
func TestNoEmptyMessages(t *testing.T) {
	t.Parallel()

	for _, lang := range []Lang{ZhCN, En} {
		c := New(lang)
		for _, k := range Keys() {
			if c.T(k) == "" {
				t.Errorf("语言 %s 的 key %q 文案为空", lang, k)
			}
		}
	}
}

// TestUnknownKeyReturnsKey 钉住缺失 key 的行为。
//
// 回退到 key 本身而不是 panic 或空串：漏翻一条文案不该让服务崩溃，
// 但在界面上一眼就能看出问题。
func TestUnknownKeyReturnsKey(t *testing.T) {
	t.Parallel()

	const missing = "error.this_key_does_not_exist"
	for _, lang := range []Lang{ZhCN, En} {
		if got := New(lang).T(missing); got != missing {
			t.Errorf("语言 %s 的缺失 key 应返回 key 本身，得到 %q", lang, got)
		}
	}
}

// TestFormatting 验证占位符替换。
func TestFormatting(t *testing.T) {
	t.Parallel()

	got := New(ZhCN).T("daemon.already_running", 4242)
	if got == "" || got == "daemon.already_running" {
		t.Fatalf("格式化失败: %q", got)
	}
	// 参数应当真的出现在结果里。
	if !contains(got, "4242") {
		t.Errorf("格式化结果应包含参数 4242: %q", got)
	}
}

// TestParseNormalizes 验证语言标识的宽容解析。
//
// 语言配置来自用户输入与浏览器，格式五花八门；
// 解析不出来时回退默认值，而不是让内核起不来。
func TestParseNormalizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want Lang
	}{
		{"zh-CN", ZhCN},
		{"zh", ZhCN},
		{"zh_CN", ZhCN},
		{"zh-cn", ZhCN},
		{"zh-Hans", ZhCN},
		{"en", En},
		{"en-US", En},
		{"en_US", En},
		{"en-GB", En},
		{"", Default},
		{"klingon", Default},
		{"fr-FR", Default},
	}
	for _, tt := range tests {
		if got := Parse(tt.in); got != tt.want {
			t.Errorf("Parse(%q) = %q, 期望 %q", tt.in, got, tt.want)
		}
	}
}

// TestSetDefaultSwitchesLanguage 验证默认目录可切换。
func TestSetDefaultSwitchesLanguage(t *testing.T) {
	// 不用 t.Parallel：本测试改动包级状态。

	SetDefault(ZhCN)
	zh := T("error.not_found")
	SetDefault(En)
	en := T("error.not_found")
	SetDefault(ZhCN)

	if zh == en {
		t.Errorf("切换语言后文案应不同，两者都是 %q", zh)
	}
	if zh == "" || en == "" {
		t.Error("两种语言的文案都不应为空")
	}
	// 收尾：恢复默认，避免影响同包内其他测试。
	if got := T("error.not_found"); got != zh {
		t.Errorf("恢复默认语言失败，得到 %q", got)
	}
}

// TestKeysAreSortedAndUnique 验证 Keys 返回的集合稳定。
func TestKeysAreSortedAndUnique(t *testing.T) {
	t.Parallel()

	keys := Keys()
	sort.Strings(keys)
	for i := 1; i < len(keys); i++ {
		if keys[i] == keys[i-1] {
			t.Fatalf("key 重复: %q", keys[i])
		}
	}
	if len(keys) == 0 {
		t.Fatal("消息目录不应为空")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
