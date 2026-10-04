package metrics

import (
	"strconv"
	"strings"
)

// 本文件只放**纯函数解析器**：把系统命令或 /proc 的文本输出变成结构化的
// 数字。它们不碰文件系统、不起进程，因此可以用抓下来的真实输出做测试 ——
// 这是这一层唯一可靠的验证方式（macOS 上没有 cgo 就拿不到 mach 调用）。
//
// 解析一律"尽力而为"：认得出来的字段才填，认不出来就留零。一个格式变化的
// 字段不该让整个采样失败，因为界面宁可少一个数字，也不要整块空白。

// parseProcStat 解析 Linux 的 /proc/stat，返回各 CPU 时间片的合计。
//
// 只取第一行（cpu 合计）。字段顺序（内核文档）：
//
//	user nice system idle iowait irq softirq steal guest guest_nice
type cpuTimes struct {
	total uint64
	idle  uint64
}

func parseProcStat(text string) (cpuTimes, bool) {
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		var values []uint64
		for _, field := range fields[1:] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				break
			}
			values = append(values, value)
		}
		if len(values) < 4 {
			return cpuTimes{}, false
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		// idle 是第 4 个字段，iowait 是第 5 个：等待 IO 的时间
		// 对用户来说也是"没在干活"。
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		return cpuTimes{total: total, idle: idle}, true
	}
	return cpuTimes{}, false
}

// cpuPercent 由两次时间片差分算出占用率。
//
// 差分而不是看绝对值：CPU 时间片是开机以来的累计值，只有差值才有意义。
func cpuPercent(prev, next cpuTimes) float64 {
	if next.total <= prev.total {
		// 计数器回绕或时间没走动：宁可报 0，也不要报一个离谱的数。
		return 0
	}
	deltaTotal := float64(next.total - prev.total)
	deltaIdle := float64(0)
	if next.idle > prev.idle {
		deltaIdle = float64(next.idle - prev.idle)
	}
	busy := deltaTotal - deltaIdle
	if busy < 0 {
		busy = 0
	}
	return busy / deltaTotal * 100
}

// parseProcMeminfo 解析 Linux 的 /proc/meminfo，返回已用与总内存（字节）。
//
// "已用"取 MemTotal - MemAvailable：MemAvailable 是内核估计的"还能拿来用"
// 的量，比 MemFree 更接近用户对"可用内存"的直觉（后者不含可回收的缓存）。
func parseProcMeminfo(text string) (used, total uint64, ok bool) {
	values := map[string]uint64{}
	for _, line := range strings.Split(text, "\n") {
		key, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		amount, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		// /proc/meminfo 的单位是 kB。
		values[strings.TrimSpace(key)] = amount * 1024
	}
	total, hasTotal := values["MemTotal"]
	if !hasTotal {
		return 0, 0, false
	}
	available, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		available = values["MemFree"]
	}
	if available > total {
		available = total
	}
	return total - available, total, true
}

