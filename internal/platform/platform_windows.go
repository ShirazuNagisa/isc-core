//go:build windows

package platform

import "runtime"

// 本文件是 Windows 平台的后端装配点。
//
// M0 阶段全部返回占位实现；随里程碑推进逐个替换：
//
//	Firewall       → M3  Defender Firewall (COM INetFwPolicy2 / netsh advfirewall)
//	ServiceManager → M5  Windows 服务 (SCM)
//	IPMonitor      → M2  GetAdaptersAddresses + 轮询
//	SecretStore    → M1  DPAPI (CryptProtectData)
//	Transport      → 已实现（命名管道，见 transport_windows.go）
//	LowPortBinder  → Windows 不限制普通进程绑定低端口

// Current 返回当前平台的默认后端集合。
func Current() *Bundle {
	return &Bundle{
		Firewall: newUnsupportedFirewall(
			"Windows Defender Firewall 后端将在 M3 实现；当前降级为引导模式"),
		ServiceManager: newUnsupportedServiceManager(
			"Windows 服务（SCM）后端将在 M5 实现"),
		IPMonitor: newUnsupportedIPMonitor(
			"GetAdaptersAddresses 监控后端将在 M2 实现"),
		SecretStore: newUnsupportedSecretStore(
			"DPAPI 密钥库后端将在 M1 实现"),
		Transport:     newLocalTransport(),
		LowPortBinder: permissiveLowPortBinder{backend: "windows-native"},
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}
