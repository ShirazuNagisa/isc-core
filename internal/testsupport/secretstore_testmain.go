package testsupport

import (
	"os"
	"testing"
)

// 这两个字面量与 internal/platform 的 EnvSecretStore / SecretStoreFile 一致。
//
// 为什么不直接引用它们：本包被 internal/platform 自己的测试文件引用，
// 而引用 platform 会形成**测试里的导入环**（platform[test] → testsupport →
// platform）。platform 那边有一条测试钉住这两个值，改了一边就会失败。
const (
	envSecretStore  = "ISC_SECRET_STORE"
	secretStoreFile = "file"
)

// IsolateSecretStore 让**整个测试二进制**不去碰开发机的系统钥匙串。
//
// 在 TestMain 里调用它：
//
//	func TestMain(m *testing.M) { os.Exit(testsupport.IsolateSecretStore(m)) }
//
// # 为什么必须这么做
//
// 三个理由，第二个是硬的：
//
//  1. macOS 上跑一次 `go test ./...`，每个以临时数据目录启动内核的用例都会
//     往登录钥匙串里写一条条目 —— 实测 60 条；而数据目录一删，它们就成了
//     永远够不着的垃圾；
//  2. 更早的版本里，这些测试会**覆盖真实安装的主密钥**（都写同一条全局
//     条目），而那等价于把用户已有的全部凭据变成解不开的密文（见 D23）；
//  3. 无人值守的 CI 上钥匙串可能被锁住，于是"写不进去"会在测试**中途**才
//     暴露，而那种失败看起来像内核坏了。
//
// 需要**真的**验证钥匙串后端的测试，应当自己构造后端（同包内可直接构造）
// 并在没有它时跳过 —— 那是少数几条，而且它们自己清理。
func IsolateSecretStore(m *testing.M) int {
	if os.Getenv(envSecretStore) == "" {
		_ = os.Setenv(envSecretStore, secretStoreFile)
	}
	return m.Run()
}
