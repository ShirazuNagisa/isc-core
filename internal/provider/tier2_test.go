package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/credential"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件验证"移植过来的 ddns-go 代码真的能用"。
//
// 这一点必须被证明，而不是靠"编译通过了"来假定 —— 8335 行机械变换之后，
// 任何一处包名前缀、字段名或 import 的差错都可能让某个服务商在运行时
// 静默地什么都不做。编译通过只说明类型对得上。
//
// 办法：用 callback 服务商（它把目标 URL 放在凭据里）对着本地假服务器
// 跑一次真实更新。这条路径串起了**适配器的全部环节**：
//
//	ISC 具名凭据 → ddns-go 位置化槽位 → DnsConfig → ForceAddr 注入 IP
//	→ 域名解析（publicsuffix）→ 真实 HTTP 请求 → 结果状态翻译
//
// 之所以选 callback：其他服务商的 API 地址是硬编码的常量，为了让它们
// 可测就必须改动移植代码；而 callback 的目标地址本来就来自配置，
// 因此这条测试**对移植代码零改动**。

// recordingServer 是一个记录收到的请求的假服务商。
type recordingServer struct {
	mu       sync.Mutex
	requests []*http.Request
	queries  []string
	status   int
}

func newRecordingServer(t *testing.T) (*recordingServer, *httptest.Server) {
	t.Helper()
	rs := &recordingServer{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests = append(rs.requests, r.Clone(context.Background()))
		rs.queries = append(rs.queries, r.URL.RawQuery)
		status := rs.status
		rs.mu.Unlock()

		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return rs, srv
}

func (rs *recordingServer) snapshot() (int, []string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	out := make([]string, len(rs.queries))
	copy(out, rs.queries)
	return len(rs.requests), out
}

func (rs *recordingServer) setStatus(code int) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.status = code
}

// callbackCredential 构造一组 callback 服务商凭据。
//
// callback 的凭据字段声明沿用 Tier-2 通用三件套：
//
//	id       槽位 0 —— ddns-go 用它当请求 URL
//	secret   槽位 1 —— 非空时改用 POST，值作为请求体模板
//	ext_param 槽位 2
func callbackCredential(urlTemplate string) dns.Credential {
	return dns.Credential{
		ID: "c1", Provider: "callback",
		Fields: map[string]string{"id": urlTemplate},
	}
}

// adapterFor 返回某家服务商的适配器，找不到实现则终止测试。
func adapterFor(t *testing.T, name string) dns.Provider {
	t.Helper()
	for _, p := range Default().List() {
		if p.Name != name {
			continue
		}
		if p.Impl == nil {
			t.Fatalf("服务商 %s 没有接上实现", name)
		}
		return p.Impl
	}
	t.Fatalf("注册表里没有服务商 %s", name)
	return nil
}

