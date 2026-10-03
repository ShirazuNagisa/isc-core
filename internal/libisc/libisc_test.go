package libisc

// 这一层不依赖 cgo，因此这些用例在默认 CI（CGO_ENABLED=0）里就会跑。

import (
	"encoding/json"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/testsupport"
)

// TestMain 把凭据存储固定成文件后端：测试**不许碰开发机的系统钥匙串**
// （见 docs/DECISIONS.md D23 与 internal/testsupport）。
func TestMain(m *testing.M) { testsupport.IsolateSecretStore(m) }

// startTemp 在短路径的临时数据目录上启动内核。
//
// 必须用 ShortTempDir：macOS 的 Unix 套接字路径上限是 104 字节，而
// t.TempDir() 给出的路径通常就超了（那个坑已经踩过一次）。
func startTemp(t *testing.T) string {
	t.Helper()

	_ = Stop() // 保证每个用例从干净状态开始
	dir := testsupport.ShortTempDir(t)
	res := Start(dir)
	if res["ok"] != true {
		t.Fatalf("启动失败: %v", res)
	}
	t.Cleanup(func() { _ = Stop() })
	return dir
}

func TestAPIVersionIsPlainString(t *testing.T) {
	if got := APIVersion(); len(got) < 2 || got[0] != 'v' {
		t.Errorf("接口版本不像 vN: %q", got)
	}
}

func TestVersionInfo(t *testing.T) {
	v := VersionInfo()
	for _, k := range []string{"ok", "api", "version", "commit"} {
		if _, present := v[k]; !present {
			t.Errorf("版本信息里缺少 %s: %v", k, v)
		}
	}
}

func TestStartStatusStop(t *testing.T) {
	startTemp(t)

	status := Status()
	if status["ok"] != true {
		t.Fatalf("状态查询失败: %v", status)
	}
	health, _ := status["health"].(map[string]any)
	if health == nil || health["status"] != "ok" {
		t.Errorf("health 不对: %v", status["health"])
	}

	// 六个平台后端必须都在。
	meta, _ := status["meta"].(map[string]any)
	caps, _ := meta["capabilities"].(map[string]any)
	for _, name := range []string{
		"firewall", "ip_monitor", "low_port_binder",
		"secret_store", "service_manager", "transport",
	} {
		if _, ok := caps[name]; !ok {
			t.Errorf("能力清单里缺少 %s（实际：%v）", name, caps)
		}
	}

	// 重复启动必须被挡住，而不是起第二个内核。
	dup := Start("/tmp/isc-libisc-dup")
	if dup["code"] != CodeAlreadyUp {
		t.Errorf("重复启动的错误码是 %v，期望 %s", dup["code"], CodeAlreadyUp)
	}

	if stop := Stop(); stop["ok"] != true || stop["stopped"] != true {
		t.Errorf("停止失败: %v", stop)
	}
	// 幂等
	if again := Stop(); again["ok"] != true || again["stopped"] != false {
		t.Errorf("重复停止应当是 ok 且 stopped=false: %v", again)
	}
}

func TestRestart(t *testing.T) {
	dir := startTemp(t)

	res := reStart(dir)
	if res["ok"] != true {
		t.Fatalf("重启失败: %v", res)
	}
	// 重启之后仍然可用
	if status := Status(); status["ok"] != true {
		t.Errorf("重启之后状态查询失败: %v", status)
	}
}

func TestCallDispatchesInProcess(t *testing.T) {
	startTemp(t)

	res := Call("GET", "/v1/health", "")
	if res["ok"] != true {
		t.Fatalf("GET /v1/health 失败: %v", res)
	}
	body, _ := res["body"].(map[string]any)
	if body["status"] != "ok" {
		t.Errorf("health 响应不对: %v", res["body"])
	}

	// 未知路径 → not_found（错误码要能被 GUI 判别）
	if nf := Call("GET", "/v1/nope", ""); nf["code"] != CodeNotFound {
		t.Errorf("未知路径的错误码是 %v，期望 %s", nf["code"], CodeNotFound)
	}

	// 路径不合法 → bad_request
	if bad := Call("GET", "v1/health", ""); bad["code"] != CodeBadRequest {
		t.Errorf("非法路径的错误码是 %v，期望 %s", bad["code"], CodeBadRequest)
	}

	// 方法为空时按 GET 处理
	if empty := Call("", "/v1/health", ""); empty["ok"] != true {
		t.Errorf("空方法应当按 GET 处理: %v", empty)
	}
}

func TestCallCanWrite(t *testing.T) {
	startTemp(t)

	// 写路径也要能走通：改一次设置再读回来。
	// 用设置接口是因为它幂等、可复原，不会在开发机上留下系统级副作用。
	before := Call("GET", "/v1/settings", "")
	if before["ok"] != true {
		t.Fatalf("读设置失败: %v", before)
	}

	// 方法是 **PATCH**（不是我凭印象写的 PUT）—— 接口面一律以
	// api/openapi.yaml 的契约为准。这类"猜方法/猜路径"的错今天已经犯过两次。
	// 字段名与取值也照契约：是 `lang`，取值是 `en` / `zh-CN`
	//（设置结构里**没有** language 这个字段 —— 又是凭印象写错的）。
	body, _ := json.Marshal(map[string]any{"lang": "en"})
	after := Call("PATCH", "/v1/settings", string(body))
	if after["ok"] != true {
		t.Fatalf("写设置失败: %v", after)
	}

	// 读回来确认真的改了，再复原成中文（别在开发机上留下副作用）。
	read := Call("GET", "/v1/settings", "")
	if read["ok"] != true {
		t.Fatalf("读设置失败: %v", read)
	}
	if settings, _ := read["body"].(map[string]any); settings["lang"] != "en" {
		t.Errorf("设置没生效: %v", read["body"])
	}
	if restore := Call("PATCH", "/v1/settings", `{"lang":"zh-CN"}`); restore["ok"] != true {
		t.Errorf("恢复语言设置失败: %v", restore)
	}
}

func TestCallBeforeStartIsNotRunning(t *testing.T) {
	_ = Stop()

	if res := Call("GET", "/v1/health", ""); res["code"] != CodeNotRunning {
		t.Errorf("未启动时的错误码是 %v，期望 %s", res["code"], CodeNotRunning)
	}
	if st := Status(); st["code"] != CodeNotRunning {
		t.Errorf("未启动时查状态的错误码是 %v，期望 %s", st["code"], CodeNotRunning)
	}
	if ev := Events(0, 0); ev["code"] != CodeNotRunning {
		t.Errorf("未启动时拉事件的错误码是 %v，期望 %s", ev["code"], CodeNotRunning)
	}
}

func TestEventsLongPoll(t *testing.T) {
	startTemp(t)

	first := Events(0, 0)
	if first["ok"] != true {
		t.Fatalf("事件拉取失败: %v", first)
	}
	if _, ok := first["next"]; !ok {
		t.Error("返回里缺少游标 next")
	}

	// 带超时的长轮询也要正常返回（没有新事件时等到超时）。
	second := Events(0, 100)
	if second["ok"] != true {
		t.Fatalf("长轮询失败: %v", second)
	}
	if second["gap"] != false {
		t.Errorf("不该报 gap: %v", second)
	}
}

func TestStopWithoutStart(t *testing.T) {
	_ = Stop()
	res := Stop()
	if res["ok"] != true || res["stopped"] != false {
		t.Errorf("未启动时停止应当是 ok 且 stopped=false: %v", res)
	}
}
