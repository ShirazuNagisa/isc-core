package dns

import (
	"context"
	"errors"
	"testing"
)

// 按域名反查凭据。
//
// # 这里最容易错的是什么
//
// 不是"找不到"，而是**找错了**。挑错凭据的症状离得很远：设置页上一切
// 正常，几天后证书该续期时才失败，而错误说的是"在区域里找不到那条
// TXT 记录" —— 用户完全想不到是凭据选错了。

// fakeFinderService 是一个只需要回答两个问题的 dns.Service 替身。
type fakeFinderService struct {
	zones     map[string][]Zone
	supports  map[string]bool
	providers map[string]string
}

func (f *fakeFinderService) SupportsCreate(_ context.Context, id string) bool {
	return f.supports[id]
}

func (f *fakeFinderService) Credential(_ context.Context, id string) (Credential, error) {
	return Credential{ID: id, Provider: f.providers[id]}, nil
}

func (f *fakeFinderService) ListZones(_ context.Context, id string) ([]Zone, error) {
	zones, ok := f.zones[id]
	if !ok {
		return nil, errors.New("no such credential")
	}
	return zones, nil
}

// newTestFinder 构造一个把 fake 包成 *Service 的反查器。
//
// 这里不能直接构造 *Service（它需要凭据解析器与实现查找表），
// 因此反查器要能接受一个更窄的接口。见 finder.go 里 zoneSource 的说明。
func newTestFinder(f *fakeFinderService, ids []string) *ZoneFinder {
	return newZoneFinderForTest(f, func(context.Context) ([]string, error) { return ids, nil })
}

func TestFindPicksTheLongestMatchingZone(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports: map[string]bool{"c1": true, "c2": true},
		zones: map[string][]Zone{
			"c1": {{ID: "z-com", Name: "com"}},
			"c2": {{ID: "z-example", Name: "example.com"}, {ID: "z-other", Name: "other.net"}},
		},
	}
	finder := newTestFinder(fake, []string{"c1", "c2"})

	// 最长后缀匹配：属于 example.com，不是 com。
	id, zone, err := finder.Find(context.Background(), "blog.example.com")
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if id != "c2" || zone.ID != "z-example" {
		t.Fatalf("选成了 %s / %s，期望 c2 / z-example", id, zone.ID)
	}
}

// **标签边界**：`notexample.com` 以 `example.com` 结尾，但它完全不属于
// 那个区域。
//
// 只判 HasSuffix 的实现会把它匹配到 example.com 那把凭据上，
// 然后去写一个自己管不着的域名 —— 失败信息还是"区域里找不到记录"。
func TestFindRespectsLabelBoundaries(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports: map[string]bool{"c1": true},
		zones:    map[string][]Zone{"c1": {{ID: "z1", Name: "example.com"}}},
	}
	finder := newTestFinder(fake, []string{"c1"})

	for _, domain := range []string{"notexample.com", "anexample.com", "xexample.com"} {
		if id, zone, err := finder.Find(context.Background(), domain); err == nil {
			t.Fatalf("%s 不该匹配到 %s（选成了 %s / %s）—— 它不属于那个区域",
				domain, "example.com", id, zone.ID)
		}
	}
	// 而真正属于它的名字要能匹配上。
	for _, domain := range []string{"example.com", "blog.example.com", "a.b.example.com"} {
		if _, _, err := finder.Find(context.Background(), domain); err != nil {
			t.Fatalf("%s 应当匹配到 example.com: %v", domain, err)
		}
	}
}

// 不支持新建记录的凭据必须被跳过。
//
// 23 家内置服务商里只有 6 家实现了 RecordCreator。选中其余的症状是
// 一句"该服务商不支持此操作"，而用户不知道该换谁。
func TestFindSkipsCredentialsThatCannotCreateRecords(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		// c1 只支持改记录，不支持建记录 —— DNS-01 需要建 TXT。
		supports: map[string]bool{"c1": false, "c2": true},
		zones: map[string][]Zone{
			"c1": {{ID: "z1", Name: "example.com"}},
			"c2": {{ID: "z2", Name: "example.com"}},
		},
	}
	finder := newTestFinder(fake, []string{"c1", "c2"})

	id, zone, err := finder.Find(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if id != "c2" || zone.ID != "z2" {
		t.Fatalf("选中了不能建记录的凭据 %s", id)
	}
}

