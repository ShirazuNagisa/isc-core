package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 子域名与 DNS 记录的生命周期。
//
// # 这里最需要钉住的是什么
//
// 用户的区域里有**他自己的**记录。一次"顺手清理"就可能毁掉他的站点。
// 因此下面有一组测试专门断言"我们只碰台账里记着的那几条 ID"，
// 而不只是断言"创建成功"。

// fakeDNS 记录所有写操作，并模拟服务商的行为。
type fakeDNS struct {
	zones []dns.Zone

	created []dns.Record
	updated []dns.Record
	deleted []string
	// failDelete 让删除失败，用来验证"删除失败不阻断台账清理"。
	failDelete bool
	// missingOnUpdate 让更新返回 not found，模拟记录被用户手动删掉。
	missingOnUpdate bool
	// nextID 是下一个新建记录的 ID。
	nextID int
}

func newFakeDNS(zones ...dns.Zone) *fakeDNS { return &fakeDNS{zones: zones} }

func (f *fakeDNS) ListZones(context.Context, string) ([]dns.Zone, error) { return f.zones, nil }

func (f *fakeDNS) ListRecords(context.Context, string, string, dns.RecordFilter) ([]dns.Record, error) {
	return nil, nil
}

func (f *fakeDNS) CreateRecord(_ context.Context, _, _ string, rec dns.Record) (dns.Record, error) {
	f.nextID++
	rec.ID = "rec-" + string(rune('0'+f.nextID))
	f.created = append(f.created, rec)
	return rec, nil
}

func (f *fakeDNS) UpdateRecord(_ context.Context, _, _, recordID string, rec dns.Record) (dns.Record, error) {
	if f.missingOnUpdate {
		return dns.Record{}, errors.New("record not found")
	}
	rec.ID = recordID
	f.updated = append(f.updated, rec)
	return rec, nil
}

func (f *fakeDNS) DeleteRecord(_ context.Context, _, _, recordID string) error {
	if f.failDelete {
		return errors.New("delete failed")
	}
	f.deleted = append(f.deleted, recordID)
	return nil
}

// ---------------------------------------------------------------------------
// 标签生成
// ---------------------------------------------------------------------------

// 标签必须是 DNS 合法的、定长的、且用了正确的字母表。
func TestPublicLabelIsDNSafeAndUsesCrockford(t *testing.T) {
	t.Parallel()

	for i := 0; i < 200; i++ {
		label, err := NewPublicLabel(nil)
		if err != nil {
			t.Fatalf("生成失败: %v", err)
		}
		if len(label) != LabelEntropy {
			t.Fatalf("长度 = %d，期望 %d", len(label), LabelEntropy)
		}
		for _, r := range label {
			if !strings.ContainsRune(strings.ToLower(crockford), r) {
				t.Fatalf("标签 %q 里有字母表之外的字符 %q", label, r)
			}
			// 这几个字符被排除是因为与 1/0 在手写与某些字体下无法区分。
			if strings.ContainsRune("ilou", r) {
				t.Fatalf("标签 %q 里出现了易混字符 %q", label, r)
			}
		}
	}
}

// 用固定随机源时输出必须可预测 —— 否则测试没法断言具体内容。
func TestPublicLabelIsDeterministicGivenReader(t *testing.T) {
	t.Parallel()

	// 0x00 → '0'，0x1f → 'Z'，0x05 → '5'。
	label, err := NewPublicLabel(bytes.NewReader(bytes.Repeat([]byte{0x00, 0x1f, 0x05}, 6)))
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	want := strings.Repeat("0z5", 5) + "0"
	if label != want {
		t.Fatalf("标签 = %q，期望 %q", label, want)
	}
}

func TestPublicLabelFailsOnShortReader(t *testing.T) {
	t.Parallel()

	if _, err := NewPublicLabel(bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("随机源不足时应当报错，而不是给出一个低熵的标签")
	}
}

