package metrics

import (
	"context"
	"errors"
	"testing"
	"time"
)

// --- Linux /proc 解析 -------------------------------------------------------

func TestParseProcStatComputesBusyRatio(t *testing.T) {
	// user nice system idle iowait irq softirq steal
	first, ok := parseProcStat("cpu  100 0 50 1000 20 0 0 0\ncpu0 1 2 3 4\n")
	if !ok {
		t.Fatal("expected the aggregate cpu line to parse")
	}
	if first.total != 1170 {
		t.Fatalf("total = %d, want 1170", first.total)
	}
	// idle 含 iowait：等 IO 对用户来说也是"没在干活"。
	if first.idle != 1020 {
		t.Fatalf("idle = %d, want 1020", first.idle)
	}

	// 同样长度的一段时间里，一半时间在忙。
	second := cpuTimes{total: first.total + 200, idle: first.idle + 100}
	if got := cpuPercent(first, second); got < 49.9 || got > 50.1 {
		t.Fatalf("cpuPercent = %v, want ~50", got)
	}
}

func TestCPUPercentHandlesCounterResets(t *testing.T) {
	// 计数器回绕或时间没走动时不能报负数或离谱的值。
	if got := cpuPercent(cpuTimes{total: 1000, idle: 900}, cpuTimes{total: 900, idle: 800}); got != 0 {
		t.Fatalf("a shrinking counter must yield 0, got %v", got)
	}
	if got := cpuPercent(cpuTimes{total: 0, idle: 0}, cpuTimes{total: 0, idle: 0}); got != 0 {
		t.Fatalf("no movement must yield 0, got %v", got)
	}
	// 全忙：这一段时间里 idle 一点没涨。
	if got := cpuPercent(cpuTimes{total: 100, idle: 100}, cpuTimes{total: 200, idle: 100}); got != 100 {
		t.Fatalf("fully busy must be 100, got %v", got)
	}
	// 全闲。
	if got := cpuPercent(cpuTimes{total: 100, idle: 100}, cpuTimes{total: 200, idle: 200}); got != 0 {
		t.Fatalf("fully idle must be 0, got %v", got)
	}
}

func TestParseProcMeminfoUsesAvailableNotFree(t *testing.T) {
	body := "MemTotal:       16000000 kB\nMemFree:         1000000 kB\nMemAvailable:    6000000 kB\n"
	used, total, ok := parseProcMeminfo(body)
	if !ok {
		t.Fatal("expected meminfo to parse")
	}
	if total != 16000000*1024 {
		t.Fatalf("total = %d", total)
	}
	// 用 MemAvailable（含可回收缓存）而不是 MemFree：后者会让人以为内存
	// 快用完了，而其实大部分是可回收的。
	if want := uint64((16000000 - 6000000) * 1024); used != want {
		t.Fatalf("used = %d, want %d", used, want)
	}
}

func TestParseProcMeminfoFallsBackToMemFree(t *testing.T) {
	// 老内核没有 MemAvailable。
	body := "MemTotal:       1000 kB\nMemFree:         400 kB\n"
	used, total, ok := parseProcMeminfo(body)
	if !ok || total != 1000*1024 || used != 600*1024 {
		t.Fatalf("used=%d total=%d ok=%v", used, total, ok)
	}
}

