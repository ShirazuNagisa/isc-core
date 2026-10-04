//go:build darwin

package metrics

import (
	"context"
	"testing"
	"time"
)

// 这两条是**跑真实命令**的测试，只在 macOS 上跑。它们守的是一类
// 单元测试守不住的故障：解析逻辑完全正确，但命令本身跑不通。
//
// 真实发生过：`netstat -ib` 不带 `-n` 时要做名字解析，实测稳定耗时 5.04 秒，
// 正好卡在当时的 5 秒命令超时上 —— 于是它每次都被杀掉、输出为空、解析失败，
// 网络速率**永远是 0**，而界面只是安静地显示"0 B/s"。当时所有单元测试
// 都是绿的，因为它们喂的是我手写的、格式正确的 fixture。

func TestSamplingCompletesWellWithinTheInterval(t *testing.T) {
	source := newPlatformSource()
	start := time.Now()
	sample, err := source.Host(context.Background())
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("host sampling failed: %v", err)
	}
	// 采样间隔是 5 秒。一次采样必须留出充裕余量，否则采样器会排队、
	// 而排队又会把命令推向超时 —— 那正是上面那个故障的成因。
	if elapsed > 4*time.Second {
		t.Fatalf("one host sample took %v, too close to the 5s interval", elapsed)
	}
	// 内存总量在任何 macOS 上都必须取得到。
	if sample.MemoryTotalBytes == 0 {
		t.Fatalf("total memory came back as zero")
	}
	if sample.MemoryUsedBytes == 0 {
		t.Fatalf("used memory came back as zero")
	}
	if sample.MemoryUsedBytes > sample.MemoryTotalBytes {
		t.Fatalf("used memory (%d) exceeds total (%d)", sample.MemoryUsedBytes, sample.MemoryTotalBytes)
	}
}

// 网卡计数器必须真的读得到。
//
// 这条比"速率非零"更可靠：速率是差值，机器真的空闲时它可以是 0，
// 而计数器读不到本身就是故障。
func TestInterfaceCountersAreReadable(t *testing.T) {
	text, err := runCommand(context.Background(), "netstat", "-ibn")
	if err != nil {
		t.Fatalf("netstat failed: %v", err)
	}
	if len(text) == 0 {
		t.Fatalf("netstat produced no output")
	}
	if _, _, ok := parseNetstatIB(text); !ok {
		t.Fatalf("no interface counters could be read from the live output")
	}
}

// 每条采样命令都必须在超时之内返回。
func TestEachSamplingCommandFinishesInTime(t *testing.T) {
	commands := []struct {
		name string
		args []string
	}{
		{"top", []string{"-l", "2", "-n", "0"}},
		{"vm_stat", nil},
		{"sysctl", []string{"-n", "hw.memsize"}},
		{"netstat", []string{"-ibn"}},
		{"ps", []string{"-o", "pid=,%cpu=,rss=", "-p", "1"}},
		// GPU 那一条：实测 0.01 秒，但「跑不起来」正是它最可能的失败方式
		//（ioreg 在某些受限环境里没有权限读 IORegistry），因此它必须
		// 和别的命令一样被盯着。
		{"ioreg", []string{"-r", "-d", "1", "-c", "IOAccelerator"}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			start := time.Now()
			text, err := runCommand(context.Background(), command.name, command.args...)
			if err != nil {
				t.Fatalf("%s failed: %v", command.name, err)
			}
			if len(text) == 0 {
				t.Fatalf("%s produced no output", command.name)
			}
			if elapsed := time.Since(start); elapsed > commandTimeout/2 {
				t.Fatalf("%s took %v, over half of the %v timeout", command.name, elapsed, commandTimeout)
			}
		})
	}
}

// 磁盘枚举必须真的读得到东西。
//
// 与上面两条同一个理由：getfsstat 的用法写错（flags、缓冲区大小、
// 定长字符数组）时，所有单元测试依然全绿 —— 它们喂的是构造出来的
// Statfs_t 数字 —— 而界面上是「这台机器没有磁盘」。
func TestDisksAreReadable(t *testing.T) {
	disks := darwinDisks()
	if len(disks) == 0 {
		t.Fatalf("这台机器上一个挂载点都没读出来")
	}

	sawRoot := false
	for _, disk := range disks {
		if disk.MountPoint == "" || disk.FSType == "" {
			t.Fatalf("挂载点或类型是空的：%#v", disk)
		}
		// 口径不变式：可用 + 已用不会超过总量（预留块两边都不算，
		// 见 diskCapacity）。这条能抓住单位或字段错位这类错误。
		if disk.UsedBytes+disk.FreeBytes > disk.TotalBytes {
			t.Fatalf("%s: used(%d) + free(%d) > total(%d)",
				disk.MountPoint, disk.UsedBytes, disk.FreeBytes, disk.TotalBytes)
		}
		// 容量为 0 的挂载点会被 finalizeDisks 主动滤掉，因此这里看到的
		// 每一个都必须有非零总量。
		if disk.TotalBytes == 0 {
			t.Fatalf("%s: 容量为 0 的挂载点不该留下", disk.MountPoint)
		}
		if disk.MountPoint == "/" {
			sawRoot = true
		}
	}
	// 每一台 macOS 都有 /，而它必然是一块真盘：这条同时证明过滤没有
	// 把系统卷一起滤掉。
	if !sawRoot {
		t.Fatalf("枚举结果里没有 /：%#v", disks)
	}
	// 排序必须确定（界面与测试都按这个顺序看）。
	for index := 1; index < len(disks); index++ {
		if disks[index-1].MountPoint >= disks[index].MountPoint {
			t.Fatalf("挂载点没有按升序排好：%q 在 %q 之前",
				disks[index-1].MountPoint, disks[index].MountPoint)
		}
	}
}

// GPU 那条命令必须能跑，而且解析的是**这台机器**的真实输出。
//
// 无头 Mac 与虚拟机上 ioreg 匹配不到加速器，那时 parseIOAccelerator
// 返回 nil 是正确结果，因此那种机器跳过而不是判失败 —— 这条测试守的是
// 「在有 GPU 的机器上真的读到了」，不是「每台机器都有 GPU」。
func TestGPUUtilizationIsReadable(t *testing.T) {
	text, err := runCommand(context.Background(), "ioreg", "-r", "-d", "1", "-c", "IOAccelerator")
	if err != nil {
		t.Fatalf("ioreg failed: %v", err)
	}
	sample := parseIOAccelerator(text)
	if sample == nil {
		t.Skip("这台机器上没有可采样的加速器（无头 / 虚拟机）")
	}
	if sample.Backend != gpuBackendDarwin {
		t.Fatalf("backend = %q, want %q", sample.Backend, gpuBackendDarwin)
	}
	if sample.Utilization == nil {
		// 老一些的、或非 Apple 驱动的 GPU 不报这个键，这是已知情形
		//（parseIOAccelerator 的注释里写了）。跳过而不是失败。
		t.Skipf("加速器 %q 没有报 Device Utilization %%", sample.Name)
	}
	if *sample.Utilization < 0 || *sample.Utilization > 100 {
		t.Fatalf("utilization = %v, outside 0..100", *sample.Utilization)
	}
	t.Logf("GPU %q 占用 %v%%（backend %s）", sample.Name, *sample.Utilization, sample.Backend)
}