// ---------------------------------------------------------------------------
// 台账的持久化
// ---------------------------------------------------------------------------

// 标签**生成一次就永远复用**。
//
// 它一变，手机上的二维码、书签、防火墙规则全部失效 ——
// 而用户不会知道为什么。
func TestEnsureLabelIsStableAcrossInstances(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first := NewPublicFace(dir, newFakeDNS(), nil)
	host, err := first.EnsureLabel("example.com")
	if err != nil {
		t.Fatalf("生成失败: %v", err)
	}
	if !strings.HasPrefix(host, DNSLabelPrefix) || !strings.HasSuffix(host, ".example.com") {
		t.Fatalf("域名形状不对: %s", host)
	}

	// 换一个实例（模拟重启），必须拿到同一个。
	second := NewPublicFace(dir, newFakeDNS(), nil)
	again, err := second.EnsureLabel("example.com")
	if err != nil {
		t.Fatalf("重启后失败: %v", err)
	}
	if again != host {
		t.Fatalf("重启后换了域名：%s → %s", host, again)
	}
}

// 换区域时重新生成：沿用旧标签会让"换了域名"看起来像"域名没生效"。
func TestEnsureLabelRegeneratesOnZoneChange(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	face := NewPublicFace(dir, newFakeDNS(), nil)
	first, _ := face.EnsureLabel("one.example.com")
	second, err := face.EnsureLabel("two.example.com")
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if first == second {
		t.Fatal("换了区域之后域名应当变化")
	}
	if !strings.HasSuffix(second, ".two.example.com") {
		t.Fatalf("新域名不对: %s", second)
	}
}

func TestEnsureLabelRejectsEmptyZone(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	if _, err := face.EnsureLabel("   "); err == nil {
		t.Fatal("空区域应当被拒绝")
	}
}

// ---------------------------------------------------------------------------
// 记录的生命周期
// ---------------------------------------------------------------------------

func TestApplyCreatesAAAARecordOnly(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS(dns.Zone{ID: "z1", Name: "example.com"})
	face := NewPublicFace(t.TempDir(), fake, nil)

	state, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "cred-1", ZoneID: "z1", Zone: "example.com",
		IPv6: net.ParseIP("2409:8a50:6a1:7450::560"),
	})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if len(fake.created) != 1 {
		t.Fatalf("应当只建一条记录，实际 %d 条", len(fake.created))
	}
	rec := fake.created[0]
	if rec.Type != dns.TypeAAAA {
		t.Fatalf("记录类型 = %s，期望 AAAA —— 只有实测过 IPv4 可达才该写 A", rec.Type)
	}
	if rec.Content != "2409:8a50:6a1:7450::560" {
		t.Fatalf("记录值不对: %s", rec.Content)
	}
	if rec.Name != state.Host() {
		t.Fatalf("记录名 %s 与域名 %s 不一致", rec.Name, state.Host())
	}
	// TTL 必须短：地址轮换之后缓存最多拖一分钟。
	if rec.TTL != 60 {
		t.Fatalf("TTL = %d，期望 60", rec.TTL)
	}
	if state.Records[string(dns.TypeAAAA)] == "" {
		t.Fatal("台账里没有记下记录 ID —— 那样就删不掉了")
	}
}

// 同样的地址再同步一次**不发写请求**。
//
// 每次同步都写一遍会让服务商的审计日志里堆满无意义的改动，
// 也会消耗 API 配额。
func TestApplyIsIdempotent(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)
	plan := PublicPlan{CredentialID: "c", ZoneID: "z", Zone: "example.com",
		IPv6: net.ParseIP("2409::1")}

	if _, err := face.Apply(context.Background(), plan); err != nil {
		t.Fatalf("第一次失败: %v", err)
	}
	if _, err := face.Apply(context.Background(), plan); err != nil {
		t.Fatalf("第二次失败: %v", err)
	}
	if len(fake.created) != 1 || len(fake.updated) != 0 {
		t.Fatalf("第二次不该再写：created=%d updated=%d", len(fake.created), len(fake.updated))
	}
}

