//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// 本文件生成 macOS 的 .pkg 安装包。
//
// # 为什么用 pkgbuild 而不是像 deb 那样手写
//
// `.deb` 是手写的（ar 归档 + tar，格式简单到几十行能写完，且任何平台都能
// 生成）。`.pkg` 不是：它是一个 **xar** 归档，里面装着 cpio.gz 的载荷、
// 一份必须按 Apple 规范渲染的 Distribution XML、以及一个存放文件校验和的
// Bom（Bill of Materials）—— 那个 Bom 是**二进制格式**，格式没有公开规范，
// 只能靠逆向。
//
// 而 pkgbuild 是 macOS 自带的（不需要安装任何东西），所以依赖它没有代价：
// **这个函数本来就只能在 macOS 上跑。**
//
// # 为什么只做 component package 而不做 distribution package
//
// `pkgbuild` 产出的是"组件包"，双击即可安装；`productbuild` 再把它包成
// 带安装界面的"分发包"。对命令行工具来说前者就够 —— 多一层包装只增加了
// 一个会失败的环节，而收益是一个用户看一眼就点掉的界面。

// BuildPkg 生成 .pkg。
//
// macOS 的打包工具不在时返回 ErrPkgToolsMissing —— 调用方据此**跳过**
// 而不是失败：打不出 pkg 不影响其余产物。
func BuildPkg(opts PkgOptions) error {
	pkgbuild, err := exec.LookPath("pkgbuild")
	if err != nil {
		return ErrPkgToolsMissing
	}

	version, err := pkgVersion(opts.Version)
	if err != nil {
		return err
	}

	// 没设 SOURCE_DATE_EPOCH（开发构建）时用"现在"。
	stamp := opts.Stamp
	if stamp.IsZero() {
		stamp = time.Now().UTC()
	}

	dir, err := os.MkdirTemp("", "isc-pkg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // 临时目录

	// pkgbuild 要的是**一棵目录树**（root），而 `--root` 里的相对路径
	// **原样**成为安装后的路径（相对 `--install-location`）。
	//
	// 因此这里搭出完整的 `/usr/local/bin/isc`，并把 `--install-location`
	// 设成 `/`（见下面的 pkgbuild 调用）。
	//
	// # 这里踩过一个只有"真的装一遍"才会暴露的坑
	//
	// 原来树是 `root/usr/local/bin/isc`，而 `--install-location` 写的是
	// `/usr/local/bin` —— 两者**叠加**，于是文件装到了
	//
	//	/usr/local/bin/usr/local/bin/isc
	//
	// 而安装过程一路成功、`pkgutil --payload-files` 也显示
	// `./usr/local/bin/isc`（那是载荷里的相对路径，不是最终落点）。
	// CI 上加的那条"装完之后看文件在不在"才把它抓出来。
	//
	// 两种写法都对（树平铺 + install-location 指到目录，或者树带完整路径 +
	// install-location 为 /），但不能混用。这里选后者：载荷里带完整路径，
	// 与大多数命令行工具的 .pkg 一致。
	root, err := stagePkgTree(dir, opts.BinaryPath)
	if err != nil {
		return err
	}

	out := filepath.Join(dir, "isc.pkg")

	// 暂存树的时间戳必须固定。
	//
	// pkgbuild 会把树里每个条目的 mtime 写进 **Payload 与 Bom**，而 `dir`
	// 是刚建的临时目录 —— 不固定的话，同一个 SOURCE_DATE_EPOCH 下两次
	// 构建出来的载荷都不同。
	//
	// 实测它确实管用：固定之后，两次构建的 Payload / Bom / PackageInfo
	// **逐字节相同**（解出来逐成员比对过）。
	//
	// # 但 .pkg 整体仍然不是逐字节可复现的（已知，且刻意不掩盖）
	//
	// xar 的目录表（TOC）里还有一批与源码无关、而 pkgbuild 不给开关的字段：
	//
	//	creation-time      归档创建时刻
	//	inode/deviceno     构建机上临时文件的 inode 与设备号
	//	uid/user/gid/group 构建者的用户名（CI 上是 runner，本机上是开发者）
	//	atime/mtime/ctime  同一批时间
	//
	// 试过把它们改写掉，**行不通**：xar 的校验覆盖到了压缩后的 TOC 字节，
	// 只要重新压缩 TOC，`xar -t` 就报 "Checksums do not match!"。
	// 实测三种情形全部失败：TOC 内容一个字符不改、只重新压缩；改一个时间
	// 值；只把 uid 改成 0。也就是说"改写 TOC"这条路得连 xar 的校验和重算
	// 规则一起实现，而那是另一件事的工程量。
	//
	// 结论：.pkg **不进**"逐字节可复现"的承诺，其余 11 个产物进
	//（CI 的可复现构建跑在 Linux 上，本来也只比对那 11 个）。
	if err := fixTreeTimes(root, stamp); err != nil {
		return err
	}

	cmd := exec.Command(pkgbuild, pkgbuildArgs(root, version, out)...)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pkgbuild 失败: %w\n%s", err, combined)
	}

	return copyFile(out, opts.OutPath)
}
