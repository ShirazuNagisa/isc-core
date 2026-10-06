//go:build !appstore

package runtime

import (
	"context"
	"testing"
)

// 能下载的构建里，清单里有的运行时都算可用 —— 来源标成 download，
// 因为那份可用性**依赖网络**。上架版本没有这个来源，见
// availability_appstore_test.go。
func TestAvailabilityReportsDownloadWhenTheBuildCanFetch(t *testing.T) {
	m, _ := testManager(t, nil)
	got := m.Availability(context.Background(), KindNode, "")
	if !got.Available || got.Source != "download" {
		t.Fatalf("能下载的构建应当报可用（source=download），得到 %+v", got)
	}
}
