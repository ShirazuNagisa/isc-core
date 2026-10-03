package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestFixTreeTimesPinsEveryEntry 钉住"暂存树的时间戳必须固定"。
//
// pkgbuild 把树里每个条目的 mtime 原样写进 Payload（odc cpio）与 Bom，
// 而那棵树是每次构建现建的临时目录 —— 不固定的话，同一个 SOURCE_DATE_EPOCH
// 下两次构建出来的**载荷**都不同（实测：差异只在 mtime 字段）。
//
// 注意它保证的范围：**载荷**可复现，而 .pkg 整体不是 —— xar 的目录表里
// 还有构建机的 inode、用户名与三个时间，而那份 TOC 改不得（理由与实测
// 证据见 pkg_darwin.go 的 BuildPkg）。这条测试守的是前半句，
// 不要把它当成后半句。
//
// 这里也是它唯一的守门人：.pkg 只在 macOS 上打得出来，CI 的
// 「可复现构建」跑在 Linux 上，看不见这一层。因此 fixTreeTimes 放在
// 没有构建标签的 pkg_def.go 里 —— 任何平台都要能测到它。
func TestFixTreeTimesPinsEveryEntry(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "usr", "local", "bin")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(nested, "isc")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 先设一个"很旧"的时间：这样一来，断言通过就只能是**真的被改了**，
	// 而不是原本就碰巧等于目标值。
	if err := fixTreeTimes(root, time.Unix(1000000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(1700000000, 0).UTC()
	if err := fixTreeTimes(root, stamp); err != nil {
		t.Fatal(err)
	}

	checked := 0
	err := filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		checked++
		if !info.ModTime().Equal(stamp) {
			t.Errorf("%s 的 mtime = %v，期望 %v",
				path, info.ModTime().UTC(), stamp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// root + usr + local + bin + isc
	if checked != 5 {
		t.Errorf("检查了 %d 个条目，期望 5 个 —— 树没搭对，断言就不可信", checked)
	}

	// 内容不能被顺手改掉。
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary" {
		t.Errorf("文件内容被改动了: %q", got)
	}
}

// TestFixTreeTimesZeroIsNoop 钉住零值 = 不改动。
//
// 零值代表"没有设 SOURCE_DATE_EPOCH"（开发构建）。那时必须保持原样 ——
// 把时间统一设成 1970 年会让开发产物看起来像坏掉的包。
func TestFixTreeTimesZeroIsNoop(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "isc")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if err := fixTreeTimes(root, time.Time{}); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("零值不该改动 mtime: %v → %v",
			before.ModTime(), after.ModTime())
	}
}
