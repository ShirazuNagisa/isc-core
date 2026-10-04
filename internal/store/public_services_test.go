package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
)

// openPhecdaTestStore 打开一个临时库，并登记一个项目 —— 部署有外键指向它。
func openPhecdaTestStore(t *testing.T) (*Store, gen.PhecdaProject) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "isc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var source gen.PhecdaProjectSource
	if err := source.FromPhecdaNonDockerSource(gen.PhecdaNonDockerSource{Mode: gen.Directory, Value: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	project := gen.PhecdaProject{Id: uuid.New(), Name: "demo", Purpose: "website", Source: source, Evidence: []gen.PhecdaScanEvidence{}}
	if err := s.Phecda().SaveProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	return s, project
}

func publicService(id uuid.UUID, name string, order int) gen.PublicService {
	favorite := true
	return gen.PublicService{
		Id:       id,
		Name:     name,
		Kind:     gen.DynamicDomain,
		Domains:  []string{name + ".example.com"},
		Favorite: &favorite,
		Order:    &order,
	}
}

func TestPublicServicesRoundTripKeepsEveryField(t *testing.T) {
	ctx := context.Background()
	s, _ := openPhecdaTestStore(t)

	verified := time.Now().UTC().Round(0)
	ddns, route, fingerprint := "ddns-1", "route-1", "fingerprint-1"
	want := gen.PublicService{
		Id:                  uuid.New(),
		Name:                "家庭媒体",
		Kind:                gen.HttpsForward,
		Domains:             []string{"home.example.com", "media.example.com"},
		DdnsId:              &ddns,
		RouteId:             &route,
		Favorite:            boolPtr(true),
		Order:               intPtr(3),
		VerifiedAt:          &verified,
		VerifiedFingerprint: &fingerprint,
	}
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{want}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Phecda().ListPublicServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected one service, got %d", len(got))
	}
	service := got[0]
	if service.Id != want.Id || service.Name != want.Name || service.Kind != want.Kind {
		t.Fatalf("identity fields not preserved: %#v", service)
	}
	if len(service.Domains) != 2 || service.Domains[0] != "home.example.com" || service.Domains[1] != "media.example.com" {
		t.Fatalf("domains not preserved in order: %#v", service.Domains)
	}
	if service.DdnsId == nil || *service.DdnsId != ddns || service.RouteId == nil || *service.RouteId != route {
		t.Fatalf("references not preserved: %#v", service)
	}
	if service.Favorite == nil || !*service.Favorite || service.Order == nil || *service.Order != 3 {
		t.Fatalf("organization fields not preserved: %#v", service)
	}
	if service.VerifiedAt == nil || !service.VerifiedAt.Equal(verified) {
		t.Fatalf("verified_at not preserved: %#v", service.VerifiedAt)
	}
	if service.VerifiedFingerprint == nil || *service.VerifiedFingerprint != fingerprint {
		t.Fatalf("fingerprint not preserved: %#v", service.VerifiedFingerprint)
	}
}

func TestPublicServicesListIsOrderedAndComplete(t *testing.T) {
	ctx := context.Background()
	s, _ := openPhecdaTestStore(t)

	first, second := publicService(uuid.New(), "second", 5), publicService(uuid.New(), "first", 1)
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{first, second}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Phecda().ListPublicServices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Id != second.Id || got[1].Id != first.Id {
		t.Fatalf("expected user order (sort_order 1 then 5), got %#v", got)
	}
	// Unset optionals must still be reported, so callers never have to distinguish
	// "absent" from "false"/"0".
	for _, service := range got {
		if service.Favorite == nil || service.Order == nil {
			t.Fatalf("favorite/order must always be present: %#v", service)
		}
	}
}

