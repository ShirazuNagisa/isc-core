//go:build !windows

package platform

import (
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// outputSink 收集子进程输出，供测试断言。
type outputSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *outputSink) write(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, chunk)
}

func (s *outputSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.lines, "")
}

// waitFor 轮询直到条件成立或超时；返回是否成立。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

// processGone 判断一个 PID 是否已经不存在。
//
// 信号 0 只做存在性与权限检查，不真的发信号。
func processGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == syscall.ESRCH
}

func TestProcessStartCapturesOutputAndExitCode(t *testing.T) {
	sink := &outputSink{}
	ctrl := newProcessController()

	p, err := ctrl.Start(ProcessSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "echo first; echo second; exit 7"},
		OnOutput:   sink.write,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	status, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if status.Code != 7 {
		t.Fatalf("exit code = %d, want 7 (%s)", status.Code, status)
	}
	if status.Signaled {
		t.Fatalf("process should not be reported as signalled: %s", status)
	}
	if got := sink.String(); !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("output not captured: %q", got)
	}
	select {
	case <-p.Done():
	default:
		t.Fatalf("Done() must be closed after Wait returns")
	}
}

func TestProcessWaitIsRepeatable(t *testing.T) {
	ctrl := newProcessController()
	p, err := ctrl.Start(ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "exit 3"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	first, err := p.Wait()
	if err != nil {
		t.Fatalf("first wait: %v", err)
	}
	second, err := p.Wait()
	if err != nil {
		t.Fatalf("second wait: %v", err)
	}
	// 回收只应发生一次；重复调用必须返回同一结果，而不是报"已经 Wait 过"。
	if first != second {
		t.Fatalf("repeat Wait returned %v then %v", first, second)
	}
}

func TestProcessTerminateStopsARunningProcess(t *testing.T) {
	ctrl := newProcessController()
	p, err := ctrl.Start(ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := p.Signal(SignalTerminate); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	done := make(chan ExitStatus, 1)
	go func() { s, _ := p.Wait(); done <- s }()
	select {
	case status := <-done:
		if !status.Signaled {
			t.Fatalf("expected a signalled exit, got %s", status)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("process did not exit after SIGTERM")
	}
}

// 这是本文件最重要的一条：业务进程几乎总是不止一个进程。
// `npm start` 会再拉起 node，只杀直接子进程会留下一个仍占着端口的孤儿。
func TestSignalReachesTheWholeProcessGroup(t *testing.T) {
	if err := syscall.Kill(0, 0); err != nil {
		t.Skip("signals are unavailable in this environment")
	}
	ctrl := newProcessController()
	sink := &outputSink{}
	p, err := ctrl.Start(ProcessSpec{
		Executable: "/bin/bash",
		// 后台起一个长时间运行的子进程并打印它的 PID，然后等待。
		Args:     []string{"-c", "sleep 30 & echo child=$!; wait"},
		OnOutput: sink.write,
	})
	if err != nil {
		t.Skipf("bash is unavailable: %v", err)
	}

	var childPID int
	if !waitFor(t, 5*time.Second, func() bool {
		out := sink.String()
		idx := strings.Index(out, "child=")
		if idx < 0 {
			return false
		}
		rest := out[idx+len("child="):]
		end := strings.IndexAny(rest, "\n ")
		if end < 0 {
			return false
		}
		pid := 0
		for _, r := range rest[:end] {
			if r < '0' || r > '9' {
				return false
			}
			pid = pid*10 + int(r-'0')
		}
		childPID = pid
		return pid > 0
	}) {
		t.Fatalf("did not observe the grandchild PID, output so far: %q", sink.String())
	}
	if processGone(childPID) {
		t.Skipf("grandchild %d already exited; environment timing too tight", childPID)
	}

	// 只杀直接子进程会留下 childPID；按进程组终止才能清干净。
	if err := p.Signal(SignalKill); err != nil {
		t.Fatalf("kill group: %v", err)
	}
	_, _ = p.Wait()

	if !waitFor(t, 10*time.Second, func() bool { return processGone(childPID) }) {
		t.Fatalf("grandchild %d survived the group signal — it would keep holding the port", childPID)
	}
}

// D30：业务进程不得继承内核环境，否则内核的访问令牌会进入用户网站的进程。
func TestProcessDoesNotInheritTheKernelEnvironment(t *testing.T) {
	t.Setenv("ISC_TEST_KERNEL_SECRET", "must-not-leak")

	ctrl := newProcessController()
	sink := &outputSink{}
	p, err := ctrl.Start(ProcessSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "echo secret=${ISC_TEST_KERNEL_SECRET:-absent}"},
		OnOutput:   sink.write,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := sink.String(); !strings.Contains(got, "secret=absent") {
		t.Fatalf("kernel environment leaked into the child: %q", got)
	}

	// 调用方显式给出的环境则应当生效。
	sink2 := &outputSink{}
	p2, err := ctrl.Start(ProcessSpec{
		Executable: "/bin/sh",
		Args:       []string{"-c", "echo value=${DECLARED_VAR:-missing}"},
		Env:        append(MinimalEnv(), "DECLARED_VAR=declared"),
		OnOutput:   sink2.write,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := p2.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if got := sink2.String(); !strings.Contains(got, "value=declared") {
		t.Fatalf("declared environment was not passed: %q", got)
	}
}

func TestSignallingAnExitedProcessIsNotAnError(t *testing.T) {
	ctrl := newProcessController()
	p, err := ctrl.Start(ProcessSpec{Executable: "/bin/sh", Args: []string{"-c", "exit 0"}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	// 停止流程会无条件发信号；进程已经自己退出不该被报成失败。
	if err := p.Signal(SignalTerminate); err != nil {
		t.Fatalf("signalling an exited process must be a no-op, got %v", err)
	}
	if err := p.Signal(SignalKill); err != nil {
		t.Fatalf("signalling an exited process must be a no-op, got %v", err)
	}
}

func TestStartRejectsEmptyExecutable(t *testing.T) {
	ctrl := newProcessController()
	if _, err := ctrl.Start(ProcessSpec{}); err == nil {
		t.Fatalf("an empty executable must be rejected")
	}
}

func TestProcessControllerIsWiredIntoTheBundle(t *testing.T) {
	bundle := Current(t.TempDir())
	if bundle.Processes == nil {
		t.Fatalf("Bundle.Processes must be wired (see platform_darwin.go / platform_linux.go)")
	}
	if state := bundle.Capabilities().Processes; !state.Available {
		t.Fatalf("process hosting should be available on this platform, got %#v", state)
	}
}
