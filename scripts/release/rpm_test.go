package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本文件**独立解析回**生成的 .rpm，验证它真的是一份合法的 RPM。
//
// # 为什么必须独立实现解析器
//
// 用同一份代码生成再用它解析等于什么都没验证。而 RPM 的格式恰恰
// 最容易出"看起来对了但 rpm 不认"的问题：
//
//	header 索引必须按 tag 升序（乱序 rpm 会拒绝）
//	字符串在数据区里必须以 NUL 结尾且 count 恒为 1
//	文件清单的三张表（目录 / 目录索引 / 基名）长度必须一一对应
//	cpio 的头部是 110 字节的**十六进制文本**，不是二进制
//	签名 header 之后要补零到 8 字节对齐
//
// 这些都是"生成时不报错、安装时才失败"的东西。

// ---------------------------------------------------------------------------
// 独立的 RPM 解析器
// ---------------------------------------------------------------------------

type rpmParsed struct {
	Lead      []byte
	Signature map[int]rpmValue
	Header    map[int]rpmValue
	Payload   []byte
}

type rpmValue struct {
	Type  int
	Count int
	// Strings / Ints 至少有一个非空，取决于 Type。
	Strings []string
	Ints    []int64
	Bytes   []byte
}

func (v rpmValue) str() string {
	if len(v.Strings) == 0 {
		return ""
	}
	return v.Strings[0]
}

// parseRPM 解析一个 .rpm 文件。
func parseRPM(t *testing.T, data []byte) rpmParsed {
	t.Helper()

	if len(data) < rpmLeadSize {
		t.Fatalf("文件只有 %d 字节，不足以容纳 96 字节的 lead", len(data))
	}

	var out rpmParsed
	out.Lead = data[:rpmLeadSize]
	rest := data[rpmLeadSize:]

	// --- 签名 header ---
	sig, consumed, err := parseRPMHeader(rest)
	if err != nil {
		t.Fatalf("解析签名 header 失败: %v", err)
	}
	out.Signature = sig

	// 签名 header 之后补零到 8 字节对齐。
	rest = rest[consumed:]
	if pad := (8 - consumed%8) % 8; pad > 0 {
		if len(rest) < pad {
			t.Fatalf("签名 header 之后只剩 %d 字节，不足以容纳 %d 字节填充",
				len(rest), pad)
		}
		for i := 0; i < pad; i++ {
			if rest[i] != 0 {
				t.Fatalf("签名 header 的对齐填充第 %d 字节是 %#x，应当是 0",
					i, rest[i])
			}
		}
		rest = rest[pad:]
	}

	// --- 主 header ---
	hdr, consumed, err := parseRPMHeader(rest)
	if err != nil {
		t.Fatalf("解析主 header 失败: %v", err)
	}
	out.Header = hdr
	out.Payload = rest[consumed:]

	return out
}

// parseRPMHeader 解析一个 header 结构，返回标签表与消耗的字节数。
func parseRPMHeader(data []byte) (map[int]rpmValue, int, error) {
	if len(data) < 16 {
		return nil, 0, fmt.Errorf("头部只有 %d 字节，至少需要 16", len(data))
	}
	if string(data[0:3]) != rpmHeaderMagic {
		return nil, 0, fmt.Errorf("魔数不对: % x", data[0:3])
	}
	if data[3] != 1 {
		return nil, 0, fmt.Errorf("版本号是 %d，期望 1", data[3])
	}

	count := binary.BigEndian.Uint32(data[8:12])
	dataLen := binary.BigEndian.Uint32(data[12:16])

	indexEnd := 16 + int(count)*16
	total := indexEnd + int(dataLen)
	if len(data) < total {
		return nil, 0, fmt.Errorf(
			"header 声明 %d 字节（%d 条索引 + %d 字节数据），实际只有 %d",
			total, count, dataLen, len(data))
	}

	headerData := data[indexEnd:total]
	out := make(map[int]rpmValue, count)

	var prevTag int = -1
	for i := 0; i < int(count); i++ {
		off := 16 + i*16
		tag := int(binary.BigEndian.Uint32(data[off : off+4]))
		typ := int(binary.BigEndian.Uint32(data[off+4 : off+8]))
		offset := int(binary.BigEndian.Uint32(data[off+8 : off+12]))
		cnt := int(binary.BigEndian.Uint32(data[off+12 : off+16]))

		// rpm 要求索引按 tag 升序。
		if tag <= prevTag {
			return nil, 0, fmt.Errorf(
				"索引没有按 tag 升序：第 %d 条是 %d，上一条是 %d",
				i+1, tag, prevTag)
		}
		prevTag = tag

		if offset > len(headerData) {
			return nil, 0, fmt.Errorf(
				"标签 %d 的偏移 %d 超出数据区（%d 字节）",
				tag, offset, len(headerData))
		}

		val, err := decodeRPMValue(typ, cnt, headerData[offset:])
		if err != nil {
			return nil, 0, fmt.Errorf("标签 %d: %w", tag, err)
		}
		out[tag] = val
	}

	return out, total, nil
}

