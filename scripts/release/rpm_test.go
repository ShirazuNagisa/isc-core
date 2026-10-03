package main

import (
	"bytes"
	"compress/gzip"
	"crypto/md5"
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
	// HeaderBlob 是主 header 的**未对齐**原始字节 —— 签名里的摘要算的就是它。
	HeaderBlob []byte
	// HeaderOffset 是主 header 在文件里的起点：签名里的 SIZE 字段
	// 就是"从这里到文件末尾"的字节数。
	HeaderOffset int
	Payload      []byte
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
	out.HeaderOffset = len(data) - len(rest)
	hdr, consumed, err := parseRPMHeader(rest)
	if err != nil {
		t.Fatalf("解析主 header 失败: %v", err)
	}
	out.Header = hdr
	out.HeaderBlob = rest[:consumed]

	// 主 header 之后同样补零到 8 字节对齐 —— 载荷从 8 的倍数处开始。
	rest = rest[consumed:]
	if pad := (8 - consumed%8) % 8; pad > 0 {
		if len(rest) < pad {
			t.Fatalf("主 header 之后只剩 %d 字节，不足以容纳 %d 字节填充",
				len(rest), pad)
		}
		for i := 0; i < pad; i++ {
			if rest[i] != 0 {
				t.Fatalf("主 header 的对齐填充第 %d 字节是 %#x，应当是 0",
					i, rest[i])
			}
		}
		rest = rest[pad:]
	}

	out.Payload = rest

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
	lead := rpm.Lead

	if len(lead) != rpmLeadSize {
		t.Fatalf("lead 是 %d 字节，期望 %d", len(lead), rpmLeadSize)
	}
	// **lead 的魔数与 header 的魔数不是一回事**：
	//
	//	lead    0xEDABEEDB   ← rpm 靠它识别"这是不是一个 RPM 包"
	//	header  0x8EADE8     ← header 结构自己的魔数
	//
	// 这里曾经写的是 `rpmHeaderMagic` —— 而实现里也用了同一个常量，
	// 于是测试与实现互相印证、一起错。断言写成**字面量**，这样常量写错
	// 时它抓得住。
	if !bytes.HasPrefix(lead, []byte{0xed, 0xab, 0xee, 0xdb}) {
		t.Errorf("lead 魔数 = % x，期望 ed ab ee db（0xEDABEEDB）", lead[0:4])
	}

	// 字段偏移（rpm 的 struct rpmlead_s）：
	//
	//	0:4   magic        4:5 major    5:6 minor   6:8 type
	//	8:10  archnum      10:76 name   76:78 osnum  78:80 signature_type
	//
	// 这条测试原先查的是 lead[4] 并把它叫"包类型" —— 那个偏移是**主版本号**，
	// 类型在 6:8。两个字段都写错，而断言把错的那个当成了基准。
	if lead[4] != 3 {
		t.Errorf("lead 主版本号 = %d，期望 3（rpm 自己写的就是 3）", lead[4])
	}
	if lead[5] != 0 {
		t.Errorf("lead 次版本号 = %d，期望 0", lead[5])
	}
	if typ := binary.BigEndian.Uint16(lead[6:8]); typ != 0 {
		t.Errorf("包类型 = %d，期望 0（二进制包；1 是源码包）", typ)
	}
	if os := binary.BigEndian.Uint16(lead[76:78]); os != 0 {
		t.Errorf("osnum = %d，期望 0（已弃用，rpm 写 0）", os)
	}
	if st := binary.BigEndian.Uint16(lead[78:80]); st != 5 {
		t.Errorf("signature_type = %d，期望 5（有签名 header）", st)
	}
	name := strings.TrimRight(string(lead[10:76]), "\x00")
	if !strings.HasPrefix(name, "isc") {
		t.Errorf("lead 里的名字 = %q，期望以包名开头（rpm 写 NEVR）", name)
	}
}

