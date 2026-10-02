package acme

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖"哪个域名用哪张证书"这个映射，以及存储到 TLS 的接合。
//
// 映射错了的症状是"浏览器说证书不对"，而用户看到的只是一个泛泛的
// 证书错误 —— 他完全不知道是路由配错了、还是证书没签成。

// ---------------------------------------------------------------------------
// 域名映射
// ---------------------------------------------------------------------------

func TestResolverExactMatch(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.Add("home.example.com", []string{"home.example.com"})
	r.Add("www.example.com+1", []string{"www.example.com", "example.com"})

	if name, ok := r.Lookup("home.example.com"); !ok || name != "home.example.com" {
		t.Errorf("精确匹配失败: %q %v", name, ok)
	}

	// 多域名证书：其中任意一个域名都要能定位到同一张证书。
	for _, d := range []string{"www.example.com", "example.com"} {
		name, ok := r.Lookup(d)
		if !ok || name != "www.example.com+1" {
			t.Errorf("%s 应当指向 www.example.com+1，得到 %q %v", d, name, ok)
		}
	}

	if _, ok := r.Lookup("other.com"); ok {
		t.Error("没配过的域名不该匹配到证书")
	}
}

// TestResolverWildcardOneLevelOnly 钉住通配的匹配范围。
//
// 与 TLS 证书的通配规则一致：`*.example.com` 匹配 a.example.com，
// 但**不**匹配 example.com 本身，也**不**匹配 a.b.example.com。
func TestResolverWildcardOneLevelOnly(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.Add("example.com", []string{"*.example.com"})

	if name, ok := r.Lookup("a.example.com"); !ok || name != "example.com" {
		t.Errorf("一级子域名应当匹配: %q %v", name, ok)
	}

	if _, ok := r.Lookup("example.com"); ok {
		t.Error("*.example.com **不**该匹配裸域名")
	}
	if _, ok := r.Lookup("a.b.example.com"); ok {
		t.Error("*.example.com **不**该匹配多级子域名")
	}
	if _, ok := r.Lookup("notexample.com"); ok {
		t.Error("后缀匹配必须按标签边界")
	}
}

// TestResolverPrefersExactOverWildcard 验证精确匹配优先。
func TestResolverPrefersExactOverWildcard(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.Add("wild", []string{"*.example.com"})
	r.Add("exact", []string{"special.example.com"})

	if name, _ := r.Lookup("special.example.com"); name != "exact" {
		t.Errorf("精确匹配应当优先，得到 %q", name)
	}
	if name, _ := r.Lookup("other.example.com"); name != "wild" {
		t.Errorf("其余子域名应当落到通配，得到 %q", name)
	}
}

// TestResolverMoreSpecificWildcardWins 验证更具体的通配优先。
//
// 不倒序的话结果取决于路由的添加顺序 —— 那是一个"改一下顺序就好了"
// 的隐蔽 bug。
func TestResolverMoreSpecificWildcardWins(t *testing.T) {
	t.Parallel()

	// 故意先加宽泛的。
	r := NewResolver()
	r.Add("broad", []string{"*.example.com"})
	r.Add("narrow", []string{"*.a.example.com"})

	if name, _ := r.Lookup("b.a.example.com"); name != "narrow" {
		t.Errorf("b.a.example.com 应当命中更具体的 narrow，得到 %q", name)
	}
}

func TestResolverNormalizes(t *testing.T) {
	t.Parallel()

	r := NewResolver()
	r.Add("cert", []string{" Example.COM. "})

	for _, d := range []string{"example.com", "EXAMPLE.COM", "example.com."} {
		if _, ok := r.Lookup(d); !ok {
			t.Errorf("%q 应当匹配", d)
		}
	}
}

// ---------------------------------------------------------------------------
// 存储到 TLS 的接合
// ---------------------------------------------------------------------------

func newTestProviderWithCert(t *testing.T, domain string, expires time.Time) (*StoreProvider, *Store) {
	t.Helper()

	store := NewStore(t.TempDir())
	certPEM, keyPEM := selfSigned(t, []string{domain}, expires)

	name := certName([]string{domain})
	if err := store.Save(context.Background(), name,
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}

	r := NewResolver()
	r.Add(name, []string{domain})

	return NewStoreProvider(store, r.Lookup, nil), store
}

