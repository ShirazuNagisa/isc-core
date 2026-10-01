package credential

import (
	"errors"
	"testing"
)

var testSpecs = []FieldSpec{
	{Key: "access_key_id", LabelKey: "provider.field.access_key_id", Required: true},
	{Key: "access_key_secret", LabelKey: "provider.field.access_key_secret", Required: true, Secret: true},
	{Key: "ext_param", LabelKey: "provider.field.ext_param"},
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cred    Credential
		wantErr error
	}{
		{
			name: "合法",
			cred: Credential{Provider: "alidns", Label: "主账号",
				Fields: map[string]string{"access_key_id": "id", "access_key_secret": "sk"}},
		},
		{
			name:    "缺少服务商",
			cred:    Credential{Label: "x", Fields: map[string]string{}},
			wantErr: ErrProviderEmpty,
		},
		{
			name:    "标签为空",
			cred:    Credential{Provider: "alidns", Fields: map[string]string{}},
			wantErr: ErrLabelEmpty,
		},
		{
			name: "标签只有空白字符",
			cred: Credential{Provider: "alidns", Label: "   ",
				Fields: map[string]string{}},
			wantErr: ErrLabelEmpty,
		},
		{
			name: "缺少必填字段",
			cred: Credential{Provider: "alidns", Label: "x",
				Fields: map[string]string{"access_key_id": "id"}},
			wantErr: ErrMissingField,
		},
		{
			name: "必填字段只有空白",
			cred: Credential{Provider: "alidns", Label: "x",
				Fields: map[string]string{"access_key_id": "id", "access_key_secret": "  "}},
			wantErr: ErrMissingField,
		},
		{
			name: "出现未声明的字段",
			cred: Credential{Provider: "alidns", Label: "x",
				Fields: map[string]string{
					"access_key_id": "id", "access_key_secret": "sk", "typo_field": "v",
				}},
			wantErr: ErrUnknownField,
		},
		{
			name: "可选字段留空是合法的",
			cred: Credential{Provider: "alidns", Label: "x",
				Fields: map[string]string{
					"access_key_id": "id", "access_key_secret": "sk", "ext_param": "",
				}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cred.Validate(testSpecs)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("期望通过校验，得到: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("期望 %v，得到 %v", tt.wantErr, err)
			}
		})
	}
}

// TestMergeKeepsMaskedValues 钉住"原样回传掩码即保留原值"这条规则。
//
// 没有它，界面上任何一次编辑（哪怕只是改个标签）都会把密钥
// 静默改成八个星号，而下一次解析失败时用户完全不知道发生了什么。
func TestMergeKeepsMaskedValues(t *testing.T) {
	t.Parallel()

	existing := map[string]string{
		"access_key_id":     "LTAI-real-id",
		"access_key_secret": "real-secret-value",
	}

	incoming := map[string]string{
		"access_key_id":     "LTAI-renamed-id", // 非敏感字段，正常更新
		"access_key_secret": Mask,              // 敏感字段回传掩码 → 保留原值
	}
	got := Merge(existing, incoming)

	if got["access_key_id"] != "LTAI-renamed-id" {
		t.Errorf("非敏感字段应被更新，得到 %q", got["access_key_id"])
	}
	if got["access_key_secret"] != "real-secret-value" {
		t.Errorf("敏感字段回传掩码时应保留原值，得到 %q", got["access_key_secret"])
	}
}

func TestMergeAppliesNewSecret(t *testing.T) {
	t.Parallel()

	existing := map[string]string{"access_key_secret": "old"}
	got := Merge(existing, map[string]string{"access_key_secret": "new-secret"})

	if got["access_key_secret"] != "new-secret" {
		t.Errorf("提交新值时应更新，得到 %q", got["access_key_secret"])
	}
}

// TestMergeEmptyClears 钉住空串的语义。
//
// 空串表示"清空"，掩码表示"保留"。二者语义清晰，不需要引入
// 三态类型（那会让 OpenAPI 契约变得别扭）。
func TestMergeEmptyClears(t *testing.T) {
	t.Parallel()

	existing := map[string]string{"ext_param": "team_123"}
	got := Merge(existing, map[string]string{"ext_param": ""})

	if got["ext_param"] != "" {
		t.Errorf("空串应清空字段，得到 %q", got["ext_param"])
	}
}

func TestMergeDoesNotMutateInputs(t *testing.T) {
	t.Parallel()

	existing := map[string]string{"a": "1"}
	incoming := map[string]string{"b": "2"}
	_ = Merge(existing, incoming)

	if len(existing) != 1 || len(incoming) != 1 {
		t.Error("Merge 不应修改入参 map")
	}
}

// TestMergeTrimsWhitespace 验证粘贴密钥时的常见问题被自动处理。
func TestMergeTrimsWhitespace(t *testing.T) {
	t.Parallel()

	got := Merge(nil, map[string]string{"secret": "  token-with-spaces  "})
	if got["secret"] != "token-with-spaces" {
		t.Errorf("应当去掉首尾空白，得到 %q", got["secret"])
	}
}

// TestMasked 覆盖对外输出。
func TestMasked(t *testing.T) {
	t.Parallel()

	fields := map[string]string{
		"access_key_id":     "LTAI-public-id",
		"access_key_secret": "super-secret",
		"ext_param":         "",
	}
	got := Masked(fields, testSpecs)

	if got["access_key_secret"] != Mask {
		t.Errorf("敏感字段应被掩码，得到 %q", got["access_key_secret"])
	}
	if got["access_key_id"] != "LTAI-public-id" {
		t.Errorf("非敏感字段应原样返回，得到 %q", got["access_key_id"])
	}
	// 空字段直接省略，让界面能区分"没填"与"填了但看不到"。
	if _, present := got["ext_param"]; present {
		t.Error("空字段应当被省略")
	}
	// 掩码本身不得泄露原文的任何片段。
	if len(got["access_key_secret"]) >= len("super-secret") {
		t.Error("掩码长度不应接近原文，那会泄露长度信息")
	}
}

// TestMaskedNeverLeaks 是一条兜底检查：无论输入是什么，
// 掩码结果里都不该出现原文。
func TestMaskedNeverLeaks(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"a", "ab", "abcdefgh", "a-very-long-api-token-value-1234567890",
		"中文密钥", "with spaces",
	}
	for _, s := range secrets {
		got := Masked(map[string]string{"access_key_secret": s}, testSpecs)
		if got["access_key_secret"] != Mask {
			t.Errorf("密钥 %q 的掩码结果 = %q，应当是固定的占位值", s, got["access_key_secret"])
		}
	}
}

func TestFieldKeysPreservesOrder(t *testing.T) {
	t.Parallel()

	got := FieldKeys(testSpecs)
	want := []string{"access_key_id", "access_key_secret", "ext_param"}
	if len(got) != len(want) {
		t.Fatalf("字段数量 = %d, 期望 %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个字段 = %q, 期望 %q", i, got[i], want[i])
		}
	}
}
