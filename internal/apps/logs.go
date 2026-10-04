package apps

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// 日志的两个上界。
//
// 应用输出是**用户程序产生的**，可以无限快、无限多。没有上界的话，
// 一个 `while true; do echo x; done` 就能把内核的内存吃光，
// 而症状是"整个应用（连界面一起）卡死"，离原因很远。
const (
	// maxLogLines 是每个应用在内存里保留的行数。
	maxLogLines = 2000
	// maxLogLineBytes 是单行的上限；超出部分被截断。
	maxLogLineBytes = 8 << 10
	// maxLogFileBytes 是磁盘日志文件的上限，超过后轮转一次。
	maxLogFileBytes = 4 << 20
)

// LogStore 保存每个应用的近期输出。
//
// # 为什么日志不进事件总线
//
// 总线是容量有限的环形缓冲（默认 1000 条），且慢订阅者会被**断开**保护。
// 把应用日志灌进去，会把真正的状态变化挤出去，而"服务崩了但我没收到事件"
// 比"日志丢了几行"严重得多。因此日志是**拉取**的：内存环形缓冲 + 文件。
type LogStore struct {
	dir string

	mu      sync.Mutex
	buffers map[string]*ringBuffer
}

// NewLogStore 构造日志存储，落盘目录为 dir（不存在时按需创建）。
func NewLogStore(dir string) *LogStore {
	return &LogStore{dir: dir, buffers: make(map[string]*ringBuffer)}
}

// Append 追加一段输出。
//
// 输入可能是任意片段（不保证按行），这里按行切分后入缓冲。
func (s *LogStore) Append(appID, chunk string) {
	if chunk == "" {
		return
	}
	lines := splitLines(chunk)
	if len(lines) == 0 {
		return
	}

	s.mu.Lock()
	buffer, ok := s.buffers[appID]
	if !ok {
		buffer = newRingBuffer(maxLogLines)
		s.buffers[appID] = buffer
	}
	s.mu.Unlock()

	for _, line := range lines {
		if len(line) > maxLogLineBytes {
			line = line[:maxLogLineBytes] + "…(truncated)"
		}
		buffer.push(line)
	}
	s.writeToFile(appID, lines)
}

// Tail 返回最近 n 行。
func (s *LogStore) Tail(appID string, n int) []string {
	if n <= 0 {
		n = LogTailDefault
	}
	if n > LogTailMax {
		n = LogTailMax
	}
	s.mu.Lock()
	buffer, ok := s.buffers[appID]
	s.mu.Unlock()
	if !ok {
		return []string{}
	}
	return buffer.tail(n)
}

// Forget 丢弃某个应用的内存日志（删除应用时调用）。
//
// 保留磁盘文件：用户可能还想看一眼它为什么挂了，而删应用是个
// 不可撤销的动作，不该顺手把证据也删掉。
func (s *LogStore) Forget(appID string) {
	s.mu.Lock()
	delete(s.buffers, appID)
	s.mu.Unlock()
}

// FilePath 返回某个应用的日志文件路径。
func (s *LogStore) FilePath(appID string) string {
	return filepath.Join(s.dir, appID+".log")
}

// writeToFile 尽力落盘。
//
// 写日志失败**不能**影响应用的运行：它是诊断手段，不是业务路径。
// 因此这里吞掉错误（但轮转失败会让文件停在上限，不会无限增长）。
func (s *LogStore) writeToFile(appID string, lines []string) {
	if s.dir == "" {
		return
	}
	path := s.FilePath(appID)
	if info, err := os.Stat(path); err == nil && info.Size() > maxLogFileBytes {
		// 简单轮转：把当前文件改成 .1，重新开始。
		_ = os.Rename(path, path+".1")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(strings.Join(lines, "\n") + "\n")
}

// ringBuffer 是固定容量的行缓冲。
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{lines: make([]string, capacity)}
}

func (r *ringBuffer) push(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
}

// tail 返回最近 n 行，按时间顺序（最旧在前）。
func (r *ringBuffer) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	size := r.next
	if r.full {
		size = len(r.lines)
	}
	if n > size {
		n = size
	}
	out := make([]string, 0, n)
	// 从"第 n 新的那一行"开始，绕环取出。
	start := (r.next - n + len(r.lines)) % len(r.lines)
	for i := 0; i < n; i++ {
		out = append(out, r.lines[(start+i)%len(r.lines)])
	}
	return out
}

// splitLines 把任意片段切成完整行。
//
// 保留换行符本身没有意义（下游按行呈现），因此这里丢掉它们。
// 片段末尾不完整的那一行也会被返回 —— 它可能永远等不到换行
// （例如进度条），丢掉就等于用户永远看不到它。
func splitLines(chunk string) []string {
	normalized := strings.ReplaceAll(chunk, "\r\n", "\n")
	parts := strings.Split(normalized, "\n")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}
