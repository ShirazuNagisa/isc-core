package apps

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/presets"
	"github.com/ShirazuNagisa/isc-core/internal/runtime"
)

// 这一组测试**真的会联网下载运行时**（几十到几百 MB），因此默认跳过。
// 设置 ISC_E2E_DOWNLOAD=1 才运行。
//
// 为什么值得留着：单元测试覆盖了供给链路的每一个环节，但它们都用一个注入
// 的合成归档。只有真跑一次，才能证明清单里那条 URL 与摘要**在今天**仍然
// 成立 —— 上游换包、撤包、改路径都不会让任何单元测试变红。
const pythonSite = `import os, http.server, socketserver

PORT = int(os.environ.get("PORT", "8000"))

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"ISC-PHECDA-PYTHON-OK"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *args):
        pass

socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", PORT), Handler) as httpd:
    print("LISTENING " + str(PORT), flush=True)
    httpd.serve_forever()
`

func requireDownloadOptIn(t *testing.T) {
	t.Helper()
	if os.Getenv("ISC_E2E_DOWNLOAD") == "" {
		t.Skip("set ISC_E2E_DOWNLOAD=1 to run the real-download test")
	}
}

// 用清单里那条固定的 Python 发行版，把一份真实的站点从下载跑到能访问。
func TestProvisionAndDeployPythonFromThePinnedRelease(t *testing.T) {
	requireDownloadOptIn(t)

	manager, _, logs := realManager(t)
	ctx := context.Background()

	// 先确认这台机器上**没有**满足要求的 python3，否则会走系统解释器那条路，
	// 下载路径根本不会被执行 —— 那样这个测试就名不副实了。
	preset, err := presets.Lookup("python-auto")
	if err != nil {
		t.Fatal(err)
	}
	if found, ok, _ := manager.runtimes.Resolve(ctx, runtime.KindPython, preset.MinVersion); ok {
		t.Skipf("this machine already has a satisfying python3 (%s %s); the download path would not run",
			found.Source, found.Version)
	}

	site := t.TempDir()
	writeSiteFile(t, filepath.Join(site, "app.py"), pythonSite)
	// 不带 requirements.txt：装依赖需要 pip 联网，而这个测试要验证的是
	// 运行时供给而不是包管理。
	app, err := manager.Create(ctx, CreateSpec{Name: "py-e2e", PresetID: "python-auto", SourcePath: site})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { manager.Shutdown(context.Background()) })

	if err := manager.Deploy(ctx, app.ID, nil); err != nil {
		t.Fatalf("deploy: %v\nlogs:\n%s", err, strings.Join(logs.Tail(app.ID, 50), "\n"))
	}

	running, _, _ := manager.Get(ctx, app.ID)
	if running.State != StateRunning || running.Health != HealthHealthy {
		t.Fatalf("expected running/healthy, got %s/%s (%s)", running.State, running.Health, running.HealthDetail)
	}
	// 运行时必须是**内核下载并校验过的**那一份，而不是系统里那个太旧的。
	used, ok, err := manager.runtimes.Resolve(ctx, runtime.KindPython, preset.MinVersion)
	if err != nil || !ok {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if used.Source != "managed" {
		t.Fatalf("the app should run on the managed runtime, got %s", used.Source)
	}

	body := httpGet(t, fmt.Sprintf("http://127.0.0.1:%d/", running.LocalPort))
	if !strings.Contains(body, "ISC-PHECDA-PYTHON-OK") {
		t.Fatalf("unexpected body: %q", body)
	}

	// 顺带确认磁盘上的东西是可以删干净的。
	if _, err := manager.runtimes.Remove(runtime.KindPython); err != nil {
		t.Fatalf("remove: %v", err)
	}
}