func decodeRPMValue(typ, count int, data []byte) (rpmValue, error) {
	v := rpmValue{Type: typ, Count: count}

	switch typ {
	case rpmTypeChar, rpmTypeInt8:
		if len(data) < count {
			return v, fmt.Errorf("需要 %d 字节，只有 %d", count, len(data))
		}
		for i := 0; i < count; i++ {
			v.Ints = append(v.Ints, int64(data[i]))
		}

	case rpmTypeInt16:
		if len(data) < count*2 {
			return v, fmt.Errorf("需要 %d 字节，只有 %d", count*2, len(data))
		}
		for i := 0; i < count; i++ {
			v.Ints = append(v.Ints, int64(binary.BigEndian.Uint16(data[i*2:])))
		}

	case rpmTypeInt32:
		if len(data) < count*4 {
			return v, fmt.Errorf("需要 %d 字节，只有 %d", count*4, len(data))
		}
		for i := 0; i < count; i++ {
			v.Ints = append(v.Ints, int64(binary.BigEndian.Uint32(data[i*4:])))
		}

	case rpmTypeInt64:
		if len(data) < count*8 {
			return v, fmt.Errorf("需要 %d 字节，只有 %d", count*8, len(data))
		}
		for i := 0; i < count; i++ {
			v.Ints = append(v.Ints, int64(binary.BigEndian.Uint64(data[i*8:])))
		}

	case rpmTypeString, rpmTypeStringArray, rpmTypeI18NString:
		rest := data
		for i := 0; i < count; i++ {
			idx := bytes.IndexByte(rest, 0)
			if idx < 0 {
				return v, fmt.Errorf("第 %d 个字符串没有 NUL 结尾", i+1)
			}
			v.Strings = append(v.Strings, string(rest[:idx]))
			rest = rest[idx+1:]
		}

	case rpmTypeBin:
		if len(data) < count {
			return v, fmt.Errorf("二进制字段需要 %d 字节，只有 %d", count, len(data))
		}
		v.Bytes = data[:count]

	default:
		return v, fmt.Errorf("不支持的标签类型 %d", typ)
	}

	return v, nil
}

// ---------------------------------------------------------------------------
// 构造测试用的 rpm
// ---------------------------------------------------------------------------

