//go:build linux

package platform

import (
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 本文件是 Linux 平台的后端装配点。
//
// 各后端的落点（M0 的占位实现已随里程碑逐个替换完）：
//
//	Firewall       → 已实现（nftables，见 firewall_linux.go）（并探测 ufw / firewalld）
//	ServiceManager → 已实现（systemd，见 service_linux.go）
//	IPMonitor      → 已实现（可移植轮询，见 ipmon.go）
//	SecretStore    → 已实现（Secret Service，无会话时回退文件，见 secret_unix.go）
//	Transport      → 已实现（Unix 套接字，见 transport_unix.go）
//	LowPortBinder  → CAP_NET_BIND_SERVICE 检测（本文件已实现）
//
// ⚠️ 这里曾经**漏接**：service_linux.go 里 systemd 后端写好了，而装配点
// 仍返回 stub，于是 `isc service install` 在 Linux 上报"将在 M5 实现"。
// 与 macOS 同一处缺口，见 platform_darwin.go 的说明。
//
// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall:       newNftablesFirewall(),
		ServiceManager: newServiceManager(),
		IPMonitor:      newPollingIPMonitor(),
		SecretStore:    newPlatformSecretStore(dataRoot),
		Transport:      newLocalTransport(),
		LowPortBinder:  detectLinuxLowPort(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
	}
}

// capNetBindService 是 CAP_NET_BIND_SERVICE 的能力位号（linux/capability.h）。
const capNetBindService = 10

// detectLinuxLowPort 检测当前进程能否绑定 <1024 端口。
//
// root（euid 0）天然可以；非 root 时需要 CAP_NET_BIND_SERVICE 能力位。
// 能力位从 /proc/self/status 的 CapEff 字段读取。
func detectLinuxLowPort() LowPortBinder {
	if os.Geteuid() == 0 {
		return restrictedLowPortBinder{
			bindLow: true,
			note:    i18n.T("platform.lowport_root"),
		}
	}
	if hasCapNetBindService() {
		return restrictedLowPortBinder{
			bindLow: true,
			note:    i18n.T("platform.lowport_cap"),
		}
	}
	return restrictedLowPortBinder{
		bindLow: false,
		note:    i18n.T("platform.lowport_denied"),
	}
}

// hasCapNetBindService 解析 /proc/self/status 的 CapEff 判断能力位。
//
// 读取失败时保守返回 false —— 宁可提示用户"不可用"，也不要让他以为可用。
func hasCapNetBindService() bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "CapEff:")
		if !ok {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(rest), 16, 64)
		if err != nil {
			return false
		}
		return mask&(1<<capNetBindService) != 0
	}
	return false
}
