package acme

import (
	"crypto/tls"
	"errors"
	"fmt"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// StoreProvider 把证书存储接到反向代理的 TLS 上。
//
// 它实现 proxy.CertProvider（在 proxy 包里定义），因此 acme 包
// **不依赖** proxy 包 —— 依赖方向是 proxy 定义接口、acme 实现它。
// 这样代理不需要知道 ACME 的存在，而 ACME 也不需要知道代理的存在。
type StoreProvider struct {
	store *Store
	log   *slog.Logger

	// resolve 把 SNI 里的域名映射到证书名。
	//
	// 由调用方提供，因为它取决于当前的 TLS 路由配置：
	// 同一个域名可能被一张通配证书覆盖，也可能是某张多域名证书的一部分。
	// 这个映射只有路由表知道。
	resolve func(domain string) (certName string, ok bool)

	mu    sync.Mutex
	cache map[string]cachedCert
}

type cachedCert struct {
	cert    *tls.Certificate
	loaded  time.Time
	expires time.Time
}

// cacheTTL 是已加载证书的缓存时长。
//
// 10 分钟的依据：证书文件在续期时会被**原地替换**，而缓存不失效
// 会让内核一直用旧证书 —— 直到某天它过期、站点打不开。
// 反过来，每次都读盘在高频访问下是明显开销（每次握手都要读文件、
// 解析 PEM、解析 X.509）。
//
// 10 分钟是两者的折中：最坏情况下续期后 10 分钟生效，
// 而正常流量下每分钟最多读一次盘。
const cacheTTL = 10 * time.Minute

// NewStoreProvider 构造证书来源。
func NewStoreProvider(store *Store, resolve func(string) (string, bool),
	log *slog.Logger) *StoreProvider {
	if log == nil {
		log = slog.Default()
	}
	return &StoreProvider{
		store:   store,
		log:     log,
		resolve: resolve,
		cache:   make(map[string]cachedCert),
	}
}

// SetResolve 更新域名到证书名的映射。
//
// 路由变化时调用。它顺带清空缓存 —— 映射变了意味着"哪个域名用哪张
// 证书"变了，而缓存是按域名索引的，不清会让旧映射继续生效。
func (p *StoreProvider) SetResolve(resolve func(string) (string, bool)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolve = resolve
	p.cache = make(map[string]cachedCert)
}

// Certificate 实现 proxy.CertProvider。
func (p *StoreProvider) Certificate(serverName string) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
	if name == "" {
		return nil, errors.New(i18n.T("acme.provider.no_sni"))
	}

	if cached, ok := p.fromCache(name); ok {
		return cached, nil
	}

	p.mu.Lock()
	resolve := p.resolve
	p.mu.Unlock()

	if resolve == nil {
		return nil, errors.New(i18n.T("acme.provider.no_routes"))
	}

	certName, ok := resolve(name)
	if !ok {
		return nil, fmt.Errorf(
			i18n.T("acme.provider.no_cert"), name)
	}

	cert, err := p.store.Load(certName)
	if err != nil {
		return nil, fmt.Errorf(
			i18n.T("acme.provider.read_failed"), name, certName, err)
	}

	pair, err := tls.X509KeyPair(cert.CertPEM, cert.KeyPEM)
	if err != nil {
		// 证书与私钥不匹配是最常见的失败。
		//
		// 单独说明它：那个提示会直接指向"重新签发"，而一句笼统的
		// "解析失败"会让用户去查 PEM 格式。
		return nil, fmt.Errorf(
			i18n.T("acme.provider.mismatch"), name, certName, err)
	}

	p.mu.Lock()
	p.cache[name] = cachedCert{
		cert:    &pair,
		loaded:  time.Now(),
		expires: cert.ExpiresAt,
	}
	p.mu.Unlock()

	return &pair, nil
}