// parseProcNetDev 解析 Linux 的 /proc/net/dev，返回非回环接口的收发字节合计。
func parseProcNetDev(text string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		_, rest, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if name == "lo" || name == "" {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		rxBytes, err1 := strconv.ParseUint(fields[0], 10, 64)
		txBytes, err2 := strconv.ParseUint(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		rx += rxBytes
		tx += txBytes
		ok = true
	}
	return rx, tx, ok
}

// parseProcPIDStat 解析 /proc/<pid>/stat 的 utime + stime（单位：时钟节拍）。
//
// 第 2 个字段是进程名，可能带空格与括号，因此从**最后一个右括号之后**开始
// 切分 —— 这是内核文档里明确建议的解析方式。
func parseProcPIDStat(text string) (ticks uint64, rssPages uint64, ok bool) {
	close := strings.LastIndex(text, ")")
	if close < 0 || close+2 >= len(text) {
		return 0, 0, false
	}
	fields := strings.Fields(text[close+2:])
	// 相对 utime 的偏移量：state(0) ppid(1) ... utime 是第 14 个字段，
	// 减去 state 与 ppid 之后在切片里的下标是 11。
	const utimeIndex = 11
	const stimeIndex = 12
	const rssIndex = 21
	if len(fields) <= rssIndex {
		return 0, 0, false
	}
	utime, err1 := strconv.ParseUint(fields[utimeIndex], 10, 64)
	stime, err2 := strconv.ParseUint(fields[stimeIndex], 10, 64)
	rss, err3 := strconv.ParseUint(fields[rssIndex], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	return utime + stime, rss, true
}

// parsePS 解析 `ps -o pid=,%cpu=,rss= -p ...` 的输出。
//
// macOS 与 Linux 的 ps 在这三个字段上的格式一致：pid、百分比、常驻内存
// （Linux 是 kB，macOS 是 kB）。单位差异由调用方按平台换算。
func parsePS(text string, rssUnit uint64) map[int]ProcessSample {
	out := map[int]ProcessSample{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(strings.ReplaceAll(fields[1], ",", "."), 64)
		if err != nil {
			cpu = 0
		}
		rss, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			rss = 0
		}
		out[pid] = ProcessSample{PID: pid, CPUPercent: cpu, MemoryBytes: rss * rssUnit}
	}
	return out
}

// parseNetstatIB 解析 macOS `netstat -ib` 的收发字节合计。
//
// # 两个坑
//
//  1. **同一接口会打多行**：每个地址（link / IPv4 / IPv6）各一行，而字节
//     计数在每行上是重复的。全部相加会把流量算成两三倍，因此只取每个接口
//     的第一行。
//  2. **列是不齐的**：没有地址的行会少一列（`strings.Fields` 把空字段吃掉），
//     于是"按表头下标取值"在那些行上会整体错位 —— 实测过：无地址的 utun
//     接口会把 Opkts 当成 Ibytes。因此这里**从右往左数**：Coll、Obytes、
//     Oerrs、Opkts、Ibytes、Ierrs、Ipkts 的位置在所有行上都是固定的。
func parseNetstatIB(text string) (rx, tx uint64, ok bool) {
	// 从右数的下标（0 是最后一列 Coll）。
	//
	// 列顺序：… Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll
	// 因此 Obytes 距末尾 1 列，Ibytes 距末尾 4 列。
	const (
		fromRightIbytes = 4
		fromRightObytes = 1
	)
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		// 最少要有：名字 + 若干数字列。
		if len(fields) < 6 {
			continue
		}
		name := fields[0]
		if name == "Name" {
			continue
		}
		if strings.HasPrefix(name, "lo") {
			continue
		}
		if seen[name] {
			continue
		}
		rxIndex := len(fields) - 1 - fromRightIbytes
		txIndex := len(fields) - 1 - fromRightObytes
		rxBytes, err1 := strconv.ParseUint(fields[rxIndex], 10, 64)
		txBytes, err2 := strconv.ParseUint(fields[txIndex], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		seen[name] = true
		rx += rxBytes
		tx += txBytes
		ok = true
	}
	return rx, tx, ok
}

// parseSysctlNumbers 解析 `sysctl -n a b c` 的输出（每行一个值）。
//
// 非数字行直接跳过而不是中止：`sysctl` 请求了多个名字时，某一个不存在
// 只会让那一行变成错误信息，其余照常输出。调用方**必须**核对返回的个数
// 是否等于请求的个数 —— 少一个就说明有名字读不出来，此时按位置取值会错位。
func parseSysctlNumbers(text string) []uint64 {
	var out []uint64
	for _, line := range strings.Split(text, "\n") {
		value, err := strconv.ParseUint(strings.TrimSpace(line), 10, 64)
		if err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}

// rate 由两次累计计数与间隔算出每秒速率。
//
// 计数变小意味着接口被重置或计数器回绕：报 0 而不是负数 ——
// 负的速率在界面上无法解释。
func rate(current, previous uint64, elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 || current < previous {
		return 0
	}
	return float64(current-previous) / elapsedSeconds
}
