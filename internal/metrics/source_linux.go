//go:build linux

package metrics

import (
	"context"
	"os"
	"strconv"
	"sync"
	"syscall"
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

	// 磁盘：从挂载表里拿挂载点，再逐个 statfs 要容量。
	//
	// # 为什么读 /proc/self/mounts 而不是 /proc/mounts
	//
	// 2.4.19 之后前者是后者的符号链接，两个名字读到的是同一个文件 ——
	// 但 "self" 把我们要的东西写清楚了：**本进程所在挂载命名空间**的挂载表。
	// 这一点在这里是硬要求，因为紧接着就要 statfs 表里的每一个路径：
	// 容器里 /proc 可能是宿主挂进来的，那时表里会出现容器里根本不存在的
	// 挂载点，用 /proc/mounts 这个名字读起来像是"整台机器有几块盘"，
	// 而我们要的始终是"这个进程看得见的盘"。
	//
	// 失败（读不到表）就留空，不报错：与主机其它几路一样，
	// 一次采不到磁盘不该让 CPU、内存、网络一起消失。
	if body, err := os.ReadFile("/proc/self/mounts"); err == nil {
		entries := parseProcMounts(string(body))
		disks := make([]DiskSample, 0, len(entries))
		for _, entry := range entries {
			var stat syscall.Statfs_t
			if err := syscall.Statfs(entry.MountPoint, &stat); err != nil {
				// 读表与 statfs 之间挂载点被卸载是常态（自动挂载、容器里
				// 的临时挂载），不是错误。
				continue
			}
			// Linux 的 Bsize 是**有符号**的 int64：0 或负数转成 uint64
			// 会变成一个天文数字，于是"已用"变成几十 EB。宁可跳过。
			if stat.Bsize <= 0 {
				continue
			}
			total, used, free := diskCapacity(uint64(stat.Bsize), stat.Blocks, stat.Bfree, stat.Bavail)
			disks = append(disks, DiskSample{
				MountPoint: entry.MountPoint,
				FSType:     entry.FSType,
				TotalBytes: total,
				UsedBytes:  used,
				FreeBytes:  free,
			})
		}
		sample.Disks = finalizeDisks(disks)
	}

	// GPU 在 Linux 上留空（nil）：那要读 nvidia-smi 或 sysfs，是另一次改动。
	// nil 的含义是"这个平台没有 GPU 采样"，界面据此显示"不支持"，
	// 而不是显示一个假的 0%。

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
