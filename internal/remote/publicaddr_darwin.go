//go:build darwin

package remote

import (
	"net"
	"os/exec"
	"strings"
)

// localScopedAddresses 通过 `ifconfig` 取本机地址及其来历。
//
// # 为什么是外部命令而不是系统调用
//
// 这些标记（`temporary` / `deprecated` / `dynamic`）来自 `getifaddrs()`
// 返回的每地址标志位，而 Go 的 `net` 包**把它们丢掉了** ——
// `net.Interfaces()` 只给出接口级的 flags。
//
// 拿回它们的正路是 cgo 调 `getifaddrs()`，但本项目的内核是刻意
// CGO_ENABLED=0 的（发布产物是一个 dylib，交叉编译能力也依赖这一点）。
// 而 `metrics` 包在 darwin 上已经出于同样的理由调用 `ioreg` 了 ——
// 这里沿用同一条约定。
//
// `ifconfig` 在 macOS 上永远存在，输出格式几十年没变，
// 而我们是按行取标记，不是按列取位置。
func localScopedAddresses() ([]ScopedAddress, error) {
	out, err := exec.Command("ifconfig", "-a").Output()
	if err != nil {
		return nil, err
	}

	// 接口名单独占一行（`en1: flags=...`），地址行跟在后面。
	// 先按行扫，遇到接口行就换当前接口名。
	var (
		result  []ScopedAddress
		current string
	)
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			if idx := strings.IndexByte(trimmed, ':'); idx > 0 {
				current = trimmed[:idx]
			}
			continue
		}

		ip, lifetime, ok := parseIfconfigInet6(trimmed)
		if !ok {
			continue
		}
		result = append(result, ScopedAddress{Address: ip, Iface: current, Lifetime: lifetime})
	}
	return result, nil
}

// 保证接口在编译期被实现。
var _ = net.IPv6len