func TestParseProcNetDevSkipsLoopback(t *testing.T) {
	body := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999999    1000    0    0    0     0          0         0  9999999    1000    0    0    0     0       0          0
  eth0: 5000    10    0    0    0     0          0         0     3000    8    0    0    0     0       0          0
  eth1: 1000     1    0    0    0     0          0         0      500    1    0    0    0     0       0          0
`
	rx, tx, ok := parseProcNetDev(body)
	if !ok {
		t.Fatal("expected netdev to parse")
	}
	if rx != 6000 || tx != 3500 {
		t.Fatalf("rx=%d tx=%d, want 6000/3500 (loopback excluded)", rx, tx)
	}
}

func TestParseProcPIDStatHandlesSpacesInTheName(t *testing.T) {
	// 进程名里可以有空格与括号，因此必须从**最后一个**右括号之后开始切。
	line := "1234 (weird ) name) S 1 1234 1234 0 -1 4194560 100 0 0 0 " +
		"7 5 0 0 20 0 3 0 1000 12345 678 " + // utime=7 stime=5 ... rss=678
		"0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	ticks, rss, ok := parseProcPIDStat(line)
	if !ok {
		t.Fatal("expected stat to parse")
	}
	if ticks != 12 {
		t.Fatalf("ticks = %d, want utime+stime = 12", ticks)
	}
	if rss != 678 {
		t.Fatalf("rss = %d, want 678", rss)
	}
}

func TestParseProcPIDStatRejectsGarbage(t *testing.T) {
	if _, _, ok := parseProcPIDStat("not a stat line"); ok {
		t.Fatalf("garbage must not parse")
	}
	if _, _, ok := parseProcPIDStat("1 (x) S 1 2"); ok {
		t.Fatalf("a truncated line must not parse")
	}
}

// --- ps / netstat 解析 ------------------------------------------------------

func TestParsePS(t *testing.T) {
	body := "  1234   0.5  12345\n  5678  12,3  456789\n\nbadline\n"
	found := parsePS(body, 1024)
	if len(found) != 2 {
		t.Fatalf("expected two processes, got %d", len(found))
	}
	if found[1234].CPUPercent != 0.5 || found[1234].MemoryBytes != 12345*1024 {
		t.Fatalf("unexpected sample: %#v", found[1234])
	}
	// 某些区域设置用逗号作小数点。
	if found[5678].CPUPercent != 12.3 {
		t.Fatalf("comma decimal separator not handled: %#v", found[5678])
	}
}

func TestParseNetstatIBCountsEachInterfaceOnce(t *testing.T) {
	// netstat -ib 对同一个接口会为每个地址各打一行，而字节计数在每行上
	// 是**重复的** —— 全部相加会把流量算成两三倍。
	body := `Name  Mtu   Network       Address            Ipkts Ierrs     Ibytes    Opkts Oerrs     Obytes  Coll
lo0   16384 <Link#1>                        100     0    9999999      100     0    9999999     0
lo0   16384 127            localhost         100     -    9999999      100     -    9999999     -
en0   1500  <Link#4>     aa:bb:cc:dd:ee:ff 1000     0  500000000      900     0  200000000     0
en0   1500  192.168.1.5   192.168.1.5      1000     -  500000000      900     -  200000000     -
utun0 1380  <Link#12>                       10     0       4096       12     0       2048     0
`
	rx, tx, ok := parseNetstatIB(body)
	if !ok {
		t.Fatal("expected netstat output to parse")
	}
	// 注意 utun0 那一行少一列（没有 Address），按表头下标取值会错位。
	if rx != 500000000+4096 {
		t.Fatalf("rx = %d, want the en0 + utun0 rows counted once each", rx)
	}
	if tx != 200000000+2048 {
		t.Fatalf("tx = %d", tx)
	}
}

func TestParseSysctlNumbersSkipsErrorLines(t *testing.T) {
	// 请求多个名字时，某一个不存在只会让那一行变成错误信息。
	body := "17179869184\n16384\nunknown oid 'vm.page_free_count'\n1234\n"
	values := parseSysctlNumbers(body)
	if len(values) != 3 {
		t.Fatalf("expected three numeric values, got %v", values)
	}
	// 个数对不上时调用方必须放弃按位置取值 —— 这里就是在验证那个前提。
	if len(values) == 4 {
		t.Fatalf("a failed oid must not silently shift the positions")
	}
}

func TestRateHandlesCounterResets(t *testing.T) {
	if got := rate(2000, 1000, 2); got != 500 {
		t.Fatalf("rate = %v, want 500", got)
	}
	if got := rate(500, 1000, 2); got != 0 {
		t.Fatalf("a shrinking counter must yield 0, got %v", got)
	}
	if got := rate(2000, 1000, 0); got != 0 {
		t.Fatalf("zero elapsed must yield 0, got %v", got)
	}
}

// --- 采样器 -----------------------------------------------------------------

type fakeSource struct {
	host      HostSample
	hostErr   error
	procs     map[int]ProcessSample
	procErr   error
	hostCalls int
}

func (f *fakeSource) Host(context.Context) (HostSample, error) {
	f.hostCalls++
	if f.hostErr != nil {
		return HostSample{}, f.hostErr
	}
	return f.host, nil
}

func (f *fakeSource) Processes(_ context.Context, pids []int) (map[int]ProcessSample, error) {
	if f.procErr != nil {
		return nil, f.procErr
	}
	out := map[int]ProcessSample{}
	for _, pid := range pids {
		if sample, ok := f.procs[pid]; ok {
			out[pid] = sample
		}
	}
	return out, nil
}

func (f *fakeSource) Describe() string { return "fake" }

func TestSamplerReportsHostAndApps(t *testing.T) {
	source := &fakeSource{
		host:  HostSample{CPUPercent: 12.5, MemoryUsedBytes: 100, MemoryTotalBytes: 200, NetRxBytesPerSec: 1, NetTxBytesPerSec: 2},
		procs: map[int]ProcessSample{42: {PID: 42, CPUPercent: 3.5, MemoryBytes: 4096}},
	}
	sampler := NewSampler(source, time.Hour, 10)
	sampler.sampleOnce(context.Background(), func() []AppRef {
		return []AppRef{
			{AppID: "with-proc", PID: 42, StartedAt: time.Now().Add(-90 * time.Second)},
			{AppID: "static", PID: 0, StartedAt: time.Now().Add(-10 * time.Second)},
		}
	})

	got := sampler.Latest()
	if got.Host.CPUPercent != 12.5 {
		t.Fatalf("host sample not recorded: %#v", got.Host)
	}
	if len(got.Apps) != 2 {
		t.Fatalf("expected two apps, got %d", len(got.Apps))
	}
	if got.Apps[0].MemoryBytes != 4096 || got.Apps[0].CPUPercent != 3.5 {
		t.Fatalf("process resources not attached: %#v", got.Apps[0])
	}
	if got.Apps[0].UptimeSeconds < 80 {
		t.Fatalf("uptime should be derived from the start time, got %d", got.Apps[0].UptimeSeconds)
	}
	// 静态站点没有独立进程：只报运行时长，不假装知道它的 CPU 与内存。
	if got.Apps[1].PID != 0 || got.Apps[1].CPUPercent != 0 || got.Apps[1].MemoryBytes != 0 {
		t.Fatalf("a static app must not report invented resources: %#v", got.Apps[1])
	}
	if got.Apps[1].UptimeSeconds < 5 {
		t.Fatalf("a static app still has an uptime, got %d", got.Apps[1].UptimeSeconds)
	}
}

// 主机采样失败不该让站点采样一起消失：两者走的是不同来源。
func TestSamplerKeepsAppsWhenHostSamplingFails(t *testing.T) {
	source := &fakeSource{
		hostErr: errors.New("sysctl unavailable"),
		procs:   map[int]ProcessSample{7: {PID: 7, CPUPercent: 1}},
	}
	sampler := NewSampler(source, time.Hour, 10)
	sampler.sampleOnce(context.Background(), func() []AppRef {
		return []AppRef{{AppID: "a", PID: 7, StartedAt: time.Now()}}
	})
	got := sampler.Latest()
	if got.Host.CPUPercent != 0 {
		t.Fatalf("a failed host sample must leave zeros, got %v", got.Host.CPUPercent)
	}
	if len(got.Apps) != 1 || got.Apps[0].CPUPercent != 1 {
		t.Fatalf("app samples must survive a host failure: %#v", got.Apps)
	}
}

// 一个已经退出的进程不该出现在结果里，也不该让整次采样失败。
func TestSamplerToleratesVanishedProcesses(t *testing.T) {
	source := &fakeSource{procs: map[int]ProcessSample{}}
	sampler := NewSampler(source, time.Hour, 10)
	sampler.sampleOnce(context.Background(), func() []AppRef {
		return []AppRef{{AppID: "gone", PID: 999999, StartedAt: time.Now()}}
	})
	got := sampler.Latest()
	if len(got.Apps) != 1 {
		t.Fatalf("the app should still be reported, got %d", len(got.Apps))
	}
	if got.Apps[0].MemoryBytes != 0 {
		t.Fatalf("a vanished process has no resources to report")
	}
}

func TestSamplerHistoryIsBounded(t *testing.T) {
	source := &fakeSource{host: HostSample{CPUPercent: 1}}
	sampler := NewSampler(source, time.Hour, 3)
	for i := 0; i < 10; i++ {
		sampler.sampleOnce(context.Background(), nil)
	}
	if got := len(sampler.HostHistory()); got != 3 {
		t.Fatalf("history should be capped at 3, got %d", got)
	}
}

func TestSamplerRunStopsWithContext(t *testing.T) {
	source := &fakeSource{host: HostSample{CPUPercent: 1}}
	sampler := NewSampler(source, 5*time.Millisecond, 5)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sampler.Run(ctx, nil); close(done) }()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run must return when the context is cancelled")
	}
	// 立刻采过一次：界面打开时不该等一个间隔才看到数字。
	if source.hostCalls == 0 {
		t.Fatalf("Run should sample once immediately")
	}
}

// alwaysFailingSource 是一个总是失败的采样源。
//
// 未实现平台的后端（source_other.go）带构建标签，在这里不可见，
// 因此用一个等价替身来锁住同一条契约。
type alwaysFailingSource struct{}

func (alwaysFailingSource) Describe() string { return "unsupported" }

func (alwaysFailingSource) Host(context.Context) (HostSample, error) {
	return HostSample{}, errors.New("unsupported")
}

func (alwaysFailingSource) Processes(context.Context, []int) (map[int]ProcessSample, error) {
	return nil, errors.New("unsupported")
}

// 不支持的平台必须**报错**而不是返回零：零看起来像"什么都没占用"，
// 而上层据此显示的应该是"此平台不支持"。
func TestUnsupportedSourceReportsAnError(t *testing.T) {
	if _, err := (alwaysFailingSource{}).Host(context.Background()); err == nil {
		t.Fatalf("an unsupported platform must report an error, not zeros")
	}
	sampler := NewSampler(alwaysFailingSource{}, time.Hour, 5)
	sampler.sampleOnce(context.Background(), nil)
	if got := sampler.Latest(); got.Host.MemoryTotalBytes != 0 {
		t.Fatalf("nothing should have been recorded")
	}
}

// 磁盘与 GPU 是 HostSample 上新加的两个字段，它们必须原样穿过采样器：
// Latest 会做一次浅拷贝，而 Disks 是切片 —— 调用方拿到的列表不该被
// 下一次采样改写（Apps 早就是这么做的）。
func TestSamplerPublishesDisksAndGPU(t *testing.T) {
	utilization := 42.5
	source := &fakeSource{host: HostSample{
		CPUPercent: 7,
		Disks: []DiskSample{
			{MountPoint: "/", FSType: "apfs", TotalBytes: 100, UsedBytes: 60, FreeBytes: 40},
		},
		GPU: &GPUSample{Name: "Apple M4", Utilization: &utilization, Backend: "darwin-ioreg"},
	}}
	sampler := NewSampler(source, time.Hour, 5)
	sampler.sampleOnce(context.Background(), nil)

	first := sampler.Latest()
	if len(first.Host.Disks) != 1 || first.Host.Disks[0].MountPoint != "/" {
		t.Fatalf("disk samples not recorded: %#v", first.Host.Disks)
	}
	if first.Host.GPU == nil || first.Host.GPU.Utilization == nil {
		t.Fatalf("gpu sample not recorded: %#v", first.Host.GPU)
	}
	if *first.Host.GPU.Utilization != 42.5 || first.Host.GPU.Name != "Apple M4" {
		t.Fatalf("unexpected gpu sample: %#v", first.Host.GPU)
	}
	// CPU 与磁盘/GPU 在同一个结构里，但互不牵连：这里顺便钉住它们没被
	// 新字段挤掉。
	if first.Host.CPUPercent != 7 {
		t.Fatalf("cpu percent = %v", first.Host.CPUPercent)
	}

	// 调用方改的是自己的副本，读第二次必须还是原值。
	first.Host.Disks[0].MountPoint = "/tampered"
	if second := sampler.Latest(); second.Host.Disks[0].MountPoint != "/" {
		t.Fatalf("Latest handed out a slice it still owns: %#v", second.Host.Disks)
	}
}

// 缺席就是缺席：Latest 的拷贝不能"顺手"把 nil 变成空切片 ——
// 界面对 nil 与 [] 的处理不一样（前者是"没有这一项"，后者是"有这一项，
// 但一个都没有"）。不支持的平台（source_other.go 那条路径）与读不到
// 数据的机器走的正是 nil 这一支。
func TestSamplerLeavesAbsentDiskAndGPUAlone(t *testing.T) {
	source := &fakeSource{host: HostSample{CPUPercent: 3}}
	sampler := NewSampler(source, time.Hour, 5)
	sampler.sampleOnce(context.Background(), nil)

	got := sampler.Latest().Host
	if got.Disks != nil || got.GPU != nil {
		t.Fatalf("absent disk/gpu must stay absent: %#v %#v", got.Disks, got.GPU)
	}
	if got.CPUPercent != 3 {
		t.Fatalf("cpu percent = %v", got.CPUPercent)
	}
}

// 下面两条用的是**这台机器上真实命令的输出**。
//
// 此前这两个指标各有一条基于"我以为的格式"的测试：CPU 用 kern.cp_time 的
// 六个数、内存用 vm.page_* 的六个数。它们在 macOS 27 上**全都不存在**，
// 于是首页的 CPU 恒为 0%、内存显示"共 Zero KB"，而测试一直是绿的。
// 教训是：解析系统命令的测试，fixture 必须来自真实输出。

func TestParseTopCPUTakesTheLastSample(t *testing.T) {
	// `top -l 2 -n 0` 的真实形状：第一行是开机至今的平均值，
	// 第二行才是当前值。取错行会得到一个几乎不动的数字。
	body := `Processes: 500 total, 2 running, 498 sleeping, 3000 threads
2026/10/04 16:14:54
Load Avg: 3.50, 3.20, 3.10
CPU usage: 3.39% user, 5.12% sys, 91.48% idle
SharedLibs: 200M resident
CPU usage: 8.39% user, 14.12% sys, 77.48% idle
`
	idle, ok := parseTopCPU(body)
	if !ok {
		t.Fatal("expected the CPU usage line to parse")
	}
	if idle != 77.48 {
		t.Fatalf("idle = %v, want the LAST sample 77.48", idle)
	}
}

func TestParseTopCPURejectsOutputWithoutTheLine(t *testing.T) {
	if _, ok := parseTopCPU("Processes: 1 total\n"); ok {
		t.Fatalf("output without a CPU usage line must not parse")
	}
}

func TestParseVMStatReadsTheRealShape(t *testing.T) {
	// 这台机器上 `vm_stat` 的真实输出（截取了需要的部分）。
	body := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    12143.
Pages active:                                 242433.
Pages inactive:                               235376.
Pages speculative:                             10309.
Pages throttled:                                   0.
Pages wired down:                             180736.
Pages purgeable:                                6124.
"Translation faults":                      512046715.
Pages copy-on-write:                        12889867.
Pages occupied by compressor:                 334128.
`
	stat, ok := parseVMStat(body)
	if !ok {
		t.Fatal("expected vm_stat to parse")
	}
	if stat.PageSize != 16384 {
		t.Fatalf("page size = %d", stat.PageSize)
	}
	if stat.Active != 242433 || stat.Wired != 180736 || stat.CompressorOccupied != 334128 {
		t.Fatalf("unexpected counters: %#v", stat)
	}
	if stat.Free != 12143 || stat.Inactive != 235376 {
		t.Fatalf("unexpected free/inactive: %#v", stat)
	}

	// 与活动监视器同口径：活跃 + wired + 压缩器占用。
	want := uint64(242433+180736+334128) * 16384
	if got := stat.used(stat.PageSize); got != want {
		t.Fatalf("used = %d, want %d", got, want)
	}
	// 那个数字应当落在总内存之内 —— 一个超过物理内存的"已用"是明显的错。
	total := uint64(17179869184)
	if got := stat.used(stat.PageSize); got >= total {
		t.Fatalf("used (%d) must stay below total (%d)", got, total)
	}
	// 而它也不该等于把 inactive/purgeable 也算进去的那个数，
	// 那个数远高于系统自己的读数。
	inflated := uint64(242433+180736+334128+235376+6124) * 16384
	if stat.used(stat.PageSize) == inflated {
		t.Fatalf("inactive/purgeable must not count as used")
	}
}

