//go:build darwin

package platform

import "runtime"

// 本文件是 macOS 平台的后端装配点。
//
// M0 阶段全部返回占位实现；随里程碑推进逐个替换：
//
//	Firewall       → M3  pf
//	ServiceManager → M5  launchd
//	IPMonitor      → M2  getifaddrs + 路由 socket 监听
//	SecretStore    → 已实现（Keychain，见 secret_unix.go）
//	Transport      → 已实现（Unix 套接字，见 transport_unix.go）
//	LowPortBinder  → macOS 不限制普通进程绑定低端口
//
// 注意：macOS 无法在开发机（Windows）上验证，依赖 CI 的 macos-latest runner
// 与一台真机/虚拟机。见 docs/PLAN.md R8。
//
// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall: newPfFirewall(),
		ServiceManager: newUnsupportedServiceManager(
			"launchd 后端将在 M5 实现"),
		IPMonitor:     newPollingIPMonitor(),
		SecretStore:   newPlatformSecretStore(dataRoot),
		Transport:     newLocalTransport(),
		LowPortBinder: permissiveLowPortBinder{backend: "darwin-native"},
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}
