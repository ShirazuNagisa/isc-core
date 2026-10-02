package i18n

import (
	"context"
	"testing"
)

// TestFromHeader 覆盖 Accept-Language 的解析。
//
// # 它为什么值得测
//
// 这个函数决定"客户端看到的语言"，而它的输入是**外部字符串** ——
// 浏览器与 curl 会送来各种形态。解析错了不会有编译错误，症状是
// "界面语言不对"，而那种问题很难归因到这一行。
func TestFromHeader(t *testing.T) {
	t.Parallel()

	cases := []struct {
		header string
		want   Lang
		ok     bool
	}{
		// 基本形态。
		{"en", En, true},
		{"zh-CN", ZhCN, true},

		// 带地区的英文。
		{"en-US", En, true},
		{"en-GB", En, true},

		// 中文的几种写法都归到 zh-CN。
		//
		// 本产品只提供 zh-CN 一种中文，把 zh-TW 当成 zh-CN
		// 比给它英文更合理（繁体用户读简体远比读英文轻松）。
		{"zh", ZhCN, true},
		{"zh-TW", ZhCN, true},
		{"zh-Hans", ZhCN, true},

		// 大小写不敏感。
		{"EN", En, true},
		{"ZH-cn", ZhCN, true},

		// 多语言列表：取第一个。
		//
		// 按协议客户端有义务按权重排好序，因此不必自己实现 q 值排序 ——
		// 自己实现只会引入解析 bug。
		{"zh-CN,zh;q=0.9,en;q=0.8", ZhCN, true},
		{"en-US,en;q=0.9", En, true},

		// 不支持的语言 → 交给调用方回退到全局默认值。
		{"fr", "", false},
		{"de-DE", "", false},
		{"ja", "", false},

		// 通配 → 同样回退。
		{"*", "", false},

		// 空与空白。
		{"", "", false},
		{"   ", "", false},
		{",", "", false},
		{";q=0.9", "", false},
	}

	for _, tc := range cases {
		got := FromHeader(tc.header)
		if !tc.ok {
			if got != nil {
				t.Errorf("FromHeader(%q) 应当返回 nil（不支持），得到 %v",
					tc.header, got.Lang())
			}
			continue
		}
		if got == nil {
			t.Errorf("FromHeader(%q) 应当识别，得到 nil", tc.header)
			continue
		}
		if got.Lang() != tc.want {
			t.Errorf("FromHeader(%q) = %q，期望 %q",
				tc.header, got.Lang(), tc.want)
		}
	}
}

// TestFromHeaderFirstSegmentWins 钉住"取第一个"这条规则的一个易错点。
//
// `en;q=0.1,zh;q=0.9` 里中文权重更高，而按协议客户端**不该**这样发 ——
// 它必须自己排好序。我们只取第一段，而这个测试把那个取舍写下来：
// 如果有人将来想"聪明地"按 q 值排序，他会先看到这条注释和这个用例。
func TestFromHeaderFirstSegmentWins(t *testing.T) {
	t.Parallel()

	got := FromHeader("en;q=0.1,zh;q=0.9")
	if got == nil {
		t.Fatal("应当识别出 en")
	}
	if got.Lang() != En {
		t.Errorf("按实现取第一段，应当是 en，得到 %q", got.Lang())
	}
}

// TestFromContextFallsBackToDefault 钉住"永不返回 nil"。
//
// 调用方直接 `.T(...)` 而不判空 —— 少一个判空就少一处
// "忘了判空导致 panic"的可能。
func TestFromContextFallsBackToDefault(t *testing.T) {
	t.Parallel()

	// 没有目录的 context。
	if got := FromContext(context.Background()); got == nil {
		t.Fatal("没有目录时应当回退到默认，而不是返回 nil")
	}

	// 空的 context 值也不该 panic。
	//nolint:staticcheck // 故意传 nil 验边界
	if got := FromContext(nil); got == nil {
		t.Fatal("nil context 也不该返回 nil")
	}
}

// TestWithCatalogRoundTrip 验证放入与取出。
func TestWithCatalogRoundTrip(t *testing.T) {
	t.Parallel()

	en := New(En)
	ctx := WithCatalog(context.Background(), en)

	got := FromContext(ctx)
	if got.Lang() != En {
		t.Errorf("取出的目录语言是 %q，期望 en", got.Lang())
	}
}

// TestWithCatalogIgnoresNil 钉住一个防崩溃的细节。
//
// 传 nil 目录时不设置 —— 否则 FromContext 会取到一个 nil 的接口值，
// 而"取到了"与"取到 nil"在类型断言里是两回事。
func TestWithCatalogIgnoresNil(t *testing.T) {
	t.Parallel()

	ctx := WithCatalog(context.Background(), nil)
	if got := FromContext(ctx); got == nil {
		t.Fatal("放入 nil 之后 FromContext 返回了 nil")
	}
}

// TestWithCatalogDoesNotAffectGlobal 钉住最重要的一条：**隔离**。
//
// 接口是并发的：两个客户端可以同时用不同语言请求。如果 per-request
// 的语言泄漏到全局，它们会互相覆盖 —— 而那种缺陷只在并发时出现，
// 是最难复现的一类。
func TestWithCatalogDoesNotAffectGlobal(t *testing.T) {
	t.Parallel()

	// 全局默认是 zh-CN（Default）。
	SetDefault(ZhCN)
	defer SetDefault(ZhCN)

	ctx := WithCatalog(context.Background(), New(En))

	// 这个 context 里是英文。
	if got := FromContext(ctx).T("error.not_found"); got == "请求的资源不存在" {
		t.Error("context 里的目录应当是英文")
	}

	// 但全局仍然是中文 —— 没有泄漏。
	if got := T("error.not_found"); got != "请求的资源不存在" {
		t.Errorf("全局默认被 context 污染了：%q", got)
	}
}