// TestRPMHeaderTagsAreRPMDefined 把每个标签的**号与类型**钉死。
//
// # 为什么需要它
//
// rpm 读主 header 时带的是 `regionTag = HEADERIMMUTABLE`，因此它会
// **逐个标签核对类型**（`hdrchkTagType`）。于是"标签号写错"不是小事：
//
//	tag 1023 我们当成 FILESIZES（i[]）
//	rpm 的表里 1023 是 PREIN（**字符串**）
//	→ 类型不符 → rpm 直接拒收整个包
//
// 而当时**所有测试都通过** —— 因为我们自己的解析器只按号取数据，
// 从不核对"这个号在 rpm 那边是什么"。这一条测试就是那张对照表。
//
// 表里的号与类型抄自 rpm 的 `include/rpm/rpmtag.h`（值写成字面量）。
// 字符串类之间允许不一致（rpm 自己就明确放行 s / s[] / s{} 互换）。
func TestRPMHeaderTagsAreRPMDefined(t *testing.T) {
	t.Parallel()

	// tag → rpm 定义的类型（4=INT32 3=INT16 6=STRING 8=STRING_ARRAY 9=I18NSTRING）
	want := map[int]int{
		1000: 6, // NAME        s
		1001: 6, // VERSION     s
		1002: 6, // RELEASE     s
		1004: 9, // SUMMARY     s{}
		1005: 9, // DESCRIPTION s{}
		1006: 4, // BUILDTIME   i
		1007: 6, // BUILDHOST   s
		1009: 4, // SIZE        i
		1014: 6, // LICENSE     s
		1016: 9, // GROUP       s{}
		1020: 6, // URL         s
		1021: 6, // OS          s
		1022: 6, // ARCH        s
		1028: 4, // FILESIZES   i[]   ← 曾经错写成 1023（那是 PREIN，字符串）
		1030: 3, // FILEMODES   h[]
		1033: 3, // FILERDEVS   h[]
		1034: 4, // FILEMTIMES  i[]
		1035: 8, // FILEDIGESTS s[]
		1036: 8, // FILELINKTOS s[]
		1037: 4, // FILEFLAGS   i[]
		1039: 8, // FILEUSERNAME  s[]
		1040: 8, // FILEGROUPNAME s[]
		1044: 6, // SOURCERPM   s
		1045: 4, // FILEVERIFYFLAGS i[]
		1046: 4, // ARCHIVESIZE i
		1047: 8, // PROVIDENAME s[]
		1048: 4, // REQUIREFLAGS i[]
		1049: 8, // REQUIRENAME s[]
		1050: 8, // REQUIREVERSION s[]
		1064: 6, // RPMVERSION  s
		1095: 4, // FILEDEVICES i[]
		1096: 4, // FILEINODES  i[]
		1116: 4, // DIRINDEXES  i[]
		1117: 8, // BASENAMES   s[]
		1118: 8, // DIRNAMES    s[]
		1124: 6, // PAYLOADFORMAT s
		1125: 6, // PAYLOADCOMPRESSOR s
		1126: 6, // PAYLOADFLAGS s
		5011: 4, // FILEDIGESTALGO i ← 曾经错写成 1095（那是 FILEDEVICES）
	}

	rpm := parseRPM(t, buildTestRPM(t, "1.0.0"))

	for tag, v := range rpm.Header {
		expected, ok := want[tag]
		if !ok {
			t.Errorf("header 里有 tag %d，但它不在我们的对照表里 —— "+
				"新增标签时请连同 rpmtag.h 里的号与类型一起加进来", tag)
			continue
		}
		if v.Type == expected {
			continue
		}
		// 字符串类之间允许互换（rpm 的 hdrchkTagType 明确放行）。
		if isStringClass(v.Type) && isStringClass(expected) {
			continue
		}
		t.Errorf("tag %d 的类型是 %d，rpm 定义的是 %d", tag, v.Type, expected)
	}

	// 反过来：该有的标签一个都不能少 —— 少了哪个都会让 rpm 报
	// "缺少必需的标签"。
	for _, tag := range []int{1000, 1001, 1002, 1004, 1005, 1009, 1014, 1016,
		1028, 1030, 1033, 1034, 1035, 1036, 1037, 1039, 1040, 1044, 1116,
		1117, 1118, 5011} {
		if _, ok := rpm.Header[tag]; !ok {
			t.Errorf("header 里缺少 tag %d", tag)
		}
	}
}

func isStringClass(typ int) bool { return typ == 6 || typ == 8 || typ == 9 }

