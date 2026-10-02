package main

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件是 macOS .pkg 生成的**共享声明**：类型、常量、哨兵错误。
//
// 与 .msi 完全同一套结构，理由也一样：**发布脚本必须在任何平台都能编译**。
// 在 Linux 上跑 `go run ./scripts/release` 打 Linux 的包是完全正常的用法，
// 那时"打不出 pkg"是事实，不是编译错误。

// PkgOptions 是生成 .pkg 所需的输入。
type PkgOptions struct {
	BinaryPath string // 要打包的可执行文件（Mach-O）
	Version    string // 版本号
	OutPath    string // 产物路径
}

// pkgIdentifier 是这个包的**跨版本标识**。
//
// 与 MSI 的 UpgradeCode 同一个角色，也必须**永远不变**：
// macOS 的安装器靠它判断"这是不是同一个产品的另一个版本"，
// 从而走升级而不是并存。改了它，用户机器上会出现两份，
// 而 receipts 数据库里会有两条互不相干的记录。
const pkgIdentifier = "com.shirazunagisa.isc"

// pkgInstallDir 是安装位置。
//
// `/usr/local/bin` 而不是 `/Applications`：这是一个**命令行工具**，
// 不是 GUI 应用；而 macOS 上 CLI 工具的惯例位置就是这里，
// 且它默认在 PATH 里。
const pkgInstallDir = "/usr/local/bin"

// ErrPkgToolsMissing 表示这台机器上没有 macOS 的打包工具。
//
// 与 ErrWixMissing 同一个角色：调用方靠它区分"**这台机器打不了 pkg**"
// 与"打 pkg 时出错了" —— 前者该跳过并说明原因，后者该报出来。
var ErrPkgToolsMissing = errors.New("pkgbuild")

// pkgVersion 把内核的版本串收敛成 macOS 安装器接受的形状。
//
// 与 MSI 的规则不同：macOS 的版本号**比较宽松**（可以带字母），但要求
// **首字符是数字** —— 而内核的版本串可能带 `v` 前缀。因此这里只做两件事：
// 去掉 `v` 前缀，去掉构建元数据（`+` 之后的部分，它不属于版本）。
//
// 刻意**不**做 MSI 那样的三段数字校验：那会把 `0.2.0-rc1` 这种合法的
// macOS 版本号也拒掉。
func pkgVersion(v string) (string, error) {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return "", fmt.Errorf("版本号为空，无法生成 pkg")
	}
	if v[0] < '0' || v[0] > '9' {
		return "", fmt.Errorf("版本号 %q 首字符不是数字，"+
			"而 macOS 安装器要求它必须以数字开头", v)
	}
	return v, nil
}