// 结果不能依赖凭据的返回顺序。
//
// 两个账号下同名区域是常见的（迁移期间），而顺序不定的实现会让同一个
// 域名在两次运行里选到不同的凭据 —— 那种抖动的症状是"证书有时能续、
// 有时不能"，极难查。
func TestFindIsDeterministicAcrossCredentialOrder(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports: map[string]bool{"aaa": true, "zzz": true},
		zones: map[string][]Zone{
			"aaa": {{ID: "z-a", Name: "example.com"}},
			"zzz": {{ID: "z-z", Name: "example.com"}},
		},
	}
	for _, ids := range [][]string{{"zzz", "aaa"}, {"aaa", "zzz"}} {
		finder := newTestFinder(fake, ids)
		id, _, err := finder.Find(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("失败: %v", err)
		}
		if id != "aaa" {
			t.Fatalf("凭据顺序影响了结果：%s", id)
		}
	}
}

// 单把凭据失败不该让整次查找失败。
//
// 密钥过期、服务商抖动都会让 ListZones 报错，而那时只要还有别的
// 凭据能匹配，就应该用它。
func TestFindToleratesOneFailingCredential(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports: map[string]bool{"bad": true, "good": true},
		zones:    map[string][]Zone{"good": {{ID: "z-good", Name: "example.com"}}},
		// "bad" 不在 zones 里 → ListZones 报错
	}
	finder := newTestFinder(fake, []string{"bad", "good"})

	id, zone, err := finder.Find(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("一把凭据失败不该让整次查找失败: %v", err)
	}
	if id != "good" || zone.ID != "z-good" {
		t.Fatalf("选中了 %s / %s", id, zone.ID)
	}
}

// 找不到时必须报错并说清是哪个域名，而不是返回一个空 ID。
//
// 空 ID 会一路走到"用一把不存在的凭据去写记录"，那时的错误信息
// 与真正的问题（域名不在任何区域下）毫无关系。
func TestFindExplainsMissingZone(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports: map[string]bool{"c1": true},
		zones:    map[string][]Zone{"c1": {{ID: "z1", Name: "example.com"}}},
	}
	finder := newTestFinder(fake, []string{"c1"})

	id, _, err := finder.Find(context.Background(), "somewhere-else.net")
	if err == nil {
		t.Fatalf("应当报错，却给出了凭据 %q", id)
	}
	if !contains(err.Error(), "somewhere-else.net") {
		t.Fatalf("错误信息里必须带上域名，实际: %v", err)
	}
}

func TestFindRejectsEmptyDomain(t *testing.T) {
	t.Parallel()

	finder := newTestFinder(&fakeFinderService{}, []string{"c1"})
	for _, domain := range []string{"", "   ", "."} {
		if _, _, err := finder.Find(context.Background(), domain); err == nil {
			t.Fatalf("%q 应当被拒绝", domain)
		}
	}
}