// TestRPMModesCarryFileTypeBits 钉住"mode 必须带文件类型位"。
//
// RPM 的头标签 `tagFileModes` 与 cpio 载荷里的 mode 字段都带 POSIX 的
// **类型位**（普通文件 = 0100000）。只写权限位（0755）时：
//
//   - `rpm -qplv` 打出来的第一列不是 `-rwxr-xr-x`，CI 里那条"可执行位"
//     的检查因此会失败；
//   - rpm 判断条目类型（文件/目录/符号链接）靠的正是这一位，
//     少了它，安装时的行为没有保证。
//
// 我们的实现曾经用 `.Perm()` 把高位丢掉，而当时的测试只检查权限位
// （`mode&0o111`），于是这个缺陷一路留到了 CI 上真正的 rpm 命令。
//
// 断言刻意用**字面量**（0o100000）而不是实现里的常量。
func TestRPMModesCarryFileTypeBits(t *testing.T) {
	t.Parallel()

	data := buildTestRPM(t, "1.0.0")
	rpm := parseRPM(t, data)

	// 头部标签里的模式。
	modes, ok := rpm.Header[tagFileModes]
	if !ok {
		t.Fatal("主 header 里没有 tagFileModes")
	}
	for i, v := range modes.Ints {
		if v&0o170000 != 0o100000 {
			t.Errorf("第 %d 个文件的 mode = %04o，没有普通文件类型位（0100000）",
				i, v)
		}
	}

	// cpio 载荷里的模式：解出第一条记录看它的 mode 字段。
	entry := firstCPIORecord(t, rpm.Payload)
	if entry.mode&0o170000 != 0o100000 {
		t.Errorf("cpio 载荷里第一条记录的 mode = %04o，没有普通文件类型位", entry.mode)
	}
}

// cpioRecord 是 cpio(newc) 里我们关心的字段。
type cpioRecord struct {
	name string
	mode uint32
}

// firstCPIORecord 解出 cpio 载荷里的第一条记录。
//
// 只为测试而写的最小解析器：这条测试要验证的正是载荷本身，
// 因此不能依赖被测代码里的解析逻辑。
func firstCPIORecord(t *testing.T, payload []byte) cpioRecord {
	t.Helper()

	gz, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("载荷不是 gzip: %v", err)
	}
	defer gz.Close() //nolint:errcheck // 只读

	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 110 || string(raw[0:6]) != "070701" {
		t.Fatal("载荷的开头不是 cpio(newc) 记录")
	}

	field := func(i int) uint32 {
		v, err := strconv.ParseUint(string(raw[6+i*8:14+i*8]), 16, 32)
		if err != nil {
			t.Fatalf("cpio 第 %d 个字段不是十六进制: %v", i, err)
		}
		return uint32(v)
	}
	mode := field(1)
	nameSize := int(field(11))
	name := string(raw[110 : 110+nameSize-1])
	return cpioRecord{name: name, mode: mode}
}