func TestParseVMStatToleratesLabelsInAnyOrderAndMissingOnes(t *testing.T) {
	// 标签取数而不是按行号：这些行的顺序在不同版本里会变。
	body := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages wired down:                                 100.
Pages active:                                     200.
`
	stat, ok := parseVMStat(body)
	if !ok {
		t.Fatal("should parse with only the essential counters present")
	}
	if stat.PageSize != 4096 || stat.Active != 100+0 {
		// Active=200, Wired=100
	}
	if stat.Active != 200 || stat.Wired != 100 {
		t.Fatalf("unexpected: %#v", stat)
	}
	if stat.used(stat.PageSize) != uint64(300)*4096 {
		t.Fatalf("used = %d", stat.used(stat.PageSize))
	}
}

func TestParseVMStatRejectsOutputWithoutCounters(t *testing.T) {
	if _, ok := parseVMStat("Mach Virtual Memory Statistics: (page size of 16384 bytes)\n"); ok {
		t.Fatalf("a header alone must not parse")
	}
}

// --- 磁盘 -------------------------------------------------------------------

// TestKeepMountBlocksOnlyPseudoFilesystems 钉住"过滤"这一刀切在哪。
//
// 两个方向都要测，而**放行**那一侧更要紧：这是一份黑名单，它唯一能做错的
// 事就是把真文件系统误伤掉 —— 用户于是在界面上少看到一块盘，
// 而且没有任何报错。
func TestKeepMountBlocksOnlyPseudoFilesystems(t *testing.T) {
	blocked := []string{
		"devfs", "devicefs", "autofs", "proc", "procfs", "sysfs",
		"cgroup", "cgroup2", "tmpfs", "devpts", "securityfs", "pstore",
		"bpf", "tracefs", "debugfs", "configfs", "fusectl", "mqueue",
		"hugetlbfs", "nsfs", "ramfs", "efivarfs", "rpc_pipefs",
		"binfmt_misc", "squashfs", "overlay", "none",
	}
	for _, fsType := range blocked {
		if keepMount(fsType) {
			t.Errorf("伪文件系统 %s 不该出现在磁盘列表里", fsType)
		}
	}

	// 这些全是**真**文件系统，其中多数不会出现在写这段代码的机器上 ——
	// 黑名单的整个理由就是它们必须原样留下（换成白名单会安静地藏掉它们，
	// 而用户看到的是"我插的 U 盘不见了"）。
	kept := []string{
		"apfs", "hfs", "ext4", "xfs", "btrfs", "zfs", "f2fs",
		"nfs", "nfs4", "cifs", "smb", "sshfs", "fuse.mergerfs",
		"exfat", "ntfs", "msdos", "vfat", "iso9660", "udf", "9p",
	}
	for _, fsType := range kept {
		if !keepMount(fsType) {
			t.Errorf("真文件系统 %s 被误伤了：用户会少看到一块盘", fsType)
		}
	}

	// 大小写不该改变判断：FUSE 与网络文件系统会报出用户自己起的名字。
	if !keepMount("SSHFS") {
		t.Errorf("类型名的大小写不该影响判断")
	}
}

// TestParseProcMountsDecodesEscapedPaths 钉住八进制转义。
//
// 这几列是空格分隔的，所以内核把值里的空格写成 \040 —— 不解码的话，
// 用户会看到一个叫 "My\040Disk" 的卷，然后以为是自己当初起错了名字。
func TestParseProcMountsDecodesEscapedPaths(t *testing.T) {
	body := `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0
/dev/nvme0n1p2 / ext4 rw,relatime 0 0
/dev/disk2s1 /Volumes/My\040Disk exfat rw,noatime 0 0
//nas.local/My\040Share /mnt/nas\040share cifs rw,vers=3.1.1 0 0
/dev/sdb1 /mnt/tab\011name vfat rw 0 0
/dev/sdc1 /mnt/back\134slash ntfs rw 0 0

this line has no columns
`
	entries := parseProcMounts(body)
	if len(entries) != 7 {
		t.Fatalf("expected 7 mount entries, got %d: %#v", len(entries), entries)
	}
	byMount := map[string]mountEntry{}
	for _, entry := range entries {
		byMount[entry.MountPoint] = entry
	}

	if got := byMount["/Volumes/My Disk"]; got.FSType != "exfat" {
		t.Fatalf("escaped space not decoded: %#v", got)
	}
	// 设备名里也可能有空格（NAS 的共享名），而且它同样要能对上。
	if got := byMount["/mnt/nas share"]; got.Device != "//nas.local/My Share" {
		t.Fatalf("device not decoded: %#v", got)
	}
	if _, found := byMount["/mnt/tab\tname"]; !found {
		t.Fatalf("escaped tab not decoded: %#v", byMount)
	}
	if _, found := byMount["/mnt/back\\slash"]; !found {
		t.Fatalf("escaped backslash not decoded: %#v", byMount)
	}
}

// TestFinalizeDisksDropsZeroSizeAndSorts 钉住三件事：
// 黑名单、**总容量为 0 的兜底**、以及稳定的排序。
func TestFinalizeDisksDropsZeroSizeAndSorts(t *testing.T) {
	raw := []DiskSample{
		// 带空格的挂载点（内核会写成 \040，见上一条转义测试）：
		// 过滤只按类型与容量，绝不按"路径里有没有怪字符"，
		// 因此它必须原样留着。
		{MountPoint: "/Volumes/My Disk", FSType: "exfat", TotalBytes: 100, FreeBytes: 10},
		{MountPoint: "/", FSType: "apfs", TotalBytes: 1000, FreeBytes: 100},
		// 黑名单里的：容量再大也不显示（tmpfs 的大小是内存的一部分，
		// 跟磁盘并列会让人以为多了一块盘）。
		{MountPoint: "/run", FSType: "tmpfs", TotalBytes: 8000, FreeBytes: 8000},
		{MountPoint: "/proc", FSType: "proc", TotalBytes: 0},
		// 类型是**真**的，但容量为 0：兜底那一条，不需要穷举名单。
		// 这里刻意用一个没人会列进黑名单的类型。
		{MountPoint: "/mnt/empty", FSType: "ext4", TotalBytes: 0},
		{MountPoint: "/mnt/nas", FSType: "cifs", TotalBytes: 5000, FreeBytes: 500},
		// 同一个挂载点挂了两次（容器里 /etc/hosts 这类 bind mount）：
		// 留最后一个，也就是盖在上面的那个。
		{MountPoint: "/mnt/nas", FSType: "cifs", TotalBytes: 6000, FreeBytes: 600},
		{MountPoint: "", FSType: "ext4", TotalBytes: 100},
		// APFS 容器里的其他卷。它们与 `/` **报同一组数字**（同一个
		// 容器的块计数），留着会让界面显示成"十几个一模一样、都快满了
		// 的磁盘"。见 finalizeDisks 里的说明。
		{MountPoint: "/System/Volumes/Data", FSType: "apfs", TotalBytes: 1000, FreeBytes: 100},
		{MountPoint: "/System/Volumes/VM", FSType: "apfs", TotalBytes: 1000, FreeBytes: 100},
	}

	got := finalizeDisks(raw)
	want := []string{"/", "/Volumes/My Disk", "/mnt/nas"}
	if len(got) != len(want) {
		t.Fatalf("kept %d mounts (%#v), want %d", len(got), got, len(want))
	}
	for index, mount := range want {
		if got[index].MountPoint != mount {
			t.Fatalf("order/content mismatch at %d: got %q, want %q (%#v)",
				index, got[index].MountPoint, mount, got)
		}
	}
	// 重复挂载点留下的是**最后**那一条。
	if got[2].TotalBytes != 6000 {
		t.Fatalf("the topmost mount of a repeated mount point must win: %#v", got[2])
	}
	// 空输入不该返回 nil 之外的东西（调用方按 len 判断）。
	if len(finalizeDisks(nil)) != 0 {
		t.Fatalf("no mounts must stay empty")
	}
}

// TestDiskCapacityLeavesReservedBlocksOutOfBothSides 钉住 used/free 的口径。
//
// 三个数刻意**不**满足 used + free == total：ext4 默认给 root 留 5%，
// 那部分既不算已用、也不算用户可用。把它摊进任何一边都是在编一个
// 用户对不上的数字（Finder / df 的"可用"取的也是 Bavail）。
func TestDiskCapacityLeavesReservedBlocksOutOfBothSides(t *testing.T) {
	// 1000 块 / 4096 字节：100 块空闲，其中只有 50 块对普通用户可用。
	total, used, free := diskCapacity(4096, 1000, 100, 50)
	if total != 1000*4096 {
		t.Fatalf("total = %d", total)
	}
	if used != 900*4096 {
		t.Fatalf("used = %d, want 900 blocks", used)
	}
	if free != 50*4096 {
		t.Fatalf("free = %d, want the AVAILABLE 50 blocks", free)
	}
	if used+free == total {
		t.Fatalf("root-reserved blocks must not be counted on either side")
	}

	// 计数器异常：可用块比总块数还多时不能下溢成一个天文数字。
	if _, used, free := diskCapacity(4096, 10, 20, 30); used != 0 || free != 10*4096 {
		t.Fatalf("an impossible counter must clamp, got used=%d free=%d", used, free)
	}
	// 块大小为 0：宁可报 0（随后会被"容量为 0"那条滤掉），
	// 也不要报一个 16 EB 的盘。
	if total, used, free := diskCapacity(0, 1000, 100, 50); total != 0 || used != 0 || free != 0 {
		t.Fatalf("zero block size must yield zeros, got %d/%d/%d", total, used, free)
	}
}

// TestCStringStopsAtTheNUL 钉住定长字符数组的转换。
//
// darwin 的 Statfs_t 里挂载点是 [1024]int8：内核只写前面几个字节，
// 后面全是 0。把整段当字符串会得到一个挂着几百个 NUL 的挂载点。
func TestCStringStopsAtTheNUL(t *testing.T) {
	raw := []int8{'/', 'V', 'o', 'l', 0, 'x', 'x'}
	if got := cString(raw[:]); got != "/Vol" {
		t.Fatalf("cString = %q, want %q", got, "/Vol")
	}
	if got := cString(nil); got != "" {
		t.Fatalf("cString(nil) = %q", got)
	}
	if got := cString([]int8{0, 'a'}); got != "" {
		t.Fatalf("a leading NUL means an empty string, got %q", got)
	}
}

// --- GPU（ioreg） -----------------------------------------------------------

// ioregIOAcceleratorSample 是这台机器上 `ioreg -r -d 1 -c IOAccelerator`
// 的**真实输出**：逐字照抄，只裁掉了 "IOReportLegend" 那一行 ——
// 它有 45 KB（几百个 IOReport 通道），与这里要验的键无关。
//
// 为什么必须用真输出：见本文件里 top / vm_stat 那两条注释。手写的
// "我以为的格式"曾经让 CPU 与内存指标在 macOS 27 上恒为 0，
// 而所有单元测试都是绿的 —— 因为喂进去的 fixture 是我编的。
//
// 注意同一行里还有 "Tiler Utilization %" 与 "Renderer Utilization %"，
// 它们的值（15）与设备占用（17）**不同**：按位置取值会安静地读错一个数。
const ioregIOAcceleratorSample = `+-o AGXAcceleratorG16G  <class AGXAcceleratorG16G, id 0x100000478, registered, matched, active, busy 0 (752 ms), retain 101>
    {
      "SchedulerState" = {"Stamps"=({"idx"=25,"sub"=896195072,"gpu"=896194816}),"BusyWorkQueues"=({"submit"=(104077551360),"finished"=619245,"id"=25,"submitted"=619246,"added"=619246,"aborted"=No,"state"="Idle"},{"submit"=(108270377472),"finished"=554667,"id"=26,"submitted"=554668,"added"=554668,"aborted"=No,"state"="Idle"})}
      "IOMatchedAtBoot" = Yes
      "vendor-id" = <6b100000>
      "GPURawCounterBundleName" = "AGXGPURawCounterBundle"
      "AGXParameterBufferMaxSizeEverMemless" = 574881792
      "GPURawCounterPluginClassName" = "AGXGPURawCounterSourceGroup"
      "MetalPluginClassName" = "AGXG16GDevice"
      "SCMVersionNumber" = ""
      "AGCInfo" = {"fLastSubmissionPID"=835,"fSubmissionsSinceLastCheck"=0,"fBusyCount"=0}
      "MetalPluginName" = "AGXMetalG16G_B0"
      "IONameMatched" = "gpu,t8132"
      "CommandSubmissionEnabled" = Yes
      "PerformanceStatistics" = {"In use system memory (driver)"=0,"Alloc system memory"=4597121024,"Tiler Utilization %"=15,"recoveryCount"=0,"lastRecoveryTime"=0,"Renderer Utilization %"=15,"TiledSceneBytes"=688128,"Device Utilization %"=17,"SplitSceneCount"=0,"Allocated PB Size"=82444288,"In use system memory"=430571520}
      "IOGLBundleName" = "AppleMetalOpenGLRenderer"
      "AGXParameterBufferMaxSizeNeverMemless" = 287440896
      "IOGLESBundleName" = "AppleMetalGLRenderer"
      "IOSourceVersion" = "360.34.5"
      "IOPersonalityPublisher" = "com.apple.AGXG16G"
      "IOPowerManagement" = {"CurrentPowerState"=1,"CapabilityFlags"=2,"MaxPowerState"=1,"DriverPowerState"=1}
      "model" = "Apple M4"
      "AGXTraceCodeVersion" = "3.44.12"
      "SCMBuildTime" = ""
      "CFBundleIdentifier" = "com.apple.AGXG16G"
      "gpu-core-count" = 10
      "IOProviderClass" = "AppleARMIODevice"
      "AGXParameterBufferMaxSize" = 862322688
      "IONameMatch" = ("gpu,t8015","gpu,t8027","gpu,t8030","gpu,t8103","gpu,t8122","gpu,t8132")
      "IOReportLegendPublic" = Yes
      "IOClass" = "AGXAcceleratorG16G"
      "CFBundleIdentifierKernel" = "com.apple.AGXG16G"
      "GPUConfigurationVariable" = {"num_gps"=4,"gpu_gen"=16,"is_sksm"=0,"usc_gen"=3,"num_cores"=10,"num_mgpus"=1,"gpu_var"="G","core_mask_list"=(1023),"num_frags"=10}
      "IOGeneralInterest" = "IOCommand is not serializable"
      "IOMatchCategory" = "IOAccelerator"
      "IOProbeScore" = 10000
      "KDebugVersion" = 4294967296
      "SurfaceList" = ()
    }
`

func TestParseIOAcceleratorReadsTheRealShape(t *testing.T) {
	sample := parseIOAccelerator(ioregIOAcceleratorSample)
	if sample == nil {
		t.Fatal("expected a GPU sample from the real ioreg output")
	}
	if sample.Backend != gpuBackendDarwin {
		t.Fatalf("backend = %q, want %q", sample.Backend, gpuBackendDarwin)
	}
	// 型号名是可选的点缀，但在这台机器上它就在输出里（"model" 那一行）。
	if sample.Name != "Apple M4" {
		t.Fatalf("name = %q, want the accelerator model", sample.Name)
	}
	if sample.Utilization == nil {
		t.Fatal("Device Utilization % was not read from the real output")
	}
	// 17 而不是 15：同一行里 Tiler / Renderer Utilization % 都是 15，
	// 读错键的话会得到 15，而且不会有任何报错。
	if *sample.Utilization != 17 {
		t.Fatalf("utilization = %v, want 17 (Device Utilization %%, not Tiler/Renderer)", *sample.Utilization)
	}
}

// ioreg 的空白用法在同一次输出里就不统一：外面是 `"K" = V`，
// PerformanceStatistics 里面是 `"K"=V`。两种都必须认得。
func TestParseIOAcceleratorToleratesFormatVariations(t *testing.T) {
	cases := []struct {
		name string
		// statistics 是 PerformanceStatistics 那一行的值。
		statistics  string
		utilization *float64
	}{
		{
			name:        "spaces around the equals sign",
			statistics:  `{"Device Utilization %" = 4}`,
			utilization: floatPtr(4),
		},
		{
			name:        "no spaces at all",
			statistics:  `{"Device Utilization %"=4}`,
			utilization: floatPtr(4),
		},
		{
			name:        "the value is the last entry, without a trailing comma",
			statistics:  `{"Renderer Utilization %"=1,"Device Utilization %"=9}`,
			utilization: floatPtr(9),
		},
		{
			name:        "a quoted value",
			statistics:  `{"Device Utilization %"="7"}`,
			utilization: floatPtr(7),
		},
		{
			// 值里的花括号不是结构：把它当括号会让配对整个错位。
			name:        "braces inside a string value",
			statistics:  `{"Note"="} not a closing brace","Device Utilization %"=9}`,
			utilization: floatPtr(9),
		},
		{
			name:        "a value that is not a number",
			statistics:  `{"Device Utilization %"=n/a}`,
			utilization: nil,
		},
		{
			// 契约把这个字段限定在 0..100（openapi.yaml）：
			// 宁可报"没读到"，也不要报一个契约不允许的数。
			name:        "a value outside the 0..100 contract",
			statistics:  `{"Device Utilization %"=250}`,
			utilization: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sample := parseIOAccelerator(syntheticAcceleratorEntry(tc.statistics))
			if sample == nil {
				t.Fatal("a PerformanceStatistics dictionary is enough to report a sample")
			}
			if sample.Backend != gpuBackendDarwin {
				t.Fatalf("backend = %q", sample.Backend)
			}
			switch {
			case tc.utilization == nil && sample.Utilization != nil:
				t.Fatalf("utilization = %v, want nil", *sample.Utilization)
			case tc.utilization != nil && sample.Utilization == nil:
				t.Fatalf("utilization = nil, want %v", *tc.utilization)
			case tc.utilization != nil && *sample.Utilization != *tc.utilization:
				t.Fatalf("utilization = %v, want %v", *sample.Utilization, *tc.utilization)
			}
		})
	}
}

// syntheticAcceleratorEntry 把一行 PerformanceStatistics 包成 ioreg 的条目形状。
//
// 条目的第一行（`+-o …`）不是可选的装饰：解析器按它切分多 GPU 的条目，
// 因此测试的输入必须带上它，否则测的是一种 ioreg 不会产生的输出。
func syntheticAcceleratorEntry(statistics string) string {
	return `+-o SomeAccelerator  <class SomeAccelerator, id 0x1, registered>
    {
      "PerformanceStatistics" = ` + statistics + `
    }
`
}

// 键缺席**不是**错误：老一些的、或非 Apple 驱动的 GPU 就是这样。
//
// 此时要给后端名 + nil 占用率，而不是编一个 0 —— 0% 在界面上表示
// "显卡闲着"，与"这台设备不报占用"是两件完全不同的事。
func TestParseIOAcceleratorWithoutTheKey(t *testing.T) {
	entry := `+-o SomeAccelerator  <class SomeAccelerator, id 0x1, registered>
    {
      "PerformanceStatistics" = {"Tiler Utilization %"=3,"Renderer Utilization %"=3}
      "model" = "Some Old GPU"
    }
`
	sample := parseIOAccelerator(entry)
	if sample == nil {
		t.Fatal("a supported backend without the key must still report a sample")
	}
	if sample.Utilization != nil {
		t.Fatalf("the key is absent, so utilization must stay nil, got %v", *sample.Utilization)
	}
	if sample.Backend != gpuBackendDarwin {
		t.Fatalf("backend = %q", sample.Backend)
	}
	if sample.Name != "Some Old GPU" {
		t.Fatalf("name = %q", sample.Name)
	}
}

// 没有可采样的加速器时是 nil，而不是一个空样本。
//
// nil 的含义是"这个平台/这台机器没有 GPU 采样"，界面据此显示"不支持"；
// 而一个 Utilization == nil 的样本的含义是"支持采样，但这次没读到值"。
// 两者混起来之后，一台无头 Mac 会被说成"有 GPU，只是读不到"。
func TestParseIOAcceleratorWithoutAnAccelerator(t *testing.T) {
	if sample := parseIOAccelerator(""); sample != nil {
		t.Fatalf("empty output must yield nil, got %#v", sample)
	}
	// 匹配到类名但没有性能统计字典的辅助对象也一样：它不是加速器。
	entry := `+-o SomeHelper  <class IOAccelerator, id 0x2, registered>
    {
      "IOClass" = "IOAccelerator"
    }
`
	if sample := parseIOAccelerator(entry); sample != nil {
		t.Fatalf("an entry without PerformanceStatistics is not an accelerator, got %#v", sample)
	}
}

// 只在 PerformanceStatistics 里找那个键。
//
// 全文搜索更省事，但 ioreg 的属性表里有几十个字典，只要别处出现同名的键，
// 就会读到另一个数字 —— 而且不会有任何报错。
func TestParseIOAcceleratorOnlyLooksInsideTheDictionary(t *testing.T) {
	entry := `+-o SomeAccelerator  <class SomeAccelerator, id 0x3, registered>
    {
      "OtherStatistics" = {"Device Utilization %"=99}
      "PerformanceStatistics" = {"Device Utilization %"=8}
    }
`
	sample := parseIOAccelerator(entry)
	if sample == nil || sample.Utilization == nil {
		t.Fatalf("expected a utilization value, got %#v", sample)
	}
	if *sample.Utilization != 8 {
		t.Fatalf("read a same-named key from outside the dictionary: %v", *sample.Utilization)
	}
}

// 多 GPU 的机器（核显 + 独显的 MacBook Pro）上，ioreg 会打好几条，
// 而**不是每一条都带占用值**。只看第一条会在那种机器上永远显示"没有值"，
// 而其实独显那边是有数字的。
func TestParseIOAcceleratorPrefersAnEntryWithAValue(t *testing.T) {
	entry := `+-o IntelAccelerator  <class IntelAccelerator, id 0x4, registered>
    {
      "PerformanceStatistics" = {"Tiler Utilization %"=1}
      "model" = "Intel Iris Plus Graphics"
    }
+-o AMDAccelerator  <class AMDAccelerator, id 0x5, registered>
    {
      "PerformanceStatistics" = {"Device Utilization %"=42}
      "model" = "AMD Radeon Pro 5500M"
    }
`
	sample := parseIOAccelerator(entry)
	if sample == nil || sample.Utilization == nil {
		t.Fatalf("the second entry has a value; it must be used: %#v", sample)
	}
	if *sample.Utilization != 42 {
		t.Fatalf("utilization = %v, want 42", *sample.Utilization)
	}
	// 名字必须来自同一条条目：把核显的名字配到独显的占用上是一种
	// 很难发现的错（两个数字都"看着合理"）。
	if sample.Name != "AMD Radeon Pro 5500M" {
		t.Fatalf("name = %q, want the name from the same entry", sample.Name)
	}
}

// floatPtr 返回指向 value 的指针：GPUSample.Utilization 用 nil 表示
// "没读到值"，因此测试里必须能表达"读到了"。
func floatPtr(value float64) *float64 { return &value }
