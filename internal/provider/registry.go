// Package provider 是 DNS 服务商的元信息注册表。
//
// 它只描述"某家服务商需要什么凭据、支持哪些操作"，不涉及任何网络调用
// 的细节 —— 真正的 API 实现在 M2 引入，届时会以 dns.Provider 接口
// （见 docs/ARCHITECTURE.md §3）挂到同一份元信息上。
//
// 为什么元信息必须先于实现存在：
//
//   - 下游 GUI 靠它渲染凭据表单，**不需要为每家服务商写死界面**；
//   - 内核靠它校验凭据字段，避免"界面允许填、后端不认"的错配；
//   - 导入 ddns-go 配置时需要知道目标服务商有哪些字段、顺序如何。
package provider

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
)

// Capabilities 描述一家服务商支持哪些操作。
//
// 全部为 false 表示"元信息已登记但实现尚未就绪"——
// GUI 应当明确显示"尚未实现"，而不是让用户对着一排灰按钮猜原因。
type Capabilities struct {
	// Available 表示该服务商的实现是否已就绪（目前指"能校验凭据"）。
	Available bool
	// ZoneList 能列出账号下的 DNS 区域。
	ZoneList bool
	// RecordList 能列出区域内的记录。
	RecordList bool
	// RecordCreate 能新增记录。
	RecordCreate bool
	// RecordUpdate 能修改记录。
	RecordUpdate bool
	// RecordDelete 能删除记录。
	RecordDelete bool
	// AllRecordTypes 支持 A/AAAA 之外的记录类型。
	AllRecordTypes bool
	// CustomTTL 支持自定义 TTL。
	CustomTTL bool
	// Proxy 支持 CDN 代理开关（如 Cloudflare 的橙云）。
	Proxy bool
	// DNS01 可用于 ACME DNS-01 证书校验。
	DNS01 bool
}

// Verifier 校验一组凭据是否可用。
//
// 实现必须**只做只读调用**：校验凭据时创建或修改任何资源都是错的，
// 用户点"测试连接"不该产生副作用。
type Verifier func(ctx context.Context, fields map[string]string) error

// Provider 是一家 DNS 服务商的元信息。
type Provider struct {
	// Name 是稳定标识，用于凭据的 provider 字段与 ddns-go 的名称对应。
	Name string
	// DisplayName 是展示名称。它是专有名词，不参与翻译。
	DisplayName string
	// Tier 是能力分层：1 = 完整 CRUD，2 = 仅动态解析。
	Tier int
	// Capabilities 声明实际支持的操作。
	Capabilities Capabilities
	// CredentialFields 声明凭据字段。**声明顺序有意义**：
	// ddns-go 配置只提供位置化的 id / secret / extParam，
	// 导入时按此顺序映射（见 docs/MIGRATION-from-ddns-go.md）。
	CredentialFields []credential.FieldSpec
	// Verify 在实现就绪后非 nil。
	Verify Verifier
}

// Registry 是服务商注册表。并发安全。
type Registry struct {
	mu    sync.RWMutex
	byKey map[string]Provider
	order []string
}

// New 构造空注册表。
func New() *Registry {
	return &Registry{byKey: make(map[string]Provider)}
}

// Register 登记一家服务商。
//
// 重复登记会 panic：这只可能来自编码错误（两次注册同一个名字），
// 而静默覆盖会让"为什么这家服务商的行为和我写的不一样"变成一个
// 需要读遍全部 init 才能回答的问题。
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.byKey[p.Name]; dup {
		panic(fmt.Sprintf("provider: 服务商 %q 被重复登记", p.Name))
	}
	r.byKey[p.Name] = p
	r.order = append(r.order, p.Name)
}

// Get 按名称取服务商。
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byKey[name]
	return p, ok
}

// List 返回全部服务商。
//
// 排序规则：已实现的排前面，其次按 Tier 升序，最后按名称 ——
// 这样界面上的默认顺序是"能用的、能力强的、名字靠前的"。
func (r *Registry) List() []Provider {
	r.mu.RLock()
	names := make([]string, len(r.order))
	copy(names, r.order)
	byKey := make(map[string]Provider, len(r.byKey))
	for k, v := range r.byKey {
		byKey[k] = v
	}
	r.mu.RUnlock()

	out := make([]Provider, 0, len(names))
	for _, n := range names {
		out = append(out, byKey[n])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Capabilities.Available != out[j].Capabilities.Available {
			return out[i].Capabilities.Available
		}
		if out[i].Tier != out[j].Tier {
			return out[i].Tier < out[j].Tier
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Names 返回全部服务商名称（按登记顺序）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Default 返回内核内置的服务商注册表。
//
// 每次调用构造一份新的：注册表在初始化后不再变化，而测试需要
// 互不干扰的实例。
func Default() *Registry {
	r := New()
	for _, p := range builtin() {
		r.Register(p)
	}
	return r
}

// anonymousProvider 声明一个"元信息已登记、实现未就绪"的服务商。
//
// 让这些名字出现在注册表里有两个实际作用：
//
//  1. 导入 ddns-go 配置时能识别出用户用的是哪家，而不是报"未知服务商"
//     把整个配置丢掉；
//  2. 界面上能如实显示"这家还没做"，而不是让用户以为是自己填错了。
func anonymousProvider(name, displayName string, tier int, fields []credential.FieldSpec) Provider {
	return Provider{
		Name:             name,
		DisplayName:      displayName,
		Tier:             tier,
		Capabilities:     Capabilities{Available: false},
		CredentialFields: fields,
	}
}