// TestRPMPayloadIsAlignedAndGzipped 钉住"载荷从 8 字节边界开始"。
//
// # 为什么值得单独一条
//
// rpm 的两个 header 都补到 8 字节边界，载荷因此必须从一个 8 的倍数处开始
// —— 而我们的实现曾经**只补了签名 header 那一次**，主 header 之后直接写
// 载荷（实测落到偏移 1739，差 5 字节）。rpm 按自己的规则推进到 1744，
// 在那里读到的是填充的零，于是报一句"不是 gzip 数据"。
//
// 这个缺陷同样是我们自己的测试看不见的：**解析器与写入端用了同一条错误
// 规则**（都直接接在 header 后面）。因此这里的断言刻意用两条与实现无关的
// 判据：偏移是不是 8 的倍数，以及那两字节是不是 gzip 的魔数。
func TestRPMPayloadIsAlignedAndGzipped(t *testing.T) {
	t.Parallel()

	data := buildTestRPM(t, "1.0.0")
	rpm := parseRPM(t, data)

	// 载荷在文件里的绝对偏移 = 文件长度 - 载荷长度。
	offset := len(data) - len(rpm.Payload)
	if offset%8 != 0 {
		t.Errorf("载荷从偏移 %d 开始，不是 8 的倍数 —— "+
			"rpm 会在对齐后的位置读到填充的零，报「不是 gzip 数据」", offset)
	}
	if !bytes.HasPrefix(rpm.Payload, []byte{0x1f, 0x8b}) {
		t.Errorf("载荷的前两字节是 % x，期望 1f 8b（gzip 魔数）",
			rpm.Payload[0:2])
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
	//
	// 比较的是**权限位**：mode 里还带着文件类型位（0100000，见
	// TestRPMModesCarryFileTypeBits），拿它跟 0755 整体比较是错的 ——
	// 这条断言原本就是这么写的，于是它把"少了类型位"这个缺陷
	// 当成了正确行为。
	mode := rpm.Header[tagFileModes].Ints[binIdx]
	if mode&0o111 == 0 {
		t.Errorf("%s 的权限是 %06o，没有执行位", rpmBinPath, mode)
	}
	if perm := mode & 0o7777; perm != 0o755 {
		t.Errorf("%s 的权限位是 %04o，期望 0755", rpmBinPath, perm)
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

// TestRPMSignatureHeader 验证签名 header 里的长度与摘要。
//
// # 这些字段的语义是**实测真实包**得出的，不是猜的
//
// 拿了三个真实 .rpm（其中两个来自 GitHub CLI 的 release，一个 header 长度
// 非 8 字节对齐）逐个字段验算：
//
//	SIZE(1000)        header（**含对齐填充**）+ 载荷
//	PAYLOADSIZE(1007) 载荷**解压后**的字节数（不是文件里那段 gzip 的长度）
//	SHA256(273)       **header 结构**的 SHA-256，十六进制串
//	MD5(1004)         header 结构 + 载荷
//
// 两个字段因此差得很远：SIZE 与文件长度是同量级的，而 PAYLOADSIZE 是解压
// 之后的量（实测一个包里 50405610 vs 13711736）。
//
// 这条测试原先断言的正是**错的**语义（PAYLOADSIZE == gzip 长度、
// SHA256 == 载荷的摘要）—— 实现与测试互相印证、一起错。
func TestRPMSignatureHeader(t *testing.T) {
	t.Parallel()

	data := buildTestRPM(t, "1.0.0")
	rpm := parseRPM(t, data)

	sigSize, ok := rpm.Signature[tagSigSize]
	if !ok {
		t.Fatal("签名 header 里缺少 SIZE")
	}
	payloadSize, ok := rpm.Signature[tagSigPayloadSize]
	if !ok {
		t.Fatal("签名 header 里缺少 PAYLOADSIZE")
	}

	// SIZE = 签名 header 之后的全部字节 = 主 header（含对齐填充）+ 载荷。
	if got, want := int(sigSize.Ints[0]), len(data)-rpm.HeaderOffset; got != want {
		t.Errorf("SIZE = %d，期望 %d（对齐后的 header + 载荷）", got, want)
	}

	// PAYLOADSIZE = **解压后**的 cpio 长度。
	gz, err := gzip.NewReader(bytes.NewReader(rpm.Payload))
	if err != nil {
		t.Fatalf("载荷不是 gzip: %v", err)
	}
	raw, err := io.ReadAll(gz)
	if err != nil {
		t.Fatal(err)
	}
	_ = gz.Close() //nolint:errcheck // 只读
	if got := int(payloadSize.Ints[0]); got != len(raw) {
		t.Errorf("PAYLOADSIZE = %d，解压后的载荷是 %d 字节 —— "+
			"这个字段要的是**解压后**的长度，不是文件里 gzip 的长度（%d）",
			got, len(raw), len(rpm.Payload))
	}

	// SHA256 = header 结构的摘要（十六进制串）。
	headerRaw := rpm.HeaderBlob
	sha, ok := rpm.Signature[tagSigSHA256]
	if !ok {
		t.Fatal("签名 header 里缺少 SHA256")
	}
	if want := sha256Hex(headerRaw); sha.str() != want {
		t.Errorf("SHA256 = %s，header 结构实际是 %s —— "+
			"这个字段是 **header** 的摘要，不是载荷的", sha.str(), want)
	}

	// MD5 = header 结构 + 载荷。
	md5v, ok := rpm.Signature[tagSigMD5]
	if !ok {
		t.Fatal("签名 header 里缺少 MD5")
	}
	h := md5.New()
	h.Write(headerRaw)
	h.Write(rpm.Payload)
	if !bytes.Equal(md5v.Bytes, h.Sum(nil)) {
		t.Errorf("MD5 = % x，header+载荷实际是 % x", md5v.Bytes, h.Sum(nil))
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