// 缓存要真的生效 —— 每次查找都打服务商 API 会让"给一批域名签证书"
// 变成一串重复的列表请求。
func TestFindCachesZoneLists(t *testing.T) {
	t.Parallel()

	calls := 0
	fake := &countingFinderService{inner: &fakeFinderService{
		supports: map[string]bool{"c1": true},
		zones:    map[string][]Zone{"c1": {{ID: "z1", Name: "example.com"}}},
	}, calls: &calls}
	finder := newZoneFinderForTest(fake, func(context.Context) ([]string, error) {
		return []string{"c1"}, nil
	})

	for i := 0; i < 5; i++ {
		if _, _, err := finder.Find(context.Background(), "a.example.com"); err != nil {
			t.Fatalf("失败: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("ListZones 被调了 %d 次，期望 1（第二次起应当走缓存）", calls)
	}

	// 手动失效之后必须重新查 —— 用户刚建的区域要能立刻用上。
	finder.Invalidate()
	if _, _, err := finder.Find(context.Background(), "a.example.com"); err != nil {
		t.Fatalf("失败: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Invalidate 之后 ListZones 被调了 %d 次，期望 2", calls)
	}
}

// 没有凭据列表时明确报错，而不是说"找不到区域"。
//
// 两者的原因完全不同：前者是装配问题，后者是用户还没配 DNS。
func TestFindWithoutCredentialListFailsLoudly(t *testing.T) {
	t.Parallel()

	finder := newZoneFinderForTest(&fakeFinderService{}, nil)
	if _, _, err := finder.Find(context.Background(), "example.com"); err == nil {
		t.Fatal("没有凭据列表时应当报错")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

type countingFinderService struct {
	inner *fakeFinderService
	calls *int
}

func (c *countingFinderService) Credential(ctx context.Context, id string) (Credential, error) {
	return c.inner.Credential(ctx, id)
}

func (c *countingFinderService) SupportsCreate(ctx context.Context, id string) bool {
	return c.inner.SupportsCreate(ctx, id)
}

func (c *countingFinderService) ListZones(ctx context.Context, id string) ([]Zone, error) {
	*c.calls++
	return c.inner.ListZones(ctx, id)
}

// List 供界面把"挂在哪个域名下"做成列表而不是输入框。
//
// 用户不需要记住自己的区域名，更不该把它打错 —— 打错的后果是
// 找不到区域，或者更糟：在一个**同名但不同账号**的区域下建记录。
func TestListReturnsDomainsWithTheirProvider(t *testing.T) {
	t.Parallel()

	fake := &fakeFinderService{
		supports:  map[string]bool{"c1": true, "c2": true, "c3": false},
		providers: map[string]string{"c1": "cloudflare", "c2": "dnspod", "c3": "godaddy"},
		zones: map[string][]Zone{
			"c1": {{ID: "z1", Name: "example.com"}, {ID: "z2", Name: "other.net."}},
			"c2": {{ID: "z3", Name: "third.org"}},
			// c3 不支持建记录，它下面的区域不该出现在候选里。
			"c3": {{ID: "z4", Name: "legacy.com"}},
		},
	}
	finder := newTestFinder(fake, []string{"c1", "c2", "c3"})

	list, err := finder.List(context.Background())
	if err != nil {
		t.Fatalf("失败: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("候选数 = %d，期望 3（不支持建记录的凭据要排除）", len(list))
	}

	byDomain := map[string]Candidate{}
	for _, c := range list {
		byDomain[c.Domain] = c
	}
	// 尾点要去掉：`other.net.` 与 `other.net` 在匹配里是不同的串。
	if _, ok := byDomain["other.net"]; !ok {
		t.Fatalf("尾点没有被去掉: %+v", list)
	}
	if byDomain["example.com"].Provider != "cloudflare" {
		t.Fatalf("服务商名不对: %+v", byDomain["example.com"])
	}
	if _, ok := byDomain["legacy.com"]; ok {
		t.Fatal("不支持新建记录的凭据下的区域不该出现在候选里")
	}
}

// 查不动 ≠ 找不到。
//
// # 两者用户该做的事完全不同
//
//   - "找不到区域" → 去确认域名加到了哪个账号下；
//   - "查不动"     → 稍后重试，或检查凭据是不是失效了。
//
// 把后者报成前者会让人去翻一个**根本不存在的配置问题**。
// 这不是理论担忧：真实环境里它由一次网络抖动触发，而错误信息
// 说的是"请确认这个域名已经加到某把凭据的账号下"。
func TestFindDistinguishesQueryFailureFromMissingZone(t *testing.T) {
	t.Parallel()

	// 凭据都不在 zones 表里 → ListZones 报错 → 一条都查不到。
	failing := &fakeFinderService{
		supports: map[string]bool{"c1": true, "c2": true},
		zones:    map[string][]Zone{},
	}
	finder := newTestFinder(failing, []string{"c1", "c2"})

	id, _, err := finder.Find(context.Background(), "example.com")
	if err == nil {
		t.Fatalf("应当报错，却给出了凭据 %q", id)
	}
	msg := err.Error()
	if contains(msg, "找不到") && contains(msg, "所属的 DNS 区域") {
		t.Fatalf("把「查不动」报成了「找不到区域」 —— 那会把用户指向一个不存在的配置问题: %v", err)
	}
	if !contains(msg, "凭据") {
		t.Fatalf("错误信息应当提到凭据，实际: %v", err)
	}
}

// 反过来：凭据都正常、只是这个域名不归它们管 → 报"找不到"。
func TestFindReportsMissingZoneWhenCredentialsAreFine(t *testing.T) {
	t.Parallel()

	ok := &fakeFinderService{
		supports: map[string]bool{"c1": true},
		zones:    map[string][]Zone{"c1": {{ID: "z1", Name: "other.net"}}},
	}
	finder := newTestFinder(ok, []string{"c1"})

	_, _, err := finder.Find(context.Background(), "example.com")
	if err == nil {
		t.Fatal("应当报错")
	}
	if !contains(err.Error(), "example.com") {
		t.Fatalf("错误信息里应当带上域名，实际: %v", err)
	}
}
