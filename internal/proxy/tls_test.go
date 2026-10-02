package proxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖"把签好的证书接进 TLS"这一段。
//
// 重点在三处容易出错的地方：
//
//	没有 SNI            必须拒绝握手，而不是随便给一张证书
//	SNI 带多余字符      要去掉端口与结尾的点，否则查不到证书
//	缓存                每次握手都读盘解析在高频访问下是明显开销

// fakeCertProvider 按域名提供自签证书。
type fakeCertProvider struct {
	mu    sync.Mutex
	certs map[string]*tls.Certificate
	calls []string
	err   error
}

func (f *fakeCertProvider) Certificate(name string) (*tls.Certificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if f.err != nil {
		return nil, f.err
	}
	cert, ok := f.certs[name]
	if !ok {
		return nil, fmt.Errorf("没有为 %s 配置证书", name)
	}
	return cert, nil
}

func (f *fakeCertProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newFakeProvider(t *testing.T, names ...string) *fakeCertProvider {
	t.Helper()

	f := &fakeCertProvider{certs: make(map[string]*tls.Certificate)}
	for _, name := range names {
		certPEM, keyPEM := selfSignedFor(name)
		cert, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			t.Fatalf("构造测试证书失败: %v", err)
		}
		f.certs[name] = &cert
	}
	return f
}

// ---------------------------------------------------------------------------
// SNI 规范化
// ---------------------------------------------------------------------------

func TestNormalizeSNI(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"example.com":     "example.com",
		"EXAMPLE.COM":     "example.com",
		" example.com ":   "example.com",
		"example.com.":    "example.com", // 结尾的点
		"example.com:443": "example.com", // 带端口
		"[::1]:443":       "::1",
		"":                "",
	}
	for in, want := range cases {
		if got := normalizeSNI(in); got != want {
			t.Errorf("normalizeSNI(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestSplitHostPortLoose(t *testing.T) {
	t.Parallel()

	// 没有端口时返回原值而不是错误 —— SNI 里通常没有端口。
	if h, _, err := splitHostPortLoose("example.com"); err != nil || h != "example.com" {
		t.Errorf("splitHostPortLoose(\"example.com\") = (%q, %v)", h, err)
	}
	// IPv6 字面量不该被拆坏。
	if h, _, err := splitHostPortLoose("2001:db8::1"); err != nil || h != "2001:db8::1" {
		t.Errorf("IPv6 字面量被拆坏了: (%q, %v)", h, err)
	}
}

// ---------------------------------------------------------------------------
// GetCertificate
// ---------------------------------------------------------------------------

func TestTLSConfigReturnsCertificateForSNI(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "home.example.com")
	s := NewServer(testLogger(), 0)

	cfg := s.TLSConfig(TLSOptions{Provider: provider})

	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "home.example.com"})
	if err != nil {
		t.Fatalf("取证书失败: %v", err)
	}
	if cert == nil {
		t.Fatal("没有返回证书")
	}
}

// TestTLSConfigRejectsMissingSNI 钉住没有 SNI 时的行为。
//
// 随便给一张证书会让客户端看到"证书域名不匹配"，而那个提示会把人
// 引向"证书配错了"，而不是"你该用域名访问"。
func TestTLSConfigRejectsMissingSNI(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "home.example.com")
	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{Provider: provider})

	_, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: ""})
	if err == nil {
		t.Fatal("没有 SNI 时应当拒绝握手")
	}
	// 错误信息必须指向正确的做法。
	if !strings.Contains(err.Error(), "域名") {
		t.Errorf("错误信息应当提示用域名访问: %v", err)
	}
	// 而且不该去问 provider —— 没有域名就无从查起。
	if provider.callCount() != 0 {
		t.Error("没有 SNI 时不该向证书来源查询")
	}
}

func TestTLSConfigSurfacesProviderError(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t) // 一张证书都没有
	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{Provider: provider})

	_, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "missing.example.com"})
	if err == nil {
		t.Fatal("没有对应证书时应当报错")
	}
	if !strings.Contains(err.Error(), "missing.example.com") {
		t.Errorf("错误信息应当点名域名: %v", err)
	}
}

// TestTLSConfigCachesCertificates 验证证书被缓存。
//
// GetCertificate 在**每次**握手时都会被调用，而每次去磁盘读 PEM 并
// 解析在高频访问下是明显的开销。
func TestTLSConfigCachesCertificates(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "home.example.com")
	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{Provider: provider})

	hello := &tls.ClientHelloInfo{ServerName: "home.example.com"}

	first, err := cfg.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cfg.GetCertificate(hello)
	if err != nil {
		t.Fatal(err)
	}

	if first != second {
		t.Error("两次握手拿到了不同的证书对象 —— 缓存没生效")
	}
	if provider.callCount() != 1 {
		t.Errorf("证书来源被调用了 %d 次，期望 1 次（其余走缓存）",
			provider.callCount())
	}
}

func TestTLSConfigCachesCaseInsensitively(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "home.example.com")
	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{Provider: provider})

	// 大小写不同、带结尾点、带端口 —— 都应当命中同一份缓存。
	for _, name := range []string{
		"home.example.com", "HOME.example.com", "home.example.com.", "home.example.com:443",
	} {
		if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	if provider.callCount() != 1 {
		t.Errorf("规范化之后应当只查询 1 次，实际 %d 次", provider.callCount())
	}
}

