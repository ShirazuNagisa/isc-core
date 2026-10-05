//go:build darwin

package remote

import (
	"os/exec"
	"strings"
)

// localHostName 返回 macOS 的"本地主机名"（LocalHostName）。
//
// 它才是 Bonjour/mDNS 在局域网里注册的那个名字 —— `os.Hostname()` 给的
// Unix 主机名可以与它无关。用错的下场是报出一条永远解析不了的
// `<Unix主机名>.local` 候选。
//
// 走 `scutil` 而不是 CoreFoundation 的 `SCDynamicStoreCopyLocalHostName`：
// 这个项目用 CGO_ENABLED=0 构建（内核要跨平台交叉编译），而那个 API 只有
// cgo 才够得着。`scutil` 是 macOS 自带的，读一个系统偏好设置而已，
// 而 `LocalAddresses()` 只在配对与生成二维码时被调，不在热路径上。
func localHostName() string {
	out, err := exec.Command("scutil", "--get", "LocalHostName").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