func TestStoreProviderReturnsCertificate(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "home.example.com", time.Now().Add(90*24*time.Hour))

	pair, err := p.Certificate("home.example.com")
	if err != nil {
		t.Fatalf("取证书失败: %v", err)
	}
	if pair == nil || len(pair.Certificate) == 0 {
		t.Fatal("返回的证书为空")
	}
}

// TestStoreProviderUnknownDomainIsActionable 验证未知域名的提示。
//
// 一句泛泛的"证书错误"会让用户去查证书本身，而真正的问题是
// "这条路由没启用 HTTPS"。
func TestStoreProviderUnknownDomainIsActionable(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "home.example.com", time.Now().Add(time.Hour))

	_, err := p.Certificate("other.example.com")
	if err == nil {
		t.Fatal("未知域名应当报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "other.example.com") {
		t.Errorf("错误信息应当点名域名: %s", msg)
	}
	if !strings.Contains(msg, "路由") {
		t.Errorf("错误信息应当指向路由配置: %s", msg)
	}
}

func TestStoreProviderNoRoutes(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	p := NewStoreProvider(store, nil, nil)

	_, err := p.Certificate("a.com")
	if err == nil {
		t.Fatal("没有路由时应当报错")
	}
	if !strings.Contains(err.Error(), "HTTPS 路由") {
		t.Errorf("错误信息应当说明还没配 HTTPS 路由: %v", err)
	}
}

func TestStoreProviderEmptyServerName(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Hour))
	if _, err := p.Certificate(""); err == nil {
		t.Error("空域名应当报错")
	}
}

// TestStoreProviderMissingCertFile 验证证书文件缺失时的提示。
func TestStoreProviderMissingCertFile(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	r := NewResolver()
	r.Add("gone.com", []string{"gone.com"})
	p := NewStoreProvider(store, r.Lookup, nil)

	_, err := p.Certificate("gone.com")
	if err == nil {
		t.Fatal("证书文件不存在时应当报错")
	}
	// 要指出是哪张证书读不到，便于排查。
	if !strings.Contains(err.Error(), "gone.com") {
		t.Errorf("错误信息应当点名证书: %v", err)
	}
}

// TestStoreProviderMismatchedKeyIsActionable 验证证书与私钥不匹配的提示。
//
// 那是最常见的失败，而提示必须直接指向"重新签发"，
// 而不是让用户去查 PEM 格式。
func TestStoreProviderMismatchedKeyIsActionable(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	certPEM, _ := selfSigned(t, []string{"a.com"}, time.Now().Add(time.Hour))
	_, otherKey := selfSigned(t, []string{"b.com"}, time.Now().Add(time.Hour))

	if err := store.Save(context.Background(), "a.com",
		Cert{CertPEM: certPEM, KeyPEM: otherKey}); err != nil {
		t.Fatal(err)
	}

	r := NewResolver()
	r.Add("a.com", []string{"a.com"})
	p := NewStoreProvider(store, r.Lookup, nil)

	_, err := p.Certificate("a.com")
	if err == nil {
		t.Fatal("证书与私钥不匹配时应当报错")
	}
	if !strings.Contains(err.Error(), "重新签发") {
		t.Errorf("错误信息应当指向重新签发: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 缓存
// ---------------------------------------------------------------------------

// TestStoreProviderCaches 验证缓存生效。
//
// GetCertificate 在每次握手时都会被调用，每次读盘 + 解析 PEM +
// 解析 X.509 在高频访问下是明显开销。
func TestStoreProviderCaches(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Hour))

	first, err := p.Certificate("a.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Certificate("a.com")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("两次调用拿到了不同的对象 —— 缓存没生效")
	}
}

// TestInvalidateForcesReload 验证续期后缓存会失效。
//
// 不清的话用户会看到"续期成功了但浏览器仍然报证书过期"。
func TestInvalidateForcesReload(t *testing.T) {
	t.Parallel()

	p, store := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Hour))

	first, err := p.Certificate("a.com")
	if err != nil {
		t.Fatal(err)
	}

	// 模拟续期：写入一张新证书到同一个名字。
	newCertPEM, newKeyPEM := selfSigned(t, []string{"a.com"}, time.Now().Add(90*24*time.Hour))
	if err := store.Save(context.Background(), "a.com",
		Cert{CertPEM: newCertPEM, KeyPEM: newKeyPEM}); err != nil {
		t.Fatal(err)
	}

	// 没清缓存时应当还是旧的。
	if cached, _ := p.Certificate("a.com"); cached != first {
		t.Error("清缓存之前不该重新加载")
	}

	p.Invalidate()

	second, err := p.Certificate("a.com")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Error("清缓存之后应当重新加载 —— 否则续期后浏览器仍会报证书过期")
	}
}

