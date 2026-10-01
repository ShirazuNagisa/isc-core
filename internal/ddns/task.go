// Package ddns 是动态解析的领域层：任务实体、执行引擎与调度器。
//
// 它把三样东西串起来：
//
//	internal/platform 的 IPMonitor   —— 发现地址与前缀变化
//	internal/ddnsgo                  —— 从网卡 / URL / 命令获取地址
//	internal/provider + internal/dns —— 把地址写进服务商的 DNS
//
// # 为什么 ddns-go 没有这一层
//
// 上游把"定时循环、IP 缓存、任务状态"全塞在 dns/index.go 的一个 RunOnce 里，
// 用两个包级全局（util.ForceCompareGlobal、dns.Ipcache）在函数之间传递状态。
// 那套写法在单一定时循环下够用，但有三个问题：
//
//   - 无法按任务单独触发（用户点"立即执行"只能触发全部）；
//   - 无法按任务显示状态（界面上只能看到一个全局的"上次运行"）；
//   - 全局状态在并发下没有任何保护，而 ISC 需要支持"事件触发 + 定时触发"
//     同时到来。
//
// 因此本包用显式的 Task 实体与 Engine 取代它，防抖缓存挂在任务上，
// 运行状态落库。逻辑语义（什么时候该去比对）与上游保持一致。
package ddns

import (
	"context"
	"errors"
	"strings"
	"time"
)

// GetType 是地址的获取方式。
type GetType string

const (
	// GetTypeNetInterface 从指定网卡读取。
	GetTypeNetInterface GetType = "netInterface"
	// GetTypeURL 通过外部接口查询（逗号分隔的多个地址，按序尝试）。
	GetTypeURL GetType = "url"
	// GetTypeCmd 执行命令并从输出中抓取。
	GetTypeCmd GetType = "cmd"
)

// Valid 报告获取方式是否受支持。
func (g GetType) Valid() bool {
	switch g {
	case GetTypeNetInterface, GetTypeURL, GetTypeCmd:
		return true
	default:
		return false
	}
}

// Source 是一路地址的获取配置。
type Source struct {
	// Enable 为 false 时该记录类型不参与解析。
	Enable bool
	// GetType 决定 Source 字段的含义。
	GetType GetType
	// Value 是网卡名 / URL 列表 / 命令，含义由 GetType 决定。
	Value string
	// Domains 是要更新的域名，支持 ddns-go 的两种写法：
	//
	//	www.example.com   自动识别根域名
	//	www:example.com   显式指定"子域名:根域名"
	Domains []string
	// Selector 是 IPv6 地址选择器：正则，或 "@N" 表示取第 N 个。
	// 留空表示取第一个。
	Selector string
}

// Task 是一条动态解析任务。
type Task struct {
	ID string
	// CredentialID 指向要使用的 DNS 服务商凭据。
	CredentialID string
	// Label 是用户可读的名称。
	Label string
	// Enabled 为 false 时调度器跳过它。
	Enabled bool

	IPv4 Source
	IPv6 Source

	// TTL 是记录的生存时间（秒），字符串形式以兼容 ddns-go 的配置模型。
	TTL string
	// HTTPInterface 是发送请求时绑定的网卡；为空表示默认网卡。
	HTTPInterface string

	// --- 运行状态（由引擎写入） ---

	LastRunAt   *time.Time
	LastStatus  Status
	LastMessage string
	LastIPv4    string
	LastIPv6    string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Status 是任务上次执行的结果。
type Status string

const (
	// StatusNever 从未执行过。
	StatusNever Status = ""
	// StatusSuccess 至少有一条记录被更新。
	StatusSuccess Status = "success"
	// StatusUnchanged 所有记录都已是目标值。
	StatusUnchanged Status = "unchanged"
	// StatusFailed 执行失败。
	StatusFailed Status = "failed"
)

// 校验错误。
var (
	// ErrLabelEmpty 表示任务名称为空。
	ErrLabelEmpty = errors.New("ddns: 任务名称不能为空")
	// ErrCredentialEmpty 表示未指定凭据。
	ErrCredentialEmpty = errors.New("ddns: 必须指定凭据")
	// ErrNoSource 表示没有启用任何地址来源。
	ErrNoSource = errors.New("ddns: 至少要启用 IPv4 或 IPv6 之一")
	// ErrNoDomains 表示启用的来源没有填写域名。
	ErrNoDomains = errors.New("ddns: 启用的地址来源必须至少填写一个域名")
	// ErrBadGetType 表示获取方式不受支持。
	ErrBadGetType = errors.New("ddns: 不支持的获取方式")
	// ErrSourceValueEmpty 表示获取方式对应的取值未填。
	ErrSourceValueEmpty = errors.New("ddns: 获取方式的取值不能为空")
	// ErrInvalidDomain 表示域名格式不合法。
	ErrInvalidDomain = errors.New("ddns: 域名格式不合法")
	// ErrNotFound 表示任务不存在。
	ErrNotFound = errors.New("ddns: 任务不存在")
)

// Validate 检查任务配置是否可用。
//
// 校验的取舍：只挡住"一定跑不通"的配置（没有域名、没选凭据、获取方式为空），
// 不校验域名能否解析、命令能不能跑通 —— 那些要联网才知道，
// 而配置保存不该依赖网络。真正的验证是执行一次，见 Engine.RunTask。
func (t Task) Validate() error {
	if strings.TrimSpace(t.Label) == "" {
		return ErrLabelEmpty
	}
	if strings.TrimSpace(t.CredentialID) == "" {
		return ErrCredentialEmpty
	}
	if !t.IPv4.Enable && !t.IPv6.Enable {
		return ErrNoSource
	}
	for _, s := range []Source{t.IPv4, t.IPv6} {
		if !s.Enable {
			continue
		}
		if !s.GetType.Valid() {
			return ErrBadGetType
		}
		if strings.TrimSpace(s.Value) == "" {
			return ErrSourceValueEmpty
		}
		if len(NormalizeDomains(s.Domains)) == 0 {
			return ErrNoDomains
		}
	}
	return nil
}

// RecordTypes 返回该任务需要处理的记录类型。
func (t Task) RecordTypes() []string {
	var out []string
	if t.IPv4.Enable && len(NormalizeDomains(t.IPv4.Domains)) > 0 {
		out = append(out, "A")
	}
	if t.IPv6.Enable && len(NormalizeDomains(t.IPv6.Domains)) > 0 {
		out = append(out, "AAAA")
	}
	return out
}

// NormalizeDomains 清理域名列表：去空白、丢空项、保序去重。
//
// 去重是必要的：用户很容易在两个地方重复填同一个域名，
// 而重复会让同一个地址被写两次 —— 多一次 API 调用，还可能撞上服务商限流。
func NormalizeDomains(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.TrimSpace(d)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

// SplitDomains 把换行分隔的域名字符串拆成列表。
func SplitDomains(raw string) []string {
	return NormalizeDomains(strings.Split(raw, "\n"))
}

// JoinDomains 把域名列表拼成换行分隔的字符串，用于落库。
func JoinDomains(list []string) string {
	return strings.Join(NormalizeDomains(list), "\n")
}

// Repository 是任务的持久化接口。
type Repository interface {
	List(ctx context.Context, enabledOnly bool) ([]Task, error)
	Get(ctx context.Context, id string) (Task, bool, error)
	Insert(ctx context.Context, t Task) error
	Update(ctx context.Context, t Task) error
	Delete(ctx context.Context, id string) error
	// CountByCredential 统计引用了某凭据的任务数，用于删除凭据前的检查。
	CountByCredential(ctx context.Context, credentialID string) (int, error)
}