// 地址变了就更新同一条记录，而不是新建。
func TestApplyUpdatesWhenAddressChanges(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)

	_, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::1")})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	state, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::2")})
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if len(fake.created) != 1 {
		t.Fatalf("不该新建第二条记录，实际 %d 条", len(fake.created))
	}
	if len(fake.updated) != 1 {
		t.Fatalf("应当更新一次，实际 %d 次", len(fake.updated))
	}
	if state.Addresses[string(dns.TypeAAAA)] != "2409::2" {
		t.Fatalf("台账里的地址没更新: %v", state.Addresses)
	}
}

// **这条是整组测试里最重要的一个。**
//
// 记录被用户在服务商后台删掉之后，我们账上还留着 ID。这时必须**新建**，
// 而不是把 "record not found" 抛给用户 —— 他不知道该怎么处理。
func TestApplyRecreatesWhenRecordWasDeletedOutside(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)

	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::1")}); err != nil {
		t.Fatalf("失败: %v", err)
	}

	fake.missingOnUpdate = true
	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::2")}); err != nil {
		t.Fatalf("记录被外部删掉之后应当自动重建，却报错: %v", err)
	}
	if len(fake.created) != 2 {
		t.Fatalf("应当重建一条，实际 created=%d", len(fake.created))
	}
}

// 从"有 IPv4"变成"没有 IPv4"时，那条 A 记录必须消失。
//
// 最实际的场景：宽带从公网 IPv4 换成了大内网。留着那条 A 会让
// 客户端一直先试一个永远连不上的地址。
func TestApplyRemovesStaleARecord(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)

	both := PublicPlan{CredentialID: "c", ZoneID: "z", Zone: "example.com",
		IPv6: net.ParseIP("2409::1"), IPv4: net.ParseIP("1.2.3.4")}
	if _, err := face.Apply(context.Background(), both); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(fake.created) != 2 {
		t.Fatalf("应当建两条（AAAA + A），实际 %d", len(fake.created))
	}

	only6 := both
	only6.IPv4 = nil
	state, err := face.Apply(context.Background(), only6)
	if err != nil {
		t.Fatalf("失败: %v", err)
	}

	if len(fake.deleted) != 1 {
		t.Fatalf("应当删掉那条 A，实际删除 %d 条", len(fake.deleted))
	}
	if _, still := state.Records[string(dns.TypeA)]; still {
		t.Fatal("台账里还留着 A 记录 —— 下次同步会以为它还在")
	}
	if _, still := state.Addresses[string(dns.TypeA)]; still {
		t.Fatal("台账里还留着 A 的地址")
	}
}

func TestApplyRefusesWithoutAnyAddress(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com"}); err == nil {
		t.Fatal("没有任何地址时应当报错，而不是建一条空记录")
	}
}

func TestApplyRefusesWithoutWriter(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), nil, nil)
	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com",
		IPv6: net.ParseIP("2409::1")}); err == nil {
		t.Fatal("没有 DNS 服务时应当明确报错，而不是静默什么都不做")
	}
}

