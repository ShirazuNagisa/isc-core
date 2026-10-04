package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"

	"context"
	"errors"
	"golang.org/x/crypto/acme"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件覆盖 DNS-01 校验里最容易出错的两处：
//
//	挑战记录名怎么拼      通配域名的 `*` 前缀必须去掉
//	记录归属哪个区域      必须按标签边界匹配并选最长的那个
//
// 这两处错了的症状都是"授权一直失败"，而 ACME 服务器给出的错误信息
// 通常只有一句 "challenge failed" —— 完全看不出是名字拼错了还是
// 记录写到了别的区域里。

// ---------------------------------------------------------------------------
// 挑战记录
// ---------------------------------------------------------------------------

// TestChallengeRecordStripsWildcard 钉住通配域名的处理。
//
// 通配证书（*.example.com）的挑战记录名是 _acme-challenge.example.com，
// **不带** `*` 前缀。直接把 `*.example.com` 拼上去会得到一个永远查不到
// 的记录名。
func TestChallengeRecordStripsWildcard(t *testing.T) {
	t.Parallel()

	cases := []struct {
		domain     string
		wantName   string
		wantDomain string
	}{
		{"example.com", "_acme-challenge.example.com", "example.com"},
		{"www.example.com", "_acme-challenge.www.example.com", "www.example.com"},
		{"*.example.com", "_acme-challenge.example.com", "*.example.com"},
		{"*.sub.example.com", "_acme-challenge.sub.example.com", "*.sub.example.com"},
	}

	for _, tc := range cases {
		t.Run(tc.domain, func(t *testing.T) {
			t.Parallel()

			got := ChallengeRecord(tc.domain, "key-auth-value")
			if got.Name != tc.wantName {
				t.Errorf("记录名 = %q，期望 %q", got.Name, tc.wantName)
			}
			// Domain 保留原始形式：清理时要按它找回当初的记录。
			if got.Domain != tc.wantDomain {
				t.Errorf("Domain = %q，期望 %q", got.Domain, tc.wantDomain)
			}
			if got.Value == "" {
				t.Error("记录值不能为空")
			}
		})
	}
}

// TestDNS01Value 钉住 RFC 8555 §8.4 的值算法。
//
// 值写错的症状是"授权一直失败"，而错误信息里没有任何线索指向它。
func TestDNS01Value(t *testing.T) {
	t.Parallel()

	// base64url(sha256("abc"))。
	//
	// 这是一个可以独立验算的向量：sha256("abc") =
	//   ba7816bf 8f01cfea 414140de 5dae2223 b00361a3 96177a9c b410ff61 f20015ad
	const want = "ungWv48Bz-pBQUDeXa4iI7ADYaOWF3qctBD_YfIAFa0"

	if got := DNS01Value("abc"); got != want {
		t.Errorf("DNS01Value(\"abc\") = %q，期望 %q", got, want)
	}

	// 必须是 base64**url**（无填充）：标准 base64 会产生 `+` 与 `/`，
	// 而它们不能出现在 DNS 记录值里。
	got := DNS01Value("some-key-authorization")
	if strings.ContainsAny(got, "+/=") {
		t.Errorf("值里含有非 URL 安全字符: %q", got)
	}
}

func TestChallengeRecordValueIsStable(t *testing.T) {
	t.Parallel()

	// 同一个 keyAuth 必须产生同一个值 —— 校验方会重算一遍并比对。
	a := ChallengeRecord("example.com", "same-key-auth")
	b := ChallengeRecord("example.com", "same-key-auth")
	if a.Value != b.Value {
		t.Error("相同输入产生了不同的值")
	}

	// 不同 keyAuth 必须产生不同的值。
	c := ChallengeRecord("example.com", "other-key-auth")
	if a.Value == c.Value {
		t.Error("不同输入产生了相同的值")
	}
}

// ---------------------------------------------------------------------------
// 区域匹配
// ---------------------------------------------------------------------------

// TestMatchZoneRespectsLabelBoundary 钉住按标签边界匹配。
//
// 用 strings.HasSuffix 会让 notexample.com 被 example.com 匹配上 ——
// 于是挑战记录被写到了一个**用户根本没有的区域**里，而 ACME 服务器
// 永远查不到它。
func TestMatchZoneRespectsLabelBoundary(t *testing.T) {
	t.Parallel()

	zones := []dns.Zone{
		{ID: "z1", Name: "example.com"},
		{ID: "z2", Name: "notexample.com"},
	}

	// notexample.com 的记录必须归到 notexample.com，而不是 example.com。
	got, ok := MatchZone("_acme-challenge.notexample.com", zones)
	if !ok {
		t.Fatal("应当匹配到区域")
	}
	if got.Name != "notexample.com" {
		t.Errorf("区域 = %q，期望 notexample.com —— 后缀匹配没有按标签边界",
			got.Name)
	}

	// example.com 自己的记录归 example.com。
	got, ok = MatchZone("_acme-challenge.example.com", zones)
	if !ok || got.Name != "example.com" {
		t.Errorf("区域 = %q", got.Name)
	}
}

