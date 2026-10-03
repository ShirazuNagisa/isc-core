// Package testsupport 放跨包共用的测试辅助。
//
// 它只被 _test.go 引用：这里的代码不进任何发布产物。
package testsupport

import (
	"os"
	"testing"
)

// ShortTempDir 返回一个**路径足够短**的临时目录。
//
// # 为什么不能直接用 t.TempDir()
//
// Unix 域套接字用文件系统路径寻址，而路径长度有硬上限 —— 来自
// sockaddr_un.sun_path：
//
//	macOS  104 字节（含结尾的 NUL）
//	Linux  108 字节
//
// 而 `t.TempDir()` 在 macOS 上落在 `/var/folders/xx/…/T/` 下：光是那个
// 前缀就有 60 来个字符，再加一个稍长的测试名与 `/001/run/isc.sock`，
// 很容易越过 104。
//
// 症状是灾难性的**误导**：`bind: invalid argument`。它看起来像内核
// 或者平台后端坏了，而真正的原因只是"临时目录名字太长"。实测中
// macOS 上所有以 t.TempDir() 当数据目录的守护进程端到端测试都因此失败。
//
// 因此这里建在 /tmp 下（macOS 上是 /private/tmp 的符号链接，路径短得多）。
// /tmp 不可写时退回 t.TempDir()：那时测试仍然能跑，只是可能撞上长度上限。
func ShortTempDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "isc-")
	if err != nil {
		return t.TempDir()
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) //nolint:errcheck // 测试清理
	return dir
}
