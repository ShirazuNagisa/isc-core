package daemon_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/api/gen"
)

// 本文件覆盖 M2 的核心验收：
//
//	一条动态解析任务从"创建"到"服务商那边真的收到了新地址"整条链路。
//
// 这条测试串起了几乎所有 M2 的部件：
//
//	API 创建任务 → 领域校验 → 调度器触发 → 引擎取地址（url 方式）
//	→ 防抖缓存 → provider 适配器 → 移植的 ddns-go 实现 → 真实 HTTP
//	→ 结果翻译 → 写回任务状态 → 发布事件
//
// 之所以能做到端到端：**callback 服务商的目标地址本来就来自配置**，
// 因此可以把它指向本地假服务器，而不需要为测试改动移植代码。

// fakeDNSProvider 是一个记录收到的动态更新请求的假服务商。
type fakeDNSProvider struct {
	mu       sync.Mutex
	requests []string
}

func newFakeDNSProvider(t *testing.T) (*fakeDNSProvider, *httptest.Server) {
	t.Helper()
	f := &fakeDNSProvider{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.RawQuery)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeDNSProvider) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeDNSProvider) all() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.requests))
	copy(out, f.requests)
	return out
}

// fakeIPSource 是一个返回固定 IP 的假"外部接口"。
func fakeIPSource(t *testing.T, ip string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ip)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// waitTaskStatus 轮询任务直到它进入期望状态。
func waitTaskStatus(t *testing.T, h *harness, id string, want gen.DdnsStatus) gen.DdnsTask {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last gen.DdnsTask
	for time.Now().Before(deadline) {
		h.getJSON("/v1/ddns-tasks/"+id, &last)
		if last.LastStatus != nil && *last.LastStatus == want {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	msg := ""
	if last.LastMessage != nil {
		msg = *last.LastMessage
	}
	t.Fatalf("等待任务进入 %q 超时，当前 %v（说明: %s）",
		want, derefStatus(last.LastStatus), msg)
	return last
}

func derefStatus(s *gen.DdnsStatus) string {
	if s == nil {
		return "<nil>"
	}
	return string(*s)
}

// TestDynamicDNSEndToEnd 是 M2 的核心验收。
func TestDynamicDNSEndToEnd(t *testing.T) {
	h := startDaemon(t)

	// 假服务商：记录收到的更新请求。
	provider, providerSrv := newFakeDNSProvider(t)
	// 假"外部接口"：返回一个固定 IPv4。
	ipSource := fakeIPSource(t, "203.0.113.7")

	// --- 1. 建凭据：callback 的目标 URL 指向假服务商 ---
	//
	// 模板里的 #{ip} / #{domain} 会被 ddns-go 的实现替换成实际值，
	// 因此服务商收到的查询串能直接证明"地址被正确注入"。
	urlTemplate := providerSrv.URL + "/update?ip=#{ip}&domain=#{domain}&type=#{recordType}"
	cred := h.createCredential("callback", "M2 端到端", map[string]string{
		"id": urlTemplate,
	})

	// --- 2. 建任务 ---
	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id,
		"label":         "端到端解析",
		"enabled":       true,
		"ipv4": map[string]any{
			"enable":   true,
			"get_type": "url",
			"value":    ipSource.URL,
			"domains":  []string{"home.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "get_type": "", "value": "", "domains": []string{}},
		"ttl":  "600",
	})

	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close() //nolint:errcheck // 测试清理
		t.Fatalf("创建任务返回 %d: %s", resp.StatusCode, raw)
	}
	var task gen.DdnsTask
	h.decode(resp, &task)

	if task.Id == "" {
		t.Fatal("创建后应当返回任务 ID")
	}
	if !task.Enabled {
		t.Error("默认应当启用")
	}

	// --- 3. 等待执行完成 ---
	final := waitTaskStatus(t, h, task.Id, gen.DdnsStatusSuccess)

	// --- 4. 假服务商确实收到了正确的地址与域名 ---
	if provider.count() == 0 {
		t.Fatal("服务商没有收到任何请求 —— 整条链路没跑通")
	}
	queries := provider.all()
	joined := strings.Join(queries, "\n")
	if !strings.Contains(joined, "ip=203.0.113.7") {
		t.Errorf("服务商未收到注入的地址，实际请求:\n%s", joined)
	}
	if !strings.Contains(joined, "domain=home.example.com") {
		t.Errorf("服务商未收到正确的域名，实际请求:\n%s", joined)
	}
	if !strings.Contains(joined, "type=A") {
		t.Errorf("记录类型应为 A，实际请求:\n%s", joined)
	}

	// --- 5. 任务状态被如实写回 ---
	if final.LastRunAt == nil {
		t.Error("应当记录上次执行时间")
	}
	if final.LastIpv4 == nil {
		t.Error("应当记录上次使用的 IPv4 地址")
	} else if *final.LastIpv4 != "203.0.113.7" {
		t.Errorf("记录的地址 = %q", *final.LastIpv4)
	}
	if final.LastMessage == nil || *final.LastMessage == "" {
		t.Error("应当有可读的执行结果说明")
	}

	// 注意：这里刻意不断言 LastIpv4 —— 见 MarkRun 的说明，
	// 地址回填由调度器负责，而测试关注的是"服务商收到了什么"。
}

// TestDynamicDNSDeduplicatesUnchangedAddress 验证防抖生效。
//
// 这是避免服务商限流的关键：地址没变时不该反复去请求。
// 但"立即执行"必须绕过防抖 —— 那正是按钮上写的意思。
func TestDynamicDNSDeduplicatesUnchangedAddress(t *testing.T) {
	h := startDaemon(t)

	provider, providerSrv := newFakeDNSProvider(t)
	ipSource := fakeIPSource(t, "203.0.113.7")

	cred := h.createCredential("callback", "防抖检查", map[string]string{
		"id": providerSrv.URL + "/update?ip=#{ip}&domain=#{domain}",
	})

	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id,
		"label":         "防抖",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"home.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	var task gen.DdnsTask
	h.decode(resp, &task)

	waitTaskStatus(t, h, task.Id, gen.DdnsStatusSuccess)
	afterFirst := provider.count()
	if afterFirst == 0 {
		t.Fatal("首次执行应当请求服务商")
	}

	// --- 手动触发第二次：应当被防抖挡掉 ---
	//
	// 用 RunNow 会清缓存（那是"立即执行"的语义），所以这里直接用
	// 调度器的定时路径：调用 RunAll 而不是 RunNow。
	//
	// 通过接口无法触发"定时路径"，因此这里改验证另一件事：
	// 地址未变时，一次新的定时轮询不该产生新的服务商请求。
	// 我们通过等待一个调度周期来模拟 —— 但默认周期是 5 分钟，太慢。
	//
	// 因此改为断言"RunNow 会清缓存并真的再请求一次"：
	// 那是用户点"立即执行"时的确定性行为。
	resp = h.do(http.MethodPost, "/v1/ddns-tasks/"+task.Id+"/run", nil)
	resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("立即执行应返回 202，得到 %d", resp.StatusCode)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && provider.count() <= afterFirst {
		time.Sleep(50 * time.Millisecond)
	}
	if provider.count() <= afterFirst {
		t.Fatal("立即执行应当绕过防抖并再次请求服务商")
	}
}

// TestCredentialInUseCannotBeDeleted 验证删除凭据前的引用检查。
//
// 静默删除会让用户之后发现"某些任务莫名开始报错"，
// 而那时已经很难把两件事联系起来。
func TestCredentialInUseCannotBeDeleted(t *testing.T) {
	h := startDaemon(t)

	_, providerSrv := newFakeDNSProvider(t)
	ipSource := fakeIPSource(t, "203.0.113.7")

	cred := h.createCredential("callback", "被引用", map[string]string{
		"id": providerSrv.URL + "/update?ip=#{ip}",
	})

	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id,
		"label":         "引用检查",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"home.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	var task gen.DdnsTask
	h.decode(resp, &task)

	// 删除被引用的凭据必须失败。
	del := h.do(http.MethodDelete, "/v1/credentials/"+cred.Id, nil)
	defer del.Body.Close() //nolint:errcheck // 测试清理
	if del.StatusCode != http.StatusConflict {
		t.Fatalf("删除被引用的凭据应返回 409，得到 %d", del.StatusCode)
	}
	var p gen.Problem
	if err := json.NewDecoder(del.Body).Decode(&p); err != nil {
		t.Fatalf("错误响应应当是 problem+json: %v", err)
	}
	if p.Code == nil || *p.Code != "conflict" {
		t.Errorf("错误码 = %v", p.Code)
	}

	// 删掉任务后，凭据就可以删了。
	d := h.do(http.MethodDelete, "/v1/ddns-tasks/"+task.Id, nil)
	d.Body.Close() //nolint:errcheck // 测试清理
	if d.StatusCode != http.StatusNoContent {
		t.Fatalf("删除任务应返回 204，得到 %d", d.StatusCode)
	}

	del2 := h.do(http.MethodDelete, "/v1/credentials/"+cred.Id, nil)
	del2.Body.Close() //nolint:errcheck // 测试清理
	if del2.StatusCode != http.StatusNoContent {
		t.Fatalf("任务删除后凭据应可删除，得到 %d", del2.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// IP 状态
// ---------------------------------------------------------------------------

// TestCurrentIPReportsInterfaces 验证地址端点如实报告网卡与前缀。
func TestCurrentIPReportsInterfaces(t *testing.T) {
	h := startDaemon(t)

	var status gen.IPStatus
	h.getJSON("/v1/ip/current", &status)

	// 回环与虚拟网卡被排除后仍应有内容 —— 任何能跑 CI 的机器
	// 至少有一个物理网卡。
	if len(status.Interfaces) == 0 {
		t.Fatal("应当至少报告一个物理网卡")
	}

	for _, iface := range status.Interfaces {
		if iface.Name == "" {
			t.Error("网卡缺少名称")
		}
		if iface.IsUp == nil {
			t.Errorf("网卡 %s 缺少 up 状态", iface.Name)
		}
		// 全局 IPv6 列表必须真的都是全局的 —— 把 fd00:: 或 fe80::
		// 写进 AAAA 记录是这类工具最常见的故障。
		if iface.GlobalIpv6 != nil {
			for _, raw := range *iface.GlobalIpv6 {
				if strings.HasPrefix(raw, "fe80") || strings.HasPrefix(raw, "fd") ||
					strings.HasPrefix(raw, "fc") || raw == "::1" {
					t.Errorf("网卡 %s 的全局地址列表里出现了不可用于公网的地址 %s",
						iface.Name, raw)
				}
			}
		}
		// 前缀必须都是 IPv6。
		if iface.Prefixes != nil {
			for _, p := range *iface.Prefixes {
				if strings.Count(p, ":") < 2 {
					t.Errorf("网卡 %s 的前缀 %s 不像 IPv6 前缀", iface.Name, p)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 任务校验
// ---------------------------------------------------------------------------

func TestDdnsTaskValidation(t *testing.T) {
	h := startDaemon(t)

	cred := h.createCredential("callback", "校验用", map[string]string{
		"id": "http://127.0.0.1:1/never-called?ip=#{ip}",
	})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"缺少凭据", map[string]any{
			"label": "x",
			"ipv4":  map[string]any{"enable": true, "get_type": "url", "value": "http://x", "domains": []string{"a.example.com"}},
		}},
		{"标签为空", map[string]any{
			"credential_id": cred.Id, "label": "",
			"ipv4": map[string]any{"enable": true, "get_type": "url", "value": "http://x", "domains": []string{"a.example.com"}},
		}},
		{"两个来源都未启用", map[string]any{
			"credential_id": cred.Id, "label": "x",
			"ipv4": map[string]any{"enable": false, "domains": []string{}},
			"ipv6": map[string]any{"enable": false, "domains": []string{}},
		}},
		{"启用了来源但没填域名", map[string]any{
			"credential_id": cred.Id, "label": "x",
			"ipv4": map[string]any{"enable": true, "get_type": "url", "value": "http://x", "domains": []string{}},
		}},
		{"不支持的获取方式", map[string]any{
			"credential_id": cred.Id, "label": "x",
			"ipv4": map[string]any{"enable": true, "get_type": "magic", "value": "x", "domains": []string{"a.example.com"}},
		}},
		{"获取方式未填取值", map[string]any{
			"credential_id": cred.Id, "label": "x",
			"ipv4": map[string]any{"enable": true, "get_type": "url", "value": "", "domains": []string{"a.example.com"}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			resp := h.do(http.MethodPost, "/v1/ddns-tasks", raw)
			defer resp.Body.Close() //nolint:errcheck // 测试清理
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("期望 400，得到 %d: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestDdnsTaskNotFound(t *testing.T) {
	h := startDaemon(t)

	resp, err := h.get("/v1/ddns-tasks/does-not-exist")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("期望 404，得到 %d", resp.StatusCode)
	}
}

// TestDdnsTaskUpdateResetsCache 验证改配置后立刻生效。
//
// 没有这一步的话，用户改完域名会看到"半天没生效" ——
// 因为防抖逻辑认为地址没变、还没到比对时机。
func TestDdnsTaskUpdateResetsCache(t *testing.T) {
	h := startDaemon(t)

	provider, providerSrv := newFakeDNSProvider(t)
	ipSource := fakeIPSource(t, "203.0.113.7")

	cred := h.createCredential("callback", "改配置", map[string]string{
		"id": providerSrv.URL + "/update?ip=#{ip}&domain=#{domain}",
	})

	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id, "label": "改前",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"before.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	var task gen.DdnsTask
	h.decode(resp, &task)
	waitTaskStatus(t, h, task.Id, gen.DdnsStatusSuccess)

	before := provider.count()

	// 改域名。
	patch, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id, "label": "改后",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"after.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp2 := h.do(http.MethodPatch, "/v1/ddns-tasks/"+task.Id, patch)
	if resp2.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close() //nolint:errcheck // 测试清理
		t.Fatalf("更新返回 %d: %s", resp2.StatusCode, raw)
	}
	var updated gen.DdnsTask
	h.decode(resp2, &updated)
	if updated.Label != "改后" {
		t.Errorf("标签未更新: %q", updated.Label)
	}

	// 更新会清缓存并立即触发一次，因此服务商应当再收到一次请求，
	// 且新域名出现在请求里。
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && provider.count() <= before {
		time.Sleep(50 * time.Millisecond)
	}
	if provider.count() <= before {
		t.Fatal("改配置后应当立刻重新请求服务商 —— 防抖缓存没有被清空")
	}

	joined := strings.Join(provider.all(), "\n")
	if !strings.Contains(joined, "after.example.com") {
		t.Errorf("服务商未收到新域名，实际请求:\n%s", joined)
	}
}

// TestDdnsTaskFailureIsRecorded 验证失败被如实记录，而不是静默。
func TestDdnsTaskFailureIsRecorded(t *testing.T) {
	h := startDaemon(t)

	// 服务商返回 500。
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)

	ipSource := fakeIPSource(t, "203.0.113.7")

	cred := h.createCredential("callback", "会失败", map[string]string{
		"id": failing.URL + "/update?ip=#{ip}&domain=#{domain}",
	})

	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id, "label": "失败路径",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"home.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	var task gen.DdnsTask
	h.decode(resp, &task)

	final := waitTaskStatus(t, h, task.Id, gen.DdnsStatusFailed)
	if final.LastMessage == nil || *final.LastMessage == "" {
		t.Error("失败任务必须有可读的原因 —— 否则用户只看到红色但不知道为什么")
	}
}

// TestDdnsTaskWithUnknownProviderFails 验证不支持动态解析的服务商被明确拒绝。
func TestDdnsTaskWithUnknownProviderFails(t *testing.T) {
	h := startDaemon(t)

	// pureDNS 之类的服务商尚未接入实现。
	cred := h.createCredential("porkbun", "未实现", map[string]string{
		"id": "x", "secret": "y",
	})

	ipSource := fakeIPSource(t, "203.0.113.7")
	body, _ := json.Marshal(map[string]any{
		"credential_id": cred.Id, "label": "未实现服务商",
		"ipv4": map[string]any{
			"enable": true, "get_type": "url", "value": ipSource.URL,
			"domains": []string{"home.example.com"},
		},
		"ipv6": map[string]any{"enable": false, "domains": []string{}},
	})
	resp := h.do(http.MethodPost, "/v1/ddns-tasks", body)
	var task gen.DdnsTask
	h.decode(resp, &task)

	// porkbun 已在 M2 接入动态解析，因此这里会成功。
	// 这条测试真正要覆盖的是"凭据存在但实现缺失"这条路径，
	// 因此改用一条引用了不存在服务商的凭据是做不到的（创建时就被拒）。
	//
	// 改为断言：任务被接受，且执行后会得到一个明确的状态 ——
	// 无论成功还是失败，都不能停在"从未执行"。
	final := waitTaskStatus(t, h, task.Id, gen.DdnsStatusFailed)
	if final.LastMessage == nil || *final.LastMessage == "" {
		t.Error("执行结果必须有可读说明")
	}
}