// TestMatchZonePicksLongest 验证多个区域匹配时选最长的。
//
// 选错的症状是"记录创建成功了，但 ACME 服务器查不到" ——
// 因为记录被写到了另一个（更宽泛的）区域里。
func TestMatchZonePicksLongest(t *testing.T) {
	t.Parallel()

	zones := []dns.Zone{
		{ID: "root", Name: "example.com"},
		{ID: "sub", Name: "sub.example.com"},
		{ID: "deep", Name: "a.sub.example.com"},
	}

	cases := map[string]string{
		"_acme-challenge.example.com":       "root",
		"_acme-challenge.www.example.com":   "root",
		"_acme-challenge.sub.example.com":   "sub",
		"_acme-challenge.x.sub.example.com": "sub",
		"_acme-challenge.a.sub.example.com": "deep",
	}

	for name, wantID := range cases {
		got, ok := MatchZone(name, zones)
		if !ok {
			t.Errorf("%s 应当匹配到区域", name)
			continue
		}
		if got.ID != wantID {
			t.Errorf("%s 匹配到 %s，期望 %s", name, got.ID, wantID)
		}
	}
}

func TestMatchZoneNormalizes(t *testing.T) {
	t.Parallel()

	// 华为云的区域名带结尾的点；各家的大小写习惯也不同。
	zones := []dns.Zone{{ID: "z1", Name: "Example.COM."}}

	if _, ok := MatchZone("_acme-challenge.example.com", zones); !ok {
		t.Error("应当能匹配带结尾点、大小写不同的区域名")
	}
	if _, ok := MatchZone("_ACME-CHALLENGE.EXAMPLE.COM", zones); !ok {
		t.Error("记录名的大小写不该影响匹配")
	}
}

func TestMatchZoneNoMatch(t *testing.T) {
	t.Parallel()

	zones := []dns.Zone{{ID: "z1", Name: "example.com"}}
	if _, ok := MatchZone("_acme-challenge.other.com", zones); ok {
		t.Error("不相关的域名不该匹配到任何区域")
	}
	if _, ok := MatchZone("_acme-challenge.example.com", nil); ok {
		t.Error("空区域列表不该匹配")
	}
}

// ---------------------------------------------------------------------------
// Present / CleanUp
// ---------------------------------------------------------------------------

// fakeDNS 是一个可编程的 DNS 服务商实现。
type fakeDNS struct {
	mu        sync.Mutex
	zones     []dns.Zone
	created   []dns.Record
	createdIn []dns.Zone
	deleted   []string
	delErr    error
	listErr   error
	createErr error
	// noZoneList 模拟不支持列区域的服务商。
	noZoneList bool
}

func (f *fakeDNS) Meta() dns.Meta {
	return dns.Meta{Name: "fake", DisplayName: "假服务商", Tier: 1}
}

func (f *fakeDNS) ListZones(context.Context, dns.Credential) ([]dns.Zone, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.zones, nil
}

func (f *fakeDNS) ListRecords(context.Context, dns.Credential, dns.Zone, dns.RecordFilter) ([]dns.Record, error) {
	return nil, nil
}

func (f *fakeDNS) CreateRecord(_ context.Context, _ dns.Credential, zone dns.Zone, rec dns.Record) (dns.Record, error) {
	if f.createErr != nil {
		return dns.Record{}, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec.ID = "rec-" + rec.Name
	f.created = append(f.created, rec)
	f.createdIn = append(f.createdIn, zone)
	return rec, nil
}

func (f *fakeDNS) UpdateRecord(context.Context, dns.Credential, dns.Zone, dns.Record) (dns.Record, error) {
	return dns.Record{}, errors.New("未实现")
}

func (f *fakeDNS) DeleteRecord(_ context.Context, _ dns.Credential, _ dns.Zone, id string) error {
	if f.delErr != nil {
		return f.delErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeDNS) snapshot() ([]dns.Record, []dns.Zone, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]dns.Record(nil), f.created...),
		append([]dns.Zone(nil), f.createdIn...),
		append([]string(nil), f.deleted...)
}

func newTestProvider(t *testing.T, impl dns.Provider) *DNS01Provider {
	t.Helper()

	creds := ResolverFunc(func(context.Context, string) (dns.Credential, error) {
		return dns.Credential{ID: "c1", Provider: "fake"}, nil
	})
	lookup := func(string) (dns.Provider, bool) { return impl, true }

	p := NewDNS01Provider(creds, lookup, "c1")
	// 测试里不需要真的等 DNS 传播。
	p.SetPropagationWait(0)
	return p
}

func TestPresentCreatesChallengeRecord(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}
	p := newTestProvider(t, impl)

	var hooked []CreatedRecord
	p.SetCreatedHook(func(rec CreatedRecord) { hooked = append(hooked, rec) })

	err := p.Present(context.Background(), "*.example.com", "tok", "key-auth")
	if err != nil {
		t.Fatalf("Present 失败: %v", err)
	}

	created, zones, _ := impl.snapshot()
	if len(created) != 1 {
		t.Fatalf("应当创建 1 条记录，得到 %d", len(created))
	}

	rec := created[0]
	if rec.Type != dns.TypeTXT {
		t.Errorf("记录类型 = %s，期望 TXT", rec.Type)
	}
	if rec.Name != "_acme-challenge.example.com" {
		t.Errorf("记录名 = %q —— 通配前缀没有去掉会导致 ACME 永远查不到",
			rec.Name)
	}
	// 记录值必须**原样**是传进来的那个值。
	//
	// 传进来的已经是 `base64url(SHA256(keyAuth))`（由 x/crypto/acme 的
	// `DNS01ChallengeRecord` 算好）。这里再哈希一次的话，写出去的是
	// "摘要的摘要" —— 格式完全正确，而 CA 只会说"找到了错误的 TXT 记录"。
	// 这个断言以前写的是 `DNS01Value("key-auth")`，也就是把那个 bug
	// 当成正确行为钉住了。
	if rec.Content != "key-auth" {
		t.Errorf("记录值 = %q，期望原样发布传入的值", rec.Content)
	}
	// TTL 必须交给服务商决定（0）：各家最小 TTL 不同，
	// 硬编码 60 会被阿里云免费版这类服务商拒绝。
	if rec.TTL != 0 {
		t.Errorf("TTL = %d，期望 0（交给服务商默认值）", rec.TTL)
	}

	// 记录必须被写进正确的区域。
	if len(zones) != 1 || zones[0].ID != "z1" {
		t.Errorf("记录被写到了 %+v", zones)
	}

	// 回调必须带上完整的清理信息 —— 它可能被持久化，
	// 用于内核重启后清理残留记录。
	if len(hooked) != 1 {
		t.Fatalf("应当回调 1 次，得到 %d", len(hooked))
	}
	h := hooked[0]
	if h.ZoneID != "z1" || h.RecordID == "" || h.Name == "" || h.Domain != "*.example.com" {
		t.Errorf("回调信息不完整: %+v", h)
	}
}

func TestPresentFailsWithoutZone(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "other.com"}}}
	p := newTestProvider(t, impl)

	err := p.Present(context.Background(), "example.com", "tok", "key-auth")
	if err == nil {
		t.Fatal("找不到区域时应当报错")
	}
	// 错误信息要指向用户能检查的事。
	if !strings.Contains(err.Error(), "账号") {
		t.Errorf("错误信息应当提示检查账号下的域名: %v", err)
	}

	created, _, _ := impl.snapshot()
	if len(created) != 0 {
		t.Error("找不到区域时不该创建任何记录")
	}
}

func TestPresentFailsWhenZoneListUnsupported(t *testing.T) {
	t.Parallel()

	// 只实现 Provider，不实现 ZoneLister。
	impl := &onlyMeta{}
	p := newTestProvider(t, impl)

	err := p.Present(context.Background(), "example.com", "tok", "key-auth")
	if err == nil {
		t.Fatal("服务商不支持列区域时应当报错")
	}
	if !strings.Contains(err.Error(), "不支持列出区域") {
		t.Errorf("错误信息应当说明原因: %v", err)
	}
}

func TestPresentSurfacesCreateError(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{
		zones:     []dns.Zone{{ID: "z1", Name: "example.com"}},
		createErr: errors.New("记录已存在"),
	}
	p := newTestProvider(t, impl)

	err := p.Present(context.Background(), "example.com", "tok", "key-auth")
	if err == nil {
		t.Fatal("创建失败时应当报错")
	}
	// 服务商的原因必须原样带上 —— 那是唯一能据此行动的线索。
	if !strings.Contains(err.Error(), "记录已存在") {
		t.Errorf("错误信息丢失了服务商原因: %v", err)
	}
}

