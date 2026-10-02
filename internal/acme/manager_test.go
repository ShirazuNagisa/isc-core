package acme

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖证书的**续期判断**。
//
// 这是整个证书流程里最要紧的一段，因为它的错误是双向的：
//
//	判断太晚    证书过期 → 站点在某天突然打不开，而那天你可能正在外面
//	判断太早    浪费 ACME 的失败配额（生产环境每小时 5 次），
//	            用光之后要等一小时
//
// 而且"该不该续期"不能只看时间：用户给一条路由加了域名之后，
// 现有证书不覆盖新域名 —— 那与剩余有效期无关，证书可能刚签了三天。

// at 造一个固定时刻，让测试不依赖真实时钟。
var now = time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

// certWith 造一张证书。
//
// 注意用 Meta: Meta{...} 而不是直接写 Domains/IssuedAt ——
// 它们是**提升字段**，而 Go 1.26 还不允许在复合字面量里使用提升字段
// （那条限制在 1.27 才放开，见 go.mod 里的 go 指令）。
func certWith(domains []string, issued, expires time.Time) Cert {
	return Cert{Meta: Meta{
		Domains:   domains,
		IssuedAt:  issued,
		ExpiresAt: expires,
	}}
}

// ninetyDayCert 造一张刚签发的 90 天证书（Let's Encrypt 的典型寿命）。
func ninetyDayCert(domains []string) Cert {
	return certWith(domains, now, now.Add(90*24*time.Hour))
}

// ---------------------------------------------------------------------------
// 续期判断
// ---------------------------------------------------------------------------

func TestRenewDecisionFreshCertNeedsNothing(t *testing.T) {
	t.Parallel()

	cert := ninetyDayCert([]string{"example.com"})
	need, reason := RenewDecision(cert, []string{"example.com"}, now)

	if need {
		t.Errorf("刚签发的证书不该需要续期，理由: %s", reason)
	}
	if reason != "" {
		t.Errorf("不需要续期时不该给理由，得到 %q", reason)
	}
}

// TestRenewDecisionNearExpiry 钉住三分之一规则。
//
// 对 90 天的证书，剩余 30 天时正好进入续期窗口。
func TestRenewDecisionNearExpiry(t *testing.T) {
	t.Parallel()

	// 建模的是**同一张 90 天证书在不同年龄**：寿命固定，剩余时间在变。
	//
	// 不能让寿命随剩余时间一起变 —— 那样阈值（寿命的三分之一）
	// 也跟着变，测出来的就不是"三分之一规则"而是别的什么东西。
	const lifetime = 90 * 24 * time.Hour

	cases := []struct {
		remaining time.Duration
		want      bool
		note      string
	}{
		{lifetime, false, "刚签发"},
		{60 * 24 * time.Hour, false, "过了三分之一，仍在阈值外"},
		{31 * 24 * time.Hour, false, "刚好在阈值外"},
		{29 * 24 * time.Hour, true, "进入阈值（寿命的三分之一 = 30 天）"},
		{10 * 24 * time.Hour, true, "接近过期"},
		{24 * time.Hour, true, "最后一天"},
		{0, true, "刚好到期"},
		{-24 * time.Hour, true, "已过期"},
	}

	for _, tc := range cases {
		issued := now.Add(-(lifetime - tc.remaining))
		cert := certWith([]string{"example.com"}, issued, now.Add(tc.remaining))

		need, reason := RenewDecision(cert, []string{"example.com"}, now)
		if need != tc.want {
			t.Errorf("%s（剩余 %v）时 need=%v，期望 %v（理由: %s）",
				tc.note, tc.remaining, need, tc.want, reason)
		}
		if need && reason == "" {
			t.Errorf("剩余 %v 时判定需要续期，但没有给出理由", tc.remaining)
		}
	}
}

// TestRenewDecisionMissingDomainIsImmediate 是最重要的一条。
//
// 用户给一条路由加了域名之后，现有证书不覆盖新域名，而**这与剩余
// 有效期无关** —— 证书可能刚签了三天。只按时间判断的话，新增的域名
// 要等三个月才会生效，而用户完全不知道要等。
func TestRenewDecisionMissingDomainIsImmediate(t *testing.T) {
	t.Parallel()

	// 一张刚刚签发的、还有 90 天的证书。
	cert := ninetyDayCert([]string{"example.com"})

	need, reason := RenewDecision(cert, []string{"example.com", "www.example.com"}, now)
	if !need {
		t.Fatal("证书不覆盖新域名时必须续期 —— 只按时间判断会让新域名等三个月")
	}
	if !strings.Contains(reason, "www.example.com") {
		t.Errorf("理由应当点名缺失的域名: %s", reason)
	}
}

func TestRenewDecisionWildcardCoverage(t *testing.T) {
	t.Parallel()

	cert := ninetyDayCert([]string{"*.example.com"})

	// 一级子域名被通配覆盖。
	if need, reason := RenewDecision(cert, []string{"a.example.com"}, now); need {
		t.Errorf("一级子域名应当被通配证书覆盖，却判定需要续期: %s", reason)
	}

	// **裸域名不被覆盖** —— 这与 TLS 证书的通配规则一致。
	if need, _ := RenewDecision(cert, []string{"example.com"}, now); !need {
		t.Error("*.example.com 不覆盖 example.com 本身，应当判定需要续期")
	}

	// **多级子域名不被覆盖**。
	if need, _ := RenewDecision(cert, []string{"a.b.example.com"}, now); !need {
		t.Error("*.example.com 不覆盖 a.b.example.com，应当判定需要续期")
	}
}

// TestRenewDecisionUnreadableExpiryRenews 验证读不出有效期时保守处理。
//
// 跳过会让一张读不出有效期的证书一直用到过期，而那时站点会突然打不开。
func TestRenewDecisionUnreadableExpiryRenews(t *testing.T) {
	t.Parallel()

	cert := Cert{Meta: Meta{Domains: []string{"example.com"}}} // 没有有效期
	need, reason := RenewDecision(cert, []string{"example.com"}, now)

	if !need {
		t.Fatal("读不出有效期时应当重新签发")
	}
	if reason == "" {
		t.Error("必须给出理由")
	}
}

// TestRenewalThresholdShortLivedCert 验证短期证书不会陷入循环。
//
// 有些 CA 签发 7 天的证书。按三分之一算只有 2.3 天，而一次失败加上
// 周末就可能过期 —— 因此有 3 天的下限。但阈值也不能超过总寿命的一半，
// 否则证书会"刚签完就要续"。
func TestRenewalThresholdShortLivedCert(t *testing.T) {
	t.Parallel()

	// 7 天的证书。
	cert := certWith([]string{"a.com"}, now, now.Add(7*24*time.Hour))
	threshold := renewalThreshold(cert)

	// 不能超过总寿命的一半（否则刚签完就进入续期窗口）。
	if threshold > 7*24*time.Hour/2 {
		t.Errorf("7 天证书的阈值 = %v，超过了总寿命的一半", threshold)
	}
	// 也不能小到没有重试余地。
	if threshold < 12*time.Hour {
		t.Errorf("7 天证书的阈值 = %v，太小了", threshold)
	}
}

func TestRenewalThresholdNormalCert(t *testing.T) {
	t.Parallel()

	cert := ninetyDayCert([]string{"a.com"})
	threshold := renewalThreshold(cert)

	// 90 天的三分之一 = 30 天。
	if threshold < 29*24*time.Hour || threshold > 31*24*time.Hour {
		t.Errorf("90 天证书的阈值 = %v，期望约 30 天", threshold)
	}
}

func TestRenewalThresholdWithoutIssuedAt(t *testing.T) {
	t.Parallel()

	// 读不出签发时间时用固定的保守值（30 天）。
	cert := Cert{Meta: Meta{Domains: []string{"a.com"}, ExpiresAt: now.Add(60 * 24 * time.Hour)}}
	threshold := renewalThreshold(cert)

	if threshold != 30*24*time.Hour {
		t.Errorf("阈值 = %v，期望 30 天", threshold)
	}
}

// ---------------------------------------------------------------------------
// 请求规范化
// ---------------------------------------------------------------------------