func buildTestRPM(t *testing.T, version string) []byte {
	t.Helper()

	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(stage, binaryName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho isc\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"LICENSE", "README.md"} {
		if err := os.WriteFile(filepath.Join(stage, name),
			[]byte("content of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	out := filepath.Join(dir, "isc.rpm")
	if err := BuildRPM(out, RpmOptions{
		Package:    "isc",
		Version:    version,
		Release:    "1",
		Arch:       "amd64",
		Summary:    "测试包",
		License:    "GPL-3.0-or-later",
		URL:        "https://example.com",
		BinaryPath: bin,
		DocDir:     stage,
	}); err != nil {
		t.Fatalf("生成 rpm 失败: %v", err)
	}

	byt, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return byt
}

// ---------------------------------------------------------------------------
// 结构验证
// ---------------------------------------------------------------------------

// TestRPMLead 验证 96 字节的 lead。
//
// 它必须存在且长度正确 —— 少了它 rpm 会直接拒绝这个文件，
// 而报错只有一句"不是 RPM 包"。
func TestRPMLead(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	if len(rpm.Lead) != rpmLeadSize {
		t.Fatalf("lead 是 %d 字节，期望 %d", len(rpm.Lead), rpmLeadSize)
	}
	if string(rpm.Lead[0:3]) != rpmHeaderMagic {
		t.Errorf("lead 魔数 = % x，期望 % x",
			rpm.Lead[0:3], []byte(rpmHeaderMagic))
	}
	if rpm.Lead[4] != 0 {
		t.Errorf("包类型 = %d，期望 0（二进制包）", rpm.Lead[4])
	}
}

// TestRPMHeaderRequiredTags 验证主 header 里的必填标签。
//
// 少任何一个，rpm 都会拒绝安装，而报错通常只有一句"缺少标签"。
func TestRPMHeaderRequiredTags(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	required := map[int]string{
		tagName:            "isc",
		tagVersion:         "1.0.0",
		tagRelease:         "1",
		tagArch:            "amd64",
		tagLicense:         "GPL-3.0-or-later",
		tagOS:              "linux",
		tagPayloadFormat:   "cpio",
		tagPayloadCompress: "gzip",
	}

	for tag, want := range required {
		v, ok := rpm.Header[tag]
		if !ok {
			t.Errorf("缺少标签 %d", tag)
			continue
		}
		if got := v.str(); got != want {
			t.Errorf("标签 %d = %q，期望 %q", tag, got, want)
		}
	}

	// SOURCERPM 是**二进制包必需**的：rpm 用它区分二进制包与源码包，
	// 缺了它 `rpm -qp` 会报"不是二进制包"。
	if _, ok := rpm.Header[tagSourceRPM]; !ok {
		t.Error("缺少 SOURCERPM —— rpm 会认为这不是二进制包")
	}
}

// TestRPMHeaderIndexIsSorted 验证索引按 tag 升序。
//
// 解析器里已经检查了这一点（乱序会直接失败），而这条测试把
// "为什么"写清楚。
func TestRPMHeaderIndexIsSorted(t *testing.T) {
	t.Parallel()

	// parseRPM 内部的升序检查会在乱序时 Fatal，
	// 因此能跑到这里就说明是升序的。
	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))
	if len(rpm.Header) == 0 {
		t.Fatal("header 是空的")
	}
}

// TestRPMFileTablesAreConsistent 是最要紧的一条。
//
// RPM 把文件路径拆成"目录""基名""目录索引"三张表，而它们必须
// **一一对应**：任何一张表的长度不同都会让 rpm 拒绝这个包，
// 而报错只有一句"文件清单不一致" —— 完全看不出是哪张表的问题。
func TestRPMFileTablesAreConsistent(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	sizes := rpm.Header[tagFileSizes]
	modes := rpm.Header[tagFileModes]
	digests := rpm.Header[tagFileDigests]
	basenames := rpm.Header[tagBaseNames]
	dirnames := rpm.Header[tagDirNames]
	dirindexes := rpm.Header[tagDirIndexes]

	n := sizes.Count
	if n == 0 {
		t.Fatal("文件清单是空的")
	}

	// 除目录表外，其余每张表都必须与文件数**完全相等**。
	checks := []struct {
		name  string
		count int
	}{
		{"FILESIZES", sizes.Count},
		{"FILEMODES", modes.Count},
		{"FILEDIGESTS", digests.Count},
		{"BASENAMES", basenames.Count},
		{"DIRINDEXES", dirindexes.Count},
		{"FILEUSERNAME", rpm.Header[tagFileUserName].Count},
		{"FILEGROUPNAME", rpm.Header[tagFileGroupName].Count},
		{"FILEFLAGS", rpm.Header[tagFileFlags].Count},
		{"FILELINKTOS", rpm.Header[tagFileLinkTos].Count},
	}
	for _, c := range checks {
		if c.count != n {
			t.Errorf("%s 有 %d 项，但文件数是 %d —— 三张表必须一一对应",
				c.name, c.count, n)
		}
	}

	// 目录索引里的每个值都必须是目录表的合法下标。
	for i, idx := range dirindexes.Ints {
		if idx < 0 || int(idx) >= dirnames.Count {
			t.Errorf("第 %d 个文件的目录索引是 %d，超出目录表（%d 项）",
				i+1, idx, dirnames.Count)
		}
	}
}

// TestRPMContainsExecutableWithMode 验证可执行文件与它的权限位。
func TestRPMContainsExecutableWithMode(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	paths := rpmFilePaths(rpm)
	var binIdx = -1
	for i, p := range paths {
		if p == rpmBinPath {
			binIdx = i
		}
	}
	if binIdx < 0 {
		t.Fatalf("文件清单里没有 %s，实际有: %v", rpmBinPath, paths)
	}

	// 没有执行位就是一个**装得上、跑不起来**的包。
	mode := rpm.Header[tagFileModes].Ints[binIdx]
	if mode&0o111 == 0 {
		t.Errorf("%s 的权限是 %04o，没有执行位", rpmBinPath, mode)
	}
	if mode != 0o755 {
		t.Errorf("%s 的权限是 %04o，期望 0755", rpmBinPath, mode)
	}
}

// TestRPMDoesNotDuplicateBinaryIntoDocs 钉住在 deb 那边抓到过的缺陷。
//
// 暂存目录里同时有可执行文件与文档，而"把目录里的文件全拷过去"会把
// 二进制也塞进文档目录 —— 二进制在包里存了两份，包体积翻倍。
//
// 这一点在 deb 那边是解包核对时发现的（15.5MB → 7.41MB），
// 这里从一开始就避免它。
func TestRPMDoesNotDuplicateBinaryIntoDocs(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))
	paths := rpmFilePaths(rpm)

	dup := rpmDocDir + "/" + binaryName
	for _, p := range paths {
		if p == dup {
			t.Errorf("可执行文件被复制进了文档目录 %s —— 包里存了两份", dup)
		}
	}

	// 文档该在的仍然要在。
	for _, want := range []string{
		rpmDocDir + "/LICENSE",
		rpmDocDir + "/README.md",
	} {
		found := false
		for _, p := range paths {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少 %s", want)
		}
	}
}