func TestPresentRequiresCredential(t *testing.T) {
	t.Parallel()

	creds := ResolverFunc(func(context.Context, string) (dns.Credential, error) {
		return dns.Credential{}, nil
	})
	lookup := func(string) (dns.Provider, bool) {
		return &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}, true
	}

	p := NewDNS01Provider(creds, lookup, "")
	p.SetPropagationWait(0)

	err := p.Present(context.Background(), "example.com", "tok", "ka")
	if err == nil {
		t.Fatal("未指定凭据时应当报错")
	}
	if !strings.Contains(err.Error(), "凭据") {
		t.Errorf("错误信息应当提到凭据: %v", err)
	}
}

// TestCleanUpDeletesOnlyRegisteredRecord 验证清理只删自己创建的那条。
//
// 这一点必须严格：挑战记录名是固定的（_acme-challenge.example.com），
// 而用户**可能自己也有**一条同名的 TXT 记录（例如用于别的 ACME 客户端）。
// 不加区分地按名字删除会把用户的记录一起删掉。
func TestCleanUpDeletesOnlyRegisteredRecord(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}
	p := newTestProvider(t, impl)
	ctx := context.Background()

	if err := p.Present(ctx, "example.com", "tok", "ka"); err != nil {
		t.Fatal(err)
	}

	// 模拟调用方把创建信息登记回来（真实流程里它来自持久化的记录）。
	p.SetCleanupTarget(CreatedRecord{
		Provider: "fake", ZoneID: "z1",
		RecordID: "rec-_acme-challenge.example.com",
		Name:     "_acme-challenge.example.com", Domain: "example.com",
	})

	if err := p.CleanUp(ctx, "example.com", "tok", "ka"); err != nil {
		t.Fatalf("清理失败: %v", err)
	}

	_, _, deleted := impl.snapshot()
	if len(deleted) != 1 {
		t.Fatalf("应当删除 1 条记录，得到 %d", len(deleted))
	}
	// 删的必须是**当初创建的那一条**（按 ID），而不是按名字重查。
	if deleted[0] != "rec-_acme-challenge.example.com" {
		t.Errorf("删除的记录 = %q", deleted[0])
	}
}

// TestCleanUpWithoutTargetIsNoop 验证没有登记信息时不乱删。
func TestCleanUpWithoutTargetIsNoop(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}
	p := newTestProvider(t, impl)

	// 没有 Present 过就直接 CleanUp。
	if err := p.CleanUp(context.Background(), "example.com", "", ""); err != nil {
		t.Fatalf("没有登记信息时不该报错: %v", err)
	}

	_, _, deleted := impl.snapshot()
	if len(deleted) != 0 {
		t.Errorf("没有登记信息时不该删除任何记录，实际删了 %v", deleted)
	}
}

// TestDeleteRecordIsIdempotent 验证删除是幂等的。
//
// 清理动作被重试是常态（内核重启后重放待清理列表），
// 把"记录已经不存在"当失败会让列表永远清不空。
func TestDeleteRecordIsIdempotent(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{delErr: dns.ErrNotFound}
	p := newTestProvider(t, impl)

	err := p.DeleteRecord(context.Background(), CreatedRecord{
		ZoneID: "z1", RecordID: "gone",
	})
	if err != nil {
		t.Errorf("记录已不存在时删除应当成功（幂等），得到 %v", err)
	}

	// 其它错误仍要报出来。
	impl.delErr = errors.New("权限不足")
	err = p.DeleteRecord(context.Background(), CreatedRecord{
		ZoneID: "z1", RecordID: "rec",
	})
	if err == nil {
		t.Error("其它删除错误应当被报出来")
	}
}

func TestPropagationWaitHonorsContext(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}
	p := newTestProvider(t, impl)
	p.SetPropagationWait(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.Present(ctx, "example.com", "tok", "ka")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("上下文超时时应当报错")
	}
	// 必须立刻返回，而不是傻等完 10 秒。
	if elapsed > 2*time.Second {
		t.Errorf("等待了 %v 才返回 —— 没有响应上下文取消", elapsed)
	}
}

// onlyMeta 是只实现 Provider 的最小替身。
type onlyMeta struct{}

func (onlyMeta) Meta() dns.Meta {
	return dns.Meta{Name: "onlymeta", DisplayName: "只有元信息", Tier: 1}
}

