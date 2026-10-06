package metrics

import (
	"sort"
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

// parseTopCPU 从 `top -l N -n 0` 的输出里取出空闲百分比。
//
// # 为什么用 top 而不是 sysctl
//
// macOS 27 上 `kern.cp_time` 已经**不存在**了（此前用它算 CPU 时间片差分），
// 而走 mach 的 host_statistics 需要 cgo，本项目的内核主体禁止 cgo（D11）。
// `top` 是系统自带、格式稳定，且与活动监视器同源。
//
// 多个采样时取**最后一行**：`top -l 1` 的第一个采样是"开机至今"的平均值，
// 拿它当"当前占用"会显示一个几乎不动的数字。调用方因此应当用 `-l 2`。
func parseTopCPU(text string) (idlePercent float64, ok bool) {
	best := ""
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "CPU usage:") {
			best = trimmed
		}
	}
	if best == "" {
		return 0, false
	}
	// 形如：CPU usage: 8.39% user, 14.12% sys, 77.48% idle
	parts := strings.Split(best, ",")
	for _, part := range parts {
		if !strings.Contains(part, "idle") {
			continue
		}
		fields := strings.Fields(part)
		for _, field := range fields {
			value := strings.TrimSuffix(field, "%")
			if value == field {
				continue
			}
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				continue
			}
			return parsed, true
		}
	}
	return 0, false
}

// vmStatSample 是 vm_stat 里我们关心的那几个计数。
type vmStatSample struct {
	// PageSize 来自输出的首行。
	PageSize int64
	Active   int64
	Inactive int64
	Free     int64
	// Wired 是"Pages wired down"。
	Wired int64
	// Purgeable 是可被回收的页。
	Purgeable int64
	// CompressorOccupied 是压缩器**实际占用**的页（不是被压缩的原始页数）。
	CompressorOccupied int64
}

// parseVMStat 解析 `vm_stat` 的输出。
//
// # 为什么是 vm_stat
//
// macOS 27 上 `vm.page_active_count` / `vm.page_wire_count` /
// `vm.page_compressor_count` 这些 OID 已经不存在了（此前用的就是它们），
// 于是内存指标整块取不到值。vm_stat 与活动监视器同源，格式也稳定。
//
// 按**标签**取数而不是按行号：这些行在不同版本里顺序会变，而标签不会。
func parseVMStat(text string) (vmStatSample, bool) {
	var sample vmStatSample
	// 首行：Mach Virtual Memory Statistics: (page size of 16384 bytes)
	if start := strings.Index(text, "page size of "); start >= 0 {
		rest := text[start+len("page size of "):]
		if end := strings.Index(rest, " "); end > 0 {
			if value, err := strconv.ParseInt(rest[:end], 10, 64); err == nil {
				sample.PageSize = value
			}
		}
	}

	labels := map[string]*int64{
		"Pages free":                   &sample.Free,
		"Pages active":                 &sample.Active,
		"Pages inactive":               &sample.Inactive,
		"Pages wired down":             &sample.Wired,
		"Pages purgeable":              &sample.Purgeable,
		"Pages occupied by compressor": &sample.CompressorOccupied,
	}
	for _, line := range strings.Split(text, "\n") {
		label, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		target, wanted := labels[strings.TrimSpace(label)]
		if !wanted {
			continue
		}
		// 值形如 "248262."（带尾点）。
		cleaned := strings.TrimSuffix(strings.TrimSpace(value), ".")
		if cleaned == "" {
			continue
		}
		parsed, err := strconv.ParseInt(cleaned, 10, 64)
		if err != nil {
			continue
		}
		*target = parsed
	}

	// 页大小与几个关键计数都得有，否则算出来的"已用内存"没有意义。
	if sample.PageSize <= 0 || sample.Active == 0 {
		return vmStatSample{}, false
	}
	return sample, true
}

