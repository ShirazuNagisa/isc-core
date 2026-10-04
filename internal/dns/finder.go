package dns

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件回答一个本该由内核自己回答的问题：
//
//	**"要给 blog.example.com 签证书 / 建记录，该用哪把凭据？"**
//
// # 为什么不该让用户回答
//
// 这个问题的答案完全由数据决定：域名在哪个区域、那个区域在哪把凭据下。
// 让用户在一列"凭据标签 · 服务商"里挑一个，是在要求他心算一件
// 内核明明知道的事 —— 而他没有任何办法验证自己挑对了。
//
// 挑错的症状还很远：设置页上一切正常，等到几天后证书该续期时
// 才失败，而错误信息说的是"在区域里找不到那条 TXT 记录"。
//
// # 区域是唯一的线索
//
// 一个域名属于哪个区域是**最长后缀匹配**：`blog.example.com` 属于
// `example.com`，而不是 `com`。而一个区域只可能在一把凭据下 ——
// 域名注册商只有一个。因此从域名可以唯一地推出凭据。

// CredentialIDs 返回账号下全部凭据的 ID。
//
// 定义成一个函数而不是接口：它只有这一个用途，而调用方（守护进程）
// 手上正好有一个能列出凭据的服务。
type CredentialIDs func(ctx context.Context) ([]string, error)

// ZoneFinder 按域名反查该用哪把凭据、哪个区域。
// zoneSource 是反查器需要的最小能力。
//
// 定义成接口而不是直接用 *Service：反查只需要"列区域"与"能不能建
// 记录"这两件事，而把它们绑在一个二十多个方法的具体类型上，会让
// 这一层**没法被测试** —— 测试要构造 *Service 就得先有一个凭据
// 解析器、一个实现查找表和一个数据库。
type zoneSource interface {
	ListZones(ctx context.Context, credentialID string) ([]Zone, error)
	SupportsCreate(ctx context.Context, credentialID string) bool
	// Credential 取凭据本身，只为拿到服务商名用于显示。
	Credential(ctx context.Context, credentialID string) (Credential, error)
}

// ZoneFinder 按域名反查该用哪把凭据、哪个区域。
type ZoneFinder struct {
	svc zoneSource
	ids CredentialIDs
	ttl time.Duration
	now func() time.Time

	mu       sync.Mutex
	cached   []zoneCandidate
	cachedAt time.Time
}

// zoneCandidate 是一把凭据下的一个区域。
type zoneCandidate struct {
	credentialID string
	provider     string
	zone         Zone
}

// NewZoneFinder 构造一个反查器。
//
// ids 为 nil 时 `Find` 会明确报错，而不是返回"找不到"——
// 两者的区别是前者说明装配有问题，后者说明用户还没配 DNS 凭据。
func NewZoneFinder(svc zoneSource, ids CredentialIDs) *ZoneFinder {
	return newZoneFinder(svc, ids)
}

// newZoneFinderForTest 让测试能注入一个更窄的来源。
func newZoneFinderForTest(svc zoneSource, ids CredentialIDs) *ZoneFinder {
	return newZoneFinder(svc, ids)
}

func newZoneFinder(svc zoneSource, ids CredentialIDs) *ZoneFinder {
	return &ZoneFinder{
		svc: svc,
		ids: ids,
		// 5 分钟：区域列表几乎不变，而每次查找都可能打服务商的 API。
		// 太短会让"给一批域名签证书"变成一串重复的列表请求；
		// 太长则用户新建了区域之后要等一阵才能用它。
		ttl: 5 * time.Minute,
		now: time.Now,
	}
}

// Find 给一个域名找出能写它的凭据与区域。
//
// domain 可以是区域本身（`example.com`）或它下面的任意名字
// （`blog.example.com`、`mizar-abc.example.com`）。
func (f *ZoneFinder) Find(ctx context.Context, domain string) (string, Zone, error) {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" {
		return "", Zone{}, errors.New(i18n.T("dns.find.empty_domain"))
	}
	if f == nil || f.svc == nil {
		return "", Zone{}, errors.New(i18n.T("dns.find.unavailable"))
	}

	candidates, err := f.candidates(ctx)
	if err != nil {
		return "", Zone{}, err
	}

	best := zoneCandidate{}
	bestLen := -1
	for _, c := range candidates {
		zoneName := strings.ToLower(strings.TrimSuffix(c.zone.Name, "."))
		if zoneName == "" {
			continue
		}
		// 完全相等，或以 ".zone" 结尾（**标签边界**）。
		//
		// 只判 HasSuffix 是不行的：`notexample.com` 以 `example.com`
		// 结尾，但它完全不属于那个区域 —— 而据此选出的凭据会去
		// 写一个它管不着的域名，失败信息还是"区域里找不到记录"。
		if domain != zoneName && !strings.HasSuffix(domain, "."+zoneName) {
			continue
		}
		if len(zoneName) > bestLen {
			best = c
			bestLen = len(zoneName)
		}
	}
	if bestLen < 0 {
		return "", Zone{}, errors.New(i18n.T("dns.find.no_zone", domain))
	}
	return best.credentialID, best.zone, nil
}

