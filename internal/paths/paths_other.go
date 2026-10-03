//go:build !windows && !linux && !darwin

package paths

import (
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"os"
	"path/filepath"
)

// defaults 是其他平台的目录布局。
type defaults struct {
	data   string
	config string
}

// SystemDataDir 在这类平台上没有系统级目录，因此总是返回 false。
//
// 之所以仍然导出它：调用方（CLI 的提示逻辑）不该按平台写分支 ——
// 那正是"某个平台漏了一处"的来源。
func SystemDataDir() (string, bool) { return "", false }

// defaultRoots 在非主流平台上回退到用户目录。
//
// 这些平台不在官方支持列表内（见 docs/DECISIONS.md D11），
// 但内核仍应能编译并启动 —— 只是平台后端全部降级为引导模式。
func defaultRoots() (defaults, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}, fmt.Errorf(i18n.T("paths.err.no_home"), err)
	}
	root := filepath.Join(home, ".isc")
	return defaults{data: root, config: root}, nil
}

// tightenDir 尽力收紧目录权限。
func tightenDir(dir string) string {
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Sprintf(i18n.T("paths.warn.chmod"), dir, err)
	}
	return ""
}
