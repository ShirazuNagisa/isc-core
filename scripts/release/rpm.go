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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 本文件生成 RPM 安装包。
//
// # 为什么手写而不是用 rpmbuild
//
// 与 deb 同样的理由：构建要在三个平台上都能跑，而 rpmbuild 只在
// RPM 系发行版上存在。用它就意味着"在 Windows 上打不出 Linux 的包"，
// 而交叉编译正是本项目发布方式的常态。
//
// # RPM 的结构
//
//	Lead            96 字节的遗留头（现在只剩魔数与类型有意义）
//	Signature       一个 header 结构，含校验和与尺寸
//	Header          一个 header 结构，含元数据与文件清单
//	Payload         cpio(newc) 归档，gzip 压缩
//
// header 结构是：
//
//	魔数(3) 版本(1) 保留(4)   共 8 字节
//	索引条数(4) 数据区长度(4)  大端
//	索引条目 × N            每条 16 字节：tag(4) type(4) offset(4) count(4)
//	数据区                  按索引里的偏移取用
//
// 手写它换来的是**产物可以被逐字节解析验证** —— 这一点在 CI 上还会
// 被真正的 rpm 命令再验一遍。

const (
	rpmLeadSize = 96

	// rpmLeadMajor 是 lead 里的主版本号。rpm 自己不校验它（只校验魔数），
	// 但 `file` 靠它报告 "RPM v3.0"。
	rpmLeadMajor = 3

	// rpmFileTypeBits 是普通文件的 POSIX 类型位（S_IFREG）。
	//
	// RPM 的 mode 字段（头部标签与 cpio 条目都是）带这一位，
	// 而**只写权限位是不对的**：rpm 靠高位区分文件、目录与符号链接。
	rpmFileTypeBits = 0o100000

	// rpmLeadMagic 是 **lead** 的魔数（0xEDABEEDB）。
	//
	// 它与 header 的魔数（0x8EADE8）**不是一回事**，而 rpm 正是靠它识别
	// "这个文件是不是 RPM 包"。这里踩过一次：lead 里写的是 header 的魔数，
	// 而当时那条测试用的也是同一个常量 —— 于是测试与实现互相印证，
	// 一起错，直到 CI 上真正的 `rpm` 命令第一次读到它。
	rpmLeadMagic = "\xed\xab\xee\xdb"

	// rpmHeaderMagic 是 header 结构的魔数（三个字节，后面跟版本号）。
	rpmHeaderMagic = "\x8e\xad\xe8"

	// RPM 数据类型。
	rpmTypeChar        = 1 // 单字节
	rpmTypeInt8        = 2
	rpmTypeInt16       = 3
	rpmTypeInt32       = 4
	rpmTypeInt64       = 5
	rpmTypeString      = 6
	rpmTypeBin         = 7
	rpmTypeStringArray = 8
	rpmTypeI18NString  = 9
)

// RPM 标签号。只列用到的那些。
const (
	tagName            = 1000
	tagVersion         = 1001
	tagRelease         = 1002
	tagSummary         = 1004
	tagDescription     = 1005
	tagBuildTime       = 1006
	tagBuildHost       = 1007
	tagSize            = 1009
	tagLicense         = 1014
	tagGroup           = 1016
	tagURL             = 1020
	tagOS              = 1021
	tagArch            = 1022
	tagFileSizes       = 1023
	tagFileModes       = 1030
	tagFileRDevs       = 1033
	tagFileMTimes      = 1034
	tagFileDigests     = 1035
	tagFileLinkTos     = 1036
	tagFileFlags       = 1037
	tagFileUserName    = 1039
	tagFileGroupName   = 1040
	tagSourceRPM       = 1044
	tagFileVerifyFlags = 1045
	tagArchiveSize     = 1046
	tagProvideName     = 1047
	tagRequireFlags    = 1048
	tagRequireName     = 1049
	tagRequireVersion  = 1050
	tagRPMVersion      = 1064
	tagFileDigestAlgo  = 1095
	tagPayloadFormat   = 1124
	tagPayloadCompress = 1125
	tagPayloadFlags    = 1126
	tagDirIndexes      = 1116
	tagBaseNames       = 1117
	tagDirNames        = 1118

	// 签名 header 里的标签。
	tagSigSize        = 1000
	tagSigMD5         = 1004
	tagSigPGP         = 1005
	tagSigPayloadSize = 1007
	tagSigSHA256      = 273
)