// 这是把记录搬进内核的**根本理由**：部署上的引用不可能悬空。
func TestReplacingPublicServicesClearsStaleDeploymentBindings(t *testing.T) {
	ctx := context.Background()
	s, project := openPhecdaTestStore(t)

	kept, dropped := uuid.New(), uuid.New()
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{
		publicService(kept, "kept", 0), publicService(dropped, "dropped", 1),
	}); err != nil {
		t.Fatal(err)
	}

	bound := uuid.New()
	keptDeployment := gen.PhecdaDeployment{Id: bound, ProjectId: project.Id, PresetId: "node-auto", State: gen.PhecdaDeploymentStateRunning, PublicServiceId: &kept}
	orphanDeployment := gen.PhecdaDeployment{Id: uuid.New(), ProjectId: project.Id, PresetId: "node-auto", State: gen.PhecdaDeploymentStateRunning, PublicServiceId: &dropped}
	for _, d := range []gen.PhecdaDeployment{keptDeployment, orphanDeployment} {
		if err := s.Phecda().SaveDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	// 替换集合，只留下 kept。
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{publicService(kept, "kept", 0)}); err != nil {
		t.Fatal(err)
	}

	stillBound, ok, err := s.Phecda().GetDeployment(ctx, keptDeployment.Id)
	if err != nil || !ok {
		t.Fatalf("get kept deployment: ok=%v err=%v", ok, err)
	}
	if stillBound.PublicServiceId == nil || *stillBound.PublicServiceId != kept {
		t.Fatalf("binding to a surviving service must not be cleared: %#v", stillBound.PublicServiceId)
	}

	cleared, ok, err := s.Phecda().GetDeployment(ctx, orphanDeployment.Id)
	if err != nil || !ok {
		t.Fatalf("get orphan deployment: ok=%v err=%v", ok, err)
	}
	if cleared.PublicServiceId != nil {
		t.Fatalf("binding to a removed service must be cleared, got %s", *cleared.PublicServiceId)
	}
}

// Supervisor 的进度循环每几百毫秒重写一次部署状态；那些写入不能把绑定抹掉。
func TestSaveDeploymentPreservesBindingWhenOmittedAndUpdatesWhenGiven(t *testing.T) {
	ctx := context.Background()
	s, project := openPhecdaTestStore(t)

	service := uuid.New()
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{publicService(service, "svc", 0)}); err != nil {
		t.Fatal(err)
	}

	id := uuid.New()
	state := gen.PhecdaDeploymentStatePreparing
	if err := s.Phecda().SaveDeployment(ctx, gen.PhecdaDeployment{Id: id, ProjectId: project.Id, PresetId: "node-auto", State: state, PublicServiceId: &service}); err != nil {
		t.Fatal(err)
	}

	// 一次不带 public_service_id 的状态更新（进度上报）。
	if err := s.Phecda().SaveDeployment(ctx, gen.PhecdaDeployment{Id: id, ProjectId: project.Id, PresetId: "node-auto", State: gen.PhecdaDeploymentStateRunning}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Phecda().GetDeployment(ctx, id)
	if err != nil || !ok {
		t.Fatalf("get deployment: ok=%v err=%v", ok, err)
	}
	if got.PublicServiceId == nil || *got.PublicServiceId != service {
		t.Fatalf("a state update must not unbind the service: %#v", got.PublicServiceId)
	}
	if got.State != gen.PhecdaDeploymentStateRunning {
		t.Fatalf("state update must still apply, got %q", got.State)
	}

	// 显式给出新的绑定则应当生效。
	other := uuid.New()
	if err := s.Phecda().ReplacePublicServices(ctx, []gen.PublicService{publicService(service, "svc", 0), publicService(other, "svc2", 1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Phecda().SaveDeployment(ctx, gen.PhecdaDeployment{Id: id, ProjectId: project.Id, PresetId: "node-auto", State: gen.PhecdaDeploymentStateRunning, PublicServiceId: &other}); err != nil {
		t.Fatal(err)
	}
	got, _, err = s.Phecda().GetDeployment(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicServiceId == nil || *got.PublicServiceId != other {
		t.Fatalf("an explicit binding must be stored: %#v", got.PublicServiceId)
	}
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }
