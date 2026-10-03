package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 本文件覆盖证书的保存与读取。
//
// 重点在**私钥的文件权限**与**文件名安全**：
//
//	权限错了        同一台机器上的其它用户能读走私钥
//	文件名没处理    通配域名（*.example.com）在 Windows 上保存失败，
//	                而症状是"申请成功了但找不到证书"

// ---------------------------------------------------------------------------
// 存储
// ---------------------------------------------------------------------------

// selfSigned 造一张自签证书，用于测试而不用连真实的 ACME。
func selfSigned(t *testing.T, domains []string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: domains[0]},
		DNSNames:     domains,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestStoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewStore(t.TempDir())

	expiry := time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)
	certPEM, keyPEM := selfSigned(t, []string{"example.com", "*.example.com"}, expiry)

	if err := store.Save(ctx, "example.com+1", Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := store.Load("example.com+1")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got.CertPEM) != string(certPEM) {
		t.Error("证书内容不一致")
	}
	if string(got.KeyPEM) != string(keyPEM) {
		t.Error("私钥内容不一致")
	}

	// 域名与有效期必须从**证书本身**读出来。
	//
	// 不依赖单独的元信息文件：它可能缺失（用户手工拷了证书过来），
	// 而决定"要不要续期"必须基于证书本身。
	if len(got.Domains) != 2 {
		t.Errorf("域名 = %v", got.Domains)
	}
	if got.ExpiresAt.IsZero() {
		t.Error("没有读出有效期 —— 续期判断会失效")
	}
	if !got.ExpiresAt.Equal(expiry) {
		t.Errorf("有效期 = %v，期望 %v", got.ExpiresAt, expiry)
	}
}

// TestStoreKeyPermissions 是这里最重要的一条。
//
// 私钥文件的权限必须是 0600。放宽它意味着同一台机器上的其它用户
// 能读走私钥，从而冒充这个域名 —— 而证书本身是公开的，
// 用户完全看不出自己的私钥已经泄漏。
func TestStoreKeyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位；访问控制由 ACL 负责")
	}

	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	certPEM, keyPEM := selfSigned(t, []string{"example.com"}, time.Now().Add(time.Hour))

	if err := store.Save(context.Background(), "example.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "example.com"+keySuffix))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("私钥权限 = %o，期望 600 —— 其它用户能读走私钥", perm)
	}

	// 证书本身是公开的（0644），因此**读**权限给出去没问题。
	//
	// 但组/其它用户的**写**权限不能有：那意味着同机的另一个用户
	// 可以把这张证书换成自己的。
	//
	// 这里曾经写成 `perm&0o077 != 0` —— 那个断言把读权限也算进去了，
	// 于是它与 Save 里刻意选择的 0644 直接矛盾，而错误信息说的却是
	// "不该给组/其它用户写权限"。断言与它自己的说明不一致时，
	// 失败信息会把人引向错误的方向（看起来像"私钥泄漏"，实际是断言写错）。
	// 只查写位：0o022。
	certInfo, err := os.Stat(filepath.Join(dir, "example.com"+certSuffix))
	if err != nil {
		t.Fatal(err)
	}
	if perm := certInfo.Mode().Perm(); perm&0o022 != 0 {
		t.Errorf("证书文件权限 = %o，不该给组/其它用户写权限", perm)
	}
}