// RpmOptions 是生成 RPM 的参数。
type RpmOptions struct {
	Package    string
	Version    string
	Release    string
	Arch       string
	Summary    string
	License    string
	URL        string
	BinaryPath string
	DocDir     string
}

// 打包时固定使用的安装路径。
const (
	rpmBinPath   = "/usr/bin/" + binaryName
	rpmDocDir    = "/usr/share/doc/" + binaryName
	rpmBuildHost = "isc-core"
)

// rpmFile 是打进包里的一个文件。
type rpmFile struct {
	// Path 是安装后的绝对路径。
	Path string
	// Mode 是权限位。
	Mode os.FileMode
	// Data 是内容。
	Data []byte
}

// BuildRPM 生成一个 .rpm 文件。
func BuildRPM(outPath string, opts RpmOptions) error {
	if err := validateRpmOptions(opts); err != nil {
		return err
	}

	files, err := rpmPayloadFiles(opts)
	if err != nil {
		return err
	}

	payload, rawPayloadSize, err := rpmCPIO(files)
	if err != nil {
		return fmt.Errorf("生成 cpio 载荷失败: %w", err)
	}

	// 未对齐的 header：**摘要算的是它**（对齐填充不参与）。
	header := rpmHeaderBytes(opts, files, payload)

	// 主 header 在文件里补零到 8 字节对齐。
	//
	// rpm 的两个 header 结构都补到 8 字节边界，而**载荷必须从 8 的倍数
	// 处开始**：读的时候 rpm 按同样的规则推进。少了这一步，载荷就会落在
	// 一个非对齐的偏移上（实测差 5 字节），而 rpm 在那里读到的是填充的零，
	// 于是报"不是 gzip 数据"—— 一个完全指不到原因的错。
	//
	// 但**对齐的长度只用于签名里的长度字段**，不参与摘要计算：
	// 实测三个真实包（其中一个 header 长度非 8 对齐）确认了这个区分。
	padded := pad8(append([]byte(nil), header...))

	// 签名 header 里含 header 与载荷的校验和，因此必须在 header
	// 生成**之后**才算得出来。
	sig := rpmSignatureBytes(header, payload, len(padded), rawPayloadSize)

	var buf bytes.Buffer
	buf.Write(rpmLead(opts))
	buf.Write(sig)
	buf.Write(padded)
	buf.Write(payload)

	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

// pad8 把字节切片补零到 8 的倍数。
func pad8(b []byte) []byte {
	if pad := (8 - len(b)%8) % 8; pad > 0 {
		b = append(b, make([]byte, pad)...)
	}
	return b
}

// rpmLead 生成 96 字节的遗留头。
//
// 它唯一还有意义的内容是魔数与类型标记（0 表示二进制包）——
// 其余字段（包名、版本、架构）在现代 RPM 里都由 header 提供，
// 而 rpm 会忽略 lead 里的这些字段。
//
// 但**它必须存在且长度正确**：少了它 rpm 会直接拒绝这个文件。
func rpmLead(opts RpmOptions) []byte {
	lead := make([]byte, rpmLeadSize)

	copy(lead[0:4], rpmLeadMagic)

	// 主/次版本号：**3.0**。
	//
	// rpm 自己的写入端就是 3（有 RPMTAG_RPMFORMAT 时是 4）—— 它不校验这个
	// 字段（只校验魔数），但 `file` 之类的工具靠它报告"RPM v3.0"，
	// 而写 0 会得到一句 "RPM v0.0"。
	lead[4] = rpmLeadMajor
	lead[5] = 0

	// 类型：0 = 二进制包，1 = 源码包。
	binary.BigEndian.PutUint16(lead[6:8], 0)
	// 架构号：已弃用，rpm 写 0。
	binary.BigEndian.PutUint16(lead[8:10], 0)

	// 名字：rpm 写 NEVR（名字-版本-发布号），NUL 结尾、超长截断。
	// 同样是弃用字段（现代 rpm 只读 header），但按它的写法来最省事。
	name := opts.Package + "-" + opts.Version + "-" + opts.Release
	if len(name) > 65 {
		name = name[:65]
	}
	copy(lead[10:76], name)

	// 操作系统号：已弃用，rpm 写 0（**这里曾经写成名字长度** —— 一个纯粹的
	// 笔误，因为 76:78 这段既不是名字长度、也没有别的东西要放）。
	binary.BigEndian.PutUint16(lead[76:78], 0)

	// 签名类型：5 = 有签名 header（RPMSIGTYPE_HEADERSIG）。
	binary.BigEndian.PutUint16(lead[78:80], 5)
	return lead
}

// ---------------------------------------------------------------------------
// header 结构
// ---------------------------------------------------------------------------

// rpmEntry 是一个 header 索引条目。
type rpmEntry struct {
	Tag    int
	Type   int
	Offset int
	Count  int
}

// rpmHeaderBuilder 累积 header 的数据区与索引。
type rpmHeaderBuilder struct {
	data    bytes.Buffer
	entries []rpmEntry
}

func (b *rpmHeaderBuilder) addString(tag int, s string) {
	// 字符串在数据区里以 NUL 结尾，而 count 恒为 1。
	b.entries = append(b.entries, rpmEntry{
		Tag: tag, Type: rpmTypeString, Offset: b.data.Len(), Count: 1,
	})
	b.data.WriteString(s)
	b.data.WriteByte(0)
}

func (b *rpmHeaderBuilder) addI18NString(tag int, s string) {
	b.entries = append(b.entries, rpmEntry{
		Tag: tag, Type: rpmTypeI18NString, Offset: b.data.Len(), Count: 1,
	})
	b.data.WriteString(s)
	b.data.WriteByte(0)
}

func (b *rpmHeaderBuilder) addStringArray(tag int, items []string) {
	b.entries = append(b.entries, rpmEntry{
		Tag: tag, Type: rpmTypeStringArray, Offset: b.data.Len(), Count: len(items),
	})
	for _, s := range items {
		b.data.WriteString(s)
		b.data.WriteByte(0)
	}
}

func (b *rpmHeaderBuilder) addInt16(tag int, vals []uint16) {
	b.entries = append(b.entries, rpmEntry{
		Tag: tag, Type: rpmTypeInt16, Offset: b.data.Len(), Count: len(vals),
	})
	for _, v := range vals {
		_ = binary.Write(&b.data, binary.BigEndian, v)
	}
}

func (b *rpmHeaderBuilder) addInt32(tag int, vals []uint32) {
	b.entries = append(b.entries, rpmEntry{
		Tag: tag, Type: rpmTypeInt32, Offset: b.data.Len(), Count: len(vals),
	})
	for _, v := range vals {
		_ = binary.Write(&b.data, binary.BigEndian, v)
	}
}

// bytes 返回完整的 header 结构（魔数 + 索引 + 数据）。
func (b *rpmHeaderBuilder) bytes() []byte {
	// 索引按 tag 升序：rpm 要求如此，乱序会让它报"header 结构错误"。
	sort.Slice(b.entries, func(i, j int) bool {
		return b.entries[i].Tag < b.entries[j].Tag
	})

	var buf bytes.Buffer
	buf.WriteString(rpmHeaderMagic)
	buf.WriteByte(1)              // 版本
	buf.Write([]byte{0, 0, 0, 0}) // 保留

	_ = binary.Write(&buf, binary.BigEndian, uint32(len(b.entries)))
	_ = binary.Write(&buf, binary.BigEndian, uint32(b.data.Len()))

	for _, e := range b.entries {
		_ = binary.Write(&buf, binary.BigEndian, uint32(e.Tag))
		_ = binary.Write(&buf, binary.BigEndian, uint32(e.Type))
		_ = binary.Write(&buf, binary.BigEndian, uint32(e.Offset))
		_ = binary.Write(&buf, binary.BigEndian, uint32(e.Count))
	}
	buf.Write(b.data.Bytes())
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// 主 header
// ---------------------------------------------------------------------------

func rpmHeaderBytes(opts RpmOptions, files []rpmFile, payload []byte) []byte {
	var b rpmHeaderBuilder

	// BUILDTIME 固定为 0：与载荷里的文件 mtime 一致，为的是可复现构建。
	// 真实包的这一项是构建时刻，但"构建时刻进产物"正是可复现性要避免的。
	b.addInt32(tagBuildTime, []uint32{0})
	// BUILDHOST 固定成产品名而不是机器名：构建机的 hostname 不该进产物。
	b.addString(tagBuildHost, "isc-core")

	b.addString(tagName, opts.Package)
	b.addString(tagVersion, opts.Version)
	b.addString(tagRelease, opts.Release)
	b.addI18NString(tagSummary, opts.Summary)
	b.addI18NString(tagDescription, opts.Description())
	b.addString(tagLicense, opts.License)
	b.addString(tagGroup, "Applications/System")
	b.addString(tagURL, opts.URL)
	b.addString(tagOS, "linux")
	b.addString(tagArch, opts.Arch)
	// SOURCERPM 是**二进制包必需**的字段：rpm 用它区分二进制包与
	// 源码包，缺了它 `rpm -qp` 会报"不是二进制包"。
	b.addString(tagSourceRPM, fmt.Sprintf("%s-%s-%s.src.rpm",
		opts.Package, opts.Version, opts.Release))
	b.addString(tagRPMVersion, "4.14.0")
	b.addString(tagPayloadFormat, "cpio")
	b.addString(tagPayloadCompress, "gzip")
	// PAYLOADFLAGS 是压缩级别。写 "9" 就真的用 9 —— 这两处必须一致，
	// 否则字段在说谎（rpm 只是把它当提示，但提示也不该是假的）。
	b.addString(tagPayloadFlags, "9")

	b.addInt32(tagSize, []uint32{uint32(rpmInstalledSize(files))})
	b.addInt32(tagArchiveSize, []uint32{uint32(len(payload))})
	b.addInt32(tagFileDigestAlgo, []uint32{8}) // 8 = SHA-256

	// --- 文件清单 ---
	//
	// RPM 把文件路径拆成"目录""基名""目录索引"三张表，
	// 而它们必须**一一对应**：任何一张表的长度不同都会让 rpm
	// 拒绝这个包，而报错只有一句"文件清单不一致"。
	dirs, dirIdx, basenames := rpmSplitPaths(files)

	var (
		sizes   []uint32
		modes   []uint16
		rdevs   []uint16
		mtimes  []uint32
		digests []string
		links   []string
		flags   []uint32
		users   []string
		groups  []string
		verify  []uint32
	)
	for _, f := range files {
		sizes = append(sizes, uint32(len(f.Data)))
		// RPM 用 POSIX 的 mode，**必须带上文件类型位**。
		//
		// 只写权限位（0755）是不够的：rpm 靠高位判断这个条目是普通文件、
		// 目录还是符号链接。少了它们，`rpm -qplv` 打出来的第一列不是
		// `-rwxr-xr-x`，而安装时的行为也没有保证 —— 我们的实现曾经就是
		// 用 `.Perm()` 把高位丢掉的。
		modes = append(modes, uint16(rpmFileTypeBits|f.Mode.Perm()))
		rdevs = append(rdevs, 0)
		// 时间戳固定为 0 —— 可复现构建。
		mtimes = append(mtimes, 0)
		sum := sha256.Sum256(f.Data)
		digests = append(digests, hex.EncodeToString(sum[:]))
		links = append(links, "") // 非符号链接
		// FILEFLAGS：1 = 配置文件，2 = 文档。
		//
		// 标成"文档"让 rpm 在 --excludedocs 时跳过它们，
		// 而 /usr/share/doc 下的东西本来就该是文档。
		if strings.HasPrefix(f.Path, "/usr/share/doc/") {
			flags = append(flags, 2)
		} else {
			flags = append(flags, 0)
		}
		users = append(users, "root")
		groups = append(groups, "root")
		// FILEVERIFYFLAGS：-1 表示用默认值。
		verify = append(verify, 0xFFFFFFFF)
	}

	b.addInt32(tagFileSizes, sizes)
	b.addInt16(tagFileModes, modes)
	b.addInt16(tagFileRDevs, rdevs)
	b.addInt32(tagFileMTimes, mtimes)
	b.addStringArray(tagFileDigests, digests)
	b.addStringArray(tagFileLinkTos, links)
	b.addInt32(tagFileFlags, flags)
	b.addStringArray(tagFileUserName, users)
	b.addStringArray(tagFileGroupName, groups)
	b.addInt32(tagFileVerifyFlags, verify)

	b.addInt32(tagDirIndexes, dirIdx)
	b.addStringArray(tagBaseNames, basenames)
	b.addStringArray(tagDirNames, dirs)

	// --- 依赖 ---
	//
	// 单一静态链接的二进制，因此没有任何文件级依赖。
	// 但 rpm 期望这几张表存在（哪怕是空的），缺了它 lint 会报错。
	b.addStringArray(tagProvideName, []string{opts.Package})
	b.addStringArray(tagRequireName, nil)
	b.addInt32(tagRequireFlags, nil)
	b.addStringArray(tagRequireVersion, nil)

	return b.bytes()
}

// rpmSplitPaths 把文件路径拆成目录表、目录索引表与基名表。
//
// 三张表的对应关系是：第 i 个文件 → basenames[i]，
// 它所在的目录是 dirnames[dirindexes[i]]。
func rpmSplitPaths(files []rpmFile) (dirs []string, dirIdx []uint32, basenames []string) {
	seen := map[string]int{}

	for _, f := range files {
		dir := path.Dir(f.Path)
		base := path.Base(f.Path)

		idx, ok := seen[dir]
		if !ok {
			idx = len(dirs)
			seen[dir] = idx
			dirs = append(dirs, dir)
		}
		dirIdx = append(dirIdx, uint32(idx))
		basenames = append(basenames, base)
	}
	return dirs, dirIdx, basenames
}

func rpmInstalledSize(files []rpmFile) int {
	var total int
	for _, f := range files {
		total += len(f.Data)
	}
	return total
}

// ---------------------------------------------------------------------------
// 签名 header
// ---------------------------------------------------------------------------

// rpmSignatureBytes 生成签名 header。
//
// 它**不签名**（那需要私钥），但必须包含校验和 —— 否则 rpm 会
// 拒绝安装。这是"包的完整性"而非"包的真实性"，而真实性由
// SHA256SUMS 与用户的核对来保证。
//
// rpmSignatureBytes 生成签名 header。
//
// # 三个长度/摘要字段的确切含义（对照真实包实测）
//
//	tagSigSize        header（**含对齐填充**）+ 载荷的字节数
//	tagSigPayloadSize 载荷**解压后**的字节数（不是文件里的长度）
//	tagSigSHA256       **header 结构**的 SHA-256，十六进制串
//	tagSigMD5          header 结构 + 载荷的 MD5（二进制 16 字节）
//
// 对三个真实包（含一个 header 长度非 8 对齐的）逐字节验算过：
// 摘要是对**未对齐**的 header 结构求的（magic + 版本 + 保留 + 索引条数 +
// 数据区长度 + 索引 + 数据区），对齐填充**不参与**；而 tagSigSize 用的是
// **对齐之后**的长度（那个包里两者差 4 字节，正好能区分）。
//
// header 是未对齐的那份（用于摘要），paddedSize 是对齐后的长度（用于长度字段）。
// 早先的实现把 SHA-256 算在了**载荷**上 —— 语义完全相反，而 rpm 校验的是
// header，于是包会被拒。
func rpmSignatureBytes(header, payload []byte, paddedSize, rawPayloadSize int) []byte {
	var b rpmHeaderBuilder

	// 这两项描述的是 header 与载荷的**总长度**，
	// 而 rpm 用它们做边界检查。
	b.addInt32(tagSigSize, []uint32{uint32(paddedSize + len(payload))})
	b.addInt32(tagSigPayloadSize, []uint32{uint32(rawPayloadSize)})

	// header 结构的 SHA-256（十六进制串，与真实包一致）。
	sum := sha256.Sum256(header)
	b.entries = append(b.entries, rpmEntry{
		Tag: tagSigSHA256, Type: rpmTypeString, Offset: b.data.Len(), Count: 1,
	})
	b.data.WriteString(hex.EncodeToString(sum[:]))
	b.data.WriteByte(0)

	// header + 载荷的 MD5。
	//
	// 这是 rpm 传统的完整性校验：从 header 结构开始到文件末尾。
	// 签名 header 本身不参与其中（它是自指的，算不了）。
	h := md5.New()
	h.Write(header)
	h.Write(payload)
	b.entries = append(b.entries, rpmEntry{
		Tag: tagSigMD5, Type: rpmTypeBin, Offset: b.data.Len(), Count: 16,
	})
	b.data.Write(h.Sum(nil))

	// 签名 header 之后要补零到 8 字节对齐 —— rpm 要求如此。
	out := b.bytes()
	if pad := (8 - len(out)%8) % 8; pad > 0 {
		out = append(out, make([]byte, pad)...)
	}
	return out
}

// ---------------------------------------------------------------------------
// cpio 载荷
// ---------------------------------------------------------------------------

// rpmCPIO 生成 gzip 压缩的 cpio(newc) 归档。
//
// newc 的头部是 **110 字节的 ASCII**：
//
//	magic(6) ino(8) mode(8) uid(8) gid(8) nlink(8) mtime(8)
//	filesize(8) devmajor(8) devminor(8) rdevmajor(8) rdevminor(8)
//	namesize(8) check(8)
//
// 全部是**十六进制文本**而不是二进制 —— 这是 newc 最容易写错的地方，
// 每个字段都是 8 位定宽十六进制。
func rpmCPIO(files []rpmFile) ([]byte, int, error) {
	var buf bytes.Buffer
	// 按 9 级压缩：PAYLOADFLAGS 里写的就是 9，两处必须一致。
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, 0, err
	}
	gz.ModTime = time.Time{}

	// 同时写一份到 raw，用来数**解压后**的字节数 —— 签名里的
	// tagSigPayloadSize 要的是它，而不是文件里那段 gzip 的长度。
	//
	// 刻意由这里返回，而不是让调用方按布局再算一遍：载荷的布局只有这一处
	// 知道，两处各算一遍迟早会漂开（今天已经在别处踩过这个形状的坑）。
	var raw bytes.Buffer
	w := io.MultiWriter(gz, &raw)

	for i, f := range files {
		if err := writeCPIOEntry(w, i+1, f); err != nil {
			return nil, 0, err
		}
	}

	// 结尾记录：名字为 TRAILER!!!，其余字段全零。
	if err := writeCPIOTrailer(w); err != nil {
		return nil, 0, err
	}

	if err := gz.Close(); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), raw.Len(), nil
}

