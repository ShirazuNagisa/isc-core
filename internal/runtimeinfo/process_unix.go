//go:build !windows

package runtimeinfo

import (
	"errors"
	"os"
	"syscall"
)

// processAlive 报告指定 PID 的进程是否存在。
//
// 类 Unix 上使用 signal 0：它不投递任何信号，只做"进程存在且我们有权限
// 向它发信号"的检查。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM 表示进程存在但不属于我们 —— 仍然算存在。
	return errors.Is(err, syscall.EPERM)
}