// TestCacheExpiresByTTL 验证缓存有时限。
//
// 证书文件在续期时会被**原地替换**，而缓存永不失效会让内核一直用
// 旧证书 —— 直到某天它过期、站点打不开。
func TestCacheExpiresByTTL(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Hour))

	if _, err := p.Certificate("a.com"); err != nil {
		t.Fatal(err)
	}

	// 手工把缓存条目的加载时间往前拨。
	p.mu.Lock()
	entry := p.cache["a.com"]
	entry.loaded = time.Now().Add(-2 * cacheTTL)
	p.cache["a.com"] = entry
	p.mu.Unlock()

	// 过期的缓存条目不该被返回。
	if _, ok := p.fromCache("a.com"); ok {
		t.Error("超过 TTL 的缓存条目应当失效")
	}
}

// TestCacheRejectsExpiredCertificate 验证已过期的证书不被缓存使用。
//
// 缓存 10 分钟可能跨过证书过期那一刻，而继续用一张过期证书
// 会让浏览器报错，同时内核这边显示"一切正常"。
func TestCacheRejectsExpiredCertificate(t *testing.T) {
	t.Parallel()

	// 造一张 1 秒后过期的证书。
	p, _ := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Second))

	if _, err := p.Certificate("a.com"); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)

	if _, ok := p.fromCache("a.com"); ok {
		t.Error("已过期的证书不该从缓存返回")
	}
}

// TestSetResolveClearsCache 验证换映射时清缓存。
//
// 映射变了意味着"哪个域名用哪张证书"变了，而缓存按域名索引 ——
// 不清会让旧映射继续生效。
func TestSetResolveClearsCache(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "a.com", time.Now().Add(time.Hour))
	if _, err := p.Certificate("a.com"); err != nil {
		t.Fatal(err)
	}

	r := NewResolver()
	r.Add("b.com", []string{"b.com"})
	p.SetResolve(r.Lookup)

	p.mu.Lock()
	n := len(p.cache)
	p.mu.Unlock()
	if n != 0 {
		t.Errorf("换映射后缓存应当清空，还剩 %d 条", n)
	}
}

// ---------------------------------------------------------------------------
// 与真实 TLS 握手接合
// ---------------------------------------------------------------------------

// TestCertificateUsableInTLSHandshake 验证取出的证书真的能用于握手。
//
// 只断言"返回了非 nil"是不够的：一个证书与私钥不匹配的对象同样非 nil，
// 而它在握手时才会失败。
func TestCertificateUsableInTLSHandshake(t *testing.T) {
	t.Parallel()

	p, _ := newTestProviderWithCert(t, "secure.example.com", time.Now().Add(time.Hour))

	cfg := &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return p.Certificate("secure.example.com")
		},
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck // 测试清理

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// 触发握手后立刻关掉 —— 我们只关心握手本身。
		if tlsConn, ok := conn.(*tls.Conn); ok {
			_ = tlsConn.Handshake() //nolint:errcheck // 握手结果由客户端侧断言
		}
		_ = conn.Close()
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{
		ServerName:         "secure.example.com",
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("TLS 握手失败: %v", err)
	}
	defer conn.Close() //nolint:errcheck // 测试清理

	// 证书里的域名必须与请求的一致。
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		t.Fatal("握手完成但没有证书")
	}
	found := false
	for _, d := range certs[0].DNSNames {
		if d == "secure.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("证书里的域名 = %v", certs[0].DNSNames)
	}
}

func TestStoreProviderUsesCacheTTLConstant(t *testing.T) {
	t.Parallel()

	// 缓存时长必须是一个**有限**的值。
	//
	// 设成 0 会让每次握手都读盘；设成无限大会让续期永远不生效。
	if cacheTTL <= 0 || cacheTTL > time.Hour {
		t.Errorf("cacheTTL = %v，既不能是无限制的，也不该过短", cacheTTL)
	}
}

func TestStoreDirIsUsed(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nested", "certs")
	store := NewStore(dir)

	certPEM, keyPEM := selfSigned(t, []string{"a.com"}, time.Now().Add(time.Hour))
	if err := store.Save(context.Background(), "a.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatalf("应当自动创建不存在的目录: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "a.com"+certSuffix)); err != nil {
		t.Errorf("证书没有写到指定目录: %v", err)
	}
}