func writeCPIOEntry(w io.Writer, ino int, f rpmFile) error {
	name := f.Path
	// cpio 里的路径**不以斜杠开头** —— rpm 装包时按相对路径处理。
	name = strings.TrimPrefix(name, "/")

	data := f.Data
	// 与 tagFileModes 同一个理由：载荷里的 mode 也要带文件类型位。
	// rpmbuild 的产物是这样，而我们曾经只写权限位。
	return writeCPIORecord(w, ino, rpmFileTypeBits|f.Mode.Perm(), int64(len(data)), name,
		func() error {
			_, err := w.Write(data)
			return err
		})
}

func writeCPIOTrailer(w io.Writer) error {
	return writeCPIORecord(w, 0, 0, 0, "TRAILER!!!", nil)
}

// writeCPIORecord 写一条 cpio 记录（含对齐填充）。
func writeCPIORecord(w io.Writer, ino int, mode os.FileMode,
	size int64, name string, body func() error) error {

	// namesize 包含结尾的 NUL。
	nameSize := len(name) + 1

	hdr := fmt.Sprintf(
		"070701"+ // magic
			"%08X"+ // ino
			"%08X"+ // mode
			"%08X"+ // uid
			"%08X"+ // gid
			"%08X"+ // nlink
			"%08X"+ // mtime
			"%08X"+ // filesize
			"%08X"+ // devmajor
			"%08X"+ // devminor
			"%08X"+ // rdevmajor
			"%08X"+ // rdevminor
			"%08X"+ // namesize
			"%08X", // check
		ino, uint32(mode), 0, 0, 1,
		0, // mtime 固定为 0 —— 可复现构建
		uint32(size), 0, 0, 0, 0, nameSize, 0)

	if len(hdr) != 110 {
		return fmt.Errorf("cpio 头部长度是 %d，必须是 110", len(hdr))
	}
	if _, err := io.WriteString(w, hdr); err != nil {
		return err
	}
	if _, err := io.WriteString(w, name); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0}); err != nil {
		return err
	}

	// 头部 + 名字补齐到 4 字节边界。
	if pad := (4 - (110+nameSize)%4) % 4; pad > 0 {
		if _, err := w.Write(make([]byte, pad)); err != nil {
			return err
		}
	}

	if body != nil {
		if err := body(); err != nil {
			return err
		}
	}
	// 内容也补齐到 4 字节边界。
	if pad := (4 - int(size)%4) % 4; pad > 0 {
		if _, err := w.Write(make([]byte, pad)); err != nil {
			return err
		}
	}
	return nil
}

