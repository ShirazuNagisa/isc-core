//go:build windows

package runtimeinfo

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// 本文件覆盖 Windows 上的进程存活判定。它此前一条测试都没有。
//
// # 为什么它值得测
//
// 这个函数的返回值直接决定 `Connect` 是否认为内核还活着：
//
//	残留的 runtime.json + 判定为"活着" → 客户端去连一个不存在的管道，
//	                                     超时后报一句含糊的"内核不可达"
//	真的内核 + 判定为"已死"          → 客户端说"内核没在运行"，
//	                                     于是用户又启动了一个
//
// 两种错误方向都有代价，而 Windows 上它必须用 OpenProcess +
// GetExitCodeProcess 实现 —— 因为 `os.FindProcess` 在 Windows 上
// **恒返回成功**（它不做任何系统调用），拿它判断存活必然是错的。

// TestProcessAliveOnSelf 验证"自己显然活着"。
func TestProcessAliveOnSelf(t *testing.T) {
	t.Parallel()

	if !processAlive(os.Getpid()) {
		t.Fatal("当前进程应当被判定为存活")
	}
}

// TestProcessAliveOnChild 覆盖"确实存在但不是自己"的进程。
//
// 只看自己的话，一个"恒返回 true"的实现也能通过 —— 而那种实现在内核
// 已经退出时会说"还活着"，于是客户端去连一个不存在的管道。
func TestProcessAliveOnChild(t *testing.T) {
	t.Parallel()

	// 用一个会活一会儿的子进程。timeout 在 Windows 上是内置命令，
	// 不需要额外的可执行文件。
	cmd := exec.Command("cmd", "/c", "ping -n 5 127.0.0.1 > NUL")
	if err := cmd.Start(); err != nil {
		t.Skipf("无法启动子进程: %v", err)
	}
	pid := cmd.Process.Pid

	// 立刻判定应当为存活。
	if !processAlive(pid) {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("刚启动的子进程（pid=%d）应当被判定为存活", pid)
	}

	// 杀掉之后应当判定为已退出。
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	// 句柄的回收有一点延迟，给它一点时间。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("已经退出的进程 %d 仍被判定为存活 —— "+
		"这会让客户端去连一个不存在的管道，并报一句含糊的"+
		"「内核不可达」", pid)
}

// TestProcessAliveRejectsNonPositivePID 钉住输入边界。
//
// pid <= 0 不是一个真实进程，必须直接返回 false —— 而不是把它交给
// OpenProcess。0 在 Windows 上表示"当前进程"，若被当成真实 pid 传下去，
// 一个写着 pid=0 的残留 runtime.json 会被判成"内核还活着"。
func TestProcessAliveRejectsNonPositivePID(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1, -9999} {
		if processAlive(pid) {
			t.Errorf("pid=%d 应当被判定为不存在 —— "+
				"0 在 Windows 上表示当前进程，会被误判成内核还活着", pid)
		}
	}
}

// TestProcessAliveOnUnusedPID 验证一个大概率不存在的 PID。
//
// 用一个极大的 PID：Windows 的 PID 是 4 的倍数且实际远小于 2^32，
// 但 OpenProcess 对不存在的 PID 返回 ERROR_INVALID_PARAMETER，因此
// 结果为 false。
func TestProcessAliveOnUnusedPID(t *testing.T) {
	t.Parallel()

	// 先确认这个 pid 确实不存在，避免测试本身不可靠。
	const unlikely = 0x7FFFFFF0

	// 只有当它确实不存在时才断言 —— 否则跳过（理论上不可能，但
	// 不该让测试因为环境的偶然性而变红）。
	if _, err := os.FindProcess(unlikely); err == nil {
		// Windows 上 FindProcess 恒成功，因此这里必然进入。
		// 真正要断言的是 processAlive 的判断。
		if processAlive(unlikely) {
			t.Errorf("几乎不可能存在的 pid %d 被判定为存活", unlikely)
		}
	}
}

// TestProcessAliveIsNotConstant 是一条**元测试**。
//
// 它防的是最糟的一种"通过"：某个实现恒返回 true（或恒返回 false），
// 于是上面那些断言里总有一半靠运气过。这里要求"自己活着"与"0 不存在"
// 给出**不同**的答案 —— 一个恒定实现无法同时满足。
func TestProcessAliveIsNotConstant(t *testing.T) {
	t.Parallel()

	alive := processAlive(os.Getpid())
	dead := processAlive(0)
	if alive == dead {
		t.Fatalf("processAlive 对「自己」与「pid=0」返回了同一个值（%v）—— "+
			"它可能是个恒定实现", alive)
	}
}
