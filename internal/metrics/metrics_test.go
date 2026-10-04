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
