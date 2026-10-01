// Package dns 是 ISC 对 DNS 服务商的统一抽象。
//
// 设计见 docs/DECISIONS.md D15 与 docs/ARCHITECTURE.md §3：
//
//   - **接口自研，不直接采用 libdns**。能力模型参考 libdns（ZoneLister /
//     RecordGetter / RecordAppender / RecordSetter / RecordDeleter），
//     但控制权留在自己手里 —— 国内厂商的社区实现普遍停留在 beta，
//     把关键路径押上去是拿可靠性换开发速度。
//   - **能力用可选接口表达**，而不是一个大接口加一堆"未实现"的返回。
//     这样"这家服务商不支持删除记录"是编译期就能看出的事实，
//     调用方用一次类型断言就能判断，不需要在运行期试错。
//   - 与 ddns-go 移植代码的桥接在 tier2.go，本文件的其余部分不依赖它。
package dns

import (
	"context"
	"strings"
)

// RecordType 是 DNS 记录类型。
//
// 用字符串而不是枚举：服务商 API 用的就是这些字面量，转换只会增加
// 出错面；而且新记录类型（HTTPS、SVCB）出现时不必改接口。
type RecordType string

// 已知的记录类型。
const (
	TypeA     RecordType = "A"
	TypeAAAA  RecordType = "AAAA"
	TypeCNAME RecordType = "CNAME"
	TypeMX    RecordType = "MX"
	TypeTXT   RecordType = "TXT"
	TypeNS    RecordType = "NS"
	TypeSRV   RecordType = "SRV"
	TypeCAA   RecordType = "CAA"
)

// Meta 是服务商的静态描述。
type Meta struct {
	// Name 是稳定标识，用于凭据的 provider 字段与 ddns-go 的名称对应。
	Name string
	// DisplayName 是展示名称（专有名词，不翻译）。
	DisplayName string
	// Tier 是能力分层：1 = 完整 CRUD，2 = 仅动态解析。
	Tier int
}

// Credential 是调用服务商时使用的凭据。
type Credential struct {
	ID       string
	Provider string
	// Fields 是凭据字段的**明文**。
	//
	// 它只在内存中短暂存在：由 credential.Service 解密后交给 provider，
	// 调用结束即失去引用。绝不允许被写进日志或审计。
	Fields map[string]string
}

// Field 取一个凭据字段并去掉首尾空白。
//
// 去空白是必要的：用户从控制台复制密钥时经常带上尾随空格或换行，
// 而服务商那边会把它当成密钥的一部分，报一个语义不明的鉴权失败。
func (c Credential) Field(key string) string {
	return strings.TrimSpace(c.Fields[key])
}

// Zone 是一个 DNS 区域。
type Zone struct {
	// ID 是服务商侧的稳定标识。
	ID string
	// Name 是区域名，例如 example.com。
	Name string
}

// Record 是一条 DNS 记录。
type Record struct {
	// ID 是服务商侧的稳定标识；新建时为空。
	ID string
	// Name 是完整记录名，例如 www.example.com。
	Name string
	// Type 是记录类型。
	Type RecordType
	// Content 是记录值。
	Content string
	// TTL 是生存时间（秒）。0 表示交给服务商默认值。
	TTL int
	// Proxied 是 CDN 代理开关，仅部分服务商支持（如 Cloudflare 橙云）。
	Proxied bool
	// Comment 是备注，仅部分服务商支持。
	Comment string
	// Priority 是 MX / SRV 记录的优先级。
	Priority int
}

// RecordFilter 是记录列表的过滤条件。
type RecordFilter struct {
	// Name 精确匹配记录名；为空表示不过滤。
	Name string
	// Type 过滤记录类型；为空表示不过滤。
	Type RecordType
}

// DynamicRequest 是一次"把域名指向当前 IP"的请求。
type DynamicRequest struct {
	// Domains 是要更新的域名，支持 ddns-go 的两种写法：
	//
	//	www.example.com        自动识别根域名
	//	www:example.com        显式指定"子域名:根域名"
	//
	// 后者用于 publicsuffix 无法正确判断的域名（例如某些二级后缀）。
	Domains []string
	// RecordType 是要更新的记录类型，A 或 AAAA。
	RecordType RecordType
	// IP 是要写入的地址。
	//
	// 由调用方（调度器）提供，而不是让 provider 自己去取 ——
	// 地址来源（网卡 / 外部接口 / 命令）是 ISC 的职责，
	// 服务商实现只该关心"怎么把它写进 DNS"。
	IP string
	// TTL 是生存时间（秒），字符串形式以兼容 ddns-go 的配置模型。
	// 空串表示使用服务商默认值。
	TTL string
	// HTTPInterface 是发送请求时绑定的网卡名；为空表示默认网卡。
	HTTPInterface string
}

// DomainResult 是单个域名的更新结果。
type DomainResult struct {
	// Domain 是完整域名，例如 www.example.com。
	Domain string
	// SubDomain 是子域名部分（根域名记录为 "@"）。
	SubDomain string
	// RootDomain 是根域名。
	RootDomain string
	// Status 是结果：success / failed / unchanged。
	Status UpdateStatus
	// Message 是补充说明（已本地化），失败时用于展示原因。
	Message string
}

