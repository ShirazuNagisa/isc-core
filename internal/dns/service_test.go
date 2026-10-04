package dns

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件守的是一个真实踩过的坑：区域内既有 ID 也有名称，而**不同服务商
// 需要的不是同一个**。Cloudflare 的 URL 里要区域 ID，阿里云和 DNSPod
// 要域名。早先 zoneOf 只是把路径参数原样塞进 Zone.ID，于是：
//
//   - Cloudflare 收到区域名当 ID → 404 "object identifier is invalid"；
//   - 阿里云 / DNSPod 收到空的 Zone.Name → 拿空域名去请求。
//
// 这个用例把"区域标识必须被解析成完整的 Zone"钉住。

// fakeZoneLister 是一个能列区域、也能列记录的假实现。
type fakeZoneLister struct {
	zones []Zone
	// gotZone 记录 ListRecords 实际收到的 Zone —— 断言点就在这里。
	gotZone  Zone
	gotCalls int
}

func (f *fakeZoneLister) Meta() Meta { return Meta{Name: "fake", DisplayName: "Fake", Tier: 1} }

func (f *fakeZoneLister) ListZones(context.Context, Credential) ([]Zone, error) {
	return f.zones, nil
}

func (f *fakeZoneLister) ListRecords(_ context.Context, _ Credential, zone Zone,
	_ RecordFilter) ([]Record, error) {
	f.gotZone = zone
	f.gotCalls++
	return nil, nil
}

type staticCreds struct{ c Credential }

func (s staticCreds) Resolve(context.Context, string) (Credential, error) { return s.c, nil }

func newTestService(lister *fakeZoneLister) *Service {
	return NewService(staticCreds{c: Credential{
		ID: "cred-1", Provider: "cloudflare", Fields: map[string]string{"token": "t"},
	}}, func(string) (Provider, bool) { return lister, true })
}

var testZones = []Zone{
	{ID: "023e105f4ecef8ad9ca31a8372d0c353", Name: "example.com"},
	{ID: "b1f2c3d4e5a6b7c8d9e0f1a2b3c4d5e6", Name: "shirazu-nagisa.com"},
}

func TestZoneOfResolvesZoneID(t *testing.T) {
	lister := &fakeZoneLister{zones: testZones}
	svc := newTestService(lister)

	if _, err := svc.ListRecords(context.Background(), "cred-1", testZones[0].ID, RecordFilter{}); err != nil {
		t.Fatalf("按 ID 查记录失败: %v", err)
	}
	if lister.gotZone.ID != testZones[0].ID || lister.gotZone.Name != testZones[0].Name {
		t.Fatalf("解析出的区域不完整: %+v", lister.gotZone)
	}
}

// 这条是本次故障的直接回归用例：调用方给的是**区域名**。
func TestZoneOfAcceptsZoneName(t *testing.T) {
	lister := &fakeZoneLister{zones: testZones}
	svc := newTestService(lister)

	if _, err := svc.ListRecords(context.Background(), "cred-1", "shirazu-nagisa.com", RecordFilter{}); err != nil {
		t.Fatalf("按名称查记录失败: %v", err)
	}
	// 关键：交给服务商的必须是带 ID 的完整区域，而不是那个名字。
	if lister.gotZone.ID != testZones[1].ID {
		t.Fatalf("区域 ID 没有被解析出来: %+v", lister.gotZone)
	}
	if lister.gotZone.Name != "shirazu-nagisa.com" {
		t.Fatalf("区域名丢了（阿里云 / DNSPod 会拿空域名去请求）: %+v", lister.gotZone)
	}
}

func TestZoneOfIsCaseInsensitiveOnName(t *testing.T) {
	lister := &fakeZoneLister{zones: testZones}
	svc := newTestService(lister)

	if _, err := svc.ListRecords(context.Background(), "cred-1", "Example.COM", RecordFilter{}); err != nil {
		t.Fatalf("大小写不同的区域名应当能匹配: %v", err)
	}
	if lister.gotZone.ID != testZones[0].ID {
		t.Fatalf("解析结果不对: %+v", lister.gotZone)
	}
}

func TestZoneOfReportsUnknownZoneClearly(t *testing.T) {
	lister := &fakeZoneLister{zones: testZones}
	svc := newTestService(lister)

	_, err := svc.ListRecords(context.Background(), "cred-1", "nope.example", RecordFilter{})
	if err == nil {
		t.Fatal("不存在的区域应当报错")
	}
	// 错误里要点名那个区域。冒泡一条服务商的 "object identifier is invalid"
	// 对用户毫无意义 —— 那正是这次让用户卡住的原因。
	if !strings.Contains(err.Error(), "nope.example") {
		t.Fatalf("错误信息里没有点名区域: %v", err)
	}
	if lister.gotCalls != 0 {
		t.Fatal("区域没解析出来就不该去调服务商")
	}
}

func TestZoneOfRejectsEmptyIdentifier(t *testing.T) {
	svc := newTestService(&fakeZoneLister{zones: testZones})
	_, err := svc.ListRecords(context.Background(), "cred-1", "", RecordFilter{})
	if err == nil {
		t.Fatal("空区域标识应当报错")
	}
	if err.Error() != i18n.T("dns.err.no_zone_id") {
		t.Fatalf("错误信息不对: %v", err)
	}
}

// 列区域失败时要原样上报，不能退化成"找不到区域"——
// 那是两件不同的事：一个是网络/权限问题，一个是用户填错了。
func TestZoneOfPropagatesListFailure(t *testing.T) {
	svc := NewService(staticCreds{c: Credential{ID: "c", Provider: "p"}},
		func(string) (Provider, bool) { return failingLister{}, true })

	_, err := svc.ListRecords(context.Background(), "c", "example.com", RecordFilter{})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("应当原样上报列区域失败: %v", err)
	}
}

type failingLister struct{}

func (failingLister) Meta() Meta { return Meta{Name: "failing", DisplayName: "Failing", Tier: 1} }

func (failingLister) ListZones(context.Context, Credential) ([]Zone, error) {
	return nil, errors.New("boom")
}

func (failingLister) ListRecords(context.Context, Credential, Zone, RecordFilter) ([]Record, error) {
	return nil, errors.New("不该走到这里")
}
