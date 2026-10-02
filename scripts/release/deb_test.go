package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件**解析回**生成的 .deb，验证它真的是一份合法的 Debian 包。
//
// # 为什么必须解析回来而不是只看文件存在
//
// .deb 是 ar 归档，而 ar 的成员头是**定长 60 字节的 ASCII**，长度与
// 时间都是十进制文本。这类格式最容易出"看起来对了但解析器不认"的问题：
// 一个字段宽度差一格，文件照样生成，而 dpkg 只会说一句"格式错误"。
//
// 因此这些测试做的是：按 ar 规范逐字解析，按 Debian 规范读取 control
// 字段，解压两个 tar 并检查内容。任何一步不对都会失败。

// arMember 是从 .deb 里解析出的一个成员。
type arMember struct {
	Name string
	Data []byte
}

// parseAr 按 ar 规范解析一个归档。
//
// 这是**独立实现**的一份解析器（不是复用生成代码里的函数）——
// 用同一份代码生成再用它解析，等于什么都没验证。
func parseAr(t *testing.T, data []byte) []arMember {
	t.Helper()

	const magic = "!<arch>\n"
	if !bytes.HasPrefix(data, []byte(magic)) {
		t.Fatalf("缺少 ar 全局头 %q", magic)
	}

	rest := data[len(magic):]
	var out []arMember

	for len(rest) > 0 {
		if len(rest) < 60 {
			t.Fatalf("剩余 %d 字节，不足以容纳 60 字节的成员头", len(rest))
		}

		hdr := rest[:60]
		rest = rest[60:]

		// 成员头的最后两个字节必须是 "`\n"。
		if string(hdr[58:60]) != "`\n" {
			t.Fatalf("成员头的魔数不是 \"`\\n\"，而是 %q", hdr[58:60])
		}

		name := strings.TrimRight(string(hdr[0:16]), " ")

		sizeStr := strings.TrimSpace(string(hdr[48:58]))
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			t.Fatalf("成员 %q 的长度字段 %q 不是十进制数: %v", name, sizeStr, err)
		}

		if len(rest) < size {
			t.Fatalf("成员 %q 声明 %d 字节，实际只剩 %d 字节",
				name, size, len(rest))
		}
		out = append(out, arMember{Name: name, Data: rest[:size]})
		rest = rest[size:]

		// 奇数长度要跳过 1 字节的对齐填充。
		if size%2 == 1 {
			if len(rest) < 1 {
				t.Fatalf("成员 %q 长度是奇数，但缺少对齐填充", name)
			}
			if rest[0] != '\n' {
				t.Fatalf("成员 %q 的对齐填充不是换行，而是 %q", name, rest[0])
			}
			rest = rest[1:]
		}
	}

	return out
}

// buildTestDeb 生成一个用于测试的 .deb。
func buildTestDeb(t *testing.T) []byte {
	t.Helper()

	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}

	// 一个假的"可执行文件"。
	bin := filepath.Join(stage, binaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho isc\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 随包文档。
	for _, name := range []string{"LICENSE", "README.md", "THIRD_PARTY_NOTICES.md"} {
		if err := os.WriteFile(filepath.Join(stage, name),
			[]byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(dir, "isc.deb")
	if err := BuildDeb(out, DebOptions{
		Package:     "isc",
		Version:     "1.2.3",
		Arch:        "amd64",
		Maintainer:  "Sh1razu <test@example.com>",
		Description: "测试包",
		Homepage:    "https://example.com",
		BinaryPath:  bin,
		DocDir:      stage,
	}); err != nil {
		t.Fatalf("生成 deb 失败: %v", err)
	}

	byt, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return byt
}

// TestDebIsValidArArchive 验证 .deb 是合法的 ar 归档。
func TestDebIsValidArArchive(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))

	// dpkg 要求恰好这三个成员，且**顺序固定**。
	//
	// 顺序错了 dpkg 会报"格式错误"，而那个提示完全看不出是顺序问题。
	want := []string{"debian-binary", "control.tar.gz", "data.tar.gz"}
	if len(members) != len(want) {
		t.Fatalf("应当有 %d 个成员，得到 %d 个: %v",
			len(want), len(members), memberNames(members))
	}
	for i, name := range want {
		if members[i].Name != name {
			t.Errorf("第 %d 个成员是 %q，期望 %q（dpkg 要求固定顺序）",
				i+1, members[i].Name, name)
		}
	}
}

// TestDebBinaryVersion 验证 debian-binary 的内容。
//
// 它必须是 "2.0\n" —— 那是 deb 格式的版本，而写错会被直接拒绝。
func TestDebBinaryVersion(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))
	if got := string(members[0].Data); got != "2.0\n" {
		t.Errorf("debian-binary = %q，期望 \"2.0\\n\"", got)
	}
}

