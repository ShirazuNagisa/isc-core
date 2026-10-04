//go:build darwin

package metrics

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"syscall"
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

	// 磁盘：走 getfsstat 系统调用，不起进程（理由见 darwinDisks）。
	//
	// 它把失败吞在自己里面，返回 nil 或一份不完整的列表 —— 一次采不到磁盘
	// 不该让上面已经读到的 CPU、内存、网络一起作废。
	sample.Disks = darwinDisks()

	// GPU：读 ioreg 里 IOAccelerator 的性能统计。
	//
	// 与上面几条命令同一个约定：跑不起来**不是**错误。命令失败、超时、
	// 输出里没有加速器，都只让 GPU 留成 nil —— 那表示"这台机器没有可采样的
	// GPU"，界面据此显示"不支持"；而一个编出来的 0% 会让用户以为显卡正在闲着。
	if text, err := runCommand(ctx, "ioreg", "-r", "-d", "1", "-c", "IOAccelerator"); err == nil {
		sample.GPU = parseIOAccelerator(text)
	}

	s.mu.Lock()
	s.hasPrev = true
	s.mu.Unlock()
	return sample, nil
}

// mntNowait 是 getfsstat(2) 的 mode 参数（BSD 的定义：MNT_WAIT=1、MNT_NOWAIT=2）。
//
// 标准库在 darwin 上没有导出这两个常量，因此按定义写死。
//
// 用 MNT_NOWAIT 而不是 MNT_WAIT：按 man 2 getfsstat，MNT_WAIT 会**逐个
// 文件系统请求一次更新**，而 MNT_NOWAIT 直接返回内核手里已有的数字，
// "不会阻塞在无法响应的文件系统上" —— 一个失联的 NFS/SMB 挂载正是那种
// 文件系统，而采样间隔只有 5 秒：卡住一次就等于这一轮的 CPU / 内存 / 网络
// 也一起没有数字了。代价是容量可能旧几秒，而容量本来就变得很慢。
//
// 也不用 0：man 页说 mode 只接受上面那两个值，而标准库自己的 TestGetfsstat
// 就是显式传 MNT_NOWAIT 的（那个常量旁边留着 "see Issue 16937"）——
// 传 0 时在某些 macOS 机器上两次调用的条数会对不上、拿回一个空的 statfs
// 结构，测试当场红过。采样器不该在一个连标准库都要绕开的取值上赌。
const mntNowait = 0x2

// darwinDisks 枚举这台机器上真实的挂载点及其容量。
//
// # 为什么这次用系统调用，而不是像 CPU/内存那样起短命进程
//
// 上面那几条命令（top / vm_stat / netstat）没有等价的系统调用：数字在 mach
// 接口后面，而没有 cgo 就够不着（D11）。磁盘容量不一样 —— getfsstat(2)
// 正是 `df` 自己用的那个调用，标准库直接暴露了它，一次调用拿全部挂载点。
//
// 因此这里刻意**不**去跑 `df`：它的输出是给人看的（列宽对齐、表头里夹着
// "Capacity"、容量按 1024/1000 折成 "228Gi"），而这些在不同 macOS 版本与
// 不同 locale 下都会变（小数点、单位、列名）。这个包前一半花的力气，
// 基本上都用在还"解析人类可读输出"这笔债上，能不再欠就不欠。
//
// 返回 nil 表示这次没读到（调用失败），不是"这台机器没有磁盘"：
// 调用方只需把它留空，不必当成错误。
//
// # 已知的口径差异：APFS 报的是容器级的数字
//
// 同一个 APFS 容器里的卷共享容量，而 getfsstat 给的是**容器**的块计数。
// 实测这台机器：`/`、`/System/Volumes/Data`、`/System/Volumes/VM` 报出
// 完全相同的 total / avail，而 `df` 的"已用"分别是 13 GiB / 143 GiB /
// 1.0 GiB —— df 报的是卷自己的占用（它另有来源）。
// 因此本包算出的 UsedBytes 在 APFS 上的含义是"这个容器用掉了多少"，
// 与用户在 Finder 里看到的某个卷的用量**对不上**；而 FreeBytes 是准的，
// 那才是真正要看的那个数（还有多少能写）。界面若显示"同一个数重复出现
// 几行"，原因在这里，不是采样重复了。
func darwinDisks() []DiskSample {
	// 先问条数、再按条数取一次：getfsstat 没有"边取边扩容"的用法。
	count, err := syscall.Getfsstat(nil, mntNowait)
	if err != nil || count <= 0 {
		return nil
	}
	buf := make([]syscall.Statfs_t, count)
	// 两次调用之间可能又挂上了新的卷：那时内核返回的是**需要的**条数，
	// 可以大于 buffer。只取填满的部分，多出来的下一次采样再说 ——
	// 为一个挂载点再调一轮不值得。
	n, err := syscall.Getfsstat(buf, mntNowait)
	if err != nil || n <= 0 {
		return nil
	}
	if n > len(buf) {
		n = len(buf)
	}

	out := make([]DiskSample, 0, n)
	for _, stat := range buf[:n] {
		total, used, free := diskCapacity(uint64(stat.Bsize), stat.Blocks, stat.Bfree, stat.Bavail)
		out = append(out, DiskSample{
			// Mntonname / Fstypename 是 [1024]int8 / [16]int8 的定长数组。
			MountPoint: cString(stat.Mntonname[:]),
			FSType:     cString(stat.Fstypename[:]),
			TotalBytes: total,
			UsedBytes:  used,
			FreeBytes:  free,
		})
	}
	return finalizeDisks(out)
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
