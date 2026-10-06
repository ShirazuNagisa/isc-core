//go:build appstore

package artifacts

import (
	"context"
	"errors"
	"testing"
)

// 上架构建里取回**必须失败**，而且要失败得能被识别。
//
// 这条测试守的是那份二进制的性质：App Review 2.5.2 禁止应用下载并执行
// 代码，因此 appstore 构建里连取回逻辑都不参与编译。留一个必然失败的
// 替身是为了让调用方（internal/runtime 的 Provision）在两种构建下都能
// 编译，而不是让整个 Provision 复制两份、日后各自漂移。
//
// 失败是安全的；静默成功才是危险的 —— 那意味着上架版本真的会去下载。
func TestAppStoreBuildCannotDownload(t *testing.T) {
	d := &Downloader{}
	if _, err := d.Download(context.Background(), "https://example.com/x.tar.gz", Digest{}, t.TempDir()+"/x"); err == nil {
		t.Fatal("上架构建不该能取回产物")
	} else if !errors.Is(err, ErrNoDownloader) {
		t.Fatalf("应当报 ErrNoDownloader，得到 %v", err)
	}
}

// 校验与解压在两种构建里都必须还在 —— 包内预置的运行时走的是同一条
// "校验 → 解压 → 落位"链路。
func TestAppStoreBuildKeepsVerification(t *testing.T) {
	if _, err := ParseDigest("sha256:" + repeatHex()); err != nil {
		t.Fatalf("上架构建里摘要解析不该消失：%v", err)
	}
}

func repeatHex() string {
	const one = "0123456789abcdef"
	out := make([]byte, 0, 64)
	for len(out) < 64 {
		out = append(out, one...)
	}
	return string(out[:64])
}