// TestDebControlFields 验证 control 文件的必填字段。
//
// 少任何一个，dpkg 都会在**安装时**才失败，而报错通常只有一句
// "control 文件缺少字段"。
func TestDebControlFields(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))
	control := readMemberFile(t, members[1].Data, "./control")

	required := []string{
		"Package: isc",
		"Version: 1.2.3",
		"Architecture: amd64",
		"Maintainer: Sh1razu <test@example.com>",
		"Description: 测试包",
	}
	for _, want := range required {
		if !strings.Contains(control, want) {
			t.Errorf("control 缺少 %q:\n%s", want, control)
		}
	}

	// 字段顺序有约定：Package 在前，Version 其次，Architecture 第三。
	// dpkg 本身不强制，但工具链的其它部分假设了这个顺序。
	lines := strings.Split(strings.TrimSpace(control), "\n")
	if len(lines) < 3 {
		t.Fatalf("control 太短:\n%s", control)
	}
	for i, prefix := range []string{"Package: ", "Version: ", "Architecture: "} {
		if !strings.HasPrefix(lines[i], prefix) {
			t.Errorf("第 %d 行应当以 %q 开头，实际是 %q", i+1, prefix, lines[i])
		}
	}
}

// TestDebDataTarContainsExecutable 验证可执行文件被装到正确位置且带执行位。
//
// 没有执行位就是一个**装得上、跑不起来**的包 —— 而那个症状
// （"命令找不到"或"权限不足"）不会让人想到是打包的问题。
func TestDebDataTarContainsExecutable(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))
	files := readTar(t, members[2].Data)

	bin, ok := files["./usr/bin/isc"]
	if !ok {
		t.Fatalf("data.tar.gz 里没有 ./usr/bin/isc，实际有: %v", keysOf(files))
	}
	if bin.mode&0o111 == 0 {
		t.Errorf("/usr/bin/isc 的权限是 %04o，没有执行位", bin.mode)
	}
	if bin.mode != 0o755 {
		t.Errorf("/usr/bin/isc 的权限是 %04o，期望 0755", bin.mode)
	}
	// 属主必须是 root —— 装进系统的东西属主不该是构建机上的某个用户。
	if bin.uid != 0 || bin.gid != 0 {
		t.Errorf("/usr/bin/isc 的属主是 %d:%d，期望 0:0", bin.uid, bin.gid)
	}
}

// TestDebShipsLicense 验证许可证随包分发。
//
// GPLv3 的要求，也是基本的分发礼节。
func TestDebShipsLicense(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))
	files := readTar(t, members[2].Data)

	if _, ok := files["./usr/share/doc/isc/LICENSE"]; !ok {
		t.Errorf("缺少 /usr/share/doc/isc/LICENSE，实际有: %v", keysOf(files))
	}
	// Debian Policy §12.5 要求每个二进制包带一份版权文件，
	// 少了它某些工具会拒绝安装。
	if _, ok := files["./usr/share/doc/isc/copyright"]; !ok {
		t.Errorf("缺少 Debian 要求的 copyright 文件，实际有: %v", keysOf(files))
	}
}

// TestDebIsDeterministic 验证同一份输入产出逐字节相同的包。
//
// 与 tar/zip 侧同样的理由：可复现构建。而 .deb 特别容易在这里出错 ——
// 它在 ar 头和两个 tar 里各有一处时间戳。
func TestDebIsDeterministic(t *testing.T) {
	t.Parallel()

	first := buildTestDeb(t)
	second := buildTestDeb(t)

	if !bytes.Equal(first, second) {
		t.Error("两次生成的 .deb 不同 —— 有时间戳泄进了产物")
	}
}

// ---------------------------------------------------------------------------
// 版本号
// ---------------------------------------------------------------------------

// TestDebVersionString 验证版本号被转成 Debian 认的形式。
//
// Debian 的版本号**必须以数字开头**，而我们的版本可能来自
// `git describe`（例如 "3994090-dirty"）。
func TestDebVersionString(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"1.2.3":         "1.2.3",
		"0.1.0":         "0.1.0",
		"1.0.0-rc1":     "1.0.0-rc1",
		"3994090-dirty": "3994090-dirty", // 恰好以数字开头
		"v1.2.3":        "0~v1.2.3",      // 非数字开头要加前缀
		"unknown":       "0~unknown",
		"":              "0~unknown",
		"1.0 beta":      "1.0-beta", // 空格换成连字符
		"1.0+build.5":   "1.0+build.5",
	}

	for in, want := range cases {
		if got := debVersionString(in); got != want {
			t.Errorf("debVersionString(%q) = %q，期望 %q", in, got, want)
		}
	}

	// 任何输入都必须以数字开头。
	for _, in := range []string{"v1.0", "unknown", "abc", "~weird"} {
		got := debVersionString(in)
		if got[0] < '0' || got[0] > '9' {
			t.Errorf("debVersionString(%q) = %q，没有以数字开头", in, got)
		}
	}
}

