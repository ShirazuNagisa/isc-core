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
//   - 内存"已用"取 active + wired + compressed 页。这是常见近似：free 与
//     inactive 被算作可回收。
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

func (s *darwinSource) Describe() string { return "darwin-sysctl+ps" }

func (s *darwinSource) Host(ctx context.Context) (HostSample, error) {
	sample := HostSample{At: time.Now()}

	if text, err := runCommand(ctx, "sysctl", "-n", "kern.cp_time"); err == nil {
		if values := parseSysctlNumbers(text); len(values) >= 5 {
			// kern.cp_time 的顺序是 user nice sys intr idle。
			var total uint64
			for _, value := range values {
				total += value
			}
			s.mu.Lock()
			if s.hasPrev {
				sample.CPUPercent = cpuPercent(s.prevCPU, cpuTimes{total: total, idle: values[4]})
			}
			s.prevCPU = cpuTimes{total: total, idle: values[4]}
			s.mu.Unlock()
		}
	}

	if text, err := runCommand(ctx, "sysctl", "-n",
		"hw.memsize", "vm.pagesize", "vm.page_free_count",
		"vm.page_active_count", "vm.page_wire_count", "vm.page_compressor_count"); err == nil {
		values := parseSysctlNumbers(text)
		// 六个值缺一不可：按位置取值时少一个就会全部错位。
		if len(values) == 6 {
			total, pageSize := values[0], values[1]
			active, wired, compressed := values[3], values[4], values[5]
			sample.MemoryTotalBytes = total
			used := (active + wired + compressed) * pageSize
			if used > total {
				used = total
			}
			sample.MemoryUsedBytes = used
		}
	}

	if text, err := runCommand(ctx, "netstat", "-ib"); err == nil {
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

// runCommand 执行一条短命命令并合并两路输出。
func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
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