// TestRPMDocFilesAreFlaggedAsDocs 验证文档被标成文档。
//
// FILEFLAGS 里的 2 表示"文档"，而 rpm 在 --excludedocs 时会跳过它们。
// 不标的话，一个只想装二进制的精简系统也会被迫装上文档。
func TestRPMDocFilesAreFlaggedAsDocs(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))
	paths := rpmFilePaths(rpm)
	flags := rpm.Header[tagFileFlags].Ints

	for i, p := range paths {
		isDoc := strings.HasPrefix(p, "/usr/share/doc/")
		flagged := flags[i]&2 != 0

		if isDoc && !flagged {
			t.Errorf("%s 在文档目录下，但没有被标成文档", p)
		}
		if !isDoc && flagged {
			t.Errorf("%s 不在文档目录下，却被标成了文档", p)
		}
	}
}

// TestRPMSignatureHeader 验证签名 header 的两项长度字段。
//
// rpm 用它们做边界检查，算错会让它报"包已损坏"。
func TestRPMSignatureHeader(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	sigSize, ok := rpm.Signature[tagSigSize]
	if !ok {
		t.Fatal("签名 header 里缺少 SIZE")
	}
	payloadSize, ok := rpm.Signature[tagSigPayloadSize]
	if !ok {
		t.Fatal("签名 header 里缺少 PAYLOADSIZE")
	}

	if int(payloadSize.Ints[0]) != len(rpm.Payload) {
		t.Errorf("PAYLOADSIZE = %d，实际载荷 %d 字节",
			payloadSize.Ints[0], len(rpm.Payload))
	}
	if int(sigSize.Ints[0]) < len(rpm.Payload) {
		t.Errorf("SIZE = %d，小于载荷长度 %d",
			sigSize.Ints[0], len(rpm.Payload))
	}

	// 载荷的 SHA-256 必须对得上。
	sha, ok := rpm.Signature[tagSigSHA256]
	if !ok {
		t.Fatal("签名 header 里缺少 SHA256")
	}
	want := sha256Hex(rpm.Payload)
	if sha.str() != want {
		t.Errorf("SHA256 = %s，载荷实际是 %s", sha.str(), want)
	}
}