// ---------------------------------------------------------------------------
// 参数校验
// ---------------------------------------------------------------------------

// TestDebValidatesOptions 验证缺参数时**在生成之前**就报错。
//
// 否则产出的包会在安装时才失败，而 dpkg 的报错看不出是哪个字段的问题。
func TestDebValidatesOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "isc")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	good := DebOptions{
		Package: "isc", Version: "1.0.0", Arch: "amd64",
		Maintainer: "a <a@b.c>", Description: "d",
		BinaryPath: bin, DocDir: dir,
	}

	// 基线：合法的应当通过校验（这里只验证 validate，不真的生成）。
	if err := validateDebOptions(good); err != nil {
		t.Fatalf("合法参数被拒绝: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*DebOptions)
	}{
		{"缺包名", func(o *DebOptions) { o.Package = "" }},
		{"包名含空格", func(o *DebOptions) { o.Package = "is c" }},
		{"缺版本", func(o *DebOptions) { o.Version = "" }},
		{"缺架构", func(o *DebOptions) { o.Arch = "" }},
		{"缺维护者", func(o *DebOptions) { o.Maintainer = "" }},
		{"缺描述", func(o *DebOptions) { o.Description = "" }},
		{"可执行文件不存在", func(o *DebOptions) { o.BinaryPath = filepath.Join(dir, "nope") }},
		{"文档目录不存在", func(o *DebOptions) { o.DocDir = filepath.Join(dir, "nope") }},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			o := good
			tc.mutate(&o)
			if err := validateDebOptions(o); err == nil {
				t.Error("应当被拒绝")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

type tarEntry struct {
	mode int64
	uid  int
	gid  int
	data []byte
}

// readTar 解压一个 tar.gz，返回路径 → 条目。
func readTar(t *testing.T, data []byte) map[string]tarEntry {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("不是合法的 gzip: %v", err)
	}
	defer gz.Close() //nolint:errcheck // 只读

	out := map[string]tarEntry{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("不是合法的 tar: %v", err)
		}
		byt, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = tarEntry{
			mode: hdr.Mode, uid: hdr.Uid, gid: hdr.Gid, data: byt,
		}
	}
	return out
}

// readMemberFile 从一个 tar.gz 成员里读出指定文件的内容。
func readMemberFile(t *testing.T, data []byte, name string) string {
	t.Helper()

	files := readTar(t, data)
	entry, ok := files[name]
	if !ok {
		t.Fatalf("tar 里没有 %q，实际有: %v", name, keysOf(files))
	}
	return string(entry.data)
}

func memberNames(members []arMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Name)
	}
	return out
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestDebDoesNotDuplicateBinaryIntoDocs 钉住一个被压缩掩盖了的缺陷。
//
// 暂存目录里同时有可执行文件与文档，而"把目录里的文件全拷过去"会把
// 18MB 的二进制也塞进 /usr/share/doc/isc/ —— 二进制因此在包里存了两份。
//
// **只看包大小发现不了它**：压缩把体积差掩盖了（15.5MB 的包 vs
// 18.3MB 的二进制）。它是解包逐条核对时才看出来的。
func TestDebDoesNotDuplicateBinaryIntoDocs(t *testing.T) {
	t.Parallel()

	members := parseAr(t, buildTestDeb(t))
	files := readTar(t, members[2].Data)

	// 文档目录里不该出现可执行文件。
	dup := "./usr/share/doc/isc/" + binaryName
	if _, ok := files[dup]; ok {
		t.Errorf("可执行文件被复制进了文档目录 %s —— 包里存了两份", dup)
	}

	// 但它必须在 /usr/bin 下。
	if _, ok := files["./usr/bin/"+binaryName]; !ok {
		t.Error("可执行文件没有被装到 /usr/bin")
	}

	// 文档该在的仍然要在 —— 别为了修上面那个把文档也一起漏掉。
	for _, want := range []string{
		"./usr/share/doc/isc/LICENSE",
		"./usr/share/doc/isc/README.md",
		"./usr/share/doc/isc/copyright",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("缺少 %s", want)
		}
	}
}
