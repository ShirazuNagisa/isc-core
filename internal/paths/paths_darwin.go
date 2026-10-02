//go:build darwin

package paths

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
)

// defaults 是 macOS 上的目录布局。
type defaults struct {
	data   string
	config string
}

// defaultRoots 返回 macOS 的默认目录。
//
// 使用 /Library/Application Support（机器级）而不是 ~/Library（用户级），
// 因为内核以 launchd 守护进程身份运行，需要与用户看到同一份数据。
// 无写权限时回退到用户目录。
func defaultRoots() (defaults, error) {
	const systemRoot = "/Library/Application Support/ISC"
	if writable(systemRoot) || os.Geteuid() == 0 {
		return defaults{data: systemRoot, config: systemRoot}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}, fmt.Errorf(i18n.T("paths.err.no_home"), err)
	}
	root := home + "/Library/Application Support/ISC"
	return defaults{data: root, config: root}, nil
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

// tightenDir 在 macOS 上把运行时目录权限收紧到 0700。
func tightenDir(dir string) string {
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Sprintf(i18n.T("paths.warn.chmod"), dir, err)
	}
	return ""
}
