package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/apps"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	"github.com/ShirazuNagisa/isc-core/internal/platform"
)

// appMemoryStore 是 API 测试用的应用仓储。
type appMemoryStore struct {
	mu   sync.Mutex
	apps map[string]apps.App
}

func newAppMemoryStore() *appMemoryStore { return &appMemoryStore{apps: map[string]apps.App{}} }

func (s *appMemoryStore) SaveApp(_ context.Context, app apps.App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[app.ID] = app
	return nil
}

func (s *appMemoryStore) GetApp(_ context.Context, id string) (apps.App, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, ok := s.apps[id]
	return app, ok, nil
}

func (s *appMemoryStore) ListApps(context.Context) ([]apps.App, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]apps.App, 0, len(s.apps))
	for _, app := range s.apps {
		out = append(out, app)
	}
	return out, nil
}

func (s *appMemoryStore) DeleteApp(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.apps[id]
	delete(s.apps, id)
	return ok, nil
}

// noopProcesses 让测试不依赖机器上装了什么命令。
type noopProcesses struct{}

func (noopProcesses) Start(platform.ProcessSpec) (platform.Process, error) {
	return nil, errors.New("processes are not available in this test")
}
func (noopProcesses) Describe() platform.ImplState {
	return platform.ImplState{Available: true, Backend: "noop"}
}

func newAppsServer(t *testing.T) *Server {
	t.Helper()
	bus := event.NewBus(64)
	t.Cleanup(bus.Close)
	jobs := job.NewEngine(t.Context(), bus, job.NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = jobs.Shutdown(t.Context()) })

	manager := apps.NewManager(apps.Deps{
		Store:     newAppMemoryStore(),
		Logs:      apps.NewLogStore(filepath.Join(t.TempDir(), "logs")),
		Processes: noopProcesses{},
		Bus:       bus,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		DataRoot:  t.TempDir(),
	})
	return &Server{Deps: Deps{Log: slog.Default(), Bus: bus, Jobs: jobs, Apps: manager}}
}

func staticSiteDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func callApp(t *testing.T, s *Server, method, path, body string, handle func(*Server, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	handle(s, rec, req)
	return rec
}

func TestListAppsWithoutHostingReturnsAnEmptyList(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	rec := callApp(t, s, http.MethodGet, "/v1/apps", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.ListApps(w, r)
	})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected an empty list, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCreateAppRegistersAStaticSite(t *testing.T) {
	s := newAppsServer(t)
	site := staticSiteDir(t)
	body := `{"name":"我的站点","preset_id":"static-html","source_path":` + jsonString(site) + `,"domains":["Home.Example.com","home.example.com"]}`
	rec := callApp(t, s, http.MethodPost, "/v1/apps", body, func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.CreateApp(w, r)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created gen.App
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Id == "" {
		t.Fatalf("the created app must have an id")
	}
	if created.State != gen.AppStateDraft {
		t.Fatalf("a freshly registered app is draft, got %s", created.State)
	}
	if created.LocalPort < 1024 {
		t.Fatalf("a port should have been allocated, got %d", created.LocalPort)
	}
	// 域名要归一化：去空白、小写、去重。
	if created.Domains == nil || len(*created.Domains) != 1 {
		t.Fatalf("domains should be normalised and deduplicated, got %v", created.Domains)
	}
	if (*created.Domains)[0].Name != "home.example.com" {
		t.Fatalf("domains should be lower-cased, got %q", (*created.Domains)[0].Name)
	}
	// 公网侧还没绑定，因此每一条都应当如实报 route_ready=false。
	if ready := (*created.Domains)[0].RouteReady; ready == nil || *ready {
		t.Fatalf("nothing has been bound yet, so route_ready must be false")
	}
}

func TestCreateAppRejectsIncompleteInput(t *testing.T) {
	s := newAppsServer(t)
	cases := map[string]string{
		"missing name":        `{"preset_id":"static-html","source_path":"/tmp"}`,
		"missing preset":      `{"name":"x","source_path":"/tmp"}`,
		"missing source":      `{"name":"x","preset_id":"static-html"}`,
		"bad json":            `{"name":`,
		"unknown preset":      `{"name":"x","preset_id":"nope","source_path":"/tmp"}`,
		"missing source path": `{"name":"x","preset_id":"static-html","source_path":"/nonexistent/isc-api-test"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := callApp(t, s, http.MethodPost, "/v1/apps", body, func(s *Server, w http.ResponseWriter, r *http.Request) {
				s.CreateApp(w, r)
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetAndDeleteApp(t *testing.T) {
	s := newAppsServer(t)
	created := createStaticApp(t, s)

	rec := callApp(t, s, http.MethodGet, "/v1/apps/"+created.Id, "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.GetApp(w, r, created.Id)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	// 不存在的 id 是 404，而不是 500。
	rec = callApp(t, s, http.MethodGet, "/v1/apps/nope", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.GetApp(w, r, "nope")
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	rec = callApp(t, s, http.MethodDelete, "/v1/apps/"+created.Id, "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.DeleteApp(w, r, created.Id)
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	rec = callApp(t, s, http.MethodDelete, "/v1/apps/"+created.Id, "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.DeleteApp(w, r, created.Id)
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleting twice should be 404, got %d", rec.Code)
	}
}

// 部署是长操作，接口返回 202 与任务 id，而不是阻塞。
func TestDeployAppSubmitsAJob(t *testing.T) {
	s := newAppsServer(t)
	created := createStaticApp(t, s)
	rec := callApp(t, s, http.MethodPost, "/v1/apps/"+created.Id+"/deploy", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.DeployApp(w, r, created.Id)
	})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var accepted gen.JobAccepted
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.JobId == "" {
		t.Fatalf("202 must carry a job id")
	}
}

func TestAppOperationsOnAMissingAppReturn404(t *testing.T) {
	s := newAppsServer(t)
	cases := map[string]func(*Server, http.ResponseWriter, *http.Request){
		"deploy": func(s *Server, w http.ResponseWriter, r *http.Request) { s.DeployApp(w, r, "nope") },
		"start":  func(s *Server, w http.ResponseWriter, r *http.Request) { s.StartApp(w, r, "nope") },
		"stop":   func(s *Server, w http.ResponseWriter, r *http.Request) { s.StopApp(w, r, "nope") },
		"restart": func(s *Server, w http.ResponseWriter, r *http.Request) {
			s.RestartApp(w, r, "nope")
		},
		"logs": func(s *Server, w http.ResponseWriter, r *http.Request) {
			s.GetAppLogs(w, r, "nope", gen.GetAppLogsParams{})
		},
	}
	for name, handle := range cases {
		t.Run(name, func(t *testing.T) {
			rec := callApp(t, s, http.MethodPost, "/v1/apps/nope", "", handle)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGetAppLogsReturnsAnArray(t *testing.T) {
	s := newAppsServer(t)
	created := createStaticApp(t, s)
	rec := callApp(t, s, http.MethodGet, "/v1/apps/"+created.Id+"/logs", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.GetAppLogs(w, r, created.Id, gen.GetAppLogsParams{})
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	// 没有日志时也要是 []，而不是 null —— 按数组解析的客户端不会因此破功。
	if !strings.Contains(rec.Body.String(), `"lines":[]`) {
		t.Fatalf("expected an empty array, got %s", rec.Body.String())
	}
}

// 没有托管能力时，写操作要明确报错，而不是静默什么都不做。
func TestAppWritesReportUnavailableHosting(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	rec := callApp(t, s, http.MethodPost, "/v1/apps", `{"name":"x","preset_id":"static-html","source_path":"/tmp"}`,
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.CreateApp(w, r) })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	rec = callApp(t, s, http.MethodDelete, "/v1/apps/x", "",
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.DeleteApp(w, r, "x") })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func createStaticApp(t *testing.T, s *Server) gen.App {
	t.Helper()
	body := `{"name":"站点","preset_id":"static-html","source_path":` + jsonString(staticSiteDir(t)) + `}`
	rec := callApp(t, s, http.MethodPost, "/v1/apps", body, func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.CreateApp(w, r)
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app failed: %d %s", rec.Code, rec.Body.String())
	}
	var created gen.App
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created
}