// TestCallbackDynamicUpdateEndToEnd 是移植是否可用的核心证据。
func TestCallbackDynamicUpdateEndToEnd(t *testing.T) {
	rs, srv := newRecordingServer(t)

	// 模板里带上域名与 IP，这样断言能直接验证替换是否发生。
	urlTemplate := srv.URL + "/update?ip=#{ip}&domain=#{domain}&type=#{recordType}&ttl=#{ttl}"
	impl := adapterFor(t, "callback")

	updater, ok := impl.(dns.DynamicUpdater)
	if !ok {
		t.Fatal("callback 应当实现 DynamicUpdater")
	}

	res, err := updater.UpdateDynamic(context.Background(),
		callbackCredential(urlTemplate),
		dns.DynamicRequest{
			Domains:    []string{"www.example.com"},
			RecordType: dns.TypeA,
			IP:         "203.0.113.7",
			TTL:        "600",
		})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	// --- 服务商确实收到了请求，且内容正确 ---
	count, queries := rs.snapshot()
	if count != 1 {
		t.Fatalf("应当发出 1 个请求，实际 %d 个（查询串: %v）", count, queries)
	}
	q := queries[0]
	for _, want := range []string{
		"ip=203.0.113.7",         // 注入的 IP 被写进了模板
		"domain=www.example.com", // 域名被正确解析
		"type=A",                 // 记录类型正确
		"ttl=600",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("请求里缺少 %q，实际查询串: %s", want, q)
		}
	}

	// --- 结果被正确翻译回来 ---
	if len(res.Domains) != 1 {
		t.Fatalf("应当返回 1 条域名结果，得到 %d", len(res.Domains))
	}
	if res.Domains[0].Status != dns.StatusSuccess {
		t.Errorf("状态 = %q, 期望 success", res.Domains[0].Status)
	}
	if res.Domains[0].Domain != "www.example.com" {
		t.Errorf("域名 = %q", res.Domains[0].Domain)
	}
	if res.Domains[0].RootDomain != "example.com" {
		t.Errorf("根域名 = %q，publicsuffix 解析可能有问题", res.Domains[0].RootDomain)
	}
	if res.IP != "203.0.113.7" || res.RecordType != dns.TypeA {
		t.Errorf("结果元信息不对: ip=%q type=%q", res.IP, res.RecordType)
	}
}

// TestCallbackDynamicIPv6 验证 AAAA 路径走的是 Ipv6 分支。
//
// IPv4 与 IPv6 在 DnsConfig 里是两套独立的字段与缓存，只测一边
// 无法发现"AAA 分支忘了注入 ForceAddr"这类错误。
func TestCallbackDynamicIPv6(t *testing.T) {
	rs, srv := newRecordingServer(t)

	urlTemplate := srv.URL + "/update?ip=#{ip}&type=#{recordType}"
	impl := adapterFor(t, "callback").(dns.DynamicUpdater)

	pub := "240e:3b0:1234:5600::1"
	res, err := impl.UpdateDynamic(context.Background(),
		callbackCredential(urlTemplate),
		dns.DynamicRequest{
			Domains:    []string{"home.example.com"},
			RecordType: dns.TypeAAAA,
			IP:         pub,
		})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}

	count, queries := rs.snapshot()
	if count != 1 {
		t.Fatalf("应当发出 1 个请求，实际 %d 个", count)
	}
	if !strings.Contains(queries[0], "type=AAAA") {
		t.Errorf("记录类型应为 AAAA，实际查询串: %s", queries[0])
	}
	if !strings.Contains(queries[0], "240e") {
		t.Errorf("IPv6 地址未被注入，实际查询串: %s", queries[0])
	}
	if res.RecordType != dns.TypeAAAA {
		t.Errorf("结果记录类型 = %q", res.RecordType)
	}
	if res.Domains[0].Status != dns.StatusSuccess {
		t.Errorf("状态 = %q, 期望 success", res.Domains[0].Status)
	}
}

// TestCallbackFailureIsReported 验证失败被如实上报。
//
// 把"不知道发生了什么"当成成功，会让用户看到一片绿色而域名其实没被更新 ——
// 那比一个红色的失败危险得多。
func TestCallbackFailureIsReported(t *testing.T) {
	rs, srv := newRecordingServer(t)
	rs.setStatus(http.StatusInternalServerError)

	urlTemplate := srv.URL + "/update?ip=#{ip}"
	impl := adapterFor(t, "callback").(dns.DynamicUpdater)

	res, err := impl.UpdateDynamic(context.Background(),
		callbackCredential(urlTemplate),
		dns.DynamicRequest{
			Domains:    []string{"www.example.com"},
			RecordType: dns.TypeA,
			IP:         "203.0.113.7",
		})
	if err != nil {
		// 上游对 HTTP 错误不返回 error，而是把状态标成失败；
		// 若将来改成返回 error 也是可接受的。
		return
	}
	if len(res.Domains) != 1 {
		t.Fatalf("应当返回 1 条域名结果，得到 %d", len(res.Domains))
	}
	if res.Domains[0].Status != dns.StatusFailed {
		t.Errorf("服务商返回 500 时状态应为 failed，得到 %q", res.Domains[0].Status)
	}
	if !res.HasFailure() {
		t.Error("HasFailure 应当为 true")
	}
	if res.Changed() != 0 {
		t.Errorf("失败时 Changed 应为 0，得到 %d", res.Changed())
	}
}

