package credential

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/secret"
)

// SpecLookup 按服务商名返回其凭据字段定义。
//
// 用函数而不是直接依赖 provider 包：provider 包需要凭据字段类型
// （credential.FieldSpec），若本包反过来依赖 provider 就成环了。
// 一个窄接口即可打破环，且让测试更容易伪造。
type SpecLookup func(providerName string) (fields []FieldSpec, ok bool)

// Service 是凭据的领域服务：把加解密、校验与持久化串起来。
type Service struct {
	repo    Repository
	secrets *secret.Manager
	specs   SpecLookup
	log     *slog.Logger

	// usage 用于在删除前检查凭据是否仍被引用。
	//
	// 允许为 nil（表示"没有引用者"）。做成可注入的钩子而不是直接依赖
	// ddns 包：凭据不该知道"谁在用我" —— 那个知识属于使用方。
	// 将来加入证书、通知通道等新引用者时，本层不用改。
	usage UsageChecker
}

// UsageChecker 报告某凭据被引用的次数。
type UsageChecker interface {
	CountByCredential(ctx context.Context, credentialID string) (int, error)
}

// NewService 构造凭据服务。
func NewService(repo Repository, secrets *secret.Manager, specs SpecLookup, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{repo: repo, secrets: secrets, specs: specs, log: log}
}

// SetUsageChecker 设置引用检查器。
//
// 单独一步而不是构造参数：引用者（动态解析任务）的构造依赖凭据服务，
// 放进构造函数会形成循环。
func (s *Service) SetUsageChecker(c UsageChecker) { s.usage = c }

// InUseError 表示凭据仍被引用，无法删除。
type InUseError struct {
	// Count 是引用它的对象数量。
	Count int
}

// Error 实现 error。
func (e *InUseError) Error() string {
	return fmt.Sprintf("credential: 凭据仍被 %d 个任务使用", e.Count)
}

// List 返回凭据列表（Fields 为明文）。
//
// 调用方（接口层）负责在输出前用 Masked 处理。
func (s *Service) List(ctx context.Context, cursor string, limit int) ([]Credential, string, error) {
	rows, next, err := s.repo.List(ctx, cursor, limit)
	if err != nil {
		return nil, "", err
	}

	out := make([]Credential, 0, len(rows))
	for _, rec := range rows {
		c, err := s.decode(rec)
		if err != nil {
			// 单条解密失败不应让整个列表打不开 —— 那会让用户
			// 连"删掉这条坏数据"都做不到。
			s.log.Warn("凭据解密失败，列表中跳过该条",
				"id", rec.ID, "provider", rec.Provider, "err", err)
			// 仍然返回条目，但字段标记为不可读，让用户能在界面上看到并删除它。
			c = Credential{
				ID: rec.ID, Provider: rec.Provider, Label: rec.Label,
				Fields:    map[string]string{},
				CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt,
			}
		}
		out = append(out, c)
	}
	return out, next, nil
}

// Get 返回单条凭据（Fields 为明文）。
func (s *Service) Get(ctx context.Context, id string) (Credential, error) {
	rec, found, err := s.repo.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if !found {
		return Credential{}, ErrNotFound
	}
	return s.decode(rec)
}

// Create 新建凭据。
func (s *Service) Create(ctx context.Context, in Credential) (Credential, error) {
	specs, ok := s.lookup(in.Provider)
	if !ok {
		return Credential{}, fmt.Errorf("%w: %s", ErrUnknownProvider, in.Provider)
	}
	in.Label = strings.TrimSpace(in.Label)
	if err := in.Validate(specs); err != nil {
		return Credential{}, err
	}

	id, err := newID()
	if err != nil {
		return Credential{}, err
	}
	now := time.Now().UTC()
	in.ID = id
	in.CreatedAt = now
	in.UpdatedAt = now

	rec, err := s.encode(in)
	if err != nil {
		return Credential{}, err
	}
	if err := s.repo.Insert(ctx, rec); err != nil {
		return Credential{}, err
	}
	return in, nil
}

// Update 修改凭据。
//
// incoming.Fields 中值为掩码的键会保留原值（见 Merge），
// 因此"只改标签"不需要重新输入密钥。
func (s *Service) Update(ctx context.Context, id string, incoming Credential) (Credential, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Credential{}, err
	}

	// 服务商不允许改：换了服务商就意味着字段语义全变了，
	// 保留旧字段只会得到一份看起来正常、实际用不了的凭据。
	// 真要换就删了重建。
	if incoming.Provider != "" && incoming.Provider != current.Provider {
		return Credential{}, fmt.Errorf(
			"%w: 服务商不可修改（当前 %s，请求 %s）",
			ErrProviderImmutable, current.Provider, incoming.Provider)
	}

	if strings.TrimSpace(incoming.Label) != "" {
		current.Label = strings.TrimSpace(incoming.Label)
	}
	current.Fields = Merge(current.Fields, incoming.Fields)

	specs, _ := s.lookup(current.Provider)
	if err := current.Validate(specs); err != nil {
		return Credential{}, err
	}

	current.UpdatedAt = time.Now().UTC()
	// 凭据变了，此前的校验结论不再成立。不清掉的话，界面上会显示
	// "上次校验成功"，而实际上那把密钥已经被换成了别的东西。
	current.LastVerifiedAt = nil
	current.LastVerifyOK = nil
	current.LastVerifyError = ""

	rec, err := s.encode(current)
	if err != nil {
		return Credential{}, err
	}
	if err := s.repo.Update(ctx, rec); err != nil {
		return Credential{}, err
	}
	return current, nil
}

