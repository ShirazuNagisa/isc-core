//go:build linux

package paths

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
)

// defaults 是 Linux 上的目录布局。
type defaults struct {
	data   string
	config string
}

// defaultRoots 返回 Linux 的默认目录。
//
// 遵循 FHS：配置放 /etc/isc，可变数据放 /var/lib/isc。
// 非 root 运行且无写权限时，回退到用户目录，保证开发与试用不受阻。
// systemDataRoot / systemConfigRoot 是 Linux 上的系统级目录（FHS，D20）。
const (
	systemDataRoot   = "/var/lib/isc"
	systemConfigRoot = "/etc/isc"
)

// SystemDataDir 返回系统级数据目录，以及它当前是否存在。
//
// 与 macOS 同一个理由（见 paths_darwin.go 的说明）：内核作为 systemd 服务
// 运行时数据在 /var/lib/isc，而普通用户的回退目录在 ~/.local/share 下 ——
// "找不到"会被误读成"没在跑"。
func SystemDataDir() (string, bool) {
	fi, err := os.Stat(systemDataRoot)
	return systemDataRoot, err == nil && fi.IsDir()
}

func defaultRoots() (defaults, error) {
	const (
		systemConfig = systemConfigRoot
		systemData   = systemDataRoot
	)
	if writable(systemData) || os.Geteuid() == 0 {
		return defaults{data: systemData, config: systemConfig}, nil
	}
	return userFallback()
}

// userFallback 在无系统目录写权限时回退到 XDG 目录。
func userFallback() (defaults, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return defaults{}, fmt.Errorf(i18n.T("paths.err.no_home"), err)
		}
		base = home + "/.local/share"
	}
	cfgBase := os.Getenv("XDG_CONFIG_HOME")
	if cfgBase == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return defaults{}, fmt.Errorf(i18n.T("paths.err.no_home"), err)
		}
		cfgBase = home + "/.config"
	}
	return defaults{data: base + "/isc", config: cfgBase + "/isc"}, nil
}

// writable 报告进程能否在 dir 下创建文件。
func writable(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".isc-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// tightenDir 在 Linux 上把运行时目录权限收紧到 0700。
//
// 该目录含 runtime.json（访问令牌），必须仅属主可读。
func tightenDir(dir string) string {
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Sprintf(i18n.T("paths.warn.chmod"), dir, err)
	}
	return ""
}
