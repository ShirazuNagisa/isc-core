//go:build !darwin

package main

// 本文件是 .pkg 生成在非 macOS 平台上的桩。
//
// 与 msi_other.go 同一个理由：**发布脚本必须在任何平台都能编译**。
// 在 Windows 或 Linux 上跑 `go run ./scripts/release` 打自己平台的包
// 是完全正常的用法，那时"打不出 pkg"是一个事实（它本来就是给 macOS
// 用户的），不是编译错误。
//
// 返回 ErrPkgToolsMissing 而不是别的：调用方对它已经有正确的处理 ——
// 跳过并说明这台机器打不了。**把"平台不支持"与"没装工具"收敛成同一个
// 信号**，因为对调用方来说该做的事完全一样。
//
// （macOS 上不存在"没装 pkgbuild"这种情况：它是系统自带的。
// 那个哨兵错误名在 darwin 上仍然用得上，只是触发条件几乎不可能出现。）
func BuildPkg(PkgOptions) error { return ErrPkgToolsMissing }
