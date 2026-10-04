package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/job"
	hosting "github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// offlineRuntimes 构造一个**不会联网**的运行时管理器。
//
// 平台键故意用一个清单里没有的组合：这样 Provision 会在"没有固定发行版"
// 处立刻返回，而不会真的去下载几百 MB。测试不该依赖开发机上装了什么，
// 更不该在 CI 里拉运行时。
func offlineRuntimes(t *testing.T) *hosting.Manager {
	t.Helper()
	manager, _ := offlineRuntimesWithRoot(t)
	return manager
}

// offlineRuntimesWithRoot 额外返回数据根目录，便于断言"真的没有下载"。
func offlineRuntimesWithRoot(t *testing.T) (*hosting.Manager, string) {
	t.Helper()
	root := t.TempDir()
	return hosting.NewManager(root, "plan9", "386"), root
}

func newHostingServer(t *testing.T, runtimes *hosting.Manager, jobs *job.Engine) *Server {
	t.Helper()
	bus := event.NewBus(64)
	t.Cleanup(bus.Close)
	if jobs == nil {
		jobs = job.NewEngine(t.Context(), bus, job.NewMemoryStore(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		t.Cleanup(func() { _ = jobs.Shutdown(t.Context()) })
	}
	return &Server{Deps: Deps{Log: slog.Default(), Runtimes: runtimes, Jobs: jobs, Bus: bus}}
}

func doJSON(t *testing.T, s *Server, method, path, body string, handle func(*Server, http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
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

// --- /v1/presets ------------------------------------------------------------

func TestListPresetsReturnsTheCatalogAndTheCustomOption(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	rec := doJSON(t, s, http.MethodGet, "/v1/presets", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.ListPresets(w, r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var catalog struct {
		Items []struct {
			Id          string `json:"id"`
			Kind        string `json:"kind"`
			DefaultPort int    `json:"default_port"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) == 0 {
		t.Fatalf("the catalog must not be empty")
	}
	ids := map[string]bool{}
	for _, item := range catalog.Items {
		ids[item.Id] = true
		if item.DefaultPort == 0 {
			t.Errorf("preset %s has no default port", item.Id)
		}
	}
	// 自定义服务器不是目录里的一项，但界面需要一个统一的选项概念。
	if !ids["custom"] {
		t.Errorf("the custom server option must be offered: %v", ids)
	}
}

// --- /v1/sources/inspect ----------------------------------------------------

func TestInspectSourceReportsEvidenceAndRecommendation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"start":"node ."}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newHostingServer(t, nil, nil)
	rec := doJSON(t, s, http.MethodPost, "/v1/sources/inspect",
		`{"path":`+jsonString(root)+`}`,
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.InspectSource(w, r) })

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result struct {
		Root                string `json:"root"`
		RecommendedPresetId string `json:"recommended_preset_id"`
		Evidence            []struct {
			File       string  `json:"file"`
			Signal     string  `json:"signal"`
			Confidence float32 `json:"confidence"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RecommendedPresetId != "node-auto" {
		t.Fatalf("recommended %q", result.RecommendedPresetId)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].File != "package.json" {
		t.Fatalf("evidence should name the file that drove the decision: %#v", result.Evidence)
	}
	if result.Evidence[0].Confidence <= 0 {
		t.Fatalf("evidence must carry a confidence")
	}
}

// 路径不存在是用户输入问题，不该报成内核故障。
func TestInspectSourceRejectsBadInput(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	cases := map[string]string{
		"missing path": `{}`,
		"empty path":   `{"path":""}`,
		"bad json":     `{"path":`,
		"no such dir":  `{"path":"/nonexistent/isc-test-path"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doJSON(t, s, http.MethodPost, "/v1/sources/inspect", body,
				func(s *Server, w http.ResponseWriter, r *http.Request) { s.InspectSource(w, r) })
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			var problem struct {
				Code *string `json:"code"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if problem.Code == nil || *problem.Code != CodeInvalidRequest {
				t.Fatalf("expected the stable %q code, got %v", CodeInvalidRequest, problem.Code)
			}
		})
	}
}

// --- /v1/runtimes -----------------------------------------------------------

func TestListRuntimesWithoutAManagerReturnsAnEmptyList(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	rec := doJSON(t, s, http.MethodGet, "/v1/runtimes", "",
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.ListRuntimes(w, r) })
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("expected an empty list, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListRuntimesUsesTheManager(t *testing.T) {
	s := newHostingServer(t, offlineRuntimes(t), nil)
	rec := doJSON(t, s, http.MethodGet, "/v1/runtimes", "",
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.ListRuntimes(w, r) })
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"items"`) {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestProvisionRuntimesRejectsWhatTheKernelCannotProvision(t *testing.T) {
	s := newHostingServer(t, offlineRuntimes(t), nil)
	cases := map[string]struct {
		body string
		want int
	}{
		"empty list":        {`{"kinds":[]}`, http.StatusBadRequest},
		"docker":            {`{"kinds":["docker"]}`, http.StatusBadRequest},
		"unknown kind":      {`{"kinds":["cobol"]}`, http.StatusBadRequest},
		"static is a no-op": {`{"kinds":[""]}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec := doJSON(t, s, http.MethodPost, "/v1/runtimes/provision", tc.body,
				func(s *Server, w http.ResponseWriter, r *http.Request) { s.ProvisionRuntimes(w, r) })
			if rec.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestProvisionRuntimesWithoutAManagerFails(t *testing.T) {
	s := newHostingServer(t, nil, nil)
	rec := doJSON(t, s, http.MethodPost, "/v1/runtimes/provision", `{"kinds":["node"]}`,
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.ProvisionRuntimes(w, r) })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

// 供给是长操作，因此接口返回 202 与任务 id，而不是阻塞到下载完成。
func TestProvisionRuntimesAcceptsAndSubmitsAJob(t *testing.T) {
	manager, dataRoot := offlineRuntimesWithRoot(t)
	s := newHostingServer(t, manager, nil)
	rec := doJSON(t, s, http.MethodPost, "/v1/runtimes/provision",
		`{"kinds":["node"],"min_versions":{"node":"18.0.0"}}`,
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.ProvisionRuntimes(w, r) })
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var accepted struct {
		JobId string `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.JobId == "" {
		t.Fatalf("202 must carry a job id")
	}

	// 任务必须收敛到终态。两种结果都合法，取决于这台机器上有没有 node：
	// 有就走"系统解释器"（成功），没有就在"这个平台没有固定发行版"处失败。
	// 测试要锁住的不变量是：**一个字节都没有下载**。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, found, err := s.Jobs.Get(t.Context(), accepted.JobId)
		if err != nil {
			t.Fatal(err)
		}
		if found && got.Status.Finished() {
			if got.Status == job.StatusFailed {
				if got.Err == nil || !strings.Contains(got.Err.Detail, "no pinned runtime build") {
					t.Fatalf("a failure must explain itself, got %#v", got.Err)
				}
			}
			assertNothingDownloaded(t, dataRoot)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the provisioning job never finished")
}

// assertNothingDownloaded 断言缓存目录里没有任何产物。
//
// 这条断言让"测试里不会真的去拉运行时"成为一条可验证的性质，
// 而不是"希望开发机上恰好已经装了"。
func assertNothingDownloaded(t *testing.T, dataRoot string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataRoot, "cache"))
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the test must not download anything, found %v", entries)
	}
}

// --- /v1/runtimes/{kind} ----------------------------------------------------

func TestRemoveRuntimeIsIdempotentAndNeedsAManager(t *testing.T) {
	s := newHostingServer(t, offlineRuntimes(t), nil)
	rec := doJSON(t, s, http.MethodDelete, "/v1/runtimes/node", "",
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.RemoveRuntime(w, r, "node") })
	// DELETE 的语义是"让它不存在"：本来就没有托管的该运行时也算成功。
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	noManager := newHostingServer(t, nil, nil)
	rec = doJSON(t, noManager, http.MethodDelete, "/v1/runtimes/node", "",
		func(s *Server, w http.ResponseWriter, r *http.Request) { s.RemoveRuntime(w, r, "node") })
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 without a manager, got %d", rec.Code)
	}
}

func jsonString(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}