// TestStoreTightensExistingFile 验证覆盖已存在的宽松文件时权限会被收紧。
//
// WriteFile 不会修改已存在文件的权限 —— 上一次可能是手工放的（0644），
// 而直接覆盖会让私钥一直保持宽松。
func TestStoreTightensExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 不使用 POSIX 权限位")
	}

	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	certPEM, keyPEM := selfSigned(t, []string{"example.com"}, time.Now().Add(time.Hour))

	// 先放一个权限宽松的文件。
	path := filepath.Join(dir, "example.com"+keySuffix)
	if err := os.WriteFile(path, []byte("旧内容"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := store.Save(context.Background(), "example.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("覆盖后权限 = %o，期望 600", perm)
	}
}

func TestStoreSanitizesWildcardNames(t *testing.T) {
	t.Parallel()

	// `*` 在 Windows 上是非法文件名字符，而通配证书的名字里就有它。
	// 不处理的话症状是"申请成功了但找不到证书"。
	name := sanitizeName("*.example.com")
	if strings.ContainsAny(name, `*/\:<>|?"`) {
		t.Errorf("文件名仍含非法字符: %q", name)
	}
	if name == "" {
		t.Error("文件名不能为空")
	}

	// 通过 Save/Load 走一遍，确认能存能取。
	store := NewStore(t.TempDir())
	certPEM, keyPEM := selfSigned(t, []string{"*.example.com"}, time.Now().Add(time.Hour))

	if err := store.Save(context.Background(), "*.example.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatalf("通配域名的证书保存失败: %v", err)
	}
	if _, err := store.Load("*.example.com"); err != nil {
		t.Fatalf("通配域名的证书读取失败: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"example.com":    "example.com",
		"*.example.com":  "wildcard.example.com",
		"a/b":            "a_b",
		"..":             "cert",
		"":               "cert",
		"a b":            "a_b",
		"sub.example.co": "sub.example.co",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestStoreExistsAndList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewStore(t.TempDir())

	if store.Exists("example.com") {
		t.Error("空目录里不该报告存在")
	}

	certPEM, keyPEM := selfSigned(t, []string{"example.com"}, time.Now().Add(time.Hour))
	if err := store.Save(ctx, "example.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "other.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}

	if !store.Exists("example.com") {
		t.Error("应当报告存在")
	}

	names, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("列出了 %d 张证书，期望 2: %v", len(names), names)
	}
}

// TestStorePartialFilesNotReportedAsExisting 验证残缺的证书不算存在。
//
// 只有证书没有私钥（或反之）的文件不能让 TLS 栈用起来，
// 而报告"存在"会让续期流程跳过它 —— 于是那个域名永远起不来。
func TestStorePartialFilesNotReportedAsExisting(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	certPEM, _ := selfSigned(t, []string{"example.com"}, time.Now().Add(time.Hour))

	// 只放证书，不放私钥。
	if err := os.WriteFile(filepath.Join(dir, "example.com"+certSuffix), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}

	if store.Exists("example.com") {
		t.Error("只有证书没有私钥时不该报告存在")
	}
}

func TestLoadMissingReturnsError(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())
	if _, err := store.Load("nope.com"); err == nil {
		t.Error("读取不存在的证书应当报错")
	}
}

func TestListMissingDirIsEmpty(t *testing.T) {
	t.Parallel()

	store := NewStore(filepath.Join(t.TempDir(), "does-not-exist"))
	names, err := store.List()
	if err != nil {
		t.Fatalf("目录不存在不该报错: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("应当返回空列表，得到 %v", names)
	}
}

// ---------------------------------------------------------------------------
// 文件名
// ---------------------------------------------------------------------------

func TestCertName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		domains []string
		want    string
	}{
		{[]string{"example.com"}, "example.com"},
		{[]string{"*.example.com"}, "example.com"},
		{[]string{"example.com", "www.example.com"}, "example.com+1"},
		{[]string{"a.com", "b.com", "c.com"}, "a.com+2"},
	}
	for _, tc := range cases {
		if got := certName(tc.domains); got != tc.want {
			t.Errorf("certName(%v) = %q，期望 %q", tc.domains, got, tc.want)
		}
	}

	// 多个域名必须产生不同的文件名，否则两张证书会互相覆盖。
	if certName([]string{"a.com"}) == certName([]string{"a.com", "b.com"}) {
		t.Error("单域名与双域名的证书名不该相同")
	}
}