// TestCertRequestKeyIsOrderIndependent 钉住"顺序不该被当成换证书"。
//
// 用户在界面上调换域名顺序不该触发一次无谓的签发 ——
// 那会白白消耗 ACME 的配额。
func TestCertRequestKeyIsOrderIndependent(t *testing.T) {
	t.Parallel()

	a := CertRequest{Domains: []string{"example.com", "www.example.com"}}
	b := CertRequest{Domains: []string{"www.example.com", "example.com"}}

	if a.Key() != b.Key() {
		t.Errorf("顺序不同导致 Key 不同:\n  %s\n  %s", a.Key(), b.Key())
	}

	// 大小写也不该影响。
	c := CertRequest{Domains: []string{"EXAMPLE.com", "WWW.example.COM"}}
	if a.Key() != c.Key() {
		t.Error("大小写不同导致 Key 不同")
	}
}

func TestCertRequestNormalize(t *testing.T) {
	t.Parallel()

	req := CertRequest{Domains: []string{
		" WWW.Example.com ", "www.example.com", "", "example.com", "example.com",
	}}
	got := req.Normalize()

	if len(got.Domains) != 2 {
		t.Fatalf("去重后应当有 2 个域名，得到 %v", got.Domains)
	}
	// 必须排序（供 Key 使用）且小写。
	if got.Domains[0] != "example.com" || got.Domains[1] != "www.example.com" {
		t.Errorf("规范化结果 = %v", got.Domains)
	}
}

func TestCertRequestValidate(t *testing.T) {
	t.Parallel()

	good := CertRequest{
		Domains:      []string{"example.com", "*.example.com"},
		CredentialID: "c1",
	}
	if err := good.Validate(); err != nil {
		t.Errorf("合法请求被拒绝: %v", err)
	}

	bad := []struct {
		name string
		req  CertRequest
	}{
		{"没有域名", CertRequest{CredentialID: "c1"}},
		{"没有凭据", CertRequest{Domains: []string{"example.com"}}},
		{"单标签域名", CertRequest{Domains: []string{"localhost"}, CredentialID: "c1"}},
		{"域名含空格", CertRequest{Domains: []string{"a b.com"}, CredentialID: "c1"}},
		{"通配符位置不对", CertRequest{Domains: []string{"a.*.com"}, CredentialID: "c1"}},
		{"只有通配符", CertRequest{Domains: []string{"*."}, CredentialID: "c1"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); err == nil {
				t.Error("应当被拒绝")
			}
		})
	}
}