// TestRPMPayloadIsCPIO 验证载荷是 gzip 压缩的 cpio(newc)。
func TestRPMPayloadIsCPIO(t *testing.T) {
	t.Parallel()

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	gz, err := gzip.NewReader(bytes.NewReader(rpm.Payload))
	if err != nil {
		t.Fatalf("载荷不是合法的 gzip: %v", err)
	}
	defer gz.Close() //nolint:errcheck // 只读

	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}

	names, err := parseCPIONames(raw)
	if err != nil {
		t.Fatalf("载荷不是合法的 cpio: %v", err)
	}

	// cpio 里的路径**不以斜杠开头**。
	want := []string{"usr/bin/isc", "usr/share/doc/isc/LICENSE", "usr/share/doc/isc/README.md"}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
			}
		}
		if !found {
			t.Errorf("cpio 里没有 %q，实际有: %v", w, names)
		}
	}

	// 最后必须有 TRAILER!!! 结尾记录。
	if len(names) == 0 || names[len(names)-1] != "TRAILER!!!" {
		t.Errorf("cpio 缺少 TRAILER!!! 结尾记录，最后一项是 %v", names)
	}
}

// parseCPIONames 解析 cpio(newc) 的文件名列表。
//
// newc 的头部是 **110 字节的 ASCII**，每个字段 8 位定宽十六进制 ——
// 这是它最容易写错的地方（写成二进制就完全解析不了）。
func parseCPIONames(data []byte) ([]string, error) {
	var names []string

	for off := 0; off+110 <= len(data); {
		hdr := data[off : off+110]
		if string(hdr[0:6]) != "070701" {
			return nil, fmt.Errorf("偏移 %d 处的魔数是 %q，期望 070701",
				off, hdr[0:6])
		}

		field := func(i int) (int, error) {
			// 每个字段 8 位十六进制。
			v, err := strconv.ParseUint(string(hdr[6+i*8:6+i*8+8]), 16, 32)
			return int(v), err
		}

		size, err := field(6)
		if err != nil {
			return nil, fmt.Errorf("偏移 %d 的 filesize 字段不是十六进制: %w", off, err)
		}
		nameSize, err := field(11)
		if err != nil {
			return nil, fmt.Errorf("偏移 %d 的 namesize 字段不是十六进制: %w", off, err)
		}
		if nameSize < 1 {
			return nil, fmt.Errorf("偏移 %d 的 namesize 是 %d", off, nameSize)
		}

		nameStart := off + 110
		if nameStart+nameSize > len(data) {
			return nil, fmt.Errorf("偏移 %d 的名字超出数据范围", off)
		}
		name := string(data[nameStart : nameStart+nameSize-1])
		names = append(names, name)

		if name == "TRAILER!!!" {
			return names, nil
		}

		// 头部 + 名字补齐到 4 字节边界，然后是内容，再补齐。
		next := nameStart + nameSize
		next += (4 - next%4) % 4
		next += size
		next += (4 - size%4) % 4
		off = next
	}

	return names, fmt.Errorf("没有找到 TRAILER!!! 结尾记录")
}

// TestRPMIsDeterministic 验证同一份输入产出逐字节相同的包。
//
// RPM 里时间戳出现在三处（cpio 的 mtime、FILEMTIMES、以及可能的
// 构建时间），任何一处没清零都会破坏可复现性。
func TestRPMIsDeterministic(t *testing.T) {
	t.Parallel()

	first := buildTestRPM(t, "1.0.0")
	second := buildTestRPM(t, "1.0.0")

	if !bytes.Equal(first, second) {
		t.Error("两次生成的 .rpm 不同 —— 有时间戳泄进了产物")
	}
}