// used 返回"已用内存"的估计，采用与活动监视器一致的口径：
// 活跃页 + 常驻（wired）页 + 压缩器实际占用的页。
//
// 刻意**不**把 inactive / purgeable 算成已用：那些页可以被立即回收，
// 把它们算进去会显示出一个远高于系统自身读数的数字，而用户会拿它跟
// 活动监视器对比 —— 对不上就会认为这个应用在乱报。
func (s vmStatSample) used(pageSize int64) uint64 {
	if pageSize <= 0 {
		pageSize = s.PageSize
	}
	return uint64(s.Active+s.Wired+s.CompressorOccupied) * uint64(pageSize)
}

// --- 磁盘 -------------------------------------------------------------------

// mountEntry 是 /proc/self/mounts 里的一行：设备、挂载点、文件系统类型。
//
// 另外三列（挂载选项、dump、fsck pass）不解析：选项说的是"怎么挂的"
// （只读、noexec、noatime…），与"还剩多少空间"无关，而把它塞进类型里
// 只会让调用方以为那些字段有用。
type mountEntry struct {
	Device     string
	MountPoint string
	FSType     string
}

// parseProcMounts 解析 /proc/self/mounts。
//
// # 为什么要解码八进制转义
//
// 这几列是**空格分隔**的，因此内核把值里的空格、制表符、换行与反斜杠
// 写成八进制转义（\040 \011 \012 \134，与 fstab 同一张表）。不解码的话，
// 一个叫 "My Disk" 的外接盘会显示成 "My\040Disk" —— 用户会以为是自己
// 当初起错了名字，然后去改一个本来没问题的卷标。
//
// 文件系统类型不解码：它不可能含空格（含空格的名字根本没法在这张表里
// 当分隔列用）。
func parseProcMounts(text string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		// 这张表的每一行固定是六列（设备、挂载点、类型、选项、dump、pass，
		// 与 fstab 同一套布局），因此列数不够的就不是挂载记录 —— 是残行。
		// 要求六列而不是"够三列就行"，是因为三列的文字行会被当成一个挂载点，
		// 然后每次采样都拿一个不存在的路径去 statfs 一遍。
		if len(fields) < 6 {
			continue
		}
		out = append(out, mountEntry{
			Device:     unescapeMount(fields[0]),
			MountPoint: unescapeMount(fields[1]),
			FSType:     fields[2],
		})
	}
	return out
}

// unescapeMount 解码 /proc/self/mounts 里的 \ooo 八进制转义。
//
// 只认**恰好三位**的八进制：内核只会写出 fstab 那张表里的四个字符，
// 而 "\\" 后面跟着别的东西（例如一个真的叫 a\b 的目录，内核会写成
// a\134b）不该被误解。
func unescapeMount(text string) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); i++ {
		// 需要三位数字，因此从 i+1 起必须还有三个字节。
		if text[i] != '\\' || i+3 >= len(text) {
			out.WriteByte(text[i])
			continue
		}
		digits := text[i+1 : i+4]
		value := 0
		ok := true
		for index := 0; index < len(digits); index++ {
			digit := digits[index]
			if digit < '0' || digit > '7' {
				ok = false
				break
			}
			value = value*8 + int(digit-'0')
		}
		if !ok || value > 0xFF {
			out.WriteByte(text[i])
			continue
		}
		out.WriteByte(byte(value))
		i += 3
	}
	return out.String()
}