// ---------------------------------------------------------------------------
// TLS 参数
// ---------------------------------------------------------------------------

// TestTLSConfigDefaults 钉住默认参数。
func TestTLSConfigDefaults(t *testing.T) {
	t.Parallel()

	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{Provider: newFakeProvider(t, "a.com")})

	// 最低 TLS 1.2：1.0/1.1 已被所有主流浏览器弃用且有已知弱点，
	// 允许它们只会让扫描报告多几条告警。
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %s，期望 TLS 1.2", tlsVersionName(cfg.MinVersion))
	}

	// ALPN 必须同时含 h2 与 http/1.1：少了 h2 会让浏览器退回
	// HTTP/1.1，而多路复用正是反代场景最需要的。
	hasH2, hasHTTP1 := false, false
	for _, p := range cfg.NextProtos {
		switch p {
		case "h2":
			hasH2 = true
		case "http/1.1":
			hasHTTP1 = true
		}
	}
	if !hasH2 || !hasHTTP1 {
		t.Errorf("ALPN 列表 = %v，应当同时含 h2 与 http/1.1", cfg.NextProtos)
	}
}

func TestTLSConfigOverrides(t *testing.T) {
	t.Parallel()

	s := NewServer(testLogger(), 0)
	cfg := s.TLSConfig(TLSOptions{
		Provider:   newFakeProvider(t, "a.com"),
		NextProtos: []string{"http/1.1"},
		MinVersion: tls.VersionTLS13,
	})

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion 覆盖没生效")
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Errorf("NextProtos 覆盖没生效: %v", cfg.NextProtos)
	}
}

func TestTLSVersionName(t *testing.T) {
	t.Parallel()

	cases := map[uint16]string{
		tls.VersionTLS10: "TLS 1.0",
		tls.VersionTLS12: "TLS 1.2",
		tls.VersionTLS13: "TLS 1.3",
	}
	for v, want := range cases {
		if got := tlsVersionName(v); got != want {
			t.Errorf("tlsVersionName(0x%04x) = %q，期望 %q", v, got, want)
		}
	}
}

func TestServeTLSRequiresProvider(t *testing.T) {
	t.Parallel()

	m := NewManager(&memStore{}, testLogger())
	err := m.ServeTLS(context.Background(), freePort(t), TLSOptions{})
	if err == nil {
		t.Fatal("没有证书来源时应当报错")
	}
	// 不该因为参数错误就留下监听。
	if m.Status().Running {
		_ = m.Stop(context.Background())
		t.Error("参数校验失败时不该启动监听")
	}
}

func TestServeTLSStartsHTTPSListener(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "secure.example.com")
	m := NewManager(&memStore{}, testLogger())
	port := freePort(t)

	if err := m.ServeTLS(context.Background(), port, TLSOptions{Provider: provider}); err != nil {
		t.Fatalf("启动 HTTPS 失败: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	if !m.Status().Running {
		t.Fatal("状态应当是在运行")
	}

	// 用一个真实的 TLS 客户端连一次 —— 只有真的握手成功才算通。
	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{
		ServerName: "secure.example.com",
		// 测试用的是自签证书，跳过校验是必要的。
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		// **客户端也必须提供 ALPN 列表**：ALPN 是双方协商的结果，
		// 只配服务端不会有任何协议被选中。
		NextProtos: []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatalf("TLS 握手失败: %v", err)
	}
	defer conn.Close() //nolint:errcheck // 测试清理

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Error("握手完成但没有拿到对端证书")
	}
	// 必须协商到 h2 或 http/1.1。
	if state.NegotiatedProtocol == "" {
		t.Error("没有协商出 ALPN 协议")
	}
}

// TestServeTLSRejectsUnknownSNI 验证未知域名的握手被拒绝。
func TestServeTLSRejectsUnknownSNI(t *testing.T) {
	t.Parallel()

	provider := newFakeProvider(t, "known.example.com")
	m := NewManager(&memStore{}, testLogger())
	port := freePort(t)

	if err := m.ServeTLS(context.Background(), port, TLSOptions{Provider: provider}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	_, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{
		ServerName:         "unknown.example.com",
		InsecureSkipVerify: true,
	})
	if err == nil {
		t.Error("未知域名的握手应当失败")
	}
}

func TestServeTLSDetectsClosedPort(t *testing.T) {
	t.Parallel()

	// 先占住端口。
	port := freePort(t)
	blocker, err := netListen(port)
	if err != nil {
		t.Fatalf("无法占用端口: %v", err)
	}
	defer blocker.Close() //nolint:errcheck // 测试清理

	m := NewManager(&memStore{}, testLogger())
	err = m.ServeTLS(context.Background(), port, TLSOptions{
		Provider: newFakeProvider(t, "a.com"),
	})
	if err == nil {
		_ = m.Stop(context.Background())
		t.Fatal("端口被占用时应当报错")
	}
	if !strings.Contains(err.Error(), "占用") {
		t.Errorf("错误信息应当提示端口被占用: %v", err)
	}
}

// 保留 errors 引用：TLS 相关的错误分类在接入证书管理器时会用到。
var _ = errors.Is

// selfSignedFor 造一张覆盖指定域名的自签证书。
func selfSignedFor(domain string) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domain},
		DNSNames:     []string{domain},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// netListen 在指定端口上建立监听（所有接口）。
func netListen(port int) (net.Listener, error) {
	return net.Listen("tcp", fmt.Sprintf(":%d", port))
}
