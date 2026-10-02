package main

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件是 .msi 生成的**共享声明**：类型、常量、哨兵错误。
//
// 实现分在两个平台文件里：msi_windows.go 真正调 WiX，msi_other.go 返回
// ErrWixMissing。这样分是因为**发布脚本必须在任何平台都能编译** ——
// 在 Linux 上跑 `go run ./scripts/release` 打 Linux 的包是完全正常的用法，
// 那时"打不出 MSI"是事实，不是编译错误。

// MSIOptions 是生成 .msi 所需的输入。
type MSIOptions struct {
	BinaryPath  string // 要打包的 isc.exe
	Version     string // 版本号（MSI 要求 x.y.z 形式）
	UpgradeCode string // 跨版本标识，升级时靠它认亲
	OutPath     string // 产物路径
}

// msiWixVersion 是要求的 WiX 版本，也是已知不受 OSMF 约束的上限。
//
// WiX v7 起要求接受 Open Source Maintenance Fee 的 EULA（可能涉及付费）。
// 那是使用者要做的**法律决定**，不该由构建脚本替他接受。
const msiWixVersion = "5.0.2"

// msiUpgradeCode 是这个产品的**跨版本标识**。
//
// 它必须**永远不变**：Windows Installer 靠它判断"即将安装的这个包
// 是不是同一个产品的另一个版本"，从而走升级而不是并存安装。
// 改了它的后果是同名产品的两份并存，而卸载只能去掉其中一份。
//
// 这个值是随机生成的，与任何其它项目无关。
const msiUpgradeCode = "7C4E1B92-3D6A-4F58-9E21-8A5B0C7D4E63"

// ErrWixMissing 表示 PATH 上没有 wix。
//
// 调用方靠它区分"**这台机器打不了 MSI**"与"打 MSI 时出错了"：
// 前者该跳过并告诉用户怎么装，后者该报出来。
var ErrWixMissing = errors.New("wix")

// msiVersion 把内核的版本串收敛成 MSI 接受的 x.y.z。
func msiVersion(v string) (string, error) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	// 去掉 -dirty / -rc1 这类后缀。
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}

	parts := strings.Split(v, ".")
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	parts = parts[:3]

	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("版本号 %q 无法转成 MSI 的 x.y.z 形式", v)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("版本号 %q 含非数字段 %q，"+
					"而 MSI 只接受 x.y.z", v, p)
			}
		}
	}
	return strings.Join(parts, "."), nil
}
