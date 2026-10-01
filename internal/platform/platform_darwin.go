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
//	SecretStore    → M1  Keychain
//	Transport      → 已实现（Unix 套接字，见 transport_unix.go）
//	LowPortBinder  → macOS 不限制普通进程绑定低端口
//
// 注意：macOS 无法在开发机（Windows）上验证，依赖 CI 的 macos-latest runner
// 与一台真机/虚拟机。见 docs/PLAN.md R8。

// Current 返回当前平台的默认后端集合。
func Current() *Bundle {
	return &Bundle{
		Firewall: newUnsupportedFirewall(
			"pf 后端将在 M3 实现；当前降级为引导模式"),
		ServiceManager: newUnsupportedServiceManager(
			"launchd 后端将在 M5 实现"),
		IPMonitor: newUnsupportedIPMonitor(
			"getifaddrs 监控后端将在 M2 实现"),
		SecretStore: newUnsupportedSecretStore(
			"Keychain 密钥库后端将在 M1 实现"),
		Transport:     newLocalTransport(),
		LowPortBinder: permissiveLowPortBinder{backend: "darwin-native"},
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}