// Description 返回包的详细描述。
//
// 它是 i18n 字段，会显示在 `rpm -qi` 里 —— 因此写成一句人话，
// 而不是重复 Summary。
func (o RpmOptions) Description() string {
	return "ISC 接入编排器内核：把一台普通电脑变成可从公网访问的服务器。\n" +
		"跟踪 IPv6 前缀变化并自动更新 DNS 记录、按需开放防火墙、" +
		"内置反向代理与自动 HTTPS。\n" +
		"安装后运行 'isc init' 开始引导。"
}

// ---------------------------------------------------------------------------
// 文件清单与校验
// ---------------------------------------------------------------------------

// rpmPayloadFiles 组装要打进包里的文件清单。
//
// **必须跳过可执行文件本身**：暂存目录里同时有二进制与文档，而
// "把目录里的文件全拷过去"会把 18MB 的二进制也塞进文档目录 ——
// 二进制因此在包里存了两份。
//
// 这个缺陷在 deb 那边是解包核对时发现的（包从 15.5MB 降到 7.41MB）。
// 这里从一开始就避免它。
func rpmPayloadFiles(opts RpmOptions) ([]rpmFile, error) {
	bin, err := os.ReadFile(opts.BinaryPath)
	if err != nil {
		return nil, err
	}

	files := []rpmFile{{
		// 0755 是必须的：装到 /usr/bin 之后没有执行位就是一个
		// 装得上、跑不起来的包。
		Path: rpmBinPath,
		Mode: 0o755,
		Data: bin,
	}}

	// **必须用 filepath.Base 而不是 path.Base**。
	//
	// opts.BinaryPath 是**宿主路径**（Windows 上是 C:\...\stage\isc），
	// 而 path 只认 "/" 作分隔符 —— 用它处理宿主路径会返回整个字符串，
	// 于是 `e.Name() == binName` 永远不成立，二进制被复制进文档目录。
	//
	// 这一点是测试抓到的：那份"不把二进制塞进文档目录"的测试
	// 立刻失败了。它与 deb 那边解包核对时发现的缺陷是同一个，
	// 只是这次的成因不同（那边是压根没排除，这边是用错了包）。
	//
	// 反过来，本文件里处理**安装路径**（/usr/bin/isc）时用 path 是对的：
	// 那些路径永远是 Unix 路径，与构建机无关。
	binName := filepath.Base(opts.BinaryPath)
	entries, err := os.ReadDir(opts.DocDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == binName {
			continue
		}
		byt, err := os.ReadFile(filepath.Join(opts.DocDir, e.Name()))
		if err != nil {
			return nil, err
		}
		files = append(files, rpmFile{
			Path: rpmDocDir + "/" + e.Name(),
			Mode: 0o644,
			Data: byt,
		})
	}

	// 排序：RPM 的文件清单顺序影响"哪个文件先被装"，
	// 而不排序会让同样的输入产出不同的包。
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

// validateRpmOptions 在动手之前检查参数。
func validateRpmOptions(opts RpmOptions) error {
	if strings.TrimSpace(opts.Package) == "" {
		return fmt.Errorf("rpm: 缺少包名")
	}
	if strings.ContainsAny(opts.Package, " \t\n") {
		return fmt.Errorf("rpm: 包名不能含空白: %q", opts.Package)
	}
	// RPM 的版本号**不能含连字符** —— 那是它分隔 Version 与 Release 的字符。
	//
	// 而我们的版本可能来自 `git describe`（例如 "1.0.0-rc1"），
	// 因此这里挡下来并提示解决方式，而不是让 rpm 在安装时报
	// 一句含糊的"版本号非法"。
	if strings.Contains(opts.Version, "-") {
		return fmt.Errorf(
			"rpm: 版本号不能含连字符（%q）—— RPM 用连字符分隔版本与发布号。"+
				"请把连字符换成下划线或点", opts.Version)
	}
	if strings.TrimSpace(opts.Release) == "" {
		return fmt.Errorf("rpm: 缺少发布号（Release）")
	}
	if strings.ContainsAny(opts.Release, " \t\n-") {
		return fmt.Errorf("rpm: 发布号不能含空白或连字符: %q", opts.Release)
	}
	if strings.TrimSpace(opts.Arch) == "" {
		return fmt.Errorf("rpm: 缺少架构")
	}
	if strings.TrimSpace(opts.Summary) == "" {
		return fmt.Errorf("rpm: 缺少摘要")
	}
	// License 是 rpm 的必填字段，缺了它 `rpm -qp --qf` 会报错。
	if strings.TrimSpace(opts.License) == "" {
		return fmt.Errorf("rpm: 缺少许可证字段（RPM 要求非空）")
	}
	if _, err := os.Stat(opts.BinaryPath); err != nil {
		return fmt.Errorf("rpm: 可执行文件不可读: %w", err)
	}
	if _, err := os.Stat(opts.DocDir); err != nil {
		return fmt.Errorf("rpm: 文档目录不可读: %w", err)
	}
	return nil
}
