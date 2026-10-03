package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// TestPackageFileNames 钉住 .deb / .rpm 的文件名。
//
// # 它守的是一个真实发生过的错位
//
// CI 的「核对 .deb」那一步写死了 `dist/isc-0.0.0-ci-linux-amd64.deb` ——
// 那是**发布压缩包**的命名（`isc-<版本>-<平台>`），而 .deb 从第一天起就
// 按 Debian 约定叫 `isc_<版本>_<架构>.deb`。于是那一步**从来没有可能通过**，
// / 只是因为任务在前面就挂了（发布脚本会先跑测试）才一直没暴露。
//
// 现在两边都靠这条测试对齐：CI 用通配符找产物，名字的形状由这里钉住。
func TestPackageFileNames(t *testing.T) {
	t.Parallel()

	// 带连字符的版本是最容易出错的一种：它是 `git describe` 的常见产物。
	const dashed = "0.0.0-ci"

	if got, want := debFileName(dashed, "amd64"), "isc_0.0.0-ci_amd64.deb"; got != want {
		t.Errorf("debFileName = %q，期望 %q（Debian 约定：包名_版本_架构）", got, want)
	}
	if got, want := rpmFileName(dashed, "amd64"), "isc-0.0.0_ci-1.amd64.rpm"; got != want {
		t.Errorf("rpmFileName = %q，期望 %q —— "+
			"RPM 用连字符分隔版本与发布号，版本里的连字符必须先净化，"+
			"否则文件名会与包内元数据不一致", got, want)
	}
	if got, want := rpmFileName("1.2.3", "arm64"), "isc-1.2.3-1.arm64.rpm"; got != want {
		t.Errorf("rpmFileName = %q，期望 %q", got, want)
	}
	if got, want := debFileName("1.2.3", "arm64"), "isc_1.2.3_arm64.deb"; got != want {
		t.Errorf("debFileName = %q，期望 %q", got, want)
	}

	// 文件名里不该出现两个连字符夹着的发布号歧义：版本部分必须无连字符。
	if name := rpmFileName(dashed, "amd64"); strings.Count(name, "-") != 2 {
		t.Errorf("rpm 文件名 %q 的连字符数量不对（应当是 包名-版本-发布号）", name)
	}
}

// TestCIToolchainMatchesPinnedVersion 钉住"CI 用的 Go 版本与 tool-versions.env 一致"。
//
// # 为什么需要它
//
// **gofmt 的输出随版本变化**：1.26 与 1.27 对含 CJK 的 map 字面量用不同的
// 对齐宽度算法（1.27 按显示宽度、1.26 按字符数），于是同一个文件在一边
// 干净、在另一边被判定"未格式化"。这一条真实发生过 —— CI 跟着 go.mod 的
// 1.26 跑 `gofmt -l .`，而源码是用 1.27 格式化的，于是两个 test job 挂在
// "gofmt 检查"上，而错误信息只说"以下文件未格式化"。
//
// 因此版本必须是**一处声明、两处使用**：`scripts/tool-versions.env` 与
// ci.yml 的工作流级 env。这条测试把它们钉在一起。
func TestCIToolchainMatchesPinnedVersion(t *testing.T) {
	t.Parallel()

	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	envFile := string(readFile(t, filepath.Join(root, "scripts", "tool-versions.env")))
	want := ""
	for _, line := range strings.Split(envFile, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "GO_VERSION="); ok {
			want = strings.TrimSpace(v)
		}
	}
	if want == "" {
		t.Fatal("scripts/tool-versions.env 里没有 GO_VERSION")
	}

	ci := string(readFile(t, filepath.Join(root, ".github", "workflows", "ci.yml")))
	if !strings.Contains(ci, "GO_VERSION: \""+want+"\"") {
		t.Errorf("ci.yml 的工作流级 env 里没有 GO_VERSION: %q —— "+
			"它与 scripts/tool-versions.env 必须是同一个值，"+
			"否则 gofmt 检查会在一边通过、在另一边失败", want)
	}
	// matrix-build 是刻意的例外：它验证"最低版本仍能构建"。
	if !strings.Contains(ci, "go-version-file: go.mod") {
		t.Error("matrix-build 应当继续用 go.mod 的最低版本编译")
	}
}

// TestBuildArgsDisablesVCSStamping 钉住 `-buildvcs=false`。
//
// 它修的是一个只在 CI 上看得见的缺陷：默认的 `-buildvcs=auto` 会把主模块的
// 伪版本写进二进制，而那个伪版本带一个 `+dirty` 后缀（来自 git status）。
// 于是"同一个 SOURCE_DATE_EPOCH 下构建两次"必然不一致 —— 因为**第一次
// 构建会创建一个未跟踪的输出目录**，第二次构建看到的工作区就不再干净。
//
// CI 的「可复现构建」因此挂了两轮，报的是
//
//	SHA256SUMS 两次构建不一致
//	isc-1.2.3-windows-amd64.zip 两次构建不一致
//
// 而那句话完全指不到原因：产物确实只取决于源码，取决于的是 **git 状态**。
// 这条测试把这个决定钉在这里，免得有人觉得它多余而删掉。
func TestBuildArgsDisablesVCSStamping(t *testing.T) {
	t.Parallel()

	args := buildArgs("-s -w", "/tmp/isc")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-buildvcs=false") {
		t.Errorf("构建参数里必须有 -buildvcs=false，否则「工作区里有什么」会改变产物：%v", args)
	}
	// 这两条不能因为上面的改动而丢：ldflags 与输出路径仍要传下去。
	if !strings.Contains(joined, "-ldflags -s -w") {
		t.Errorf("ldflags 没有传下去: %v", args)
	}
	if !strings.Contains(joined, "-o /tmp/isc") {
		t.Errorf("输出路径没有传下去: %v", args)
	}
	if !strings.Contains(joined, "-trimpath") {
		t.Errorf("-trimpath 不能丢（构建机路径会进二进制）: %v", args)
	}
}

// TestSourceTimeHonorsSourceDateEpoch 验证可复现构建的约定。
func TestSourceTimeHonorsSourceDateEpoch(t *testing.T) {
	// 1700000000 = 2023-11-14T22:13:20Z
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")

	got := sourceTime().Format(time.RFC3339)
	const want = "2023-11-14T22:13:20Z"
	if got != want {
		t.Errorf("sourceTime() = %q，期望 %q", got, want)
	}
}

func TestSourceTimeFallsBackToNow(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")

	got := sourceTime()
	if got.IsZero() {
		t.Fatal("未设置 SOURCE_DATE_EPOCH 时应当回退到当前时间")
	}
	if _, err := time.Parse(time.RFC3339, got.Format(time.RFC3339)); err != nil {
		t.Errorf("回退值不是合法的 RFC3339: %q", got)
	}
}

// TestSourceTimeRejectsBadEpoch 验证非法值不会让构建失败。
//
// 报错中止构建是过度反应：一个坏的环境变量不该让人打不出包，
// 而回退到当前时间只是让构建不可复现 —— 那是一个已知的、可接受的降级。
func TestSourceTimeRejectsBadEpoch(t *testing.T) {
	for _, bad := range []string{"not-a-number", "-5", "0", "  "} {
		t.Setenv("SOURCE_DATE_EPOCH", bad)
		if got := sourceTime(); got.IsZero() {
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
