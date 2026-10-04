package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/google/uuid"
	"github.com/oapi-codegen/runtime/types"
)

// fakePhecdaStore 只记录被写入的集合，用来验证处理器与存储端口之间的契约。
type fakePhecdaStore struct {
	services []gen.PublicService
	replaced [][]gen.PublicService
	err      error
}

func (f *fakePhecdaStore) ListProjects(context.Context) ([]gen.PhecdaProject, error) {
	return nil, nil
}
func (f *fakePhecdaStore) GetProject(context.Context, types.UUID) (gen.PhecdaProject, bool, error) {
	return gen.PhecdaProject{}, false, nil
}
func (f *fakePhecdaStore) SaveProject(context.Context, gen.PhecdaProject) error { return nil }
func (f *fakePhecdaStore) DeleteProject(context.Context, types.UUID) (bool, error) {
	return false, nil
}
func (f *fakePhecdaStore) SaveEvidence(context.Context, types.UUID, []gen.PhecdaScanEvidence) error {
	return nil
}
func (f *fakePhecdaStore) SaveDeployment(context.Context, gen.PhecdaDeployment) error { return nil }
func (f *fakePhecdaStore) ListDeployments(context.Context) ([]gen.PhecdaDeployment, error) {
	return nil, nil
}
func (f *fakePhecdaStore) GetDeployment(context.Context, types.UUID) (gen.PhecdaDeployment, bool, error) {
	return gen.PhecdaDeployment{}, false, nil
}
func (f *fakePhecdaStore) ListPublicServices(context.Context) ([]gen.PublicService, error) {
	return f.services, f.err
}
func (f *fakePhecdaStore) ReplacePublicServices(_ context.Context, services []gen.PublicService) error {
	f.replaced = append(f.replaced, services)
	return f.err
}

func newPublicServiceTestServer(store PhecdaStore) *Server {
	return &Server{Deps: Deps{Phecda: store, Log: slog.Default()}}
}

func putPublicServices(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/v1/public-services", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.ReplacePublicServices(rec, req)
	return rec
}

func TestReplacePublicServicesStoresTheCollection(t *testing.T) {
	store := &fakePhecdaStore{}
	id := uuid.New()
	rec := putPublicServices(t, newPublicServiceTestServer(store),
		`{"items":[{"id":"`+id.String()+`","name":"home","kind":"httpsForward","domains":["home.example.com"],"order":2,"favorite":true}]}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.replaced) != 1 || len(store.replaced[0]) != 1 {
		t.Fatalf("collection was not handed to the store: %#v", store.replaced)
	}
	got := store.replaced[0][0]
	if got.Id != id || got.Name != "home" || got.Kind != gen.HttpsForward || len(got.Domains) != 1 {
		t.Fatalf("stored service differs from the request: %#v", got)
	}
}

// 整体替换是唯一写入口，因此这里必须是"要么整份生效，要么整份拒绝"。
func TestReplacePublicServicesRejectsInconsistentCollections(t *testing.T) {
	shared := uuid.New()
	cases := map[string]string{
		"missing id":       `{"items":[{"id":"00000000-0000-0000-0000-000000000000","name":"x","kind":"dynamicDomain","domains":[]}]}`,
		"missing name":     `{"items":[{"id":"` + uuid.New().String() + `","name":"  ","kind":"dynamicDomain","domains":[]}]}`,
		"unknown kind":     `{"items":[{"id":"` + uuid.New().String() + `","name":"x","kind":"carrier-pigeon","domains":[]}]}`,
		"duplicate id":     `{"items":[{"id":"` + shared.String() + `","name":"a","kind":"dynamicDomain","domains":[]},{"id":"` + shared.String() + `","name":"b","kind":"dynamicDomain","domains":[]}]}`,
		"duplicate domain": `{"items":[{"id":"` + uuid.New().String() + `","name":"a","kind":"dynamicDomain","domains":["same.example.com"]},{"id":"` + uuid.New().String() + `","name":"b","kind":"dynamicDomain","domains":["same.example.com"]}]}`,
		"malformed json":   `{"items":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakePhecdaStore{}
			rec := putPublicServices(t, newPublicServiceTestServer(store), body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if len(store.replaced) != 0 {
				t.Fatalf("a rejected collection must not reach the store: %#v", store.replaced)
			}
			var problem gen.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not problem+json: %v", err)
			}
			if problem.Code == nil || *problem.Code != CodeInvalidRequest {
				t.Fatalf("expected the stable %q code, got %#v", CodeInvalidRequest, problem.Code)
			}
		})
	}
}

// 空集合是合法输入：删掉最后一个已发布服务就是它。
func TestReplacePublicServicesAcceptsAnEmptyCollection(t *testing.T) {
	store := &fakePhecdaStore{}
	rec := putPublicServices(t, newPublicServiceTestServer(store), `{"items":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.replaced) != 1 || len(store.replaced[0]) != 0 {
		t.Fatalf("empty collection was not propagated: %#v", store.replaced)
	}
}

func TestListPublicServicesReturnsAnEmptyListNotAnError(t *testing.T) {
	rec := httptest.NewRecorder()
	newPublicServiceTestServer(&fakePhecdaStore{}).ListPublicServices(rec, httptest.NewRequest(http.MethodGet, "/v1/public-services", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected an empty items array, got %s", rec.Body.String())
	}

	// 没有配置存储时也要给出同一形状的答案，而不是 500。
	rec = httptest.NewRecorder()
	newPublicServiceTestServer(nil).ListPublicServices(rec, httptest.NewRequest(http.MethodGet, "/v1/public-services", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected an empty list without a store, got %d %s", rec.Code, rec.Body.String())
	}
}
