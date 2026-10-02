package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 本文件测 .pkg 生成里**与平台无关**的部分。
//
// 真正的打包（pkgbuild）只能在 macOS 上跑，因此那部分由 CI 的
// release-pkg job 验证。这里守住的是纯逻辑 —— 而它恰恰是最容易错、
// 也最值得在**任何平台**上都能测的部分（与 service_def.go 同一个取舍）。

// TestPkgVersionAcceptsMacOSShapes 钉住"哪些版本号是合法的"。
//
// 与 MSI 的规则**刻意不同**：macOS 的版本号可以带字母（`0.2.0-rc1` 是
// 合法的），只要求首字符是数字。按 MSI 那套三段数字去校验，会把
// 合法的 macOS 版本号也拒掉 —— 而拒掉的表现是"打不出包"，
// 排查起来会往错的方向走。
func TestPkgVersionAcceptsMacOSShapes(t *testing.T) {
	t.Parallel()

	ok := []struct{ in, want string }{
		{"0.1.0", "0.1.0"},
		{"v0.1.0", "0.1.0"},
		{"0.2.0-rc1", "0.2.0-rc1"}, // macOS 允许，MSI 不允许
		{"0.1.0-dirty", "0.1.0-dirty"},
		{"0.1.0+meta", "0.1.0"}, // 构建元数据不属于版本
		{"1", "1"},
	}
	for _, tc := range ok {
		got, err := pkgVersion(tc.in)
		if err != nil {
			t.Errorf("pkgVersion(%q) 报错: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("pkgVersion(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// TestPkgVersionRejectsNonNumericStart 钉住唯一那条硬要求。
func TestPkgVersionRejectsNonNumericStart(t *testing.T) {
	t.Parallel()

	// 首字符必须数字：macOS 安装器拿它排序，字母开头会让升级判断失效。
	// **猜一个数字出来的后果比报错严重** —— 用户装上的是版本号错误的包，
	// 而升级逻辑正是靠版本号认亲。
	for _, bad := range []string{"", "   ", "v", "abc", "x1.0"} {
		if got, err := pkgVersion(bad); err == nil {
			t.Errorf("pkgVersion(%q) 返回了 %q 而没有报错", bad, got)
		}
	}
}

// TestPkgMissingToolsIsNotFatal 验证没有 macOS 工具时返回的是哨兵错误。
//
// 在非 darwin 上这条是必然的（桩直接返回它）；在 darwin 上把 PATH 清空
// 也能构造出来。两种情形都要**可判断** —— 调用方靠 errors.Is 决定跳过
// 而不是失败。
func TestPkgMissingToolsIsNotFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := BuildPkg(PkgOptions{
		BinaryPath: "does-not-matter",
		Version:    "0.1.0",
		OutPath:    filepath.Join(t.TempDir(), "x.pkg"),
	})
	if err == nil {
		t.Fatal("没有 macOS 打包工具时应当报错")
	}
	if !errors.Is(err, ErrPkgToolsMissing) {
		t.Errorf("应当是 ErrPkgToolsMissing（调用方靠它决定跳过），得到: %v", err)
	}
}

// TestPkgIdentifierIsStable 钉住那个"永远不变"的常量。
//
// 它变了，macOS 会认为这是**另一个产品**：用户机器上出现两份，
// receipts 数据库里有两条互不相干的记录，而卸载只能去掉其中一份。
//
// 这条测试的价值不在于"值是多少"，而在于**改动它时必须是有意的** ——
// 它会失败，然后改的人会读到上面那段话。
func TestPkgIdentifierIsStable(t *testing.T) {
	t.Parallel()

	const want = "com.shirazunagisa.isc"
	if pkgIdentifier != want {
		t.Errorf("pkgIdentifier = %q，期望 %q。\n\n"+
			"这个值是**跨版本标识**，必须永远不变：改了它，macOS 会认为\n"+
			"这是另一个产品，用户机器上会出现两份而卸载只能去掉一份。\n"+
			"确实要改的话，请连同这段注释一起重新想一遍。",
			pkgIdentifier, want)
	}
	if !strings.HasPrefix(pkgIdentifier, "com.") {
		t.Errorf("pkgIdentifier 应当是反向域名形式，得到 %q", pkgIdentifier)
	}
}

// TestPkgInstallDirIsOnPath 钉住安装位置。
//
// `/usr/local/bin` 默认在 macOS 的 PATH 里。装到别处（比如
// `/opt/isc/bin`）安装会成功，而用户敲 `isc` 会得到 command not found ——
// 那种"装上了但用不了"的失败最难归因。
func TestPkgInstallDirIsOnPath(t *testing.T) {
	t.Parallel()

	if pkgInstallDir != "/usr/local/bin" {
		t.Errorf("pkgInstallDir = %q，期望 /usr/local/bin。"+
			"它默认在 PATH 里；换到别处的话安装会成功而命令找不到。",
			pkgInstallDir)
	}
}

// TestPkgNonDarwinStubReturnsSentinel 验证非 darwin 上桩的行为。
//
// 它确保 `go run ./scripts/release` 在 Windows / Linux 上不会因为
// "这个平台打不了 pkg"而失败 —— 那是一个**事实**，不是错误。
func TestPkgNonDarwinStubReturnsSentinel(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin 上走的是真实实现，这条不适用")
	}

	err := BuildPkg(PkgOptions{Version: "0.1.0", OutPath: os.DevNull})
	if !errors.Is(err, ErrPkgToolsMissing) {
		t.Errorf("非 darwin 上应当返回 ErrPkgToolsMissing，得到: %v", err)
	}
}
