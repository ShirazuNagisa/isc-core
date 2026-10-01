//go:build !windows && !linux && !darwin

package platform

import "runtime"

// 本文件覆盖 Windows / Linux / macOS 之外的平台（FreeBSD、OpenBSD 等）。
//
// 存在的意义是保证 `GOOS=<任意> go build ./...` 都能通过 ——
// 这是 docs/PLAN.md R2 的处置方式：任何时刻三平台（乃至更多平台）都可编译。
// 这些平台全部降级为引导模式，功能可用性由用户自行判断。

// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall: newUnsupportedFirewall(
			"当前平台不在支持列表内（Windows / Linux / macOS）"),
		ServiceManager: newUnsupportedServiceManager(
			"当前平台不在支持列表内（Windows / Linux / macOS）"),
		IPMonitor:     newPollingIPMonitor(),
		SecretStore:   newPlatformSecretStore(dataRoot),
		Transport:     newLocalTransport(),
		LowPortBinder: permissiveLowPortBinder{backend: "unknown"},
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
	}
}
