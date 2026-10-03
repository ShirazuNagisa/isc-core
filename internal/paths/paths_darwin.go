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

// systemDataRoot 是 macOS 上的系统级数据目录（见 docs/DECISIONS.md D20）。
const systemDataRoot = "/Library/Application Support/ISC"

// SystemDataDir 返回系统级数据目录，以及它当前是否存在。
//
// # 为什么需要单独一个函数
//
// 内核以 launchd 守护进程身份运行，数据落在系统目录；而普通用户跑
// `isc status` 时用的是自己的回退目录 —— 于是"找不到 runtime.json"会被
// 读成"内核没在跑"，而真相往往是"它在跑，只是你找错了地方"。
//
// 真机上就是这么发生的：普通用户拿到的是
// "内核未运行。请先执行 'isc daemon run' 或安装为系统服务。"
// —— 而他可能**已经**装成系统服务了。CLI 靠这个函数补一句提示。
func SystemDataDir() (string, bool) {
	fi, err := os.Stat(systemDataRoot)
	return systemDataRoot, err == nil && fi.IsDir()
}

// defaultRoots 返回 macOS 的默认目录。
//
// 使用 /Library/Application Support（机器级）而不是 ~/Library（用户级），
// 因为内核以 launchd 守护进程身份运行，需要与用户看到同一份数据。
// 无写权限时回退到用户目录。
func defaultRoots() (defaults, error) {
	if writable(systemDataRoot) || os.Geteuid() == 0 {
		return defaults{data: systemDataRoot, config: systemDataRoot}, nil
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