// fromCache 取出缓存里的证书。
//
// 除了 TTL，还要检查证书本身的有效期：一张已经过期的证书不该被
// 继续用 —— 而缓存到 10 分钟可能会跨过过期那一刻。
func (p *StoreProvider) fromCache(name string) (*tls.Certificate, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry, ok := p.cache[name]
	if !ok {
		return nil, false
	}
	if time.Since(entry.loaded) > cacheTTL {
		delete(p.cache, name)
		return nil, false
	}
	if !entry.expires.IsZero() && time.Now().After(entry.expires) {
		delete(p.cache, name)
		return nil, false
	}
	return entry.cert, true
}

// Invalidate 清空缓存。
//
// 续期成功后应当调用它：新证书已经写进磁盘，而缓存里还是旧的。
// 不清的话用户会看到"续期成功了但浏览器仍然报证书过期"。
func (p *StoreProvider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = make(map[string]cachedCert)
}

// ---------------------------------------------------------------------------
// 域名映射
// ---------------------------------------------------------------------------

// Resolver 把域名映射到证书名。
//
// 它由路由表驱动：一条 TLS 路由声明了它要用 HTTPS 的域名，而这些
// 域名共用一张证书（证书名由域名集合算出）。
//
// 通配路由（*.example.com）会让任意一级子域名都指向同一张证书 ——
// 这是必须的：用户配了通配路由之后不会为每个子域名单独建一条。
type Resolver struct {
	mu sync.RWMutex
	// exact 是精确域名到证书名的映射。
	exact map[string]string
	// wildcard 是通配后缀到证书名的映射（键形如 ".example.com"）。
	wildcard map[string]string
	// order 让通配按后缀长度倒序匹配。
	wildcards []string
}

// NewResolver 构造空映射。
func NewResolver() *Resolver {
	return &Resolver{
		exact:    make(map[string]string),
		wildcard: make(map[string]string),
	}
}

// Add 登记一组域名与其证书名。
func (r *Resolver) Add(certName string, domains []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, d := range domains {
		d = normalizeDomain(d)
		if d == "" {
			continue
		}
		if strings.HasPrefix(d, "*.") {
			suffix := d[1:] // ".example.com"
			if _, exists := r.wildcard[suffix]; !exists {
				r.wildcards = append(r.wildcards, suffix)
			}
			r.wildcard[suffix] = certName
			continue
		}
		r.exact[d] = certName
	}

	// 更具体的通配先匹配。
	//
	// 不倒序的话结果取决于路由的添加顺序 —— 那是一个"改一下顺序就好了"
	// 的隐蔽 bug。
	sortByLengthDesc(r.wildcards)
}

// normalizeDomain 规范化一个域名。
//
// **结尾的点必须去掉**：`example.com.` 是 FQDN 的合法写法，而 DNS
// 客户端、证书里的 DNSNames、用户手输都可能带上它。不去掉的话
// 存储的键与查询的键不一致，表现为"明明配了这个域名却说没有证书"。
//
// Lookup 与 Add 必须用**同一个**规范化函数，否则两边会各按各的
// 规则处理，而产生只在某些写法下出现的匹配失败。
func normalizeDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

// Lookup 实现 StoreProvider 需要的映射函数。
func (r *Resolver) Lookup(domain string) (string, bool) {
	domain = normalizeDomain(domain)

	r.mu.RLock()
	defer r.mu.RUnlock()

	if name, ok := r.exact[domain]; ok {
		return name, true
	}
	for _, suffix := range r.wildcards {
		if !strings.HasSuffix(domain, suffix) {
			continue
		}
		// 通配只匹配**一级**：b.a.example.com 不该被 *.example.com 命中。
		//
		// 这与 TLS 证书的通配规则一致。放宽它会让用户拿到一个证书
		// 不匹配的域名，而浏览器只会说"证书无效"。
		prefix := strings.TrimSuffix(domain, suffix)
		if prefix == "" || strings.Contains(prefix, ".") {
			continue
		}
		return r.wildcard[suffix], true
	}
	return "", false
}

// Len 返回登记的精确域名数量（用于诊断）。
func (r *Resolver) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.exact) + len(r.wildcards)
}

func sortByLengthDesc(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && len(items[j]) > len(items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}
