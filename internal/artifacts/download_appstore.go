//go:build appstore

package artifacts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// ErrNoDownloader 表示这份构建不带下载能力。
var ErrNoDownloader = errors.New("this build cannot download artifacts")

// Downloader 在上架版本里只是一个**占位**：它的取回逻辑不参与编译
// （见 download.go 的说明），任何调用都会失败。
//
// # 为什么留一个同名的空类型，而不是让调用方不编译
//
// 调用方（internal/runtime）的 Provision 要在两种构建下都能编译，而它的
// 分支里有一处"没有内置就去下载"。与其把整个 Provision 复制两份、让它们
// 日后各自漂移，不如让这一处**必然失败**：上架版本里内置缺失就该失败，
// 而失败是安全的（ErrNoDownloader），静默成功才是危险的。
//
// 字段与真实实现保持一致，这样调用方不需要为两种构建写两套构造代码。
type Downloader struct {
	Client   *http.Client
	MaxBytes int64
	Progress func(received, total int64)
}

// Download 在上架版本里永远失败。
func (d *Downloader) Download(_ context.Context, rawURL string, _ Digest, _ string) (int64, error) {
	return 0, fmt.Errorf("%w: %s", ErrNoDownloader, rawURL)
}
