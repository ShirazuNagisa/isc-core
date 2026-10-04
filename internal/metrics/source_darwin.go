//go:build darwin

package metrics

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// darwinSource 在 macOS 上取采样。
//
// # 为什么用短命进程而不是系统调用
//
// 走 mach（host_statistics64 / task_info）需要 cgo，而本项目禁止 cgo（D11）。
// 因此这里用三条一次性命令：`sysctl`（CPU 时间片与内存页）、`netstat -ib`
// （接口字节数）、`ps`（进程资源）。它们在采样间隔（默认 5 秒）上各起一次，
// 对桌面应用是可接受的代价；换来的是不需要 cgo、也不需要维护 mach 结构体布局。
//
// # 已知的近似
//
//   - `ps -o %cpu` 在 macOS 上是**衰减平均**而不是瞬时占用。它对"这个进程
//     一直很忙"是准确的，对尖峰不敏感。Linux 侧走 /proc 差分，是瞬时值。
//   - 内存"已用"取 vm_stat 的 活跃 + wired + 压缩器实际占用，与活动监视器
//     同口径。inactive / purgeable 不算已用：它们可以被立即回收，算进去会
//     显示出一个远高于系统自身读数的数字，而用户会拿它去对比。
type darwinSource struct {
	mu      sync.Mutex
	prevCPU cpuTimes
	prevRx  uint64
	prevTx  uint64
	prevAt  time.Time
	hasPrev bool
	rssUnit uint64
}

func newPlatformSource() Source { return &darwinSource{rssUnit: 1024} }

func (s *darwinSource) Describe() string { return "darwin-top+vm_stat+netstat+ps" }

func (s *darwinSource) Host(ctx context.Context) (HostSample, error) {
	sample := HostSample{At: time.Now()}

	// CPU 走 `top -l 2`：macOS 27 上 `kern.cp_time` 已经不存在，而 mach 的
	// host_statistics 需要 cgo（内核主体禁止，D11）。
	//
	// 取**第二次**采样：`-l 1` 报的是"开机至今"的平均值，拿它当当前占用
	// 会显示一个几乎不动的数字。代价是这条命令要跑约一秒 —— 在 5 秒的
	// 采样间隔里可以接受。
	if text, err := runCommand(ctx, "top", "-l", "2", "-n", "0"); err == nil {
		if idle, ok := parseTopCPU(text); ok {
			sample.CPUPercent = 100 - idle
		}
	}

	// 总内存：hw.memsize 这个 OID 仍然存在。
	if text, err := runCommand(ctx, "sysctl", "-n", "hw.memsize"); err == nil {
		if values := parseSysctlNumbers(text); len(values) == 1 {
			sample.MemoryTotalBytes = values[0]
		}
	}
	// 已用内存走 vm_stat：此前用的那几个 vm.page_* OID 在 macOS 27 上
	// **已经不存在**，而当时要求"六个值缺一不可"，于是整块内存指标静默地
	// 变成 0 —— 界面上显示"共 Zero KB"。
	if text, err := runCommand(ctx, "vm_stat"); err == nil {
		if stat, ok := parseVMStat(text); ok {
			sample.MemoryUsedBytes = stat.used(stat.PageSize)
			if sample.MemoryTotalBytes == 0 {
				sample.MemoryTotalBytes = uint64(stat.PageSize) * 1048576
			}
		}
	}

	// `-n`（数字输出）不是可选项，是**必须的**：不带它时 netstat 会去做
	// 名字解析，在这台机器上实测稳定耗时 5.04 秒 —— 正好卡在命令超时上，
	// 于是它每次都被杀掉、返回空输出、解析失败，网络速率**永远是 0**，
	// 而界面只是安静地显示"0 B/s"。加上 -n 之后是 0.00 秒。
	if text, err := runCommand(ctx, "netstat", "-ibn"); err == nil {
		if rx, tx, ok := parseNetstatIB(text); ok {
			s.mu.Lock()
			if s.hasPrev {
				elapsed := sample.At.Sub(s.prevAt).Seconds()
				if elapsed > 0 {
					sample.NetRxBytesPerSec = rate(rx, s.prevRx, elapsed)
					sample.NetTxBytesPerSec = rate(tx, s.prevTx, elapsed)
				}
			}
			s.prevRx, s.prevTx, s.prevAt = rx, tx, sample.At
			s.mu.Unlock()
		}
	}

	s.mu.Lock()
	s.hasPrev = true
	s.mu.Unlock()
	return sample, nil
}

func (s *darwinSource) Processes(ctx context.Context, pids []int) (map[int]ProcessSample, error) {
	if len(pids) == 0 {
		return map[int]ProcessSample{}, nil
	}
	args := []string{"-o", "pid=,%cpu=,rss=", "-p", joinPIDs(pids)}
	text, err := runCommand(ctx, "ps", args...)
	if err != nil {
		return nil, err
	}
	return parsePS(text, s.rssUnit), nil
}

func joinPIDs(pids []int) string {
	parts := make([]string, 0, len(pids))
	for _, pid := range pids {
		parts = append(parts, itoa(pid))
	}
	return strings.Join(parts, ",")
}

// commandTimeout 是单条采样命令的上限。
//
// 8 秒而不是 5 秒：采样链里有 `top -l 2`（本身就要 1.4 秒，机器忙时更久），
// 而超时的后果是**静默的** —— 命令被杀、输出为空、解析失败、指标停在 0，
// 界面上看不出任何异常。宁可偶尔慢一点，也不要安静地报错值。
const commandTimeout = 8 * time.Second

// runCommand 执行一条短命命令并合并两路输出。
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, name, args...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil && strings.TrimSpace(text) == "" {
		return "", err
	}
	// 有些命令（例如 ps 对不存在的 PID）会以非零码退出但仍给出有用输出。
	return text, nil
}