func TestApplyRefusesWithoutCredentialOrZone(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	for _, plan := range []PublicPlan{
		{ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::1")},
		{CredentialID: "c", Zone: "example.com", IPv6: net.ParseIP("2409::1")},
	} {
		if _, err := face.Apply(context.Background(), plan); err == nil {
			t.Fatalf("凭据或区域缺失时应当报错: %+v", plan)
		}
	}
}

// ---------------------------------------------------------------------------
// 拆除
// ---------------------------------------------------------------------------

// 关闭公网访问时**只删我们建的那几条**。
//
// 断言删除的是 ID，而不是"按名字找了一遍"。用户的区域里可能有
// 别人建的、名字恰好也以 mizar- 开头的记录。
func TestTeardownDeletesOnlyOurRecordIDs(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)

	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com",
		IPv6: net.ParseIP("2409::1"), IPv4: net.ParseIP("1.2.3.4")}); err != nil {
		t.Fatalf("失败: %v", err)
	}

	// 我们在 fake 里分配到的 ID 是 rec-1 与 rec-2。
	if err := face.Teardown(context.Background(), "c", "z"); err != nil {
		t.Fatalf("拆除失败: %v", err)
	}

	if len(fake.deleted) != 2 {
		t.Fatalf("应当删两条，实际 %d 条", len(fake.deleted))
	}
	for _, id := range fake.deleted {
		if !strings.HasPrefix(id, "rec-") {
			t.Fatalf("删掉的不是我们记下的 ID: %q", id)
		}
	}

	state := face.State()
	if len(state.Records) != 0 || len(state.Addresses) != 0 {
		t.Fatalf("台账没有清空: %+v", state)
	}
	// 标签本身保留：用户下次再开时应当拿到同一个域名。
	if state.Label == "" {
		t.Fatal("拆除不该丢掉标签 —— 再开时域名应当不变")
	}
}

// 删除失败时：台账**一定**清空，但错误要报出去。
//
// 这两件事缺一不可：
//
//   - 不清台账 → "关闭公网访问"变成一个关不掉的开关（每次重试都
//     去删一个已经不存在的记录，永远失败）。记录可能已经被用户
//     在服务商后台手动删掉了，那是很常见的。
//   - 不报错 → 用户的区域里留下一条指向"已经不再服务"的地址的记录，
//     而没有人知道。界面应当据此提示"去 DNS 后台确认一下"。
func TestTeardownClearsLedgerEvenWhenDeleteFails(t *testing.T) {
	t.Parallel()

	fake := newFakeDNS()
	face := NewPublicFace(t.TempDir(), fake, nil)
	if _, err := face.Apply(context.Background(), PublicPlan{
		CredentialID: "c", ZoneID: "z", Zone: "example.com", IPv6: net.ParseIP("2409::1")}); err != nil {
		t.Fatalf("失败: %v", err)
	}

	fake.failDelete = true
	err := face.Teardown(context.Background(), "c", "z")
	if err == nil {
		t.Fatal("失败必须报出来 —— 否则用户不知道区域里还留着一条记录")
	}
	if len(face.State().Records) != 0 {
		t.Fatal("台账必须清空，否则开关永远关不掉")
	}
	if len(face.State().Addresses) != 0 {
		t.Fatal("地址也应当清掉")
	}
}

// ---------------------------------------------------------------------------
// 自检结论
// ---------------------------------------------------------------------------

func TestSetCheckPersists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	face := NewPublicFace(dir, newFakeDNS(), nil)
	if err := face.SetCheck(PublicCheck{Verdict: "reachable", Detail: "通了", Family: "ipv6"}); err != nil {
		t.Fatalf("失败: %v", err)
	}

	reopened := NewPublicFace(dir, newFakeDNS(), nil)
	check := reopened.State().LastCheck
	if check == nil || check.Verdict != "reachable" || check.Family != "ipv6" {
		t.Fatalf("结论没落盘: %+v", check)
	}
	if check.At.IsZero() {
		t.Fatal("时间戳应当被填上")
	}
}

// ---------------------------------------------------------------------------
// 公网 IPv4 探测
// ---------------------------------------------------------------------------

func TestPublicIPv4ProbeRejectsIPv6Answers(t *testing.T) {
	t.Parallel()

	// 回显服务返回 IPv6 是最容易发生的一种错误：端点同时有 A 与 AAAA 记录，
	// 而默认的拨号会先走 IPv6。这时拿到的是 IPv6 出口地址，
	// 写进 A 记录就是一条永远解析不出地址的记录。
	prober := &HTTPProber{}
	if _, err := prober.probe(context.Background(), httpClientReturning("2409:8a50::1"), "http://x/"); err == nil {
		t.Fatal("回显返回 IPv6 时应当报错")
	}
	if ip, err := prober.probe(context.Background(), httpClientReturning("120.227.48.210"), "http://x/"); err != nil {
		t.Fatalf("合法 IPv4 应当通过: %v", err)
	} else if ip.String() != "120.227.48.210" {
		t.Fatalf("解析结果不对: %s", ip)
	}
}

