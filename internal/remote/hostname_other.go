//go:build !darwin

package remote

import "os"

// localHostName 在非 macOS 平台上退回 Unix 主机名。
//
// 那些平台上没有 Bonjour 的"本地主机名"概念，两个名字本来就是同一个，
// 因此不需要额外处理。
func localHostName() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}
