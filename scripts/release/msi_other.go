//go:build !windows

package main

// 本文件是 .msi 生成在非 Windows 平台上的桩。
//
// 它存在的原因是**发布脚本必须在任何平台都能编译**：在 Linux 上跑
// `go run ./scripts/release` 打 Linux 的包是完全正常的用法，那时
// "打不出 MSI"是一个事实（它本来就是给 Windows 用户的），不是编译错误。
//
// 返回 ErrWixMissing 而不是别的：调用方对它已经有正确的处理 ——
// 跳过、并告诉用户这个产物要什么工具。**把"平台不支持"与"没装工具"
// 收敛成同一个信号**，因为对调用方来说该做的事完全一样。
func BuildMSI(MSIOptions) error { return ErrWixMissing }
