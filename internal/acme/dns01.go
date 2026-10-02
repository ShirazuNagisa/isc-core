// Package acme 实现 ACME 证书的申请与续期，用 **DNS-01** 校验。
//
// # 为什么用 DNS-01 而不是 HTTP-01
//
// HTTP-01 要求 Let's Encrypt 能从公网访问 80 端口。而本产品的目标场景
// （国内家宽）里，80 端口经常被运营商封禁，即使没封也需要额外开一条
// 防火墙规则 —— 而那意味着证书能否签发取决于一堆与 HTTPS 无关的条件。
//
// DNS-01 只需要能改 DNS 记录，而那是本产品已经在做的事。
//
// # 为什么不用 certmagic
//
// docs/PLAN.md 原本写的是集成 certmagic。实际动手后改为直接用
// `golang.org/x/crypto/acme`，理由是**新增依赖为零** ——
// x/crypto 已经在依赖图里（作为间接依赖），而 certmagic 会带来
// 8 个左右的传递依赖。
//
// 更重要的是 certmagic 的价值在这里用不上：
//
//   - 存储后端：我们自己有 SQLite 与数据目录；
//   - HTTP-01 / TLS-ALPN 求解器：我们只要 DNS-01；
//   - 配置文件格式：我们用的是自己的设置；
//   - libdns 生态：我们有自研的 dns.Provider 接口（见 D15），
//     而 Tier-1 六家已经实现了完整的记录 CRUD ——
//     写一份 libdns 适配器反而是多一层无谓的转换。
//
// 代价是续期调度与边界情况由我们自己负责。ACME 协议本身由
// x/crypto/acme 处理，那部分是我们不该自己写的。
package acme