// ---------------------------------------------------------------------------
// 版本号与校验
// ---------------------------------------------------------------------------

// TestRpmVersionReplacesHyphen 钉住 RPM 对版本号的限制。
//
// RPM 用连字符分隔 Version 与 Release，因此 Version 里不能有连字符。
// 而我们的版本可能来自 `git describe`（例如 "1.0.0-rc1"）。
func TestRpmVersionReplacesHyphen(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"1.0.0":         "1.0.0",
		"1.0.0-rc1":     "1.0.0_rc1",
		"3994090-dirty": "3994090_dirty",
		"0~v1.0":        "0~v1.0",
	}
	for in, want := range cases {
		got := rpmVersion(in)
		if got != want {
			t.Errorf("rpmVersion(%q) = %q，期望 %q", in, got, want)
		}
		if strings.Contains(got, "-") {
			t.Errorf("rpmVersion(%q) 的结果 %q 仍含连字符", in, got)
		}
	}
}

// TestRPMVersionWithHyphenIsRejected 验证带连字符的版本被**明确拒绝**。
//
// 让 rpm 在安装时报一句含糊的"版本号非法"，不如在生成时就告诉他
// 该怎么改。
func TestRPMVersionWithHyphenIsRejected(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "isc")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := BuildRPM(filepath.Join(dir, "out.rpm"), RpmOptions{
		Package: "isc", Version: "1.0.0-rc1", Release: "1", Arch: "amd64",
		Summary: "s", License: "GPL-3.0-or-later",
		BinaryPath: bin, DocDir: dir,
	})
	if err == nil {
		t.Fatal("含连字符的版本号应当被拒绝")
	}
	if !strings.Contains(err.Error(), "连字符") {
		t.Errorf("错误信息应当说清原因: %v", err)
	}
}

func TestRPMValidatesOptions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "isc")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	good := RpmOptions{
		Package: "isc", Version: "1.0.0", Release: "1", Arch: "amd64",
		Summary: "s", License: "GPL-3.0-or-later",
		BinaryPath: bin, DocDir: dir,
	}
	if err := validateRpmOptions(good); err != nil {
		t.Fatalf("合法参数被拒绝: %v", err)
	}

	bad := []struct {
		name   string
		mutate func(*RpmOptions)
	}{
		{"缺包名", func(o *RpmOptions) { o.Package = "" }},
		{"包名含空格", func(o *RpmOptions) { o.Package = "is c" }},
		{"缺发布号", func(o *RpmOptions) { o.Release = "" }},
		{"发布号含连字符", func(o *RpmOptions) { o.Release = "1-2" }},
		{"缺架构", func(o *RpmOptions) { o.Arch = "" }},
		{"缺摘要", func(o *RpmOptions) { o.Summary = "" }},
		{"缺许可证", func(o *RpmOptions) { o.License = "" }},
		{"可执行文件不存在", func(o *RpmOptions) { o.BinaryPath = filepath.Join(dir, "nope") }},
		{"文档目录不存在", func(o *RpmOptions) { o.DocDir = filepath.Join(dir, "nope") }},
	}

	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			o := good
			tc.mutate(&o)
			if err := validateRpmOptions(o); err == nil {
				t.Error("应当被拒绝")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// rpmFilePaths 从三张表里还原出完整的文件路径。
//
// 还原本身也是对那三张表一致性的验证 —— 索引越界会在这里 panic。
func rpmFilePaths(rpm rpmParsed) []string {
	basenames := rpm.Header[tagBaseNames].Strings
	dirnames := rpm.Header[tagDirNames].Strings
	dirindexes := rpm.Header[tagDirIndexes].Ints

	out := make([]string, 0, len(basenames))
	for i, base := range basenames {
		dir := dirnames[dirindexes[i]]
		out = append(out, strings.TrimSuffix(dir, "/")+"/"+base)
	}
	return out
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
