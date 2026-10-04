package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/metrics"
)

// stubSource 让指标测试不依赖机器上装了什么命令。
type stubSource struct {
	host  metrics.HostSample
	procs map[int]metrics.ProcessSample
}

func (s stubSource) Host(context.Context) (metrics.HostSample, error) { return s.host, nil }

func (s stubSource) Processes(_ context.Context, pids []int) (map[int]metrics.ProcessSample, error) {
	out := map[int]metrics.ProcessSample{}
	for _, pid := range pids {
		if sample, ok := s.procs[pid]; ok {
			out[pid] = sample
		}
	}
	return out, nil
}

func (s stubSource) Describe() string { return "stub" }

func newInsightsServer(t *testing.T, sampler *metrics.Sampler) *Server {
	t.Helper()
	bus := event.NewBus(16)
	t.Cleanup(bus.Close)
	return &Server{Deps: Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Bus:     bus,
		Metrics: sampler,
	}}
}

func TestGetMetricsWithoutASamplerReportsUnsupported(t *testing.T) {
	s := newInsightsServer(t, nil)
	rec := callApp(t, s, http.MethodGet, "/v1/metrics", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.GetMetrics(w, r)
	})
	// 不是 404/500：界面要能区分"此平台不支持"和"出错了"。
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var snapshot gen.MetricsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Host.Backend == nil || *snapshot.Host.Backend != "unsupported" {
		t.Fatalf("the backend must say unsupported, got %v", snapshot.Host.Backend)
	}
	if len(snapshot.Apps) != 0 {
		t.Fatalf("expected no app metrics, got %d", len(snapshot.Apps))
	}
}

func TestGetMetricsReturnsHostAppsAndHistory(t *testing.T) {
	source := stubSource{
		host:  metrics.HostSample{CPUPercent: 42.5, MemoryUsedBytes: 1000, MemoryTotalBytes: 2000, NetRxBytesPerSec: 10, NetTxBytesPerSec: 20},
		procs: map[int]metrics.ProcessSample{9: {PID: 9, CPUPercent: 5, MemoryBytes: 4096}},
	}
	sampler := metrics.NewSampler(source, time.Hour, 5)
	// 采两次以产生历史。
	sampler.SampleNow(func() []metrics.AppRef {
		return []metrics.AppRef{{AppID: "app-1", PID: 9, StartedAt: time.Now().Add(-30 * time.Second)}}
	})

	s := newInsightsServer(t, sampler)
	rec := callApp(t, s, http.MethodGet, "/v1/metrics", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.GetMetrics(w, r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var snapshot gen.MetricsSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Host.CpuPercent != 42.5 || snapshot.Host.MemoryTotalBytes != 2000 {
		t.Fatalf("unexpected host metrics: %#v", snapshot.Host)
	}
	if len(snapshot.Apps) != 1 || snapshot.Apps[0].AppId != "app-1" {
		t.Fatalf("unexpected app metrics: %#v", snapshot.Apps)
	}
	if snapshot.Apps[0].Pid != 9 || snapshot.Apps[0].MemoryBytes != 4096 {
		t.Fatalf("process resources not attached: %#v", snapshot.Apps[0])
	}
	if snapshot.History == nil || len(*snapshot.History) == 0 {
		t.Fatalf("history should be included for the dashboard graph")
	}
}

// 建议的文案必须在**请求自己的语言**下渲染好，而不是把 key 直接发给界面。
func TestListAdvisoriesRendersLocalisedText(t *testing.T) {
	s := newInsightsServer(t, nil)
	rec := callApp(t, s, http.MethodGet, "/v1/advisories", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.ListAdvisories(w, r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var list gen.AdvisoryList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) == 0 {
		t.Fatalf("a bare kernel should still advise the first step")
	}
	for _, item := range list.Items {
		if strings.HasPrefix(item.Title, "advisory.") {
			t.Fatalf("the raw key reached the client: %q", item.Title)
		}
		if item.Title == "" {
			t.Fatalf("%s has an empty title", item.Id)
		}
		if item.Detail != nil && strings.HasPrefix(*item.Detail, "advisory.") {
			t.Fatalf("the raw detail key reached the client: %q", *item.Detail)
		}
	}
}

func TestListAdvisoriesWithoutAnyManagerStillAnswers(t *testing.T) {
	// 所有管理器都缺席（库的使用者只要 DNS 能力）时不该 panic。
	s := newInsightsServer(t, nil)
	rec := callApp(t, s, http.MethodGet, "/v1/advisories", "", func(s *Server, w http.ResponseWriter, r *http.Request) {
		s.ListAdvisories(w, r)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
