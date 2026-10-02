//go:build darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	dir, err := os.MkdirTemp("", "isc-pkg-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir) //nolint:errcheck // 临时目录

	// pkgbuild 要的是**一棵目录树**（root），它会把树里的东西按
	// --install-location 装到目标机器上。因此这里搭出与安装位置一致的
	// 层级，而不是把二进制丢在一个平铺的目录里。
	//
	// 层级错了的表现是"装完之后命令找不到" —— 而安装过程本身是成功的，
	// 所以不看安装位置是发现不了的。
	binDir := filepath.Join(dir, "root", strings.TrimPrefix(pkgInstallDir, "/"))
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	binary := filepath.Join(binDir, "isc")
	if err := copyFile(opts.BinaryPath, binary); err != nil {
		return err
	}
	// 可执行位必须显式设：Windows 上交叉编译出的产物没有执行位这个概念
	// （那个坑在 tar 打包时已经踩过一次）。
	if err := os.Chmod(binary, 0o755); err != nil {
		return err
	}

	out := filepath.Join(dir, "isc.pkg")
	cmd := exec.Command(pkgbuild,
		"--root", filepath.Join(dir, "root"),
		"--identifier", pkgIdentifier,
		"--version", version,
		"--install-location", pkgInstallDir,
		out,
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("pkgbuild 失败: %w\n%s", err, combined)
	}

	return copyFile(out, opts.OutPath)
}