import (
	"context"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// DNS01Provider 通过 DNS 记录完成 ACME 的 DNS-01 校验。
//
// 它把"写一条 _acme-challenge TXT 记录"这件事翻译成对 Tier-1
// 服务商的记录操作 —— 而那是内核已经在做的事，不需要任何新概念。
type DNS01Provider struct {
	creds     CredentialResolver
	providers ImplLookup

	// credentialID 是用于校验的那个凭据。
	credentialID string

	// propagationWait 是写记录之后、通知 ACME 服务器之前等待的时间。
	propagationWait time.Duration

	// cleanup 保存"域名 → 待清理的记录"。
	//
	// 用独立的锁：它只在 Present 与 CleanUp 时被短暂持有，
	// 与网络请求无关。
	cleanupMu sync.Mutex
	cleanup   map[string]CreatedRecord

	// onCreated 在成功写入挑战记录后回调，用于记录待清理的记录。
	//
	// 这是必要的：CleanUp 可能在内核重启后才被调用（或者根本不会 ——
	// 用户中途关掉了内核），那时内存里已经没有"我写了哪些记录"的
	// 信息。把清理所需的信息交给调用方去持久化，才能避免在用户的
	// DNS 里留下垃圾记录。
	onCreated func(CreatedRecord)
}

// CredentialResolver 按 ID 取出解密后的凭据。
//
// 与 ddns.CredentialResolver 及 dns.CredentialResolver 的签名完全一致 ——
// 因此装配处那一个适配器能同时满足三者，不必写三遍。
type CredentialResolver interface {
	Resolve(ctx context.Context, id string) (dns.Credential, error)
}

// ResolverFunc 把一个函数适配成 CredentialResolver。
//
// 给测试与一次性用途提供便利，而不必为此定义一个具名类型。
type ResolverFunc func(ctx context.Context, id string) (dns.Credential, error)

// Resolve 实现 CredentialResolver。
func (f ResolverFunc) Resolve(ctx context.Context, id string) (dns.Credential, error) {
	return f(ctx, id)
}

// ImplLookup 按服务商名取出实现。
type ImplLookup func(providerName string) (dns.Provider, bool)

// CreatedRecord 是一条为 ACME 挑战创建的 DNS 记录。
//
// 它带着**撤销所需的全部信息**，因此可以被持久化。
type CreatedRecord struct {
	// Provider 是服务商名。
	Provider string `json:"provider"`
	// ZoneID 是区域 ID。
	ZoneID string `json:"zone_id"`
	// RecordID 是记录的 ID。
	RecordID string `json:"record_id"`
	// Name 是完整记录名，便于人工核对与清理。
	Name string `json:"name"`
	// Domain 是这次校验的域名。
	Domain string `json:"domain"`
	// CreatedAt 是创建时间。
	CreatedAt time.Time `json:"created_at"`
}

// NewDNS01Provider 构造 DNS-01 提供者。
func NewDNS01Provider(
	creds CredentialResolver, providers ImplLookup, credentialID string,
) *DNS01Provider {
	return &DNS01Provider{
		creds:        creds,
		providers:    providers,
		credentialID: credentialID,

		// 60 秒的依据：DNS 记录的传播时间取决于服务商与 TTL。
		// 国内几家主流服务商的 API 写入到全网生效通常在几秒内，
		// 但权威 NS 的缓存与递归解析器的缓存可能更久。
		//
		// 这个值是可以调小的 —— 调小只是增加"ACME 服务器查不到记录
		// 而重试"的次数，不会导致失败（x/crypto/acme 会重试授权）。
		propagationWait: 60 * time.Second,
	}
}

// SetPropagationWait 覆盖等待时间，供测试使用。
func (p *DNS01Provider) SetPropagationWait(d time.Duration) {
	if d >= 0 {
		p.propagationWait = d
	}
}

// SetCreatedHook 设置在写入成功后调用的回调。
func (p *DNS01Provider) SetCreatedHook(fn func(CreatedRecord)) {
	p.onCreated = fn
}

// Present 实现 DNS-01 的"写入挑战记录"。
//
// domain 是待签发的域名（可能是通配形式 `*.example.com`），
// token 与 keyAuth 由 ACME 流程给出。
func (p *DNS01Provider) Present(ctx context.Context, domain, token, keyAuth string) error {
	rec := ChallengeRecord(domain, keyAuth)

	cred, impl, err := p.resolve(ctx)
	if err != nil {
		return err
	}

	lister, ok := impl.(dns.ZoneLister)
	if !ok {
		return fmt.Errorf(
			i18n.T("acme.dns01.no_list_zones"),
			cred.Provider, rec.Name)
	}
	creator, ok := impl.(dns.RecordCreator)
	if !ok {
		return fmt.Errorf(i18n.T("acme.dns01.no_create"), cred.Provider)
	}

	zones, err := lister.ListZones(ctx, cred)
	if err != nil {
		return fmt.Errorf(i18n.T("acme.dns01.list_zones_failed"), err)
	}

	zone, ok := MatchZone(rec.Name, zones)
	if !ok {
		return fmt.Errorf(
			i18n.T("acme.dns01.zone_not_found"),
			p.credentialID, len(zones), rec.Name)
	}

	created, err := creator.CreateRecord(ctx, cred, zone, dns.Record{
		Name:    rec.Name,
		Type:    dns.TypeTXT,
		Content: rec.Value,
		// TTL 取一个较小的值：挑战记录只在签发期间存在，
		// 大 TTL 会让它在 DNS 缓存里留很久。
		//
		// 注意这里用的是"服务商默认"（0）而不是硬编码 60 ——
		// 各家的最小 TTL 不同，阿里云免费版最小 600，硬编码 60
		// 会被服务商拒绝。让各家自己决定。
		TTL: 0,
	})
	if err != nil {
		return fmt.Errorf(i18n.T("acme.dns01.write_failed"), rec.Name, err)
	}

	if p.onCreated != nil {
		p.onCreated(CreatedRecord{
			Provider:  cred.Provider,
			ZoneID:    zone.ID,
			RecordID:  created.ID,
			Name:      rec.Name,
			Domain:    domain,
			CreatedAt: time.Now().UTC(),
		})
	}

	// 等记录传播出去再让 ACME 服务器去查。
	//
	// 这一步不能省：ACME 服务器在收到"挑战就绪"之后会**立刻**去查
	// TXT 记录，查到没有就可能判定失败。而 DNS 写入到全网可查之间
	// 总有延迟。
	if p.propagationWait > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(p.propagationWait):
		}
	}
	return nil
}

// CleanUp 实现 DNS-01 的"删除挑战记录"。
//
// 它**尽力而为**：删除失败不该让证书签发失败 —— 证书已经拿到了，
// 而残留一条 TXT 记录是可以事后清理的。调用方（CertManager）
// 会把失败记进日志与待清理列表。
func (p *DNS01Provider) CleanUp(ctx context.Context, domain, _, _ string) error {
	// 需要的定位信息由调用方通过 SetCleanupTarget 提供 ——
	// 见 CleanUpRecord。
	rec, ok := p.takeCleanup(domain)
	if !ok {
		return nil
	}
	return p.DeleteRecord(ctx, rec)
}