// TestValidateRejectsSingleLabelDomain 验证"localhost"被早一点挡住。
//
// 公网 CA 不为单标签域名签证书，而让用户等一轮 ACME 校验才发现
// 是在浪费他的时间。
func TestValidateRejectsSingleLabelDomain(t *testing.T) {
	t.Parallel()

	err := CertRequest{Domains: []string{"localhost"}, CredentialID: "c1"}.Validate()
	if err == nil {
		t.Fatal("应当被拒绝")
	}
	if !strings.Contains(err.Error(), "单标签") {
		t.Errorf("错误信息应当说清原因: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 待办判断
// ---------------------------------------------------------------------------

func newTestManager(t *testing.T, dir string) *Manager {
	t.Helper()
	return NewManager(NewStore(dir), nil, nil, nil)
}

// TestNeedsWorkDeduplicatesByCertName 验证同一个域名集合只算一次。
//
// 两条路由可能落在同一个域名集合上，重复签发会白白消耗配额。
func TestNeedsWorkDeduplicatesByCertName(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, t.TempDir())

	reqs := []CertRequest{
		{Domains: []string{"example.com"}, CredentialID: "c1"},
		{Domains: []string{"example.com"}, CredentialID: "c1"},
		{Domains: []string{"example.com"}, CredentialID: "c2"},
	}

	pending, err := m.NeedsWork(reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("应当去重成 1 个请求，得到 %d", len(pending))
	}
}

func TestNeedsWorkSkipsInvalidRequests(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, t.TempDir())

	reqs := []CertRequest{
		{Domains: []string{"localhost"}, CredentialID: "c1"}, // 非法
		{Domains: []string{"example.com"}, CredentialID: ""}, // 缺凭据
		{Domains: []string{"good.example.com"}, CredentialID: "c1"},
	}

	pending, err := m.NeedsWork(reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("应当只保留 1 个合法请求，得到 %d: %v", len(pending), pending)
	}
	if pending[0].Domains[0] != "good.example.com" {
		t.Errorf("保留的是 %v", pending[0].Domains)
	}
}

func TestNeedsWorkForMissingCert(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, t.TempDir())

	pending, err := m.NeedsWork([]CertRequest{
		{Domains: []string{"example.com"}, CredentialID: "c1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Error("证书不存在时应当需要签发")
	}
}

// ---------------------------------------------------------------------------
// 并发
// ---------------------------------------------------------------------------

// TestEnsureRejectsConcurrentIssuance 验证防重入。
//
// 并发的签发会创建两个 ACME 订单，而**失败配额是按订单计的**。
// 并发的重试很容易把配额用光，而用光之后要等一小时。
func TestEnsureRejectsConcurrentIssuance(t *testing.T) {
	t.Parallel()

	store := NewStore(t.TempDir())

	var (
		mu      sync.Mutex
		started int
		release = make(chan struct{})
	)

	// 一个新的客户端工厂，签发过程会阻塞直到测试放行。
	newClient := func(CertRequest) (*Client, error) {
		mu.Lock()
		started++
		mu.Unlock()
		return nil, errors.New("测试用：不该走到这里")
	}

	m := NewManager(store, newClient, nil, nil)

	// 手工占住 inflight，模拟"已经在签发中"。
	if !m.acquire("example.com") {
		t.Fatal("首次获取应当成功")
	}

	_, _, err := m.Ensure(context.Background(), CertRequest{
		Domains: []string{"example.com"}, CredentialID: "c1",
	})
	if err == nil {
		t.Fatal("同一个域名集合的并发签发应当被拒绝")
	}
	if !strings.Contains(err.Error(), "正在签发") {
		t.Errorf("错误信息应当说明原因: %v", err)
	}

	_ = release
	mu.Lock()
	if started != 0 {
		t.Error("被拒绝的请求不该走到客户端构造")
	}
	mu.Unlock()
}

// ---------------------------------------------------------------------------
// 状态
// ---------------------------------------------------------------------------

func TestStatusReportsNeedsRenew(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)
	m := NewManager(store, nil, nil, nil)

	// 存一张快过期的证书。
	certPEM, keyPEM := selfSigned(t, []string{"example.com"}, now.Add(10*24*time.Hour))
	if err := store.Save(context.Background(), "example.com",
		Cert{CertPEM: certPEM, KeyPEM: keyPEM}); err != nil {
		t.Fatal(err)
	}

	// 固定"现在"，让判断可预期。
	m.now = func() time.Time { return now }

	statuses, err := m.Status(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("应当有 1 张证书，得到 %d", len(statuses))
	}
	if !statuses[0].NeedsRenew {
		t.Error("剩余 10 天的证书应当需要续期")
	}
	if statuses[0].Reason == "" {
		t.Error("必须给出续期理由 —— 用户看到的第一个问题就是「为什么」")
	}
}

func TestStatusReportsUnreadableCert(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := NewStore(dir)

	// 手工造一个读不出来的证书文件。
	if err := writeFileMode(dir+"/broken"+certSuffix, []byte("不是 PEM"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileMode(dir+"/broken"+keySuffix, []byte("不是 PEM"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := NewManager(store, nil, nil, nil)
	statuses, err := m.Status(nil)
	if err != nil {
		t.Fatalf("坏证书不该让整个列表失败: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("应当仍列出这张证书，得到 %d", len(statuses))
	}
	// 读不出来时必须报告需要续期 —— 否则它会一直用到过期。
	if !statuses[0].NeedsRenew {
		t.Error("读不出的证书应当判定需要续期")
	}
	if statuses[0].Error == "" {
		t.Error("应当报告读取失败的原因")
	}
}