// diskCapacity 由 statfs 的块计数算出三个字节数。
//
// # 口径：used + free 刻意**不**等于 total
//
//   - total = 总块数 × 块大小
//   - free  = **Bavail** × 块大小：普通用户真的还能写进去的量，
//     也就是 Finder / df 的"可用"。取 Bfree 会把 ext4 预留给 root 的
//     那 5% 算成可用空间，而用户往那里写是写不进去的。
//   - used  = (总块数 - Bfree) × 块大小：已经被文件占掉的量。
//
// 差额（Bfree - Bavail）是预留给 root 的部分。把它摊进任何一边都是在编
// 一个用户对不上的数字；而**空着**不解释又会被当成 bug，所以接口那边
// 应当按"已用 + 可用可以小于总量"来展示。
//
// 计数器异常时**收敛**而不是外推：块大小为 0 直接报 0（这样的挂载点随后
// 会被"容量为 0"那条滤掉），空闲 / 可用块数大于总块数时按总块数截断。
// 都是为了让结果落回 [0, total]，而不是因为一次下溢报出一个 16 EB 的盘 ——
// 一个荒谬的大数字比没有数字更糟，它会真的画进界面里。
func diskCapacity(blockSize, blocks, freeBlocks, availBlocks uint64) (total, used, free uint64) {
	if blockSize == 0 {
		return 0, 0, 0
	}
	total = blocks * blockSize
	if freeBlocks > blocks {
		freeBlocks = blocks
	}
	if availBlocks > blocks {
		availBlocks = blocks
	}
	return total, (blocks - freeBlocks) * blockSize, availBlocks * blockSize
}

// pseudoFilesystems 是不值得展示给用户的伪文件系统。
//
// # 为什么是黑名单，不是白名单
//
// 白名单（只放行 apfs / ext4 / …）看起来更干净，但它会**静默地**藏起
// 作者没想到的真文件系统：ZFS 池、sshfs 挂载、NAS 上的 nfs/smb、
// exfat 的 U 盘、btrfs 的子卷。用户明明插了一块盘，界面上却什么都没有 ——
// 而看不见的失败比一个难看的数字难查得多。
//
// 黑名单只藏"数字本身没有意义"的那些：它们的容量来自内存（tmpfs）、
// 内核对象（procfs/sysfs/cgroup）、自动挂载器（autofs/devfs）或一个
// 只读镜像（squashfs/overlay），而不是一块存储。这类数字显示出来只会
// 让用户以为自己多了一块盘。
//
// # 为什么不追求穷举
//
// 内核里的伪文件系统有几十个，而且每次新增子系统都可能再多一个，
// 逐一列举既列不完、也会过期。因此这里只列**常见且容易撞上**的，
// 剩下的靠"总容量为 0 就不显示"兜底（见 finalizeDisks）：伪文件系统
// 几乎都报不出总容量，而真盘不可能容量为 0。
var pseudoFilesystems = map[string]bool{
	"devfs":       true, // macOS 的 /dev
	"devicefs":    true, // CoreDevice 的虚拟设备文件系统：1 TiB 是编出来的容量
	"autofs":      true, // 自动挂载器的占位目录，容量为 0
	"proc":        true,
	"procfs":      true,
	"sysfs":       true,
	"cgroup":      true,
	"cgroup2":     true,
	"tmpfs":       true, // 容量随内存浮动，跟磁盘并列会误导
	"devpts":      true,
	"securityfs":  true,
	"pstore":      true,
	"bpf":         true,
	"tracefs":     true,
	"debugfs":     true,
	"configfs":    true,
	"fusectl":     true,
	"mqueue":      true,
	"hugetlbfs":   true,
	"nsfs":        true,
	"ramfs":       true,
	"efivarfs":    true, // 主板变量，不是存储
	"rpc_pipefs":  true,
	"binfmt_misc": true,
	"squashfs":    true, // snap 之类的只读镜像：占用恒等于镜像大小
	"overlay":     true, // 容器/快照的叠加层，容量来自下层
	"none":        true, // 没报出类型的挂载（老内核与某些 FUSE 会这样）
}

