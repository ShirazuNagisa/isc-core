//go:build linux

package metrics

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"
)

// linuxSource 在 Linux 上直接读 /proc，不起任何短命进程。
//
// 进程 CPU 走**差分**（utime+stime 的两次采样），因此是瞬时占用，
// 而不是 ps 那种生命周期平均。
type linuxSource struct {
	// clockTicks 是每秒的时钟节拍数。内核把它暴露为 sysconf(_SC_CLK_TCK)，
	// 而绝大多数平台是 100；没有 cgo 时取不到 sysconf，因此按 100 计算。
	//
	// 这不影响"谁在忙"的判断（比例是线性的），只影响绝对值的刻度。
	clockTicks float64

	mu         sync.Mutex
	prevCPU    cpuTimes
	prevRx     uint64
	prevTx     uint64
	prevAt     time.Time
	hasPrev    bool
	prevProc   map[int]procTicks
	prevProcAt time.Time
}

type procTicks struct {
	ticks uint64
	at    time.Time
}

func newPlatformSource() Source { return &linuxSource{clockTicks: 100, prevProc: map[int]procTicks{}} }

func (s *linuxSource) Describe() string { return "linux-procfs" }

func (s *linuxSource) Host(ctx context.Context) (HostSample, error) {
	sample := HostSample{At: time.Now()}

	if body, err := os.ReadFile("/proc/stat"); err == nil {
		if current, ok := parseProcStat(string(body)); ok {
			s.mu.Lock()
			if s.hasPrev {
				sample.CPUPercent = cpuPercent(s.prevCPU, current)
			}
			s.prevCPU = current
			s.mu.Unlock()
		}
	}
	if body, err := os.ReadFile("/proc/meminfo"); err == nil {
		if used, total, ok := parseProcMeminfo(string(body)); ok {
			sample.MemoryUsedBytes, sample.MemoryTotalBytes = used, total
		}
	}
	if body, err := os.ReadFile("/proc/net/dev"); err == nil {
		if rx, tx, ok := parseProcNetDev(string(body)); ok {
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

func (s *linuxSource) Processes(ctx context.Context, pids []int) (map[int]ProcessSample, error) {
	now := time.Now()
	out := make(map[int]ProcessSample, len(pids))
	current := make(map[int]procTicks, len(pids))

	s.mu.Lock()
	previous := s.prevProc
	s.mu.Unlock()

	for _, pid := range pids {
		body, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			// 进程已经退出是常态，不是错误。
			continue
		}
		ticks, rssPages, ok := parseProcPIDStat(string(body))
		if !ok {
			continue
		}
		sample := ProcessSample{PID: pid, MemoryBytes: rssPages * uint64(os.Getpagesize())}
		if prev, seen := previous[pid]; seen {
			elapsed := now.Sub(prev.at).Seconds()
			if elapsed > 0 && ticks >= prev.ticks {
				seconds := float64(ticks-prev.ticks) / s.clockTicks
				sample.CPUPercent = seconds / elapsed * 100
			}
		}
		out[pid] = sample
		current[pid] = procTicks{ticks: ticks, at: now}
	}

	s.mu.Lock()
	s.prevProc = current
	s.mu.Unlock()
	return out, nil
}
