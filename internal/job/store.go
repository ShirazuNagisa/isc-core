package job

import (
	"context"
	"sync"
)

// MemoryStore 是 Store 的内存实现。
//
// 用途：
//
//   - M0 阶段（SQLite 尚未接入）承载任务状态；
//   - 测试中作为轻量替身。
//
// 它**不做持久化** —— 进程退出即丢失。生产环境由 M1 的 SQLite 实现替换，
// 引擎代码不需要改动。
type MemoryStore struct {
	mu sync.Mutex
	// jobs 按 ID 索引当前状态。
	jobs map[string]Job
	// order 是按创建先后排列的 ID 列表。
	//
	// 分页需要稳定的全序，而 CreatedAt 在同一毫秒内可能重复，
	// 因此单独维护插入顺序，而不是靠时间戳排序。
	order []string
}

// NewMemoryStore 构造内存任务存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: make(map[string]Job)}
}

// Save 写入或更新一个任务。
func (s *MemoryStore) Save(_ context.Context, j Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.jobs[j.ID]; !exists {
		s.order = append(s.order, j.ID)
	}
	s.jobs[j.ID] = j
	return nil
}

// Get 查询单个任务。
func (s *MemoryStore) Get(_ context.Context, id string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.jobs[id]
	return j, ok, nil
}

// List 查询任务列表，按创建时间倒序（最新在前）。
func (s *MemoryStore) List(_ context.Context, f Filter) ([]Job, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	// 先按倒序收集全部符合条件的 ID，再按游标截取。
	// 任务量级在单机场景下很小（几千条），这样做比维护索引简单得多。
	matched := make([]string, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		id := s.order[i]
		j, ok := s.jobs[id]
		if !ok {
			continue
		}
		if !matches(j, f) {
			continue
		}
		matched = append(matched, id)
	}

	start := 0
	if f.Cursor != "" {
		found := false
		for i, id := range matched {
			if id == f.Cursor {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			// 游标指向的任务已不在结果集中（例如状态被过滤掉或已被清理）。
			// 从头开始而不是报错：客户端拿到的是"最新的一页"，
			// 比一个 400 更能让界面自愈。
			start = 0
		}
	}

	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}

	out := make([]Job, 0, end-start)
	for _, id := range matched[start:end] {
		out = append(out, s.jobs[id])
	}

	nextCursor := ""
	if end < len(matched) && len(out) > 0 {
		nextCursor = out[len(out)-1].ID
	}
	return out, nextCursor, nil
}

// matches 判断任务是否满足过滤条件。
func matches(j Job, f Filter) bool {
	if f.Kind != "" && j.Kind != f.Kind {
		return false
	}
	if len(f.Statuses) == 0 {
		return true
	}
	for _, s := range f.Statuses {
		if j.Status == s {
			return true
		}
	}
	return false
}
