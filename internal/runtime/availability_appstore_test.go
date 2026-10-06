//go:build appstore

package runtime

import (
	"context"
	"testing"
)

// 上架构建里，**包里没有的运行时必须报不可用**，而不是含糊地说"能装"。
//
// 这条守的是用户看到错误信息的时机：如果这里报可用，界面就会让用户一路
// 填完建站表单，直到部署中途才失败 —— 而那时错误说的是"这个运行时没被
// 打进包里"，与他在界面上做的事看不出关系。
func TestAppStoreBuildReportsBundledRuntimesOnly(t *testing.T) {
	m, _ := testManager(t, nil)
	got := m.Availability(context.Background(), KindNode, "")
	if got.Available {
		t.Fatalf("上架构建里包里没有的运行时不该报可用：%+v", got)
	}
	if got.Reason == "" {
		t.Fatal("不可用时必须给出原因，界面要显示它")
	}
}