// keepMount 报告一个挂载点是否值得展示给用户。
//
// 纯函数：只看文件系统类型，既不碰系统调用也不看容量，因此这张判据
// 可以用一张表直接测 —— 而它是这次改动里唯一"删东西"的地方
// （判断错了，用户就会少看到一块盘）。
func keepMount(fsType string) bool {
	// 类型名统一按小写比较：内核给的是小写，但 FUSE 与某些网络文件系统
	// 会报出用户自己起的名字（"SSHFS"）。
	return !pseudoFilesystems[strings.ToLower(strings.TrimSpace(fsType))]
}

// finalizeDisks 过滤掉伪文件系统、丢掉容量为 0 的挂载点，并按挂载点排序。
//
// 纯函数，两个平台共用：darwin 走 getfsstat、Linux 走 /proc/self/mounts，
// 但"哪些该显示"是同一个问题，各写一份迟早会分叉。
//
// # 排序是为了稳定
//
// 内核给出的顺序（挂载顺序 / 挂载表顺序）在不同机器上完全不同，
// 而界面与测试都希望同一台机器的两次采样长得一样。按挂载点排序是唯一
// 与人无关的顺序。
func finalizeDisks(raw []DiskSample) []DiskSample {
	byMount := make(map[string]DiskSample, len(raw))
	for _, disk := range raw {
		if !keepMount(disk.FSType) {
			continue
		}
		// 总容量为 0 的一律不显示：伪文件系统几乎都报 0，所以这一条
		// 兜住了上面那张名单没列到的（新子系统的、发行版特有的）。
		// 真盘不会容量为 0，因此不会误伤。
		if disk.TotalBytes == 0 {
			continue
		}
		// 没有挂载点的记录既没法展示、也没法让用户定位，直接丢掉。
		if disk.MountPoint == "" {
			continue
		}
		// APFS 的"卷"不是用户以为的那个磁盘。
		//
		// macOS 会把一个容器（物理盘上的一个 APFS Container）拆成多个卷
		// 分别挂载：`/` 是系统快照，`/System/Volumes/Data` 是数据卷，
		// 还有 VM、Preboot、Update 等等。而 `getfsstat` 对**同一个容器里的
		// 每个卷都报容器级的块计数** —— 于是这十几个挂载点会显示成
		// 十几个容量完全相同的"磁盘"。
		//
		// 那不是数字不准，而是**同一块盘被数了十几遍**：用户看到"12 个盘
		// 都快满了"，实际只有一块盘。留着 `/` 一个就够 —— 它报的正是
		// 容器级的已用/可用，也正是 Finder 的"关于本机 → 储存空间"
		// 想表达的那个数。
		if strings.HasPrefix(disk.MountPoint, "/System/Volumes/") {
			continue
		}
		// 同一个挂载点被挂多次时只留最后一个：后来的那次盖在上面，
		// 用户在那个路径下看到的字节属于它。容器里 /etc/hosts 这类
		// 单文件 bind mount 就会出现两遍。
		byMount[disk.MountPoint] = disk
	}
	out := make([]DiskSample, 0, len(byMount))
	for _, disk := range byMount {
		out = append(out, disk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	return out
}

// cString 把 C 的定长字符数组（NUL 结尾）转成 Go 字符串。
//
// darwin 的 Statfs_t 里挂载点与类型分别是 [1024]int8 与 [16]int8：
// 内核只在前面写有效字节、后面留着 NUL，而且是 int8（不能直接当 byte 用）。
// 用 string(raw[:]) 会把一整串 NUL 带进结果里，于是挂载点在界面上变成
// 一个带方块的长条。
func cString(raw []int8) string {
	buf := make([]byte, 0, len(raw))
	for _, ch := range raw {
		if ch == 0 {
			break
		}
		buf = append(buf, byte(ch))
	}
	return string(buf)
}

// --- GPU（macOS 的 ioreg） --------------------------------------------------

// gpuBackendDarwin 是 macOS 上 GPU 采样的后端名。
//
// 它是**契约的一部分**（api/openapi.yaml 里 GpuMetrics.backend 的 examples，
// 界面也按它决定怎么说"不支持"），改名字要让那两处一起改。
const gpuBackendDarwin = "darwin-ioreg"

// parseIOAccelerator 从 `ioreg -r -d 1 -c IOAccelerator` 的输出里取 GPU 占用。
//
// # 为什么是 ioreg
//
// 没有 cgo（D11）就拿不到 IOKit，而 `Device Utilization %` 只存在于
// IOAccelerator 的性能统计字典里 —— ioreg 是它唯一的命令行出口。
// 与 CPU/内存那边不同的是：这里连"自己算"的余地都没有，
// 那个数字是驱动自己维护的。
//
// # 输出是什么形状
//
// ioreg 打的是 plist 的**文本**形式（不是 XML）：一条属性一行、形如
// `"键" = 值`，字典用 {}、数组用 ()，整个属性表可能几十 KB
// （IOReportLegend 那一行实测就有 45 KB）。因此这里先按条目切开、
// 再按**键**取值，而不是按位置或行号 —— 这个包的教训已经写过一次：
// 解析系统输出时，位置和顺序都会变，只有键名不会。
//
// # 返回值
//
// 找不到任何带性能统计的加速器条目（无头 Mac、虚拟机）时返回 nil：
// 那表示"这台机器没有可采样的 GPU"，与"GPU 占用是 0"是两件事。
// 找到条目但里面没有那个键时返回 Utilization == nil 的样本：老一些的
// 或非 Apple 驱动的 GPU 就是这样，界面该显示"此设备不报占用"，
// 而不是一个编出来的 0%。
func parseIOAccelerator(text string) *GPUSample {
	// 多 GPU 的机器（核显 + 独显的 MacBook Pro）上 ioreg 会打好几条，
	// 而且**不是每一条都带占用值**。因此逐条看：第一条真的带值的才算数，
	// 只看第一条会在那种机器上永远显示"没有值"。
	var fallback *GPUSample
	for _, entry := range ioregEntries(text) {
		sample := parseAcceleratorEntry(entry)
		if sample == nil {
			continue
		}
		if sample.Utilization != nil {
			return sample
		}
		if fallback == nil {
			fallback = sample
		}
	}
	return fallback
}

// ioregEntries 按条目切分 ioreg 的输出。
//
// 每个条目的第一行长这样：`+-o AGXAcceleratorG16G  <class …>`，
// 后面是缩进过的属性表。列表型输出（数组）没有这个前缀，
// 不是我们要找的东西，正好被排除在外。
func ioregEntries(text string) []string {
	lines := strings.Split(text, "\n")
	var out []string
	start := -1
	for index, line := range lines {
		if !strings.HasPrefix(line, "+-o ") {
			continue
		}
		if start >= 0 {
			out = append(out, strings.Join(lines[start:index], "\n"))
		}
		start = index
	}
	if start >= 0 {
		out = append(out, strings.Join(lines[start:], "\n"))
	}
	return out
}

// parseAcceleratorEntry 从一个 ioreg 条目里取占用与名字。
//
// 没有 PerformanceStatistics 字典的条目直接返回 nil：那不是加速器
// （`-c IOAccelerator` 偶尔会带上匹配了同一个类名的辅助对象），
// 把它当成"支持采样但没值"会让界面在一个根本没有 GPU 的机器上显示
// GPU 那一栏。
func parseAcceleratorEntry(entry string) *GPUSample {
	stats, ok := plistDictionary(entry, "PerformanceStatistics")
	if !ok {
		return nil
	}
	sample := &GPUSample{Backend: gpuBackendDarwin}
	// 名字是展示用的点缀：取不到就不填。它不在 PerformanceStatistics 里，
	// 而是同级的 "model"（实测是 "Apple M4" 这样的型号名）。
	if name, ok := plistString(entry, "model"); ok {
		sample.Name = name
	}
	// 与 Renderer / Tiler Utilization % 区分：这里要的是设备整体占用，
	// 与活动监视器显示的是同一个数。
	if raw, ok := plistValue(stats, "Device Utilization %"); ok {
		value, err := strconv.ParseFloat(raw, 64)
		// 契约把这个字段限定在 0..100（openapi.yaml）。超范围的值只可能
		// 来自解析错位或驱动抽风：宁可报"没读到"，也不要报一个契约不允许的
		// 数字 —— 界面会把它画成一根戳出图表的柱子。
		if err == nil && value >= 0 && value <= 100 {
			sample.Utilization = &value
		}
	}
	return sample
}

// plistDictionary 取 `"key" = { … }` 里大括号**之间**的正文。
func plistDictionary(text, key string) (string, bool) {
	raw, ok := plistRaw(text, key)
	if !ok || len(raw) < 2 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return "", false
	}
	return raw[1 : len(raw)-1], true
}

// plistString 取 `"key" = "值"` 里的字符串值。
//
// 值**必须**是引号串：`"model" = Yes` 这种裸标量不是名字，
// 当成名字填进去会在界面上显示"GPU: Yes"。
func plistString(text, key string) (string, bool) {
	raw, ok := plistRaw(text, key)
	if !ok || len(raw) < 2 || raw[0] != '"' {
		return "", false
	}
	return matchQuote(raw)
}

// plistValue 取 `"key" = 值` 的值，引号与字典外壳都去掉。
//
// 只按**键**取值：`Device Utilization %` 旁边就挨着 `Renderer Utilization %`
// 与 `Tiler Utilization %`，按位置取会在键顺序变化时安静地读错一个数字
// （netstat 那条注释记的就是同一类事故）。
func plistValue(text, key string) (string, bool) {
	raw, ok := plistRaw(text, key)
	if !ok {
		return "", false
	}
	switch {
	case strings.HasPrefix(raw, `"`):
		return matchQuote(raw)
	case strings.HasPrefix(raw, "{"):
		if len(raw) < 2 {
			return "", false
		}
		return raw[1 : len(raw)-1], true
	default:
		return raw, true
	}
}

// plistRaw 在文本里找 `"key" = 值`，返回值的**原文**（含引号或大括号）。
//
// # 为什么要容忍空白的两种写法
//
// 本次实测的同一份 ioreg 输出里，两种写法**同时存在**：
// `"MetalPluginName" = "AGXMetalG16G_B0"`（等号两边带空格）与
// `"Tiler Utilization %"=15`（不带）。值这边也一样：字符串带引号、
// 数字是裸的、字典是大括号。因此这里不假设任何一种，
// 而是按值的首字符决定怎么读。
//
// 找不到键、或者键后面不是等号（同名的字符串值也会被 Index 命中）
// 都返回 false —— 调用方必须显式处理"没读到"，而不是拿到一个空串。
func plistRaw(text, key string) (string, bool) {
	quoted := `"` + key + `"`
	for search := 0; search < len(text); {
		at := strings.Index(text[search:], quoted)
		if at < 0 {
			return "", false
		}
		at += search
		search = at + len(quoted)

		rest := strings.TrimLeft(text[search:], " \t\r\n")
		value, found := strings.CutPrefix(rest, "=")
		if !found {
			continue
		}
		if raw, ok := plistToken(strings.TrimLeft(value, " \t\r\n")); ok {
			return raw, true
		}
	}
	return "", false
}

// plistToken 从值的开头读出一个完整的值，返回它的原文。
func plistToken(text string) (string, bool) {
	if text == "" {
		return "", false
	}
	switch text[0] {
	case '"':
		end := 1
		for end < len(text) {
			if text[end] == '\\' {
				end++
			} else if text[end] == '"' {
				return text[:end+1], true
			}
			end++
		}
		return "", false
	case '{':
		body, ok := matchBrace(text)
		if !ok {
			return "", false
		}
		return "{" + body + "}", true
	default:
		// 裸标量（数字、Yes/No、<data>）：到下一个分隔符为止。
		end := strings.IndexAny(text, ",}\n")
		if end < 0 {
			end = len(text)
		}
		token := strings.TrimSpace(text[:end])
		if token == "" {
			return "", false
		}
		return token, true
	}
}

// matchBrace 从 text 首字符的 '{' 开始配对，返回大括号**之间**的正文。
//
// 必须跳过引号串：属性表里有 `"IOGeneralInterest" = "IOCommand is not
// serializable"` 这样的值，将来也可能出现带花括号的名字；把字符串里的
// 括号当成结构，配对准会错位，而错位之后读到的就是另一段数字 ——
// 一个安静的错误答案。
//
// 值里再嵌字典是常见的（`"AGCInfo" = {…}`、`"SchedulerState" = {…}`），
// 因此用深度计数而不是找第一个 '}'。
func matchBrace(text string) (string, bool) {
	if text == "" || text[0] != '{' {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for index := 0; index < len(text); index++ {
		ch := text[index]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[1:index], true
			}
		}
	}
	return "", false
}

