package cli

import (
	"strings"
	"testing"
)

// 本文件覆盖新补齐的那几个命令里的**纯逻辑**部分。
//
// # 它们为什么值得单独测
//
// 这些命令的大部分代码是"调接口、打印结果"，而真正会出错的只有两处：
//
//	--field 的解析    值的切分位置
//	表格的截断        按字节还是按字符
//
// 两处错了都不会有编译错误，而症状分别是"凭据看起来填对了却校验失败"
// 与"中文被截成半个字"。

// ---------------------------------------------------------------------------
// --field 解析
// ---------------------------------------------------------------------------

// TestParseFieldsKeepsEqualsInValue 钉住一个很容易写错的地方。
//
// **值里允许出现等号**，因此只能按第一个等号切分。Token 与密钥里出现
// `=` 是常见的（base64 的填充字符就是 =），按最后一个切会把它截断，
// 而症状是"凭据看起来填对了但校验失败"。
func TestParseFieldsKeepsEqualsInValue(t *testing.T) {
	t.Parallel()

	got, err := parseFields([]string{"token=abc=def=="})
	if err != nil {
		t.Fatal(err)
	}
	if got["token"] != "abc=def==" {
		t.Errorf("值 = %q，期望 %q —— 等号被截断了", got["token"], "abc=def==")
	}
}

func TestParseFieldsMultiple(t *testing.T) {
	t.Parallel()

	got, err := parseFields([]string{"id=12345", "secret=abc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应当有 2 个字段，得到 %v", got)
	}
	if got["id"] != "12345" || got["secret"] != "abc" {
		t.Errorf("解析结果 = %v", got)
	}
}

func TestParseFieldsRejectsBadInput(t *testing.T) {
	t.Parallel()

	bad := []string{
		"noequals",
		"=value", // 缺字段名
		"",       // 空
	}
	for _, in := range bad {
		if _, err := parseFields([]string{in}); err == nil {
			t.Errorf("%q 应当被拒绝", in)
		}
	}

	// 一个都没给也要报错 —— 否则会发出一个字段全空的凭据，
	// 而它必然校验失败，用户却不知道是漏了参数。
	if _, err := parseFields(nil); err == nil {
		t.Error("没有字段时应当报错")
	}
}

// TestParseFieldsTrimsNameOnly 钉住"只去字段名的空白"。
//
// 字段名是标识符（来自服务商定义），带空白一定是用户手误；
// 而**值里的空白可能是密钥的一部分**，不能动。
func TestParseFieldsTrimsNameOnly(t *testing.T) {
	t.Parallel()

	got, err := parseFields([]string{" token =abc def "})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["token"]; !ok {
		t.Errorf("字段名应当被去空白，得到 %v", got)
	}
	if got["token"] != "abc def " {
		t.Errorf("值不该被去空白，得到 %q", got["token"])
	}
}

// ---------------------------------------------------------------------------
// 表格截断
// ---------------------------------------------------------------------------

// TestTruncateCountsRunes 钉住按**字符**而不是字节截断。
//
// 中文域名与记录内容按字节截断会出现半个字（UTF-8 的一个汉字占三字节），
// 而终端上显示为乱码。
func TestTruncateCountsRunes(t *testing.T) {
	t.Parallel()

	// 中文：每个字 3 字节。
	cn := "这是一个很长的中文域名示例点例子点公司"
	got := truncate(cn, 10)

	// 结果必须是**合法**的 UTF-8（没有半个字）。
	for i, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("偏移 %d 处出现了替换字符 —— 按字节截断了：%q", i, got)
		}
	}
	// 按字符数算应当正好 10 个。
	if n := len([]rune(got)); n != 10 {
		t.Errorf("截断后 %d 个字符，期望 10：%q", n, got)
	}
	// 被截断了要有省略号。
	if !strings.HasSuffix(got, "…") {
		t.Errorf("截断后应当有省略号：%q", got)
	}
}

func TestTruncateShortStrings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in string
		n  int
	}{
		{"abc", 5},
		{"abc", 3},
		{"", 5},
		{"中文", 5},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.n); got != tc.in {
			t.Errorf("truncate(%q, %d) = %q，不该被改动", tc.in, tc.n, got)
		}
	}
}

func TestTruncateTinyLimit(t *testing.T) {
	t.Parallel()

	// n <= 1 时不该 panic，也不该返回空串加省略号这种怪东西。
	for _, n := range []int{0, 1} {
		got := truncate("abcdef", n)
		if len([]rune(got)) > 1 && n == 1 {
			t.Errorf("truncate(_, 1) = %q，应当只有 1 个字符", got)
		}
	}
}

// ---------------------------------------------------------------------------
// 校验结果的展示
// ---------------------------------------------------------------------------

// TestFirstLine 验证列表里只显示校验失败说明的第一行。
//
// humanizeVerifyError 会追加一段"可能是权限不足"，那是多行的；
// 而列表里塞下整段会把表格挤歪。完整说明在 verify 子命令里。
func TestFirstLine(t *testing.T) {
	t.Parallel()

	multi := "第一行\n第二行\n第三行"
	if got := firstLine(multi); got != "第一行" {
		t.Errorf("firstLine = %q，期望 %q", got, "第一行")
	}
	if got := firstLine("只有一行"); got != "只有一行" {
		t.Errorf("firstLine = %q", got)
	}
	if got := firstLine(""); got != "" {
		t.Errorf("空串应当返回空串，得到 %q", got)
	}
	// 末尾的换行不该产生一个空的第一行。
	if got := firstLine("\n第二行"); got != "" {
		t.Errorf("以换行开头时第一行是空的，得到 %q", got)
	}
}
