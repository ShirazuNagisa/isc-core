//go:build !windows

package platform

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

// 本文件实现 Unix（macOS / Linux / 其它 unix）上的业务子进程控制。
//
// # 为什么必须按**进程组**管理
//
// 业务进程几乎不会只有一个进程：`npm start` 会拉起 node，`mvn spring-boot:run`
// 会拉起一个 JVM，Compose 会拉起一串容器客户端。只杀直接子进程的话，
// 被它拉起来的那个进程会活下来继续占着端口 —— 症状是"应用已停止，
// 但端口仍被占用，重新启动失败"。因此：
//
//   - 启动时 Setpgid，让子进程成为新进程组的首进程；
//   - 终止时对 **-pid** 发信号，覆盖整组；
//   - 先 TERM 后 KILL，给应用收尾的机会，但不无限等。

type unixProcessController struct{}

func newProcessController() ProcessController { return &unixProcessController{} }

func (c *unixProcessController) Describe() ImplState {
	return ImplState{Available: true, Backend: "unix-process-group"}
}

// Start 启动子进程。
func (c *unixProcessController) Start(spec ProcessSpec) (Process, error) {
	if strings.TrimSpace(spec.Executable) == "" {
		return nil, fmt.Errorf("process: executable is required")
	}

	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = spec.Env
	} else {
		// 刻意不继承内核环境（D30）。见 ProcessSpec.Env 的说明。
		cmd.Env = MinimalEnv()
	}
	// 自成进程组：这样终止能覆盖它拉起的所有后代。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// 用显式管道而不是 StdoutPipe：后者只能在 Wait 之前读，
	// 与"后台 goroutine 负责 Wait"的写法冲突。
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = writer
	cmd.Stderr = writer
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	// 父进程必须关掉写端，否则读取端永远等不到 EOF。
	_ = writer.Close()

	p := &unixProcess{cmd: cmd, done: make(chan struct{}), pid: cmd.Process.Pid}
	go p.drain(reader, spec.OnOutput)
	go p.reap()
	return p, nil
}

type unixProcess struct {
	cmd  *exec.Cmd
	pid  int
	done chan struct{}
	once sync.Once

	mu     sync.Mutex
	status ExitStatus
	waitEr error
}

func (p *unixProcess) PID() int              { return p.pid }
func (p *unixProcess) Done() <-chan struct{} { return p.done }

// drain 把合并后的输出切成片段回调给调用方。
//
// 按换行切分，但**不假设输出一定是整行**：一个进程可能长时间不换行
// （例如打印进度条），因此到 EOF 时要把残留一起交出。单行长度上限
// maxOutputChunk 防止一个只写不换行的进程把内存吃光。
func (p *unixProcess) drain(reader io.ReadCloser, onOutput func(string)) {
	defer func() { _ = reader.Close() }()
	if onOutput == nil {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	const maxOutputChunk = 8 << 10
	buffered := bufio.NewReaderSize(reader, 4<<10)
	var pending strings.Builder
	for {
		chunk, err := buffered.ReadString('\n')
		if chunk != "" {
			pending.WriteString(chunk)
			if pending.Len() >= maxOutputChunk || strings.HasSuffix(chunk, "\n") {
				onOutput(pending.String())
				pending.Reset()
			}
		}
		if err != nil {
			if pending.Len() > 0 {
				onOutput(pending.String())
			}
			return
		}
	}
}

// reap 是唯一调用 cmd.Wait 的地方，保证回收只发生一次。
//
// 非零退出**不是** error：退出码由 ExitStatus 表达，调用方按状态分支即可。
// 把 `exit status 7` 同时塞进 error 会逼每个调用点写两次同样的判断，
// 也容易让人误以为"等待本身失败了"。error 只留给真正取不到状态的情况。
func (p *unixProcess) reap() {
	err := p.cmd.Wait()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		err = nil
	}
	status := ExitStatus{Code: p.cmd.ProcessState.ExitCode()}
	if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			status.Signaled = true
			status.Code = -1
			status.Signal = signalName(ws.Signal())
		}
	}
	p.mu.Lock()
	p.status = status
	p.waitEr = err
	p.mu.Unlock()
	p.once.Do(func() { close(p.done) })
}

func (p *unixProcess) Wait() (ExitStatus, error) {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status, p.waitEr
}

// Signal 向整个进程组发信号。
//
// 组已经不存在（进程已退出并被回收）时不算错误：调用方关心的是
// "它现在停了没有"，而答案是停了 —— 把 ESRCH 当失败会让停止流程
// 在"刚好自己退出了"的情况下报错。
func (p *unixProcess) Signal(sig Signal) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	target := -p.pid
	var err error
	if sig == SignalKill {
		err = syscall.Kill(target, syscall.SIGKILL)
	} else {
		err = syscall.Kill(target, syscall.SIGTERM)
	}
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

func signalName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGTERM:
		return "terminated"
	case syscall.SIGKILL:
		return "killed"
	case syscall.SIGINT:
		return "interrupted"
	case syscall.SIGHUP:
		return "hangup"
	default:
		return fmt.Sprintf("signal-%d", int(sig))
	}
}

// unsupportedProcessController 供未实现的平台使用；见 stub.go。
//
// 编译期断言。
var (
	_ ProcessController = (*unixProcessController)(nil)
	_ Process           = (*unixProcess)(nil)
)
