package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/paths"
	"github.com/ShirazuNagisa/isc-core/internal/runtimeinfo"
	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestExposeJSONWithYesActuallyApplies 钉住一个真实踩到的缺陷。
//
// # 现象
//
//	isc expose --port 18080 --yes --json
//
// 打印出变更计划、**退出码 0**，但系统里什么都没变 —— pf 没启用、anchor
// 没写、变更停在 pending。原因是 `--json` 分支在生成计划之后直接 return，
// 把后面的"确认 + 应用"整段跳过了。
//
// # 为什么这条最该测
//
// JSON 输出是给脚本用的，而脚本判断成败的唯一依据就是**退出码**。
// "看起来成功、实际没做"比直接报错危险得多。
func TestExposeJSONWithYesActuallyApplies(t *testing.T) {
	var (
		mu      sync.Mutex
		seen    []string
		applied bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok","uptime_seconds":1,"version":"test"}`)

		case strings.HasSuffix(r.URL.Path, "/plan"):
			_, _ = io.WriteString(w, `{
				"id":"firewall-1","kind":"firewall.expose_port","title":"放行 18080/tcp",
				"risk":"medium","empty":false,
				"created_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-01T00:10:00Z",
				"diff":[{"op":"add","text":"入站 tcp 18080（来源：任意）"}],"steps":[]}`)

		case strings.HasSuffix(r.URL.Path, "/apply"):
			mu.Lock()
			applied = true
			mu.Unlock()
			_, _ = io.WriteString(w, `{
				"plan_id":"firewall-1","kind":"firewall.expose_port","title":"放行 18080/tcp",
				"risk":"medium","status":"applied",
				"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:01Z","steps":[]}`)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 假守护进程：写一份指向它的 runtime.json。
	//
	// PID 必须是**活着的**进程，否则 Connect 会把文件当成崩溃残留，
	// 直接报"内核未运行"。
	dir := testsupport.ShortTempDir(t)
	t.Setenv(paths.EnvDataDir, dir)

	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	info := runtimeinfo.Info{
		PID:       os.Getpid(),
		Version:   "test",
		Endpoint:  "tcp://" + strings.TrimPrefix(srv.URL, "http://"),
		Token:     "test-token",
		StartedAt: time.Now(),
	}
	if err := runtimeinfo.Write(filepath.Join(runDir, "runtime.json"), info); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newWithApp(&App{out: &out, in: strings.NewReader("")})
	root.SetArgs([]string{"expose", "--port", "18080", "--yes", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("命令失败: %v\n输出: %s", err, out.String())
	}

	if !applied {
		t.Errorf("带 --yes 时**没有**调用 apply —— 命令静默什么都没做。\n收到过的请求: %v\n输出: %s",
			seen, out.String())
	}

	// JSON 输出应当是**变更记录**（有 status），而不是计划。
	if !strings.Contains(out.String(), `"status"`) {
		t.Errorf("JSON 输出不是变更记录（缺少 status 字段）: %s", out.String())
	}
}

// TestExposeJSONWithoutYesOnlyPreviews 确认另一半语义：
// 不带 --yes 时只输出计划、**不应用**（脚本里"先看计划再决定"要能成立）。
func TestExposeJSONWithoutYesOnlyPreviews(t *testing.T) {
	var (
		mu      sync.Mutex
		applied bool
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/health":
			_, _ = io.WriteString(w, `{"status":"ok","uptime_seconds":1,"version":"test"}`)
		case strings.HasSuffix(r.URL.Path, "/plan"):
			_, _ = io.WriteString(w, `{
				"id":"firewall-2","kind":"firewall.expose_port","title":"放行 18081/tcp",
				"risk":"medium","empty":false,
				"created_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-01T00:10:00Z",
				"diff":[],"steps":[]}`)
		case strings.HasSuffix(r.URL.Path, "/apply"):
			mu.Lock()
			applied = true
			mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	dir := testsupport.ShortTempDir(t)
	t.Setenv(paths.EnvDataDir, dir)

	runDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runtimeinfo.Write(filepath.Join(runDir, "runtime.json"), runtimeinfo.Info{
		PID: os.Getpid(), Version: "test",
		Endpoint:  "tcp://" + strings.TrimPrefix(srv.URL, "http://"),
		Token:     "test-token",
		StartedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	root := newWithApp(&App{out: &out, in: strings.NewReader("")})
	root.SetArgs([]string{"expose", "--port", "18081", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("命令失败: %v\n输出: %s", err, out.String())
	}

	if applied {
		t.Error("没带 --yes 却应用了变更 —— 那是更危险的错")
	}
	if !strings.Contains(out.String(), "firewall-2") {
		t.Errorf("输出里没有计划 ID: %s", out.String())
	}
}