// Delete 删除凭据。
//
// 删除前检查引用：给出"仍被 N 个任务使用"这样的明确提示，而不是让用户
// 删完之后发现某些任务莫名开始报错。数据库侧刻意**没有**加外键级联 ——
// 静默级联删除会让"我的解析任务去哪了"变成一个查不明白的问题。
func (s *Service) Delete(ctx context.Context, id string) error {
	if s.usage != nil {
		n, err := s.usage.CountByCredential(ctx, id)
		if err != nil {
			return fmt.Errorf("credential: 检查引用失败: %w", err)
		}
		if n > 0 {
			return &InUseError{Count: n}
		}
	}
	return s.repo.Delete(ctx, id)
}

// Resolve 返回凭据的明文字段，供内部调用者（校验任务、DNS 引擎）使用。
//
// 它**不得**被接口层的直出路径调用 —— 那是 Masked 的职责。
func (s *Service) Resolve(ctx context.Context, id string) (Credential, error) {
	return s.Get(ctx, id)
}

// MarkVerified 记录一次校验结果。
func (s *Service) MarkVerified(ctx context.Context, id string, verifyErr error) error {
	rec, found, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}

	now := time.Now().UTC()
	ok := verifyErr == nil
	rec.LastVerifiedAt = &now
	rec.LastVerifyOK = &ok
	rec.LastVerifyError = ""
	if verifyErr != nil {
		rec.LastVerifyError = verifyErr.Error()
	}
	// 刻意不更新 UpdatedAt：校验结果不是用户对凭据的修改，
	// 把它算作修改会让"凭据什么时候被改过"这个审计问题失去答案。
	return s.repo.Update(ctx, rec)
}

// SpecsFor 返回某服务商的字段定义。
func (s *Service) SpecsFor(providerName string) ([]FieldSpec, bool) {
	return s.lookup(providerName)
}

// ---------------------------------------------------------------------------
// 内部
// ---------------------------------------------------------------------------

func (s *Service) lookup(providerName string) ([]FieldSpec, bool) {
	if s.specs == nil {
		return nil, false
	}
	return s.specs(providerName)
}

// encode 把领域实体转成持久化记录（加密字段）。
func (s *Service) encode(c Credential) (Record, error) {
	fields := c.Fields
	if fields == nil {
		fields = map[string]string{}
	}
	plaintext, err := json.Marshal(fields)
	if err != nil {
		return Record{}, fmt.Errorf("credential: 序列化字段失败: %w", err)
	}
	cipher, err := s.secrets.Encrypt(plaintext)
	if err != nil {
		return Record{}, fmt.Errorf("credential: 加密字段失败: %w", err)
	}
	return Record{
		ID:              c.ID,
		Provider:        c.Provider,
		Label:           c.Label,
		SecretCipher:    cipher,
		KeyVersion:      secret.KeyVersion,
		CreatedAt:       c.CreatedAt,
		UpdatedAt:       c.UpdatedAt,
		LastVerifiedAt:  c.LastVerifiedAt,
		LastVerifyOK:    c.LastVerifyOK,
		LastVerifyError: c.LastVerifyError,
	}, nil
}

// decode 把持久化记录还原为领域实体（解密字段）。
func (s *Service) decode(rec Record) (Credential, error) {
	plaintext, err := s.secrets.Decrypt(rec.SecretCipher)
	if err != nil {
		return Credential{}, fmt.Errorf("credential: 解密 %s 的字段失败: %w", rec.ID, err)
	}
	var fields map[string]string
	if err := json.Unmarshal(plaintext, &fields); err != nil {
		return Credential{}, fmt.Errorf("credential: 解析 %s 的字段失败: %w", rec.ID, err)
	}
	if fields == nil {
		fields = map[string]string{}
	}
	return Credential{
		ID:              rec.ID,
		Provider:        rec.Provider,
		Label:           rec.Label,
		Fields:          fields,
		CreatedAt:       rec.CreatedAt,
		UpdatedAt:       rec.UpdatedAt,
		LastVerifiedAt:  rec.LastVerifiedAt,
		LastVerifyOK:    rec.LastVerifyOK,
		LastVerifyError: rec.LastVerifyError,
	}, nil
}

// newID 生成凭据 ID。
//
// 与任务 ID 同样使用 16 字节随机值：可预测的 ID 让攻击者能枚举
// 凭据列表（虽然接口需要令牌，但纵深防御不该在这里省）。
func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("credential: 生成 ID 失败: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// 服务层错误。
var (
	// ErrUnknownProvider 表示服务商不在注册表中。
	ErrUnknownProvider = errors.New("credential: 未知的服务商")
	// ErrProviderImmutable 表示试图修改凭据的服务商。
	ErrProviderImmutable = errors.New("credential: 服务商不可修改")
)
