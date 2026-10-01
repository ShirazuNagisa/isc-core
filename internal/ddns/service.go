package ddns

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// Service 是任务的领域服务：把校验、时间戳、缓存失效与持久化串起来。
//
// 接口层只跟它打交道，不直接碰仓储与调度器 —— 这样"创建任务时要不要
// 清缓存""更新时间戳的是谁"这类规则只有一个地方定义。
type Service struct {
	repo      Repository
	engine    *Engine
	scheduler *Scheduler
}

// NewService 构造任务服务。
func NewService(repo Repository, engine *Engine, scheduler *Scheduler) *Service {
	return &Service{repo: repo, engine: engine, scheduler: scheduler}
}

// List 返回全部任务。
func (s *Service) List(ctx context.Context) ([]Task, error) {
	tasks, err := s.repo.List(ctx, false)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		// 返回空切片而不是 nil：JSON 序列化 nil 切片会得到 null，
		// 而客户端普遍按数组处理。
		tasks = []Task{}
	}
	return tasks, nil
}

// Get 返回单个任务。
func (s *Service) Get(ctx context.Context, id string) (Task, error) {
	t, found, err := s.repo.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	if !found {
		return Task{}, ErrNotFound
	}
	return t, nil
}

// Create 新建任务。
func (s *Service) Create(ctx context.Context, in Task) (Task, error) {
	in.Label = strings.TrimSpace(in.Label)
	in.IPv4.Domains = NormalizeDomains(in.IPv4.Domains)
	in.IPv6.Domains = NormalizeDomains(in.IPv6.Domains)

	if err := in.Validate(); err != nil {
		return Task{}, err
	}

	id, err := newTaskID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	in.ID = id
	in.CreatedAt = now
	in.UpdatedAt = now
	in.LastStatus = StatusNever

	if err := s.repo.Insert(ctx, in); err != nil {
		return Task{}, err
	}

	// 新建后立刻执行一次：用户的期待是"保存完就能用"，
	// 而不是"等五分钟看看"。
	s.trigger(in.ID)
	return in, nil
}

// Update 修改任务。
func (s *Service) Update(ctx context.Context, id string, in Task) (Task, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}

	in.Label = strings.TrimSpace(in.Label)
	in.IPv4.Domains = NormalizeDomains(in.IPv4.Domains)
	in.IPv6.Domains = NormalizeDomains(in.IPv6.Domains)

	// 保留创建时间与运行状态：那些不属于"用户提交的内容"。
	in.ID = current.ID
	in.CreatedAt = current.CreatedAt
	in.LastRunAt = current.LastRunAt
	in.LastStatus = current.LastStatus
	in.LastMessage = current.LastMessage
	in.LastIPv4 = current.LastIPv4
	in.LastIPv6 = current.LastIPv6
	in.UpdatedAt = time.Now().UTC()

	if err := in.Validate(); err != nil {
		return Task{}, err
	}
	if err := s.repo.Update(ctx, in); err != nil {
		return Task{}, err
	}

	// 清空防抖缓存，使下一次执行必定与服务商比对。
	//
	// 不做这一步的话，用户改完域名会看到"半天没生效"——
	// 因为防抖逻辑认为地址没变、还没到比对时机，于是根本不去请求。
	// 上游靠一个被到处改写的全局开关解决这个问题，这里精确到单个任务。
	if s.engine != nil {
		s.engine.ResetCache(in.ID)
	}
	s.trigger(in.ID)
	return in, nil
}

// Delete 删除任务。
func (s *Service) Delete(ctx context.Context, id string) error {
	err := s.repo.Delete(ctx, id)
	if err == nil && s.engine != nil {
		s.engine.ResetCache(id)
	}
	return err
}

// RunNow 请求立即执行一次任务。
//
// 非阻塞：一次执行可能包含多个域名的更新，在服务商限流时可能耗时数秒。
// 调用方（HTTP 处理器）拿到的是"已受理"，实际进度通过任务与事件流观察。
func (s *Service) RunNow(id string) {
	if s.engine != nil {
		// 先清缓存，否则"立即执行"会被防抖逻辑挡掉 ——
		// 那与按钮上写的意思完全相反。
		s.engine.ResetCache(id)
	}
	s.trigger(id)
}

// trigger 请求调度器执行。
func (s *Service) trigger(id string) {
	if s.scheduler != nil {
		s.scheduler.Trigger(id)
	}
}

// EnabledTasks 返回启用中的任务，供调度器使用。
func (s *Service) EnabledTasks(ctx context.Context) ([]Task, error) {
	return s.repo.List(ctx, true)
}

// newTaskID 生成任务 ID。
//
// 与凭据、作业 ID 一样使用 16 字节随机值：可预测的 ID 让攻击者能枚举
// 任务列表，而任务里含域名与凭据引用。
func newTaskID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ddns: 生成任务 ID 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