// TestDynamicRejectsEmptyIP 验证缺少 IP 时立刻失败。
//
// 不拦的话，上游会拿着空地址去发请求，服务商那边可能把记录改成空值 ——
// 那是一次静默的破坏。
func TestDynamicRejectsEmptyIP(t *testing.T) {
	impl := adapterFor(t, "callback").(dns.DynamicUpdater)
	if _, err := impl.UpdateDynamic(context.Background(),
		callbackCredential("http://127.0.0.1:1/x"),
		dns.DynamicRequest{Domains: []string{"a.example.com"}, RecordType: dns.TypeA},
	); err == nil {
		t.Fatal("未提供 IP 时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 槽位映射
// ---------------------------------------------------------------------------

// TestDdnsGoSlotMapping 验证具名字段被正确翻译成位置化槽位。
//
// 这是适配器最容易出错的地方：Cloudflare 只用 secret 槽位（API Token），
// 早期按声明顺序映射的版本会让它拿到空的 id ——
// 表现为"导入成功但凭据是空的"。
func TestDdnsGoSlotMapping(t *testing.T) {
	tests := []struct {
		name   string
		specs  []credential.FieldSpec
		fields map[string]string
		want   [3]string // id, secret, extParam
	}{
		{
			name: "Cloudflare 只用 secret 槽位",
			specs: []credential.FieldSpec{
				{Key: "token", DdnsGoSlot: credential.DdnsGoSecret},
			},
			fields: map[string]string{"token": "cf-token"},
			want:   [3]string{"", "cf-token", ""},
		},
		{
			name: "阿里云用 id 与 secret",
			specs: []credential.FieldSpec{
				{Key: "access_key_id", DdnsGoSlot: credential.DdnsGoID},
				{Key: "access_key_secret", DdnsGoSlot: credential.DdnsGoSecret},
			},
			fields: map[string]string{"access_key_id": "AK", "access_key_secret": "SK"},
			want:   [3]string{"AK", "SK", ""},
		},
		{
			name: "Tier-2 三件套一一对应",
			specs: []credential.FieldSpec{
				{Key: "id", DdnsGoSlot: credential.DdnsGoID},
				{Key: "secret", DdnsGoSlot: credential.DdnsGoSecret},
				{Key: "ext_param", DdnsGoSlot: credential.DdnsGoExtParam},
			},
			fields: map[string]string{"id": "1", "secret": "2", "ext_param": "3"},
			want:   [3]string{"1", "2", "3"},
		},
		{
			// 零值表示"未声明"，必须被安全忽略。
			// 如果槽位是 0 基，这个字段会静默抢走 id 槽位 ——
			// 而症状是"凭据看起来正常但服务商鉴权失败"。
			name:   "未声明槽位的字段被忽略",
			specs:  []credential.FieldSpec{{Key: "note"}},
			fields: map[string]string{"note": "无关"},
			want:   [3]string{"", "", ""},
		},
		{
			name:   "越界的槽位号被忽略",
			specs:  []credential.FieldSpec{{Key: "x", DdnsGoSlot: 99}},
			fields: map[string]string{"x": "v"},
			want:   [3]string{"", "", ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ddnsGoDNS("test", tt.specs, tt.fields)
			if got.ID != tt.want[0] || got.Secret != tt.want[1] || got.ExtParam != tt.want[2] {
				t.Errorf("槽位映射 = (%q,%q,%q)，期望 (%q,%q,%q)",
					got.ID, got.Secret, got.ExtParam, tt.want[0], tt.want[1], tt.want[2])
			}
			if got.Name != "test" {
				t.Errorf("服务商名 = %q", got.Name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 注册表与能力
// ---------------------------------------------------------------------------

// TestTier2ProvidersAreImplemented 验证移植过来的服务商确实被接上了。
//
// 这条测试的意义：移植脚本失败或工厂表漏登记时，编译仍然会通过，
// 只是那些服务商在界面上显示为"尚未实现"。没有这条断言，
// 一次静默的回归只能等用户报告才会被发现。
func TestTier2ProvidersAreImplemented(t *testing.T) {
	reg := Default()

	implemented := 0
	for _, p := range reg.List() {
		if p.Impl != nil {
			implemented++
		}
	}
	// 移植了 30 家（callback 等在内），留一点余量以免一条边缘条目
	// 让整条测试变得脆弱，但必须挡住"全都没接上"这类灾难。
	if implemented < 25 {
		t.Fatalf("实际接上的服务商只有 %d 家，移植可能出了问题", implemented)
	}

	// 抽查几家必须可用。
	for _, name := range []string{"cloudflare", "alidns", "dnspod", "huaweicloud", "godaddy", "callback"} {
		impl := adapterFor(t, name)
		if _, ok := impl.(dns.DynamicUpdater); !ok {
			t.Errorf("%s 应当具备动态解析能力", name)
		}
	}
}

// TestCloudflareHasVerifyButTier2DoesNot 验证能力断言如实反映实现。
//
// Cloudflare 有专门的只读令牌校验端点，因此能"测试连接"；
// 其余 Tier-2 服务商没有，界面必须据此把按钮置灰 ——
// 而不是让用户点了才发现它其实会去创建记录。
func TestCloudflareHasVerifyButTier2DoesNot(t *testing.T) {
	cf := adapterFor(t, "cloudflare")
	if _, ok := cf.(dns.Verifier); !ok {
		t.Error("Cloudflare 应当实现 Verifier —— 它有只读的令牌校验端点")
	}

	cb := adapterFor(t, "callback")
	if _, ok := cb.(dns.Verifier); ok {
		t.Error("callback 不应报告具备凭据校验能力 —— 它的校验只能靠真实调用，" +
			"而那是带副作用的")
	}

	// 接口层看到的能力位必须与之一致。
	for _, p := range Default().List() {
		if p.Name != "cloudflare" {
			continue
		}
		caps := p.Capabilities()
		if !caps.Available || !caps.Dynamic {
			t.Errorf("Cloudflare 能力位不对: %+v", caps)
		}
		// Cloudflare 是已接入记录管理的 Tier-1，这些能力必须为 true。
		// 这条断言同时守着"合并逻辑没把动态解析顶掉"与
		// "记录管理确实接上了"两件事。
		if !caps.ZoneList || !caps.RecordList || !caps.RecordCreate ||
			!caps.RecordUpdate || !caps.RecordDelete {
			t.Errorf("Cloudflare 应具备完整记录管理能力: %+v", caps)
		}
	}

	// Tier-2 服务商必须**只**有动态解析能力 —— 它们不该报告记录管理，
	// 那会误导界面显示一堆点了就报错的按钮。
	for _, p := range Default().List() {
		if p.Name != "callback" {
			continue
		}
		caps := p.Capabilities()
		if !caps.Dynamic {
			t.Errorf("callback 应具备动态解析能力: %+v", caps)
		}
		if caps.ZoneList || caps.RecordCreate || caps.RecordDelete {
			t.Errorf("Tier-2 服务商不应报告记录管理能力: %+v", caps)
		}
	}
}

// TestCallbackVerifyIsNil 验证没有副作用包装的服务商不会被误判为可校验。
func TestCallbackVerifyIsNil(t *testing.T) {
	if verifierFor("callback") != nil {
		t.Error("callback 不应有凭据校验实现")
	}
	if verifierFor("cloudflare") == nil {
		t.Error("cloudflare 应当有凭据校验实现")
	}
}
