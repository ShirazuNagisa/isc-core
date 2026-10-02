package credential

import (
	"context"
	"errors"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"time"
)

// Record 是凭据在持久化层的形态。
//
// 它与领域实体 Credential 的区别只有一个但很关键：**Fields 是密文**。
// 领域实体在内存中始终持有明文，而 Record 从不接触明文 ——
// 加解密发生在两者之间的转换处（见 Service）。
//
// 把这件事做成两个类型而不是同一个类型加一个 bool，是为了让
// "明文不该出现在这一层"成为编译期就能看出来的事实。
type Record struct {
	ID       string
	Provider string
	Label    string

	// SecretCipher 是 Fields 的 JSON 经 AES-256-GCM 加密后的信封。
	SecretCipher []byte
	// KeyVersion 记录加密时用的主密钥版本，为将来的轮换留路。
	KeyVersion int

	CreatedAt time.Time
	UpdatedAt time.Time

	LastVerifiedAt  *time.Time
	LastVerifyOK    *bool
	LastVerifyError string
}

// Repository 是凭据的持久化接口。
//
// 定义在领域层而不是持久化层：这样"凭据怎么存"由领域需求决定，
// 而不是被某个数据库的能力牵着走。
type Repository interface {
	// List 按创建时间倒序返回凭据，cursor 为空表示第一页。
	// 返回的 nextCursor 为空表示没有更多数据。
	List(ctx context.Context, cursor string, limit int) ([]Record, string, error)

	// Get 按 ID 取凭据；不存在时返回 found=false 而不是错误。
	Get(ctx context.Context, id string) (rec Record, found bool, err error)

	// FindByProviderLabel 按 (服务商, 标签) 查找，用于查重。
	FindByProviderLabel(ctx context.Context, provider, label string) (Record, bool, error)

	// Insert 插入新凭据。标签重复时返回 ErrDuplicateLabel。
	Insert(ctx context.Context, rec Record) error

	// Update 更新凭据。不存在时返回 ErrNotFound。
	Update(ctx context.Context, rec Record) error

	// Delete 删除凭据。不存在时返回 ErrNotFound。
	Delete(ctx context.Context, id string) error
}

// 仓储层错误。它们由持久化实现返回，由服务层向上翻译。
var (
	// ErrNotFound 表示凭据不存在。
	ErrNotFound = errors.New(i18n.T("cred.err.not_found"))
	// ErrDuplicateLabel 表示同一服务商下标签重复。
	ErrDuplicateLabel = errors.New(i18n.T("cred.err.dup_label"))
)
