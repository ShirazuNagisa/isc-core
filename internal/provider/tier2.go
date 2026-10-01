package provider

import (
	"context"
	"fmt"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件把移植过来的 ddns-go 实现适配成 ISC 的 dns 接口。
//
// # 边界在哪
//
// internal/ddnsgo 是**照搬**的上游代码，一行逻辑都不改；
// 本文件是**我们**写的薄适配层，负责：
//
//   - 把 ISC 的 Credential（具名字段）翻译成 ddns-go 的 DNS（位置化槽位）；
//   - 把 ISC 的 DynamicRequest 翻译成 ddns-go 的 DnsConfig；
//   - 把 ddns-go 返回的 Domains 翻译回 ISC 的 DynamicResult。
//
// 这样上游代码的升级只需重跑移植脚本，适配层不受影响；
// 反过来 ISC 的接口演进也不用去动那 8000 多行移植代码。

// ddnsGoProvider 是移植代码里所有服务商共同实现的接口。
//
// 它对应 ddns-go 的 dns.DNS 接口，签名逐字相同 —— 正因为如此，
// 移植过来的 30 个 provider 不需要任何改动。
type ddnsGoProvider interface {
	Init(dnsConf *ddnsgo.DnsConfig, ipv4cache *ddnsgo.IpCache, ipv6cache *ddnsgo.IpCache)
	AddUpdateDomainRecords() ddnsgo.Domains
}

// tier2Factories 把服务商名映射到移植代码里的构造函数。
//
// 这张表是从 ddns-go 的 dns/index.go 里那个 switch 抄过来的。
// 之所以不做成 init 注册（那样更"优雅"）：显式的一张表能让
// "到底接了哪几家"在一处看全，也让新增服务商时必须显式改这里 ——
// 而不是靠某个文件被 import 的副作用生效。
var tier2Factories = map[string]func() ddnsGoProvider{
	"alidns":       func() ddnsGoProvider { return &ddnsgo.Alidns{} },
	"aliesa":       func() ddnsGoProvider { return &ddnsgo.Aliesa{} },
	"baiducloud":   func() ddnsGoProvider { return &ddnsgo.BaiduCloud{} },
	"callback":     func() ddnsGoProvider { return &ddnsgo.Callback{} },
	"cloudflare":   func() ddnsGoProvider { return &ddnsgo.Cloudflare{} },
	"cloudns":      func() ddnsGoProvider { return &ddnsgo.ClouDNS{} },
	"desec":        func() ddnsGoProvider { return &ddnsgo.DeSEC{} },
	"dnsla":        func() ddnsGoProvider { return &ddnsgo.Dnsla{} },
	"dnspod":       func() ddnsGoProvider { return &ddnsgo.Dnspod{} },
	"dynadot":      func() ddnsGoProvider { return &ddnsgo.Dynadot{} },
	"dynv6":        func() ddnsGoProvider { return &ddnsgo.Dynv6{} },
	"edgeone":      func() ddnsGoProvider { return &ddnsgo.EdgeOne{} },
	"eranet":       func() ddnsGoProvider { return &ddnsgo.Eranet{} },
	"gcore":        func() ddnsGoProvider { return &ddnsgo.Gcore{} },
	"godaddy":      func() ddnsGoProvider { return &ddnsgo.GoDaddyDNS{} },
	"hipmdnsmgr":   func() ddnsGoProvider { return &ddnsgo.HiPMDnsMgr{} },
	"huaweicloud":  func() ddnsGoProvider { return &ddnsgo.Huaweicloud{} },
	"name_com":     func() ddnsGoProvider { return &ddnsgo.NameCom{} },
	"namecheap":    func() ddnsGoProvider { return &ddnsgo.NameCheap{} },
	"namesilo":     func() ddnsGoProvider { return &ddnsgo.NameSilo{} },
	"nowcn":        func() ddnsGoProvider { return &ddnsgo.Nowcn{} },
	"nsone":        func() ddnsGoProvider { return &ddnsgo.NSOne{} },
	"porkbun":      func() ddnsGoProvider { return &ddnsgo.Porkbun{} },
	"rainyun":      func() ddnsGoProvider { return &ddnsgo.Rainyun{} },
	"spaceship":    func() ddnsGoProvider { return &ddnsgo.Spaceship{} },
	"tencentcloud": func() ddnsGoProvider { return &ddnsgo.TencentCloud{} },
	"tnethk":       func() ddnsGoProvider { return &ddnsgo.Tnethk{} },
	"trafficroute": func() ddnsGoProvider { return &ddnsgo.TrafficRoute{} },
	"vercel":       func() ddnsGoProvider { return &ddnsgo.Vercel{} },
}

// dynamicAdapter 是 Tier-2 服务商在 ISC 接口下的实现。
//
// 它只实现 dns.DynamicUpdater：Tier-2 的存在意义就是让动态地址能被解析，
// 记录管理交给 Tier-1。调用方用类型断言即可判断某家支持什么 ——
// 见 dns.Capabilities。
type dynamicAdapter struct {
	meta  dns.Meta
	specs []credential.FieldSpec
	new   func() ddnsGoProvider
}

// Meta 实现 dns.Provider。
func (a *dynamicAdapter) Meta() dns.Meta { return a.meta }

// UpdateDynamic 实现 dns.DynamicUpdater。
//
// 流程：组装 DnsConfig → 注入已知 IP → 调用上游实现 → 翻译结果。
//
// 两个关键设计：
//
//  1. **IP 由调用方注入**，不让上游代码自己去取。地址来源（网卡 /
//     外部接口 / 命令）是 ISC 的职责，而且 IPMonitor 已经在跟踪前缀变化，
//     再让每个 provider 各取一次既浪费又会在边界时刻取到不同的值。
//  2. **每次都新建 IpCache**，使上游的防抖逻辑必定放行。防抖是调度器的
//     职责（它知道"地址真的变了"还是"只是到点比对"），放在这里会形成
//     两层互相干扰的缓存 —— 那正是上游 util.ForceCompareGlobal 存在的原因，
//     而它是个很难排查的设计。
func (a *dynamicAdapter) UpdateDynamic(
	ctx context.Context, cred dns.Credential, req dns.DynamicRequest,
) (dns.DynamicResult, error) {
	if a.new == nil {
		return dns.DynamicResult{}, fmt.Errorf("provider: %s 未注册实现", a.meta.Name)
	}
	if req.IP == "" {
		return dns.DynamicResult{}, fmt.Errorf("provider: 未提供 IP")
	}

	conf := &ddnsgo.DnsConfig{
		Name:          a.meta.Name,
		DNS:           ddnsGoDNS(a.meta.Name, a.specs, cred.Fields),
		TTL:           req.TTL,
		HttpInterface: req.HTTPInterface,
	}

	switch req.RecordType {
	case dns.TypeAAAA:
		conf.Ipv6.Enable = true
		conf.Ipv6.Domains = req.Domains
		// 显式注入地址：GetType 留空也无妨，因为 ForceIpv6 会让
		// 取值路径直接返回它（见 internal/ddnsgo/ipsource.go）。
		conf.Ipv6.ForceAddr = req.IP
	default:
		conf.Ipv4.Enable = true
		conf.Ipv4.Domains = req.Domains
		conf.Ipv4.ForceAddr = req.IP
	}

	impl := a.new()
	// 每次新建缓存，等价于"跳过防抖"。
	impl.Init(conf, &ddnsgo.IpCache{}, &ddnsgo.IpCache{})

	// 上游实现不感知 context：它的 HTTP 客户端自带超时，
	// 而一次更新通常在一秒内结束。这里做一次前置检查，
	// 让"已被取消的任务"不会白白发出请求。
	if err := ctx.Err(); err != nil {
		return dns.DynamicResult{}, err
	}

	domains := impl.AddUpdateDomainRecords()
	return toDynamicResult(domains, req), nil
}

// ddnsGoDNS 把具名凭据字段翻译成 ddns-go 的位置化槽位。
//
// 复用 FieldSpec.DdnsGoSlot —— 与配置导入用的是**同一份声明**。
// 这一点很重要：如果导入和运行期各有一套映射，两边迟早会不一致，
// 表现为"导入的凭据能用，但迁移到新版本后突然失效"。
//
// 槽位是 1 基的（见 credential.DdnsGoSlot 的说明），因此零值 ——
// 也就是"忘了声明" —— 会被安全地忽略，而不是静默抢走 id 槽位。
func ddnsGoDNS(name string, specs []credential.FieldSpec, fields map[string]string) ddnsgo.DNS {
	var slots [3]string
	for _, s := range specs {
		idx := s.DdnsGoSlot - 1 // 1 基 → 0 基
		if idx < 0 || idx >= len(slots) {
			continue
		}
		slots[idx] = fields[s.Key]
	}
	return ddnsgo.DNS{
		Name:     name,
		ID:       slots[0],
		Secret:   slots[1],
		ExtParam: slots[2],
	}
}

// toDynamicResult 把上游的 Domains 翻译成 ISC 的结果类型。
func toDynamicResult(domains ddnsgo.Domains, req dns.DynamicRequest) dns.DynamicResult {
	out := dns.DynamicResult{RecordType: req.RecordType, IP: req.IP}

	src := domains.Ipv4Domains
	if req.RecordType == dns.TypeAAAA {
		src = domains.Ipv6Domains
	}

	for _, d := range src {
		out.Domains = append(out.Domains, dns.DomainResult{
			Domain:     d.String(),
			SubDomain:  d.GetSubDomain(),
			RootDomain: d.DomainName,
			Status:     toUpdateStatus(d.UpdateStatus),
		})
	}
	return out
}

// toUpdateStatus 翻译更新结果。
//
// 参数用 any 而不是具体类型：上游的 UpdateStatus 字段属于一个**未导出**
// 的类型（updateStatusType），跨包无法命名它。用 any + fmt.Sprint 是
// 在不改名移植代码的前提下唯一干净的做法。
//
// 未识别的值一律当成失败：把"不知道发生了什么"当成成功，会让用户在界面上
// 看到一片绿色而域名其实没被更新 —— 那比一个红色的失败要危险得多。
func toUpdateStatus(s any) dns.UpdateStatus {
	switch fmt.Sprint(s) {
	case string(ddnsgo.UpdatedSuccess):
		return dns.StatusSuccess
	case string(ddnsgo.UpdatedNothing):
		return dns.StatusUnchanged
	default:
		return dns.StatusFailed
	}
}

// newDynamicAdapter 为一家服务商构造适配器；未注册实现时返回 nil。
//
// 返回 nil 而不是一个"会报未实现"的桩：调用方（注册表、接口层）需要
// 用 `impl != nil` 判断这家到底做没做，一个恒返回错误的桩会让这个判断
// 变成运行期试错。
func newDynamicAdapter(meta dns.Meta, specs []credential.FieldSpec) *dynamicAdapter {
	factory, ok := tier2Factories[meta.Name]
	if !ok {
		return nil
	}
	return &dynamicAdapter{meta: meta, specs: specs, new: factory}
}

// verifiedDynamic 在动态更新之上补一个凭据校验能力。
//
// 为什么校验要单独写：ddns-go 没有"只校验凭据"的概念 —— 它启动就跑一次
// 更新，失败了才知道凭据不对。而 ISC 的界面上有一个"测试连接"按钮，
// 它**不能有副作用**：用户点一下就在服务商那边创建一条记录是不可接受的。
// 因此校验用各家自己的只读端点（如 Cloudflare 的 /user/tokens/verify）。
type verifiedDynamic struct {
	*dynamicAdapter
	verify func(ctx context.Context, fields map[string]string) error
}

// Verify 实现 dns.Verifier。
func (v *verifiedDynamic) Verify(ctx context.Context, cred dns.Credential) error {
	return v.verify(ctx, cred.Fields)
}

// attachImplementations 为已登记的服务商接上实现。
//
// 单独一步而不是写在各自的字面量里：这样 tier-2 的实现来源（一张工厂表）
// 与元信息（字段定义、分层）保持解耦 —— 新增一家只需在工厂表里加一行，
// 元信息那边什么都不用改。
func attachImplementations(list []Provider) []Provider {
	for i := range list {
		p := &list[i]
		adapter := newDynamicAdapter(
			dns.Meta{Name: p.Name, DisplayName: p.DisplayName, Tier: p.Tier},
			p.CredentialFields,
		)
		if adapter == nil {
			continue
		}
		if verify := verifierFor(p.Name); verify != nil {
			p.Impl = &verifiedDynamic{dynamicAdapter: adapter, verify: verify}
			continue
		}
		p.Impl = adapter
	}
	return list
}

// verifierFor 返回该服务商自己的只读凭据校验实现；没有则返回 nil。
//
// 目前只有 Cloudflare 有：它提供了专门的令牌校验端点。
// 其余服务商要么得靠一次真实请求（有副作用，不能用于"测试连接"），
// 要么得等 M2-d 接入各自的区域列表接口后顺带实现。
func verifierFor(name string) func(ctx context.Context, fields map[string]string) error {
	switch name {
	case "cloudflare":
		return NewCloudflareVerifier().Verify
	default:
		return nil
	}
}