// UpdateStatus 是单个域名的更新结果。
type UpdateStatus string

// 更新结果取值。
const (
	// StatusSuccess 记录已写入或已更新。
	StatusSuccess UpdateStatus = "success"
	// StatusFailed 写入失败。
	StatusFailed UpdateStatus = "failed"
	// StatusUnchanged 记录已是目标值，无需改动。
	StatusUnchanged UpdateStatus = "unchanged"
)

// DynamicResult 是一次动态更新的完整结果。
type DynamicResult struct {
	// Domains 是每个域名的结果，顺序与请求一致（解析成功的那些）。
	Domains []DomainResult
	// RecordType 是本次更新的记录类型。
	RecordType RecordType
	// IP 是本次写入的地址。
	IP string
}

// HasFailure 报告本次更新是否含失败项。
func (r DynamicResult) HasFailure() bool {
	for _, d := range r.Domains {
		if d.Status == StatusFailed {
			return true
		}
	}
	return false
}

// Changed 返回实际发生改动的域名数量。
//
// 用于决定是否发通知：一次"记录本来就是对的"的执行不该打扰用户。
func (r DynamicResult) Changed() int {
	n := 0
	for _, d := range r.Domains {
		if d.Status == StatusSuccess {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// 能力接口
// ---------------------------------------------------------------------------

// Verifier 校验凭据是否可用。
//
// 实现**必须只做只读调用**：用户点"测试连接"不该在服务商那边留下任何痕迹。
type Verifier interface {
	Verify(ctx context.Context, cred Credential) error
}

// DynamicUpdater 把 A/AAAA 记录更新到指定 IP。
//
// 这是 Tier-2 服务商**唯一**需要实现的能力 —— 它们的存在意义就是
// 让动态地址能被解析，其余记录管理交给 Tier-1。
type DynamicUpdater interface {
	UpdateDynamic(ctx context.Context, cred Credential, req DynamicRequest) (DynamicResult, error)
}

// ZoneLister 列出账号下的 DNS 区域。
type ZoneLister interface {
	ListZones(ctx context.Context, cred Credential) ([]Zone, error)
}

// RecordLister 列出区域内的记录。
type RecordLister interface {
	ListRecords(ctx context.Context, cred Credential, zone Zone, filter RecordFilter) ([]Record, error)
}

// RecordCreator 新增记录。
type RecordCreator interface {
	CreateRecord(ctx context.Context, cred Credential, zone Zone, rec Record) (Record, error)
}

// RecordUpdater 修改记录。
type RecordUpdater interface {
	UpdateRecord(ctx context.Context, cred Credential, zone Zone, rec Record) (Record, error)
}

// RecordDeleter 删除记录。
type RecordDeleter interface {
	DeleteRecord(ctx context.Context, cred Credential, zone Zone, recordID string) error
}

// Provider 是所有服务商实现的入口接口。
//
// 它只要求"能识别自己"这一个方法。其余能力（校验凭据、动态解析、
// 记录增删改查）全部通过上面的可选接口按需断言 ——
//
//   - 一个只有动态解析能力的服务商只需要实现 Provider 与 DynamicUpdater，
//     不必为"不支持删除记录"写一堆返回 ErrNotImplemented 的桩；
//   - 调用方用 `impl != nil` 判断这家做没做，用类型断言判断它能做什么，
//     两者都是代码事实，而不是需要人工维护、迟早过期的能力表。
//
// 把 Verify 放进这个接口是曾经的设计，但它逼着"只能做动态解析"的服务商
// 也得提供一个永远失败的 Verify —— 那正是本设计要避免的东西。
type Provider interface {
	Meta() Meta
}

// Capabilities 是从一组类型断言推导出的能力集合。
//
// 存在的意义：接口层要把它输出给 GUI 决定哪些按钮可点，
// 而"这个 provider 到底实现了哪些接口"只有运行期才知道。
func Capabilities(p any) CapabilitySet {
	set := CapabilitySet{}
	if _, ok := p.(Verifier); ok {
		set.Verify = true
	}
	if _, ok := p.(DynamicUpdater); ok {
		set.Dynamic = true
	}
	if _, ok := p.(ZoneLister); ok {
		set.ZoneList = true
	}
	if _, ok := p.(RecordLister); ok {
		set.RecordList = true
	}
	if _, ok := p.(RecordCreator); ok {
		set.RecordCreate = true
	}
	if _, ok := p.(RecordUpdater); ok {
		set.RecordUpdate = true
	}
	if _, ok := p.(RecordDeleter); ok {
		set.RecordDelete = true
	}
	return set
}

// CapabilitySet 是能力集合。
type CapabilitySet struct {
	Verify       bool
	Dynamic      bool
	ZoneList     bool
	RecordList   bool
	RecordCreate bool
	RecordUpdate bool
	RecordDelete bool
	// AllRecordTypes 支持 A/AAAA 之外的记录类型。
	AllRecordTypes bool
	// CustomTTL 支持自定义 TTL。
	CustomTTL bool
	// Proxy 支持 CDN 代理开关。
	Proxy bool
	// DNS01 可用于 ACME DNS-01 校验。
	DNS01 bool
}
