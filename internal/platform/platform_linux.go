//go:build linux

package platform

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 本文件是 Linux 平台的后端装配点。
//
// M0 阶段大部分返回占位实现；随里程碑推进逐个替换：
//
//	Firewall       → 已实现（nftables，见 firewall_linux.go）（并探测 ufw / firewalld）
//	ServiceManager → M5  systemd
//	IPMonitor      → M2  netlink (RTM_NEWADDR / RTM_DELADDR)
//	SecretStore    → 已实现（Secret Service，无会话时回退文件，见 secret_unix.go）
//	Transport      → 已实现（Unix 套接字，见 transport_unix.go）
//	LowPortBinder  → CAP_NET_BIND_SERVICE 检测（本文件已实现）
//
// dataRoot 是内核的数据根目录：密钥存储需要它来决定文件落点。
func Current(dataRoot string) *Bundle {
	return &Bundle{
		Firewall: newNftablesFirewall(),
		ServiceManager: newUnsupportedServiceManager(
			"systemd 后端将在 M5 实现"),
		IPMonitor:     newPollingIPMonitor(),
		SecretStore:   newPlatformSecretStore(dataRoot),
		Transport:     newLocalTransport(),
		LowPortBinder: detectLinuxLowPort(),
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
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
			note:    "以 root 运行，可绑定低端口",
		}
	}
	if hasCapNetBindService() {
		return restrictedLowPortBinder{
			bindLow: true,
			note:    "已授予 CAP_NET_BIND_SERVICE，可绑定低端口",
		}
	}
	return restrictedLowPortBinder{
		bindLow: false,
		note:    "缺少 CAP_NET_BIND_SERVICE，无法绑定 <1024 端口；建议改用高位端口或授予能力",
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
