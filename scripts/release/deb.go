package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 本文件生成 Debian 安装包（.deb）。
//
// # 为什么手写而不是用 dpkg-deb
//
// 构建要在三个平台上都能跑，而 dpkg-deb 只在 Debian 系上存在 ——
// 用它就意味着"在 Windows 上打不出 Linux 的包"，而交叉编译恰恰是
// 本项目发布方式的常态。
//
// deb 的格式本身很简单：
//
//	ar 归档，含三个成员：
//	  debian-binary   "2.0\n"
//	  control.tar.gz  元数据与安装脚本
//	  data.tar.gz     要装进系统的文件
//
// ar 的格式也简单到可以手写（全局头 + 每个成员 60 字节头 + 数据），
// 而手写它换来的是**产物可以被逐字节解析验证** —— 这一点比
// "调用一个本机没有的工具"有价值得多。

const (
	debArMagic    = "!<arch>\n"
	debVersion    = "2.0"
	debMemberMode = "100644"
)

// DebOptions 是生成 deb 的参数。
type DebOptions struct {
	// Package / Version / Arch 是控制字段。
	Package string
	Version string
	// Arch 是 Debian 的架构名（amd64 / arm64），不是 GOARCH 之外的别的
	// 东西 —— 恰好两者同名。
	Arch string
	// Maintainer 是维护者字段。Debian 要求它有值。
	Maintainer string
	// Description 是包描述的第一行（摘要）。
	Description string
	// Homepage 是项目地址。
	Homepage string

	// BinaryPath 是已经编译好的可执行文件。
	BinaryPath string
	// DocDir 是随包分发的文档所在目录（许可证、说明）。
	DocDir string
}

