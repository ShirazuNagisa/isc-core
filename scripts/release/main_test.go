package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 本文件守住发布构建里几处**只在产物里才看得出来**的性质。
//
// 它们的共同点是：构建过程本身不会报任何错，而缺陷要到用户解压之后
// 才显现 —— 那时已经发出去了。

// TestExeNameFor 验证可执行文件名按目标平台推导。
//
// 这个名字同时用于两处：编译输出，以及打包时判断"哪个文件该有执行位"。
// 两处判断不一致会让那个文件既没有执行位、也不被当成二进制。
func TestExeNameFor(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"windows": "isc.exe",
		"linux":   "isc",
		"darwin":  "isc",
		"freebsd": "isc",
	}
	for goos, want := range cases {
		got := exeNameFor(target{GOOS: goos})
		if got != want {
			t.Errorf("exeNameFor(%s) = %q，期望 %q", goos, got, want)
		}
	}
}

// TestPackTarGzSetsExecutableBit 钉住一个只在 Windows 上复现的缺陷。
//
// tar 头里的模式如果来自文件在磁盘上的 mode，那么在 Windows 上
// （go build 产出 0666，没有执行位这个概念）交叉编译出的 Linux 产物，
// 用户解压后会得到 "Permission denied"。
//
// 交叉编译是完全正当的用法 —— 本项目就是这么发布的 —— 因此不能靠
// "构建机上恰好有正确的权限位"。
func TestPackTarGzSetsExecutableBit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}

	// 造一个**故意没有执行位**的二进制，模拟 Windows 上的情况。
	bin := filepath.Join(stage, "isc")
	if err := os.WriteFile(bin, []byte("not really a binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 造一个说明文件。
	doc := filepath.Join(stage, "README.md")
	if err := os.WriteFile(doc, []byte("# readme"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "out.tar.gz")
	if err := packTarGz(out, stage, "isc"); err != nil {
		t.Fatalf("打包失败: %v", err)
	}

	modes := readTarModes(t, out)

	// 二进制必须有执行位 —— 这是本测试的核心。
	if modes["isc"]&0o111 == 0 {
		t.Errorf("isc 的权限是 %04o，没有执行位 —— "+
			"用户解压后会得到 Permission denied", modes["isc"])
	}
	if modes["isc"] != 0o755 {
		t.Errorf("isc 的权限是 %04o，期望 0755", modes["isc"])
	}

	// 说明文件不该有执行位。
	if modes["README.md"]&0o111 != 0 {
		t.Errorf("README.md 的权限是 %04o，不该有执行位", modes["README.md"])
	}
}

// TestPackTarGzIsDeterministic 验证同一份输入产出同样的字节。
//
// 时间戳必须清零、目录顺序必须排序 —— 否则同样的源码会产出不同的包，
// 而"校验和一致"就无法证明"两个产物来自同一份源码"。
func TestPackTarGzIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	// 故意用会打乱目录读取顺序的文件名。
	for _, name := range []string{"zeta", "alpha", "mid", "isc"} {
		if err := os.WriteFile(filepath.Join(stage, name),
			[]byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first := filepath.Join(dir, "a.tar.gz")
	second := filepath.Join(dir, "b.tar.gz")

	if err := packTarGz(first, stage, "isc"); err != nil {
		t.Fatal(err)
	}
	// 稍等一下，确认"时间"没有泄进产物里。
	time.Sleep(20 * time.Millisecond)
	if err := packTarGz(second, stage, "isc"); err != nil {
		t.Fatal(err)
	}

	a := readFile(t, first)
	b := readFile(t, second)
	if string(a) != string(b) {
		t.Error("两次打包的字节不同 —— 有时间戳或顺序泄进了产物")
	}
}

// TestPackZipIsDeterministic 钉住 zip 那条路径。
//
// zip 的可复现性比 tar 更容易出错：FileInfoHeader 会把文件在磁盘上的
// mtime 写进包头，而编译产物每次构建的 mtime 都不同。这一点是实测
// 发现的 —— 加了 SOURCE_DATE_EPOCH 之后 tar.gz 已经一致，而 zip 仍然不同。
func TestPackZipIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"isc.exe", "LICENSE"} {
		if err := os.WriteFile(filepath.Join(stage, name),
			[]byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first := filepath.Join(dir, "a.zip")
	second := filepath.Join(dir, "b.zip")

	if err := packZip(first, stage); err != nil {
		t.Fatal(err)
	}
	// 改一下源文件的 mtime —— 这正是真实构建里会变的东西。
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(stage, "isc.exe"), future, future); err != nil {
		t.Fatal(err)
	}
	if err := packZip(second, stage); err != nil {
		t.Fatal(err)
	}

	a := readFile(t, first)
	b := readFile(t, second)
	if string(a) != string(b) {
		t.Error("改了源文件 mtime 之后 zip 内容变了 —— " +
			"文件在磁盘上的时间被写进了包头")
	}

	// 顺便确认 zip 可读。
	zr, err := zip.OpenReader(first)
	if err != nil {
		t.Fatalf("产出的 zip 无法读取: %v", err)
	}
	defer zr.Close() //nolint:errcheck // 只读

	if len(zr.File) != 2 {
		t.Errorf("zip 里有 %d 个文件，期望 2", len(zr.File))
	}
}

// TestBuildTimestampHonorsSourceDateEpoch 验证可复现构建的约定。
func TestBuildTimestampHonorsSourceDateEpoch(t *testing.T) {
	// 1700000000 = 2023-11-14T22:13:20Z
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	got := buildTimestamp()
	const want = "2023-11-14T22:13:20Z"
	if got != want {
		t.Errorf("buildTimestamp() = %q，期望 %q", got, want)
	}
}

func TestBuildTimestampFallsBackToNow(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")

	got := buildTimestamp()
	if got == "" {
		t.Fatal("未设置 SOURCE_DATE_EPOCH 时应当回退到当前时间")
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("回退值不是合法的 RFC3339: %q", got)
	}
}

// TestBuildTimestampRejectsBadEpoch 验证非法值不会让构建失败。
//
// 报错中止构建是过度反应：一个坏的环境变量不该让人打不出包，
// 而回退到当前时间只是让构建不可复现 —— 那是一个已知的、可接受的降级。
func TestBuildTimestampRejectsBadEpoch(t *testing.T) {
	for _, bad := range []string{"not-a-number", "-5", "0", "  "} {
		t.Setenv("SOURCE_DATE_EPOCH", bad)
		if got := buildTimestamp(); got == "" {
			t.Errorf("SOURCE_DATE_EPOCH=%q 时应当回退到当前时间", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	byt, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return byt
}

// readTarModes 读出 tar.gz 里每个条目的权限位。
func readTarModes(t *testing.T, path string) map[string]os.FileMode {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // 只读

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close() //nolint:errcheck // 只读

	out := map[string]os.FileMode{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = os.FileMode(hdr.Mode).Perm()
	}
	return out
}