// TestChallengeValueMatchesUpstream 是一条**跨库**的棘轮。
//
// # 它挡的是什么
//
// 这个功能第一次真实运行时失败在 Let's Encrypt 的一句
// "Incorrect TXT record ... found at _acme-challenge.xxx" ——
// 记录格式完全正确、长度完全正确，只是值是**摘要的摘要**：
//
//	x/crypto/acme 的 DNS01ChallengeRecord 已经返回 base64url(SHA256(keyAuth))，
//	而本地又用 DNS01Value 哈希了一遍。
//
// 两个函数各自都是对的，错的是它们之间的**接缝** —— 而接缝正是单元
// 测试最容易漏掉的地方。因此这里直接拿上游的返回值当输入，
// 断言 ChallengeRecord 不再加工它。
func TestChallengeValueMatchesUpstream(t *testing.T) {
	t.Parallel()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	// DNS01ChallengeRecord 只用到 c.Key.Public() 与 token，不发网络请求。
	client := &acme.Client{Key: key}

	const token = "some-challenge-token"
	const domain = "www.example.com"

	value, err := client.DNS01ChallengeRecord(token)
	if err != nil {
		t.Fatalf("上游计算失败: %v", err)
	}

	rec := ChallengeRecord(domain, value)

	if rec.Value != value {
		t.Fatalf("挑战记录值被二次加工了：\n  上游给出 %q\n  我们发布 %q\n"+
			"CA 会报\"找到了错误的 TXT 记录\"，而格式看起来完全正常。\n"+
			"（二次哈希的结果是 %q）", value, rec.Value, DNS01Value(value))
	}
	if rec.Name != "_acme-challenge."+domain {
		t.Fatalf("记录名 = %q", rec.Name)
	}

	// 通配符要去掉 `*.`：`_acme-challenge.*.example.com` 不是合法记录名。
	wildcard := ChallengeRecord("*.example.com", value)
	if wildcard.Name != "_acme-challenge.example.com" {
		t.Fatalf("通配符记录名 = %q", wildcard.Name)
	}
}

// DNS01Value 本身仍然是 RFC 8555 §8.4 的那个算法 —— 它只是**不该**
// 被用在已经算好的值上。
func TestDNS01ValueStillImplementsRFC8554(t *testing.T) {
	t.Parallel()

	keyAuth := "token.thumbprint"
	want := base64.RawURLEncoding.EncodeToString(func() []byte {
		sum := sha256.Sum256([]byte(keyAuth))
		return sum[:]
	}())
	if got := DNS01Value(keyAuth); got != want {
		t.Fatalf("DNS01Value(%q) = %q，期望 %q", keyAuth, got, want)
	}
}

// **CleanUp 必须真的删掉记录。**
//
// # 这条测试挡的是一类"静默成功"的 bug
//
// `CleanUp` 在找不到清理目标时**返回 nil**（记录可能本来就没写成功，
// 那不是错误）。但 `Present` 忘了把目标登记到内存里 —— 于是它每次都
// 走那条路，沉默地什么也不做，而用户的 DNS 里留下几条自己没建过的
// `_acme-challenge` TXT，日志里一个错都没有。
//
// 这类 bug 的特征是：**返回值是对的，只是什么都没发生**。
// 因此这里不检查错误，而是检查"记录有没有被删掉"。
func TestCleanUpActuallyDeletesTheRecord(t *testing.T) {
	t.Parallel()

	impl := &fakeDNS{zones: []dns.Zone{{ID: "z1", Name: "example.com"}}}
	p := newTestProvider(t, impl)

	const domain = "example.com"
	if err := p.Present(context.Background(), domain, "token", "value"); err != nil {
		t.Fatalf("Present 失败: %v", err)
	}
	created, _, deleted := impl.snapshot()
	if len(created) != 1 {
		t.Fatalf("应当写入 1 条记录，得到 %d", len(created))
	}

	// 传空的 token 与值：它们的契约就是"用不上" —— 定位信息来自
	// Present 时登记的目标，而不是这两个参数。这也正是这个 bug 的
	// 成因：登记那一步漏了，参数再对也没用。
	if err := p.CleanUp(context.Background(), domain, "", ""); err != nil {
		t.Fatalf("CleanUp 失败: %v", err)
	}
	_, _, deleted = impl.snapshot()
	if len(deleted) != 1 {
		t.Fatalf("CleanUp 没有删掉记录 —— 它静默返回了成功，而用户的 DNS 里会留下残留（已删除 %d 条）",
			len(deleted))
	}
	if deleted[0] != created[0].ID {
		t.Fatalf("删的是 %q，期望 %q", deleted[0], created[0].ID)
	}
}