// candidates 返回"凭据 × 它可管理的区域"的全部组合，带缓存。
func (f *ZoneFinder) candidates(ctx context.Context) ([]zoneCandidate, error) {
	f.mu.Lock()
	if f.cached != nil && f.now().Sub(f.cachedAt) < f.ttl {
		cached := f.cached
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	if f.ids == nil {
		return nil, errors.New(i18n.T("dns.find.unavailable"))
	}
	ids, err := f.ids(ctx)
	if err != nil {
		return nil, err
	}

	// 稳定顺序：结果不依赖凭据的返回顺序，否则同一个域名可能在两次
	// 运行里选到不同的凭据（当两个区域同名时），而那种抖动极难查。
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)

	// 记下"有没有凭据查不动"，而不是静默跳过。
	//
	// 静默跳过的后果是：网络抖一下、或者某把凭据的密钥过期了，
	// 用户看到的却是"找不到 XXX 所属的 DNS 区域，请确认这个域名
	// 已经加到某把凭据的账号下" —— 那句话把他指向一个**不存在的
	// 配置问题**，而真正的原因（查不动）一个字都没提。
	var (
		out         []zoneCandidate
		failed      []string
		lastFailure error
	)
	for _, id := range sorted {
		// 只考虑**能新建记录**的凭据。
		//
		// DNS-01 需要写一条 TXT，建站需要写一条 A/AAAA。支持改记录
		// 但不支持建记录的服务商（23 家里的 17 家）在这里被排除 ——
		// 不排除的话会选中一把看起来能用、一写就报"不支持"的凭据。
		if !f.svc.SupportsCreate(ctx, id) {
			continue
		}
		zones, err := f.svc.ListZones(ctx, id)
		if err != nil {
			// 单把凭据失败（密钥过期、网络抖动）不该让整次查找失败 ——
			// 只要还有别的凭据能匹配就行。但**要记下来**，见下。
			failed = append(failed, id)
			lastFailure = err
			continue
		}
		provider := ""
		if cred, err := f.svc.Credential(ctx, id); err == nil {
			provider = cred.Provider
		}
		for _, zone := range zones {
			out = append(out, zoneCandidate{credentialID: id, provider: provider, zone: zone})
		}
	}

	// 一条都没查到、而且有凭据查不动时，报"查不动"而不是"找不到"。
	//
	// 两者用户该做的事完全不同：前者是"过一会儿重试 / 检查凭据"，
	// 后者是"去确认域名加到了哪个账号下"。报错报成后者会让他去翻
	// 一个根本不存在的配置问题。
	if len(out) == 0 && len(failed) > 0 {
		return nil, fmt.Errorf(i18n.T("dns.find.list_failed"),
			len(failed), len(sorted), lastFailure)
	}

	f.mu.Lock()
	f.cached = out
	f.cachedAt = f.now()
	f.mu.Unlock()
	return out, nil
}

// Invalidate 丢掉缓存。
//
// 用户新建了区域、或者刚加了一把凭据之后调用 —— 否则他要等五分钟
// 才能用它，而界面上没有任何东西解释为什么"刚加的域名用不了"。
func (f *ZoneFinder) Invalidate() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.cached = nil
	f.mu.Unlock()
}

// Candidate 是一个可以承载子域名的区域。
type Candidate struct {
	// Domain 是区域名，例如 example.com。
	Domain string
	// CredentialID 是能写它的凭据。
	CredentialID string
	// Provider 是服务商名，供界面显示"这个域名归谁管"。
	Provider string
}

// List 返回全部可以承载子域名的区域。
//
// 供界面把"挂在哪个域名下"做成一个列表而不是一个输入框 ——
// 用户不需要记住自己的区域名，更不该把它打错（打错的后果是
// 找不到区域，或者更糟：在一个**同名但不同账号**的区域下建记录）。
func (f *ZoneFinder) List(ctx context.Context) ([]Candidate, error) {
	if f == nil || f.svc == nil {
		return nil, errors.New(i18n.T("dns.find.unavailable"))
	}
	candidates, err := f.candidates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		out = append(out, Candidate{
			Domain:       strings.TrimSuffix(c.zone.Name, "."),
			CredentialID: c.credentialID,
			Provider:     c.provider,
		})
	}
	return out, nil
}

// FindCredential 只返回凭据 ID，供只需要它的调用方使用。
func (f *ZoneFinder) FindCredential(ctx context.Context, domain string) (string, error) {
	id, _, err := f.Find(ctx, domain)
	return id, err
}
