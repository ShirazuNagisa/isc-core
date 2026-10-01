//go:build windows

package platform

import "runtime"

// 本文件是 Windows 平台的后端装配点。
//
// M0 阶段全部返回占位实现；随里程碑推进逐个替换：
//
//	Firewall       → 已实现（PowerShell NetSecurity 模块，见 firewall_windows.go）
//	ServiceManager → M5  Windows 服务 (SCM)
//	IPMonitor      → M2  GetAdaptersAddresses + 轮询
//	SecretStore    → 已实现（DPAPI 保护 + 文件承载，见 secret_windows.go）
//	Transport      → 已实现（命名管道，见 transport_windows.go）
//	LowPortBinder  → Windows 不限制普通进程绑定低端口
//
// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall: newWindowsFirewall(),
		ServiceManager: newUnsupportedServiceManager(
			"Windows 服务（SCM）后端将在 M5 实现"),
		IPMonitor:     newPollingIPMonitor(),
		SecretStore:   newPlatformSecretStore(dataRoot),
		Transport:     newLocalTransport(),
		LowPortBinder: permissiveLowPortBinder{backend: "windows-native"},
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}
