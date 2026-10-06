//go:build appstore

package runtime

import (
	"context"
	"errors"
	"testing"
)

// 上架构建里，包内没有的运行时必须报"没被打进包里"，而不是别的。
//
// 这条测试守的是一个**错误信息的可操作性**：用户看到"这份构建不能下载"
// 会以为要换个构建方式；看到"这个运行时没被打进包里"才知道该往包里加。
// 两者都对，但只有后者指向他真正要做的动作。
//
// 它同时钉住了"上架版本不会去下载"：appstore 标签下 Downloader 是个必然
// 失败的替身，所以任何走到取回的路径都会失败 —— 而失败是安全的，静默
// 成功才是危险的。
func TestAppStoreBuildReportsMissingBundleNotDownloadFailure(t *testing.T) {
	m, _ := testManager(t, nil)
	// 不声明任何内置目录：包内可用的那份不存在。
	_, err := m.Provision(context.Background(), KindNode, "", nil)
	if err == nil {
		t.Fatal("包内没有的运行时不该装成功")
	}
	if !errors.Is(err, ErrNotBundled) {
		t.Fatalf("期望 ErrNotBundled（构建配置问题，重试无用），得到 %v", err)
	}
}

// 上架构建不该被认为有下载能力。
func TestAppStoreBuildReportsItCannotDownload(t *testing.T) {
	m, _ := testManager(t, nil)
	if m.CanDownload() {
		t.Fatal("appstore 构建报自己可以下载 —— 那份二进制不该有这个能力")
	}
}