func TestIsStaging(t *testing.T) {
	t.Parallel()

	if !isStaging(LetsEncryptStaging) {
		t.Error("测试环境地址应当被识别")
	}
	if isStaging(LetsEncryptProduction) {
		t.Error("生产环境地址不该被识别为测试")
	}
}

// ---------------------------------------------------------------------------
// 账户密钥
// ---------------------------------------------------------------------------

// TestAccountKeyIsPersisted 验证账户密钥被保存并复用。
//
// # 为什么这件事很要紧
//
// ACME 账户由密钥标识。密钥丢了就等于换了一个账户，
// 而**已经签发的证书无法再用原账户续期**。
//
// 更糟的是它的症状有 90 天的延迟：今天一切正常，三个月后证书
// 突然过期，而那时已经很难把两件事联系起来。
func TestAccountKeyIsPersisted(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "account.key")

	first, err := loadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}

	second, err := loadOrCreateAccountKey(path)
	if err != nil {
		t.Fatalf("再次读取失败: %v", err)
	}

	// 两次必须是同一个密钥。
	a, ok1 := first.Public().(*ecdsa.PublicKey)
	b, ok2 := second.Public().(*ecdsa.PublicKey)
	if !ok1 || !ok2 {
		t.Fatal("密钥类型不对")
	}
	if a.X.Cmp(b.X) != 0 || a.Y.Cmp(b.Y) != 0 {
		t.Error("两次调用生成了不同的密钥 —— 已签发的证书将无法续期")
	}
}

// TestCorruptAccountKeyIsNotOverwritten 验证损坏的密钥文件不被覆盖。
//
// 覆盖会让这个账户永久失效 —— 已经签发的证书仍然有效，但再也无法
// 续期，而症状要到 90 天后才出现。
func TestCorruptAccountKeyIsNotOverwritten(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "account.key")
	original := []byte("这不是一个 PEM 密钥")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadOrCreateAccountKey(path)
	if err == nil {
		t.Fatal("损坏的密钥文件应当报错，而不是静默重建")
	}
	// 错误信息必须说清后果。
	if !strings.Contains(err.Error(), "续期") {
		t.Errorf("错误信息应当说明后果（证书将无法续期）: %v", err)
	}

	// 文件必须原样保留，供用户备份或排查。
	after, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != string(original) {
		t.Error("损坏的密钥文件被改动了 —— 用户失去了排查的线索")
	}
}

func TestAccountKeyEmptyPathIsEphemeral(t *testing.T) {
	t.Parallel()

	// 没给路径时生成一个临时密钥，不落盘也不报错。
	key, err := loadOrCreateAccountKey("")
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if key == nil {
		t.Error("应当返回一个密钥")
	}
}

// ---------------------------------------------------------------------------
// CSR
// ---------------------------------------------------------------------------

// TestCSRKeepsWildcard 钉住通配域名在 CSR 里的形式。
//
// 去掉 `*` 会签出一张不覆盖子域名的证书，而症状是
// "申请成功了但浏览器仍报证书错误"。
func TestCSRKeepsWildcard(t *testing.T) {
	t.Parallel()

	domains := []string{"example.com", "*.example.com"}
	_, der, err := newCSR(domains)
	if err != nil {
		t.Fatalf("生成 CSR 失败: %v", err)
	}

	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("解析 CSR 失败: %v", err)
	}

	found := false
	for _, d := range csr.DNSNames {
		if d == "*.example.com" {
			found = true
		}
	}
	if !found {
		t.Errorf("CSR 里的域名 = %v，通配形式丢失了", csr.DNSNames)
	}
}

func TestCSRCommonNameStripsWildcard(t *testing.T) {
	t.Parallel()

	// CommonName 不能含 `*`（那不符合 X.509 的 CN 规范），
	// 而 DNSNames 里必须保留。
	_, der, err := newCSR([]string{"*.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(csr.Subject.CommonName, "*") {
		t.Errorf("CommonName = %q，不该含通配符", csr.Subject.CommonName)
	}
}