func TestPublicIPv4ProbeRejectsGarbage(t *testing.T) {
	t.Parallel()

	prober := &HTTPProber{}
	for _, body := range []string{"", "not an ip", "<html>captive portal</html>", "1.2.3.4.5"} {
		if _, err := prober.probe(context.Background(), httpClientReturning(body), "http://x/"); err == nil {
			t.Fatalf("垃圾响应应当报错: %q", body)
		}
	}
}

// httpClientReturning 构造一个总是返回固定响应体的客户端。
func httpClientReturning(body string) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     make(http.Header),
			}, nil
		}),
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// SyncPublic 的 IPv4 门禁
// ---------------------------------------------------------------------------

// 探测到公网 IPv4 **不等于**外面能连进来。
//
// 家用宽带上多数已经是大内网（CGNAT）：机器能看到一个公网出口地址，
// 但入站连接到不了它。写一条指向那个地址的 A 记录比不写更糟 ——
// 客户端默认先试 IPv4，于是它会先卡在一个永远连不上的地址上。
//
// 因此规则是：**只有实测过 IPv4 可达之后才写 A**。
func TestSyncPublicWritesIPv4OnlyAfterItWasVerifiedReachable(t *testing.T) {
	t.Parallel()

	newService := func(check *PublicCheck) (*Service, *fakeDNS) {
		fake := newFakeDNS(dns.Zone{ID: "z1", Name: "example.com"})
		face := NewPublicFace(t.TempDir(), fake, nil)
		if check != nil {
			if err := face.SetCheck(*check); err != nil {
				t.Fatalf("写入自检结论失败: %v", err)
			}
		}
		svc, err := New(Options{
			Dir:            t.TempDir(),
			PublicFace:     face,
			PublicSettings: staticPublicSettings{enabled: true},
			PublicZones:    staticZoneResolver{credentialID: "cred-1", zoneID: "z1", zoneName: "example.com"},
		})
		if err != nil {
			t.Fatalf("构造服务失败: %v", err)
		}
		// 探测到的公网 IPv4 是"看起来正常"的 —— 它的存在本身
		// 不构成任何证据。
		face.prober = staticProber{ip: net.ParseIP("120.227.48.210")}
		return svc, fake
	}

	// 没测过 → 只写 AAAA。
	svc, fake := newService(nil)
	if _, err := svc.SyncPublic(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	for _, rec := range fake.created {
		if rec.Type == dns.TypeA {
			t.Fatal("还没验证过 IPv4 可达就写了 A 记录 —— 客户端会先试一个连不上的地址")
		}
	}

	// 实测不可达 → 仍然不写。
	svc, fake = newService(&PublicCheck{Verdict: PublicVerdictUnreachable, Family: "ipv4"})
	if _, err := svc.SyncPublic(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	for _, rec := range fake.created {
		if rec.Type == dns.TypeA {
			t.Fatal("实测过 IPv4 不可达，却还是写了 A 记录")
		}
	}

	// 实测可达 → 写。
	svc, fake = newService(&PublicCheck{Verdict: PublicVerdictReachable, Family: "ipv4"})
	if _, err := svc.SyncPublic(context.Background()); err != nil {
		t.Fatalf("同步失败: %v", err)
	}
	var wroteA bool
	for _, rec := range fake.created {
		if rec.Type == dns.TypeA && rec.Content == "120.227.48.210" {
			wroteA = true
		}
	}
	if !wroteA {
		t.Fatal("实测 IPv4 可达之后应当写 A 记录")
	}
}

// 关闭状态下同步必须报错，而不是静默什么都不做。
func TestSyncPublicRefusesWhenDisabled(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	svc, err := New(Options{
		Dir:            t.TempDir(),
		PublicFace:     face,
		PublicSettings: staticPublicSettings{},
		PublicZones:    staticZoneResolver{credentialID: "cred-1", zoneID: "z1", zoneName: "example.com"},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := svc.SyncPublic(context.Background()); err == nil {
		t.Fatal("未开启时应当报错")
	}
}

// 反查失败时必须把错误原样抛出去，而不是继续往下走。
//
// 继续走的后果是在一个**不存在的区域**下建记录 —— 那不会报错，
// 用户只会看到"域名建好了但连不上"，而原因与症状隔了十万八千里。
func TestSyncPublicSurfacesResolverFailure(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	svc, err := New(Options{
		Dir:            t.TempDir(),
		PublicFace:     face,
		PublicSettings: staticPublicSettings{enabled: true},
		PublicZones:    staticZoneResolver{err: errors.New("找不到区域")},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := svc.SyncPublic(context.Background()); err == nil {
		t.Fatal("反查失败时应当报错")
	}
}

// 没有反查器时明确报错，而不是静默什么都不做。
//
// 静默的后果是"开了公网访问但什么都没建"，而用户在界面上
// 看不到任何异常。
func TestSyncPublicRefusesWithoutResolver(t *testing.T) {
	t.Parallel()

	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	svc, err := New(Options{
		Dir:            t.TempDir(),
		PublicFace:     face,
		PublicSettings: staticPublicSettings{enabled: true},
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := svc.SyncPublic(context.Background()); err == nil {
		t.Fatal("没有反查器时应当报错")
	}
}

// 域名归一：大小写与尾点在匹配里是不同的串。
func TestDomainNormalizationForMatching(t *testing.T) {
	t.Parallel()

	// ZoneFinder 内部会做小写化与去尾点；这里钉住的是
	// **设置层**也要做一次 —— 用户从别处复制来的域名常常带着它们。
	// 设置层的行为由 settings 包的测试覆盖，这里只断言
	// 反查器收到的域名是归一之后的。
	seen := &capturingResolver{}
	face := NewPublicFace(t.TempDir(), newFakeDNS(), nil)
	svc, err := New(Options{
		Dir:            t.TempDir(),
		PublicFace:     face,
		PublicSettings: staticPublicSettings{enabled: true, domain: "example.com"},
		PublicZones:    seen,
	})
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := svc.SyncPublic(context.Background()); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if seen.domain != "example.com" {
		t.Fatalf("反查器收到的域名是 %q", seen.domain)
	}
}

type capturingResolver struct{ domain string }

func (c *capturingResolver) Find(_ context.Context, domain string) (string, string, string, error) {
	c.domain = domain
	return "cred-1", "z1", "example.com", nil
}

// staticPublicSettings 是一个固定的设置来源。
type staticPublicSettings struct {
	enabled bool
	domain  string
}

func (s staticPublicSettings) PublicConfig() (bool, string) {
	domain := s.domain
	if domain == "" {
		domain = "example.com"
	}
	return s.enabled, domain
}

// staticZoneResolver 是一个固定的区域反查器。
//
// 它替掉的是"用户填两个 ID"那件事：现在内核拿到的只有域名，
// 凭据与区域由反查得出。
type staticZoneResolver struct {
	credentialID string
	zoneID       string
	zoneName     string
	err          error
}

func (r staticZoneResolver) Find(context.Context, string) (string, string, string, error) {
	if r.err != nil {
		return "", "", "", r.err
	}
	return r.credentialID, r.zoneID, r.zoneName, nil
}

// staticProber 返回一个固定的公网 IPv4。
type staticProber struct{ ip net.IP }

func (s staticProber) PublicIPv4(context.Context) (net.IP, error) { return s.ip, nil }
