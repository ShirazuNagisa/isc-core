//go:build windows

package runtimeinfo

import "golang.org/x/sys/windows"

// stillActive 是 Windows 上 GetExitCodeProcess 返回的"进程仍在运行"标志。
const stillActive = 259

// processAlive 报告指定 PID 的进程是否存在。
//
// Windows 上 os.FindProcess 恒返回成功（它不做任何系统调用），
// 因此必须用 OpenProcess + GetExitCodeProcess 判断。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// 权限不足时（例如内核以 SYSTEM 运行而我们在用户态），
		// 保守地认为进程存在 —— 宁可让客户端尝试连接后失败，
		// 也不要误报"内核没在跑"而让用户重复启动。
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h) //nolint:errcheck // 句柄关闭失败无可挽回

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