// matchQuote 从 text 首字符的 '"' 开始配对，返回去掉引号的正文。
//
// 反斜杠转义原样保留：这里的值只用来展示（GPU 型号名），
// 为一个显示用的字符串实现 plist 的转义表不值得。
func matchQuote(text string) (string, bool) {
	if text == "" || text[0] != '"' {
		return "", false
	}
	for index := 1; index < len(text); index++ {
		switch text[index] {
		case '\\':
			index++ // 跳过被转义的那个字符，它是字面量
		case '"':
			return text[1:index], true
		}
	}
	return "", false
}

// parseNettop 解析 macOS `nettop -n -P -l 1 -x -J bytes_in,bytes_out` 的输出。
//
// 输出形如（第一行是表头）：
//
//	                     bytes_in       bytes_out
//	apsd.399               112734          128672
//	mDNSResponder.507     1062670          619956
//
// # 两个坑
//
//  1. 进程名里**可以有点**（`com.apple.WebKit.Networking.1234`），所以必须
//     按**最后一个**点切分 pid —— 按第一个点切会把 pid 解析成 "apple"。
//  2. nettop 只列出**有网络活动**的进程。缺席不等于 0 字节，但对求和而言
//     两者等价：调用方按 0 处理即可，前提是别把"没出现"当成"这次没读到"。
func parseNettop(text string) map[int]NetCounters {
	out := map[int]NetCounters{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		dot := strings.LastIndexByte(fields[0], '.')
		if dot <= 0 || dot == len(fields[0])-1 {
			continue
		}
		pid, err := strconv.Atoi(fields[0][dot+1:])
		if err != nil || pid <= 0 {
			continue
		}
		rx, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			continue
		}
		out[pid] = NetCounters{RxBytes: rx, TxBytes: tx}
	}
	return out
}

// parsePPID 解析 `ps -A -o pid=,ppid=` 的输出，得到"父 → 子"表。
//
// 用 -A 全量取一次而不是按 pid 逐个问：一次调用就能建出完整的进程树，
// 而逐个查询在站点多起来之后会变成几十次 fork。
func parsePPID(text string) map[int][]int {
	children := map[int][]int{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
	}
	return children
}

// parseProcPPID 从 /proc/<pid>/stat 里取出父进程号。
//
// 与 parseProcPIDStat 同样从最后一个右括号之后切分（进程名里可能有空格
// 与括号）。ppid 是紧随 state 之后的那一项，在切片里下标为 1。
func parseProcPPID(text string) (int, bool) {
	close := strings.LastIndex(text, ")")
	if close < 0 || close+2 >= len(text) {
		return 0, false
	}
	fields := strings.Fields(text[close+2:])
	const ppidIndex = 1
	if len(fields) <= ppidIndex {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[ppidIndex])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