// BuildDeb 生成一个 .deb 文件。
//
// 装进系统的路径遵循 Debian 惯例：
//
//	/usr/bin/isc                     可执行文件
//	/usr/share/doc/isc/…             文档与许可证
//
// 刻意**不**放 systemd unit：服务由 `isc service install` 注册，
// 而那让用户可以自己选择要不要开机自启。包一装上就自启，
// 对"我只是想试试"的人来说是个不愉快的意外。
func BuildDeb(outPath string, opts DebOptions) error {
	if err := validateDebOptions(opts); err != nil {
		return err
	}

	data, err := debDataTar(opts)
	if err != nil {
		return fmt.Errorf("生成 data.tar.gz 失败: %w", err)
	}
	control, err := debControlTar(opts)
	if err != nil {
		return fmt.Errorf("生成 control.tar.gz 失败: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // 只写文件

	if _, err := io.WriteString(f, debArMagic); err != nil {
		return err
	}
	for _, m := range []struct {
		name string
		data []byte
	}{
		{"debian-binary", []byte(debVersion + "\n")},
		{"control.tar.gz", control},
		{"data.tar.gz", data},
	} {
		if err := writeArMember(f, m.name, m.data); err != nil {
			return err
		}
	}

	return f.Sync()
}

// writeArMember 写一个 ar 成员。
//
// ar 的成员头是**定长 60 字节**的 ASCII：
//
//	名字(16) 时间(12) 属主(6) 属组(6) 模式(8) 长度(10) 魔数(2)
//
// 长度与时间都是十进制文本而不是二进制 —— 这是 ar 格式最容易写错的
// 地方，而写错之后 `dpkg-deb` 只会说一句"格式错误"。
func writeArMember(w io.Writer, name string, data []byte) error {
	if len(name) > 16 {
		return fmt.Errorf("ar 成员名过长: %q", name)
	}

	// 时间戳固定为 0（Unix 纪元）。
	//
	// 与 tar/zip 侧同样的理由：可复现构建。当前时间会让同一份源码
	// 产出不同的包，而"校验和一致"就无法证明"两个产物来自同一份源码"。
	hdr := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d`\n",
		name, 0, 0, 0, debMemberMode, len(data))

	if len(hdr) != 60 {
		return fmt.Errorf("ar 成员头长度是 %d，必须是 60", len(hdr))
	}
	if _, err := io.WriteString(w, hdr); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}

	// 奇数长度要补一个换行：ar 要求成员数据按 2 字节对齐。
	if len(data)%2 == 1 {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	return nil
}

// debDataTar 生成 data.tar.gz。
func debDataTar(opts DebOptions) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{} // 可复现
	tw := tar.NewWriter(gz)

	bin, err := os.ReadFile(opts.BinaryPath)
	if err != nil {
		return nil, err
	}

	// 目录条目必须在前，且一个都不能少（见 addTarDir 的说明）。
	// 顺序是"从根往下"：dpkg 按 tar 里的顺序装，父目录得先存在。
	for _, dir := range []string{
		"./",
		"./usr/",
		"./usr/bin/",
		"./usr/share/",
		"./usr/share/doc/",
		"./usr/share/doc/" + opts.Package + "/",
	} {
		if err := addTarDir(tw, dir, 0o755); err != nil {
			return nil, err
		}
	}

	// 可执行文件：0755 是必须的 —— 装到 /usr/bin 之后没有执行位
	// 就是一个装得上、跑不起来的包。
	if err := addTarBytes(tw, "./usr/bin/"+binaryName, 0o755, bin); err != nil {
		return nil, err
	}

	// 文档：GPLv3 要求分发时附带许可证。
	//
	// **必须跳过可执行文件**：暂存目录里同时有二进制与文档，而
	// "把目录里的文件全拷过去"会把 18MB 的二进制也塞进文档目录 ——
	// 二进制因此在包里存了两份。
	//
	// 这个缺陷是解包核对时看出来的：压缩把体积差掩盖了
	//（15.5MB 的包 vs 18.3MB 的二进制），只看包大小发现不了。
	binName := filepath.Base(opts.BinaryPath)
	docs, err := os.ReadDir(opts.DocDir)
	if err != nil {
		return nil, err
	}
	for _, d := range docs {
		if d.IsDir() || d.Name() == binName {
			continue
		}
		byt, err := os.ReadFile(filepath.Join(opts.DocDir, d.Name()))
		if err != nil {
			return nil, err
		}
		name := "./usr/share/doc/" + opts.Package + "/" + d.Name()
		if err := addTarBytes(tw, name, 0o644, byt); err != nil {
			return nil, err
		}
	}

	// 版权文件是 Debian 的**硬性要求**：没有它 lintian 会报错，
	// 而某些工具会拒绝安装。
	copyright := debCopyright(opts)
	if err := addTarBytes(tw,
		"./usr/share/doc/"+opts.Package+"/copyright", 0o644, []byte(copyright)); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// debControlTar 生成 control.tar.gz。
func debControlTar(opts DebOptions) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	gz.ModTime = time.Time{}
	tw := tar.NewWriter(gz)

	control := debControlFile(opts)
	// control 文件的权限是 0644，而**属主必须是 root** ——
	// dpkg 会检查它，非 root 属主会让安装失败。
	if err := addTarBytes(tw, "./control", 0o644, []byte(control)); err != nil {
		return nil, err
	}

	// md5sums 是可选的，但有它 lintian 更安静，而它对用户也有用
	//（dpkg -V 能靠它发现被改动的文件）。
	if err := addTarBytes(tw, "./md5sums", 0o644, []byte(debMD5Sums(opts))); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// debControlFile 生成 control 文件的内容。
//
// 字段顺序有约定（Debian Policy §5.3）：先 Package，再 Version，
// 然后是 Architecture。dpkg 本身不强制顺序，但工具链的其它部分
// （lintian、各种解析脚本）假设了这个顺序。
func debControlFile(opts DebOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Package: %s\n", opts.Package)
	fmt.Fprintf(&b, "Version: %s\n", debVersionString(opts.Version))
	fmt.Fprintf(&b, "Architecture: %s\n", opts.Arch)
	fmt.Fprintf(&b, "Maintainer: %s\n", opts.Maintainer)
	if opts.Homepage != "" {
		fmt.Fprintf(&b, "Homepage: %s\n", opts.Homepage)
	}
	// Section / Priority 是"建议字段"，但缺了会被 lintian 提示。
	b.WriteString("Section: net\n")
	b.WriteString("Priority: optional\n")
	// 单一静态链接的可执行文件，没有任何运行时依赖。
	//
	// 这一行是"开箱可用"的技术依据：装了它不需要再装别的东西。
	b.WriteString("Installed-Size: " + debInstalledSize(opts) + "\n")
	fmt.Fprintf(&b, "Description: %s\n", opts.Description)

	return b.String()
}

// debVersionString 把版本号转成 Debian 认的形式。
//
// Debian 的版本号不能以非数字开头，而我们的版本可能来自
// `git describe`（例如 "3994090-dirty"）。加一个 "0~" 前缀把
// 它变成合法版本，同时保证它**排序在正式版本之前** ——
// 这是 Debian 里 `~` 的含义，用它是刻意的：
// 一个 git 快照不该被当成正式版。
func debVersionString(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "0~unknown"
	}
	// 允许的字符：字母数字与 . + - ~ :
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '.', r == '+', r == '-', r == '~', r == ':':
			return r
		default:
			return '-'
		}
	}, v)

	// 必须以数字开头。
	if cleaned[0] < '0' || cleaned[0] > '9' {
		return "0~" + cleaned
	}
	return cleaned
}

// debCopyright 生成 Debian 的版权文件。
//
// 它是**硬性要求**：Debian Policy §12.5 要求每个二进制包都带一份
// 版权声明与分发许可。少了它，某些工具会拒绝安装这个包。
func debCopyright(opts DebOptions) string {
	var b strings.Builder
	fmt.Fprintf(&b, "本包由 ISC-Core 的发布构建生成。\n\n")
	fmt.Fprintf(&b, "Upstream-Name: %s\n", opts.Package)
	if opts.Homepage != "" {
		fmt.Fprintf(&b, "Source: %s\n", opts.Homepage)
	}
	b.WriteString("\nFiles: *\n")
	b.WriteString("License: GPL-3.0-or-later\n")
	b.WriteString(" 本程序是自由软件：你可以按自由软件基金会发布的 GNU 通用公共许可\n")
	b.WriteString(" 协议的条款重新分发和/或修改它，无论是协议的第三版，还是（按你\n")
	b.WriteString(" 的选择）任何更新的版本。\n")
	b.WriteString(" .\n")
	b.WriteString(" 本程序的分发是希望它有用，但没有任何担保，甚至没有适销性或\n")
	b.WriteString(" 特定用途适用性的默示担保。\n")
	b.WriteString(" .\n")
	b.WriteString(" 完整的许可证文本见 /usr/share/doc/" + opts.Package + "/LICENSE。\n")
	b.WriteString(" 第三方组件的许可证见 THIRD_PARTY_NOTICES.md。\n")
	return b.String()
}

// debMD5Sums 生成 md5sums 文件。
//
// 格式是 `<md5>  <路径>`（两空格），路径**不带**开头的 ./。
func debMD5Sums(opts DebOptions) string {
	// 这里刻意只列可执行文件：文档文件的校验和意义不大，
	// 而 dpkg -V 最关心的是"二进制有没有被改过"。
	sum, err := md5File(opts.BinaryPath)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s  usr/bin/%s\n", sum, binaryName)
}

func debInstalledSize(opts DebOptions) string {
	var total int64
	if info, err := os.Stat(opts.BinaryPath); err == nil {
		total += info.Size()
	}
	if entries, err := os.ReadDir(opts.DocDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				total += info.Size()
			}
		}
	}
	// Debian 用 KB 为单位。
	return fmt.Sprintf("%d", (total+1023)/1024)
}

// addTarBytes 往 tar 里加一个内存里的文件。
//
// 时间戳统一为零值：可复现构建。
// addTarDir 写入一个**目录条目**。
//
// # 为什么必须有目录条目
//
// dpkg 与 GNU tar 在这件事上不一样：tar 解包时会顺手创建缺失的父目录，
// **dpkg 不会**。少了它们，安装会在解第一个文件时报
//
//	unable to create '/usr/share/doc/isc/LICENSE.dpkg-new'
//	(while processing './usr/share/doc/isc/LICENSE'): No such file or directory
//
// 而 `dpkg-deb --contents` 只看清单、不真装，因此完全看不出来。
// 这个缺陷在本项目里是 CI 的「核对 .deb」第一次真正跑到 `dpkg -i` 时才
// 暴露的 —— 在那之前那一步总是死在更前面。
//
// 目录名必须**以 / 结尾**：那是 tar 表达"这是目录"的方式（typeflag '5'）。
func addTarDir(tw *tar.Writer, name string, mode int64) error {
	hdr := &tar.Header{
		Typeflag: tar.TypeDir,
		Name:     name,
		Mode:     mode,
		ModTime:  time.Time{},
		Uid:      0,
		Gid:      0,
	}
	return tw.WriteHeader(hdr)
}

func addTarBytes(tw *tar.Writer, name string, mode int64, data []byte) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Time{},
		// 属主固定为 root(0)/root(0)：装进系统的东西属主不该是
		// 构建机上的某个用户，而 dpkg 会按这个字段设置属主。
		Uid: 0,
		Gid: 0,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// validateDebOptions 在动手之前检查参数。
//
// 这些字段少了任何一个，产出的包都会在**安装时**才失败，
// 而 dpkg 的报错通常只有一句"control 文件缺少字段"。
func validateDebOptions(opts DebOptions) error {
	if strings.TrimSpace(opts.Package) == "" {
		return fmt.Errorf("deb: 缺少包名")
	}
	if strings.ContainsAny(opts.Package, " \t\n") {
		return fmt.Errorf("deb: 包名不能含空白: %q", opts.Package)
	}
	if strings.TrimSpace(opts.Version) == "" {
		return fmt.Errorf("deb: 缺少版本号")
	}
	if strings.TrimSpace(opts.Arch) == "" {
		return fmt.Errorf("deb: 缺少架构")
	}
	// Maintainer 是 Debian 的必填字段。
	if strings.TrimSpace(opts.Maintainer) == "" {
		return fmt.Errorf("deb: 缺少维护者（Debian 要求该字段非空）")
	}
	if strings.TrimSpace(opts.Description) == "" {
		return fmt.Errorf("deb: 缺少描述")
	}
	if _, err := os.Stat(opts.BinaryPath); err != nil {
		return fmt.Errorf("deb: 可执行文件不可读: %w", err)
	}
	if _, err := os.Stat(opts.DocDir); err != nil {
		return fmt.Errorf("deb: 文档目录不可读: %w", err)
	}
	return nil
}
