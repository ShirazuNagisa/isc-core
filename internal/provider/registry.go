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
	"fmt"
	"sort"
	"sync"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// Provider 是一家 DNS 服务商的元信息。
type Provider struct {
	// Name 是稳定标识，用于凭据的 provider 字段与 ddns-go 的名称对应。
	Name string
	// DisplayName 是展示名称。它是专有名词，不参与翻译。
	DisplayName string
	// Tier 是能力分层：1 = 完整 CRUD，2 = 仅动态解析。
	Tier int
	// CredentialFields 声明凭据字段。
	//
	// 每个字段的 DdnsGoSlot 指出它对应 ddns-go 的哪个槽位 ——
	// 配置导入与运行期调用**共用这一份声明**，避免两处映射逐渐漂移。
	CredentialFields []credential.FieldSpec

	// Impl 是该服务商在 dns 接口下的实现；为 nil 表示尚未实现。
	//
	// 用一个可空字段而不是"恒返回未实现错误的桩"：调用方需要能用
	// `Impl != nil` 判断这家到底做没做，而不是在运行期试错。
	Impl dns.Provider

	// Declared 是无法从接口断言推导出来的能力声明。
	//
	// 例如"支持自定义 TTL""支持 CDN 代理开关""可用于 DNS-01"——
	// 这些是服务商 API 的属性，不是 Go 接口的形状。
	Declared Capabilities
}

// Capabilities 返回该服务商的实际能力。
//
// 从 Impl 上做了哪些接口断言推导而来，再叠加 Declared 里那些
// 接口无法表达的部分。这样"某家不支持删除记录"是**代码事实**，
// 而不是一份需要人工维护、迟早会过期的能力表。
func (p Provider) Capabilities() Capabilities {
	set := dns.Capabilities(p.Impl)
	return Capabilities{
		Available:      p.Impl != nil,
		Dynamic:        set.Dynamic,
		ZoneList:       set.ZoneList,
		RecordList:     set.RecordList,
		RecordCreate:   set.RecordCreate,
		RecordUpdate:   set.RecordUpdate,
		RecordDelete:   set.RecordDelete,
		AllRecordTypes: p.Declared.AllRecordTypes,
		CustomTTL:      p.Declared.CustomTTL,
		Proxy:          p.Declared.Proxy,
		DNS01:          p.Declared.DNS01,
	}
}

// Capabilities 描述一家服务商支持哪些操作。
type Capabilities struct {
	// Available 表示该服务商的实现是否已就绪。
	//
	// 为 false 时，服务商出现在列表里只是为了让配置导入与界面展示完整，
	// 其能力位一律为 false。GUI 应当明确显示"尚未实现"，
	// 而不是让用户对着一排灰按钮猜原因。
	Available bool
	// Dynamic 能把 A/AAAA 记录更新到指定 IP。
	Dynamic bool
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

// DynamicUpdater 返回某家服务商的动态解析实现。
//
// 用类型断言而不是在元信息里存一个布尔位：能力的有无是**代码事实**，
// 断言一次即可确定，不需要一份需要人工维护、迟早会过期的能力表。
//
// 返回 found=false 覆盖三种情况：服务商不存在、未接入实现、
// 实现不支持动态解析。
func (r *Registry) DynamicUpdater(name string) (dns.DynamicUpdater, bool) {
	p, ok := r.Get(name)
	if !ok || p.Impl == nil {
		return nil, false
	}
	updater, ok := p.Impl.(dns.DynamicUpdater)
	return updater, ok
}

// Verifier 返回某家服务商的凭据校验实现。
func (r *Registry) Verifier(name string) (dns.Verifier, bool) {
	p, ok := r.Get(name)
	if !ok || p.Impl == nil {
		return nil, false
	}
	v, ok := p.Impl.(dns.Verifier)
	return v, ok
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
		ci, cj := out[i].Capabilities(), out[j].Capabilities()
		if ci.Available != cj.Available {
			return ci.Available
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

// anonymousProvider 声明一个"元信息已登记"的服务商。
//
// 让尚未实现的服务商出现在注册表里有两个实际作用：
//
//  1. 导入 ddns-go 配置时能识别出用户用的是哪家，而不是报"未知服务商"
//     把整个配置丢掉；
//  2. 界面上能如实显示"这家还没做"，而不是让用户以为是自己填错了。
//
// 它不声明任何能力：能力一律从 Impl 的接口断言推导（见 Provider.Capabilities）。
func anonymousProvider(name, displayName string, tier int, fields []credential.FieldSpec) Provider {
	return Provider{
		Name:             name,
		DisplayName:      displayName,
		Tier:             tier,
		CredentialFields: fields,
	}
}