// DeleteRecord 删除一条此前创建的挑战记录。
//
// 它是**幂等**的：记录已经不存在时返回成功。清理动作被重试是常态
// （内核重启后重放待清理列表），把重试当失败会让列表永远清不空。
func (p *DNS01Provider) DeleteRecord(ctx context.Context, rec CreatedRecord) error {
	cred, err := p.creds.Resolve(ctx, p.credentialID)
	if err != nil {
		return fmt.Errorf(i18n.T("acme.cred_failed"), err)
	}
	impl, ok := p.providers(cred.Provider)
	if !ok || impl == nil {
		return fmt.Errorf(i18n.T("acme.dns01.impl_unavailable"), cred.Provider)
	}
	deleter, ok := impl.(dns.RecordDeleter)
	if !ok {
		return fmt.Errorf(i18n.T("acme.dns01.no_delete"), cred.Provider)
	}

	// 只填 ID：删除只需要它，而各家的删除接口也只用它。
	err = deleter.DeleteRecord(ctx, cred, dns.Zone{ID: rec.ZoneID}, rec.RecordID)
	if err != nil && errors.Is(err, dns.ErrNotFound) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// 清理目标的登记
// ---------------------------------------------------------------------------

// SetCleanupTarget 登记一个待清理的目标。
//
// # 为什么需要它
//
// DNS-01 的 CleanUp 回调只给出域名，而删除一条记录需要区域 ID 与
// 记录 ID —— 那些是 Present 才知道的。ACME 库不负责在我们的
// 调用与它自己的调用之间传递这些信息，因此必须由我们自己记住。
//
// 刻意做成"登记"而不是"从内存 map 里取"：调用方可以把这些信息
// 持久化，从而在内核重启后仍能清理残留记录。见 CreatedRecord。
func (p *DNS01Provider) SetCleanupTarget(rec CreatedRecord) {
	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	if p.cleanup == nil {
		p.cleanup = make(map[string]CreatedRecord)
	}
	p.cleanup[rec.Domain] = rec
}

func (p *DNS01Provider) takeCleanup(domain string) (CreatedRecord, bool) {
	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	rec, ok := p.cleanup[domain]
	if ok {
		delete(p.cleanup, domain)
	}
	return rec, ok
}

func (p *DNS01Provider) resolve(ctx context.Context) (dns.Credential, dns.Provider, error) {
	if p.credentialID == "" {
		return dns.Credential{}, nil, errors.New(
			i18n.T("acme.need_cred"))
	}

	cred, err := p.creds.Resolve(ctx, p.credentialID)
	if err != nil {
		return dns.Credential{}, nil, fmt.Errorf(i18n.T("acme.cred_failed"), err)
	}
	impl, ok := p.providers(cred.Provider)
	if !ok || impl == nil {
		return dns.Credential{}, nil, fmt.Errorf(
			i18n.T("acme.dns01.impl_for_dns01"), cred.Provider)
	}
	return cred, impl, nil
}

// ---------------------------------------------------------------------------
// 挑战记录的构造与区域匹配
// ---------------------------------------------------------------------------

// Challenge 描述一条 DNS-01 挑战记录。
type Challenge struct {
	// Name 是完整的记录名，例如 _acme-challenge.example.com。
	Name string
	// Value 是记录内容（base64url 的 SHA-256）。
	Value string
	// Domain 是这次挑战对应的域名（去掉通配前缀）。
	Domain string
}

// ChallengeRecord 由 ACME 给出的 keyAuth 算出挑战记录。
//
// # 通配域名的处理
//
// 通配证书（`*.example.com`）的挑战记录名是
// `_acme-challenge.example.com` —— **不带** `*` 前缀。
// 直接把 `*.example.com` 拼上去会得到一个永远查不到的记录名，
// 而症状是"授权一直超时"，看不出是名字拼错了。
//
// # 值的算法
//
// 值是 `base64url(SHA256(keyAuth))`，这是 RFC 8555 §8.4 规定的。
// 校验方（ACME 服务器）会重新算一遍并比对。
func ChallengeRecord(domain, keyAuth string) Challenge {
	clean := strings.TrimPrefix(domain, "*.")

	return Challenge{
		Name:   "_acme-challenge." + clean,
		Value:  DNS01Value(keyAuth),
		Domain: domain,
	}
}

// MatchZone 找出一个记录名所属的区域。
//
// # 为什么要按标签边界匹配
//
// 用 `strings.HasSuffix` 会出两个错：
//
//	notexample.com  被 example.com 匹配上（后缀相同但不是子域）
//	a.example.com   与 example.com 同时匹配时可能选错
//
// 因此必须按 `.` 边界比较，并在多个区域匹配时选**最长**的那个 ——
// 那才是 DNS 意义上的权威区域。选错的症状是"记录创建成功了，
// 但 ACME 服务器查不到"，因为记录被写到了另一个区域里。
func MatchZone(recordName string, zones []dns.Zone) (dns.Zone, bool) {
	name := strings.ToLower(strings.TrimSuffix(recordName, "."))

	var (
		best    dns.Zone
		bestLen = -1
		found   bool
	)
	for _, z := range zones {
		zoneName := strings.ToLower(strings.TrimSuffix(z.Name, "."))
		if zoneName == "" {
			continue
		}

		// 完全相等，或者以 ".zone" 结尾（标签边界）。
		if name != zoneName && !strings.HasSuffix(name, "."+zoneName) {
			continue
		}
		if len(zoneName) > bestLen {
			best = z
			bestLen = len(zoneName)
			found = true
		}
	}
	return best, found
}
