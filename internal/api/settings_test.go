package api

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/settings"
)

// toGenSettings 必须映射**每一个**字段。
//
// # 这条测试挡的是一类反复发生的 bug
//
// 给 `settings.Settings` 加了字段、却忘了往 `toGenSettings` 里加一行，
// 后果是：写入正常、接口返回 200、而**读回来是空的** ——
// 用户看到的是"保存之后开关弹回去了"。
//
// 这个仓库里同类问题已经发生过三次（`UpdateSettings` 的注释里记着
// 两次是写入方向，因此它改成了直接解码进 `settings.Patch`；
// 第三次是公网访问的两个字段，在读取方向）。
//
// # 按 JSON tag 匹配，而不是按 Go 字段名
//
// 两边的字段名**故意**不同：内部用 `ProxyTLS` / `ACMEEmail`（缩写按
// Go 惯例全大写），契约里是 `ProxyTls` / `AcmeEmail`（按 JSON 惯例）。
// 契约的对应关系是 tag，因此判据也必须是 tag —— 按名字比会得到一堆
// 假失败，而假失败会让人把这条测试删掉。
func TestToGenSettingsMapsEveryField(t *testing.T) {
	t.Parallel()

	in := settings.Settings{}
	fillNonZero(t, &in)
	out := toGenSettings(in)

	// 契约里刻意**没有**的字段。每一条都要有理由 —— 否则"例外表"
	// 会变成一个可以随便塞东西的地方，而这条测试就失去意义了。
	excluded := map[string]string{
		"remote_enabled":               "远程监听自己的开关走 /v1/remote/settings（RemoteSettingsPatch），因为它与端口是一起提交的：分两次会让刚开的监听在瞬间用一个用户已经改过的端口",
		"remote_port":                  "同上",
		"remote_notifications_enabled": "推送开关随设备设置走，见 /v1/remote/settings",
	}

	// 按 tag 收集**输出值**。
	//
	// 只检查"契约类型里有这个字段"是不够的 —— 那在删掉一行赋值之后
	// 依然成立（类型定义还在），于是测试照样通过。真正要断言的是
	// "这一行赋值确实发生了"，而那只能看输出值本身。
	outByTag := map[string]reflect.Value{}
	outValue := reflect.ValueOf(out)
	outType := outValue.Type()
	for i := 0; i < outType.NumField(); i++ {
		if tag := jsonName(outType.Field(i)); tag != "" {
			outByTag[tag] = outValue.Field(i)
		}
	}

	present := map[string]bool{}
	for tag := range outByTag {
		present[tag] = true
	}

	inType := reflect.TypeOf(in)
	missing := 0
	for i := 0; i < inType.NumField(); i++ {
		tag := jsonName(inType.Field(i))
		if tag == "" {
			continue
		}
		if why, ok := excluded[tag]; ok {
			if why == "" {
				t.Errorf("%s 在例外表里但没有理由", tag)
			}
			continue
		}

		field, ok := outByTag[tag]
		if !ok {
			missing++
			t.Errorf("settings.Settings 的 %q 在契约里没有对应字段。\n"+
				"（如果它刻意不在通用设置契约里，把它加进 excluded 并**写清理由**）", tag)
			continue
		}
		// **这一条才是真正抓住 bug 的。**
		//
		// 契约里有字段、但 toGenSettings 忘了赋值 → 指针是 nil →
		// 客户端读回来是空的，而写入明明成功了。
		if field.Kind() == reflect.Ptr && field.IsNil() {
			missing++
			t.Errorf("settings.Settings 的 %q 没有映射到输出里 —— 读回来会是空的。\n"+
				"写入会成功、接口会返回 200，而用户看到的是「保存之后开关弹回去了」。", tag)
		}
	}
	if missing > 0 {
		t.Logf("契约里现有的字段：%v", keysOf(present))
	}
}

// jsonName 取字段的 JSON 名（去掉 ,omitempty 之类）。
func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "" || tag == "-" {
		return ""
	}
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fillNonZero 用反射给结构体的每个字段填一个非零值。
func fillNonZero(t *testing.T, target any) {
	t.Helper()
	v := reflect.ValueOf(target).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.CanSet() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Int, reflect.Int64:
			f.SetInt(1)
		}
	}
}
