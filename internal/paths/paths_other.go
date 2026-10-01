//go:build !windows && !linux && !darwin

package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// defaults 是其他平台的目录布局。
type defaults struct {
	data   string
	config string
}

// defaultRoots 在非主流平台上回退到用户目录。
//
// 这些平台不在官方支持列表内（见 docs/DECISIONS.md D11），
// 但内核仍应能编译并启动 —— 只是平台后端全部降级为引导模式。
func defaultRoots() (defaults, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return defaults{}, fmt.Errorf("paths: 无法确定用户主目录: %w", err)
	}
	root := filepath.Join(home, ".isc")
	return defaults{data: root, config: root}, nil
}

// tightenDir 尽力收紧目录权限。
func tightenDir(dir string) string {
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Sprintf("无法将 %s 权限收紧至 0700：%v", dir, err)
	}
	return ""
}
