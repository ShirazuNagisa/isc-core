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
