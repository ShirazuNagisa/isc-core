//go:build darwin

package platform

import "runtime"

// 本文件是 macOS 平台的后端装配点。
//
// 各后端的落点（M0 的占位实现已随里程碑逐个替换完）：
//
//	Firewall       → 已实现（pf，见 firewall_darwin.go）
//	ServiceManager → 已实现（launchd，见 service_darwin.go）
//	IPMonitor      → 已实现（可移植轮询，见 ipmon.go）
//	SecretStore    → 已实现（Keychain，见 secret_unix.go）
//	Transport      → 已实现（Unix 套接字，见 transport_unix.go）
//	LowPortBinder  → macOS 不限制普通进程绑定低端口
//
// ⚠️ 这里曾经**漏接**：service_darwin.go 里 launchd 后端写好了，而装配点
// 仍返回 stub，于是 `isc service install` 在 macOS 上报"将在 M5 实现"。
// 这个缺口没有任何测试看得见 —— 单元测试测的是 plist 渲染（与平台无关），
// 而"装配点接对了没有"没人检查。现在由 TestServiceManagerIsWired 守住。
//
// 注意：macOS 无法在开发机（Windows）上验证，依赖 CI 的 macos-latest runner
// 与一台真机/虚拟机。见 docs/PLAN.md R8。
//
// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall:       newPfFirewall(),
		ServiceManager: newServiceManager(),
		IPMonitor:      newPollingIPMonitor(),
		SecretStore:    newPlatformSecretStore(dataRoot),
		Transport:      newLocalTransport(),
		LowPortBinder:  permissiveLowPortBinder{backend: "darwin-native"},
		Processes:      newProcessController(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
}
