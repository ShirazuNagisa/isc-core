package tier1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件是 GoDaddy 的测试，结构照 internal/provider/tier1/cloudflare_test.go
// 那份参考测试写：起一个假服务商，断言"发出的请求长什么样"与
// "响应被翻译成了什么"。
//
// fakeAPI / recordedRequest / writeJSONBody / asAPIError 等辅助已经在
// cloudflare_test.go 里定义（同一个包），这里直接复用，不重复定义。
//
// 假服务器对 GoDaddy 尤其重要：它限流紧（约 60 次/分钟），
// 而且它的记录接口有一个危险性质 —— PUT 会替换掉同名同类型的全部记录，
// DELETE 会删掉全部。拿真实账号去试这些路径，代价是用户的解析记录。

// godaddyCred 是测试用凭据。
func godaddyCred() dns.Credential {
	return dns.Credential{
		ID: "g1", Provider: "godaddy",
		Fields: map[string]string{"api_key": "test-key", "api_secret": "test-secret"},
	}
}

// godaddyZone 是测试用的区域。
//
// 注意 Name 与 ID 都是域名：GoDaddy 没有独立的 zone 概念，
// 也没有 zone id —— 它的"区域"就是域名本身。
func godaddyZone() dns.Zone {
	return dns.Zone{ID: "example.com", Name: "example.com"}
}

// godaddyRecordID 是"记录没有原生 ID"这件事在测试里的体现：
// 断言某条记录拿到了合成 ID，并把它拆回三段。
func godaddyRecordID(t *testing.T, rec dns.Record) (typ, name, data string) {
	t.Helper()
	if strings.TrimSpace(rec.ID) == "" {
		t.Fatal("记录 ID 为空：GoDaddy 没有原生 ID，必须合成一个")
	}
	for i, part := range strings.Split(rec.ID, "|") {
		v, err := url.PathUnescape(part)
		if err != nil {
			t.Fatalf("记录 ID 的第 %d 段无法解码: %v", i, err)
		}
		switch i {
		case 0:
			typ = v
		case 1:
			name = v
		case 2:
			data = v
		}
	}
	if typ == "" || name == "" || data == "" {
		t.Fatalf("记录 ID %q 不是三段式（type|name|data）", rec.ID)
	}
	return typ, name, data
}

// godaddyDecodeRecordBody 断言请求体是一个 JSON 数组并取出第一条。
//
// 这一点必须单独测：GoDaddy 的记录接口**请求体永远是数组**，
// 发单个对象会被直接拒绝（400），而这是一个很容易写错、
// 且只有打真实 API 才会暴露的错误。
func godaddyDecodeRecordBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var arr []map[string]any
	if err := json.Unmarshal([]byte(body), &arr); err != nil {
		t.Fatalf("请求体应当是一个 JSON 数组，解析失败: %v（原文 %s）", err, body)
	}
	if len(arr) != 1 {
		t.Fatalf("请求体应当只有一条记录，得到 %d 条", len(arr))
	}
	return arr[0]
}

// ---------------------------------------------------------------------------
// 元信息
// ---------------------------------------------------------------------------

func TestGoDaddyMeta(t *testing.T) {
	t.Parallel()

	m := NewGoDaddy("").Meta()
	// 这三个值不是随便定的：Name 必须与 internal/provider/builtin.go 里的
	// 注册名一致，否则凭据与导入流程会找不到实现。
	if m.Name != "godaddy" {
		t.Errorf("Name = %q, 期望 godaddy", m.Name)
	}
	if m.DisplayName != "GoDaddy" {
		t.Errorf("DisplayName = %q, 期望 GoDaddy", m.DisplayName)
	}
	if m.Tier != 1 {
		t.Errorf("Tier = %d, 期望 1", m.Tier)
	}
	// 能力集合必须真的是从类型断言推出来的，而不是靠人工维护的能力表。
	caps := dns.Capabilities(NewGoDaddy("http://127.0.0.1:1"))
	if !caps.ZoneList || !caps.RecordList || !caps.RecordCreate ||
		!caps.RecordUpdate || !caps.RecordDelete {
		t.Errorf("五个 CRUD 能力应当全部具备，得到 %+v", caps)
	}
}

// ---------------------------------------------------------------------------
// 鉴权
// ---------------------------------------------------------------------------

// TestGoDaddySendsSSOKeyHeader 钉住鉴权方式。
//
// GoDaddy 的鉴权是静态明文头 `sso-key key:secret`，与 Cloudflare 的
// Bearer 完全不同 —— 写错这一处，所有调用都会 401，
// 而错误信息只会说"鉴权失败"，看不出是头部格式的问题。
func TestGoDaddySendsSSOKeyHeader(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"domains":[{"domain":"example.com"}]}`)
	})

	if _, err := NewGoDaddy(srv.URL).ListZones(context.Background(), godaddyCred()); err != nil {
		t.Fatalf("列出域名失败: %v", err)
	}

	if got := f.last().Header.Get("Authorization"); got != "sso-key test-key:test-secret" {
		t.Errorf("Authorization = %q, 期望 %q", got, "sso-key test-key:test-secret")
	}
}

// TestGoDaddyCredentialNeverLeaksIntoError 是安全上最关键的一条。
//
// sso-key 头是明文的 key:secret，一旦它出现在错误信息里就是凭据泄漏 ——
// 错误信息会进日志、进界面、进用户粘贴出去的截图。
// 因此无论服务商回什么，错误文本里都不允许出现密钥。
func TestGoDaddyCredentialNeverLeaksIntoError(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 401, `{"code":"UNABLE_TO_AUTHENTICATE","message":"Unauthorized : Credential is invalid"}`)
	})

	_, err := NewGoDaddy(srv.URL).ListZones(context.Background(), godaddyCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	msg := err.Error()
	for _, secret := range []string{"test-key", "test-secret", "sso-key"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("错误信息里出现了凭据相关内容 %q: %s", secret, msg)
		}
	}
	// 但服务商的原话必须保留 —— 用户要靠它判断是密钥错了还是权限不足。
	if !strings.Contains(msg, "Credential is invalid") {
		t.Errorf("错误信息应当带上 GoDaddy 的原始说明，得到: %s", msg)
	}
}

func TestGoDaddyVerifyRequiresBothFields(t *testing.T) {
	t.Parallel()

	// 地址指向一个必然连不上的端口：缺字段时必须**在发请求之前**就失败，
	// 否则会白耗一次 GoDaddy 那本就很紧的限流配额。
	c := NewGoDaddy("http://127.0.0.1:1")

	cases := []struct {
		name   string
		fields map[string]string
	}{
		{"全缺", map[string]string{}},
		{"缺 secret", map[string]string{"api_key": "k"}},
		{"缺 key", map[string]string{"api_secret": "s"}},
		{"只有空白", map[string]string{"api_key": "  ", "api_secret": "\t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := c.ListZones(context.Background(), dns.Credential{Fields: tc.fields})
			if err == nil {
				t.Fatal("凭据不完整时应当报错")
			}
			if !strings.Contains(err.Error(), "API Key") {
				t.Errorf("错误信息应当说清缺什么，得到: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 区域（域名）
// ---------------------------------------------------------------------------

// TestGoDaddyListZonesFollowsMarkerCursor 验证游标分页被正确跟随。
//
// GoDaddy 这个接口不返回总数，唯一的分页信号是响应里的 next ——
// 而 next 是一个**完整 URL**，不是裸游标。把它们混淆（直接把 next
// 当 marker 发回去）会得到 400，且只在账号有多个域名时才会暴露。
func TestGoDaddyListZonesFollowsMarkerCursor(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("marker") == "page2" {
			writeJSONBody(w, 200, `{"domains":[{"domain":"second.com"}]}`)
			return
		}
		writeJSONBody(w, 200, `{"domains":[{"domain":"example.com"}],
		  "next":"https://api.godaddy.com/v1/domains?limit=100&marker=page2"}`)
	})

	zones, err := NewGoDaddy(srv.URL).ListZones(context.Background(), godaddyCred())
	if err != nil {
		t.Fatalf("列出域名失败: %v", err)
	}
	if len(zones) != 2 {
		t.Fatalf("应当跟随游标得到 2 个域名，得到 %d: %+v", len(zones), zones)
	}
	if zones[0].Name != "example.com" || zones[1].Name != "second.com" {
		t.Errorf("域名列表不符: %+v", zones)
	}
	// GoDaddy 没有 zone id，ID 与 Name 都必须是域名本身 ——
	// ID 留空会让后续所有记录调用拼出一个残缺的 URL。
	for _, z := range zones {
		if z.ID != z.Name {
			t.Errorf("区域 ID 应当等于域名（GoDaddy 没有独立 zone id），得到 ID=%q Name=%q", z.ID, z.Name)
		}
	}

	if f.count() != 2 {
		t.Fatalf("应当请求 2 页，实际 %d 次", f.count())
	}
	all := f.all()
	if !strings.Contains(all[0].Query, "limit=100") {
		t.Errorf("第一页应当带上 limit，查询串: %s", all[0].Query)
	}
	if strings.Contains(all[0].Query, "marker") {
		t.Errorf("第一页不该带 marker，查询串: %s", all[0].Query)
	}
	// 关键断言：回传的是从 next 里**解析出来的裸游标**，不是整个 URL。
	if !strings.Contains(all[1].Query, "marker=page2") {
		t.Errorf("第二页应当回传解析后的 marker=page2，查询串: %s", all[1].Query)
	}
	if strings.Contains(all[1].Query, "http") {
		t.Errorf("next 是完整 URL，不该被原样当成 marker 发回去: %s", all[1].Query)
	}
}

func TestGoDaddyListZonesErrorIsClassified(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 401, `{"code":"UNABLE_TO_AUTHENTICATE","message":"Unauthorized"}`)
	})

	_, err := NewGoDaddy(srv.URL).ListZones(context.Background(), godaddyCred())
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	// 界面靠它把"凭据过期了"与"服务商那边出错了"区分开。
	if !apiErr.IsUnauthorized() {
		t.Errorf("IsUnauthorized 应当为 true，状态码 %d", apiErr.Status)
	}
}

// ---------------------------------------------------------------------------
// 列出记录
// ---------------------------------------------------------------------------

// TestGoDaddyListRecordsMapsNamesAndPriority 覆盖三处最容易错的翻译：
//
//  1. 记录名：GoDaddy 用**相对名**（"@" 表示根记录），接口约定的是完整名；
//  2. TTL：0 表示"没设置"，不是"TTL 等于 0 秒"；
//  3. priority：GoDaddy 对非 MX/SRV 记录也会回一个 0，照单全收会让界面
//     把每条 A 记录都显示成"优先级 0"。
func TestGoDaddyListRecordsMapsNamesAndPriority(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains/example.com/records", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"records":[
		  {"type":"A","name":"@","data":"203.0.113.7","ttl":3600,"priority":0},
		  {"type":"A","name":"www","data":"203.0.113.8","ttl":600},
		  {"type":"MX","name":"@","data":"mail.example.com","ttl":3600,"priority":10},
		  {"type":"TXT","name":"_dmarc","data":"v=DMARC1; p=none","ttl":0}
		]}`)
	})

	records, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		godaddyZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 4 {
		t.Fatalf("应当得到 4 条记录，得到 %d: %+v", len(records), records)
	}

	// "@" 必须被还原成域名本身，否则界面会显示一条名字叫 "@" 的记录。
	if records[0].Name != "example.com" {
		t.Errorf(`根记录名 = %q, 期望 example.com（"@" 是 GoDaddy 的相对名写法）`, records[0].Name)
	}
	if records[1].Name != "www.example.com" {
		t.Errorf("子域记录名 = %q, 期望 www.example.com", records[1].Name)
	}
	if records[3].Name != "_dmarc.example.com" {
		t.Errorf("下划线开头的记录名 = %q, 期望 _dmarc.example.com", records[3].Name)
	}

	if records[0].Type != dns.TypeA || records[0].Content != "203.0.113.7" {
		t.Errorf("A 记录解析错误: %+v", records[0])
	}
	// 非 MX/SRV 的 priority=0 是"没有这个字段"的意思，不该被当成优先级。
	if records[0].Priority != 0 {
		t.Errorf("A 记录的优先级应当为 0，得到 %d", records[0].Priority)
	}
	if records[2].Priority != 10 {
		t.Errorf("MX 优先级 = %d, 期望 10", records[2].Priority)
	}
	// TTL=0 是"服务商没给"，对外统一表达为 0（未设置），不是 0 秒。
	if records[3].TTL != 0 {
		t.Errorf("ttl=0 应当保持为 0（未设置），得到 %d", records[3].TTL)
	}

	// 合成的 ID 必须能拆回三段，且第二段是**API 用的相对名**（不是完整名）——
	// Update / Delete 会把它直接拼进 URL 路径。
	typ, name, data := godaddyRecordID(t, records[1])
	if typ != "A" || name != "www" || data != "203.0.113.8" {
		t.Errorf("合成 ID 的三段 = %q/%q/%q，期望 A/www/203.0.113.8", typ, name, data)
	}
	_, rootName, _ := godaddyRecordID(t, records[0])
	if rootName != "@" {
		t.Errorf(`根记录的 ID 里应当是 "@"，得到 %q`, rootName)
	}
}

// TestGoDaddyListRecordsFiltersLocally 钉住"过滤在本地做"这个决定。
//
// GoDaddy 没有 type 查询参数；要按类型过滤只能走 /records/{type}/{name}，
// 而那条路径要求类型与名字同时给出。因此实现是"拉全量 + 本地过滤"，
// 请求次数与过滤条件的组合数无关 —— 这对 60 次/分钟的限流很重要。
func TestGoDaddyListRecordsFiltersLocally(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains/example.com/records", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"records":[
		  {"type":"A","name":"www","data":"203.0.113.7","ttl":600},
		  {"type":"AAAA","name":"www","data":"2001:db8::1","ttl":600},
		  {"type":"A","name":"home","data":"203.0.113.9","ttl":600}
		]}`)
	})

	records, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		godaddyZone(), dns.RecordFilter{Type: dns.TypeAAAA, Name: "www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Type != dns.TypeAAAA {
		t.Fatalf("应当只剩 1 条 AAAA，得到 %+v", records)
	}
	// 名字过滤匹配的是**完整名**（接口约定），不是 GoDaddy 的相对名。
	if records[0].Name != "www.example.com" {
		t.Errorf("记录名 = %q", records[0].Name)
	}
	// 一次请求就够 —— 过滤是本地做的，不该按类型各自发一次请求。
	if f.count() != 1 {
		t.Errorf("应当只发 1 次请求（本地过滤），实际 %d 次", f.count())
	}
	// 而且不该带任何服务商侧的过滤参数（GoDaddy 也不支持）。
	if q := f.last().Query; strings.Contains(q, "type=") || strings.Contains(q, "name=") {
		t.Errorf("GoDaddy 不支持按类型/名字的查询参数，不该发出: %s", q)
	}
}

func TestGoDaddyListRecordsUnusableRowsAreSkipped(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains/example.com/records", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"records":[
		  {"type":"","name":"www","data":"203.0.113.7"},
		  {"type":"A","name":"","data":"203.0.113.8"},
		  {"type":"A","name":"ok","data":"203.0.113.9"}
		]}`)
	})

	records, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		godaddyZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// 缺类型或缺名字的行无法被翻译成一条有意义的记录，丢掉它们；
	// 但**不能**因此让整次列出失败 —— 用户该看到其余的记录。
	if len(records) != 1 || records[0].Name != "ok.example.com" {
		t.Fatalf("应当只保留 1 条可用记录，得到 %+v", records)
	}
}

// TestGoDaddyListRecordsRejectsForeignName 验证"记录名不属于该域名"被拒绝。
//
// 这个校验存在的意义是防一条静默写错位置的路径：GoDaddy 的名字是相对名，
// 把完整名直接发过去，要么被拒，要么更糟 —— 在域名下创建一条名字真的
// 叫 www.other.com 的记录。宁可在这里报错。
func TestGoDaddyListRecordsRejectsForeignName(t *testing.T) {
	t.Parallel()

	_, srv := newFakeAPI(t)
	_, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		godaddyZone(), dns.RecordFilter{Name: "www.other.com"})
	if err == nil {
		t.Fatal("记录名不在该域名下时应当报错")
	}
	if !strings.Contains(err.Error(), "www.other.com") {
		t.Errorf("错误信息应当指出是哪个名字有问题，得到: %v", err)
	}
}

func TestGoDaddyListRecordsRequiresDomain(t *testing.T) {
	t.Parallel()

	_, srv := newFakeAPI(t)
	_, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		dns.Zone{}, dns.RecordFilter{})
	if err == nil {
		t.Fatal("缺少域名时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 新增记录
// ---------------------------------------------------------------------------

// TestGoDaddyCreateRecordUsesPatchWithRelativeName 覆盖新增的全部要点。
//
// 用 PATCH 而不是 PUT 是刻意的：PUT 会**替换掉**该 (type, name) 下的
// 全部记录。用户想加第二条 MX，用 PUT 的结果是第一条没了。
func TestGoDaddyCreateRecordUsesPatchWithRelativeName(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPatch, "/v1/domains/example.com/records/A/www", func(w http.ResponseWriter, _ *http.Request) {
		// GoDaddy 成功时只回 200 与一个空体 —— 没有"服务端返回的记录"可读。
		writeJSONBody(w, 200, `{}`)
	})

	rec, err := NewGoDaddy(srv.URL).CreateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	if f.last().Method != http.MethodPatch {
		t.Errorf("新增应当用 PATCH（PUT 会替换掉同名同类型的其它记录），实际 %s", f.last().Method)
	}
	// 路径里的名字必须是相对名 —— 发完整名要么被拒，要么写错位置。
	// 路由能匹配上就说明路径就是 /records/A/www。
	if f.last().Path != "/v1/domains/example.com/records/A/www" {
		t.Errorf("请求路径 = %q", f.last().Path)
	}

	body := godaddyDecodeRecordBody(t, f.last().Body)
	if body["type"] != "A" || body["name"] != "www" || body["data"] != "203.0.113.7" {
		t.Errorf("请求体不符（注意 GoDaddy 用 data 而不是 content）: %v", body)
	}
	if body["ttl"].(float64) != 600 {
		t.Errorf("ttl = %v, 期望 600", body["ttl"])
	}
	// 非 MX/SRV 不该发送 priority —— GoDaddy 会直接拒绝。
	if _, ok := body["priority"]; ok {
		t.Error("A 记录不该发送 priority")
	}

	// 返回值：GoDaddy 没有响应体可读，ID 由我们合成。
	typ, name, data := godaddyRecordID(t, rec)
	if typ != "A" || name != "www" || data != "203.0.113.7" {
		t.Errorf("返回的合成 ID = %q/%q/%q", typ, name, data)
	}
	if rec.TTL != 600 || rec.Content != "203.0.113.7" || rec.Name != "www.example.com" {
		t.Errorf("返回的记录不符: %+v", rec)
	}
}

// TestGoDaddyCreateRecordRootNameIsAt 验证根记录写成 "@"。
//
// GoDaddy 不接受把域名本身当记录名的写法；写成 "example.com" 时，
// 要么报错，要么在域名下创建一条名字真的叫 example.com 的记录。
func TestGoDaddyCreateRecordRootNameIsAt(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPatch, "/v1/domains/example.com/records/TXT/@", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{}`)
	})

	_, err := NewGoDaddy(srv.URL).CreateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{Name: "example.com", Type: dns.TypeTXT, Content: "v=spf1 -all", TTL: 3600})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	body := godaddyDecodeRecordBody(t, f.last().Body)
	if body["name"] != "@" {
		t.Errorf(`根记录的名字应当是 "@"，得到 %v`, body["name"])
	}
	if len(f.all()) != 1 {
		t.Errorf("应当只发 1 次请求，实际 %d 次", len(f.all()))
	}
}

// TestGoDaddyCreateRecordClampsTTL 验证 TTL 的归一。
//
// GoDaddy 的 TTL 下限是 600 秒、上限 86400 秒，且不接受 0。
// 直接把用户的 0 / 60 发过去只会换来一个"TTL 不合法"的报错，
// 而用户真正想表达的（"给个合理的值"）是明确的。
func TestGoDaddyCreateRecordClampsTTL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		in      int
		wantTTL float64
	}{
		{"0 表示交给服务商默认", 0, 3600},
		{"低于下限被夹到 600", 60, 600},
		{"上限之上被夹到 86400", 200000, 86400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, srv := newFakeAPI(t)
			f.on(http.MethodPatch, "/v1/domains/example.com/records/A/www", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONBody(w, 200, `{}`)
			})

			rec, err := NewGoDaddy(srv.URL).CreateRecord(context.Background(), godaddyCred(), godaddyZone(),
				dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: tc.in})
			if err != nil {
				t.Fatal(err)
			}

			body := godaddyDecodeRecordBody(t, f.last().Body)
			if body["ttl"].(float64) != tc.wantTTL {
				t.Errorf("ttl = %v, 期望 %v", body["ttl"], tc.wantTTL)
			}
			// 返回值必须反映**实际写入的** TTL，而不是用户填的那个 ——
			// 否则界面上会显示一个服务商那边并不存在的值。
			if float64(rec.TTL) != tc.wantTTL {
				t.Errorf("返回记录的 TTL = %d, 期望 %v", rec.TTL, tc.wantTTL)
			}
		})
	}
}

// TestGoDaddyCreateRecordSendsPriorityForMX 验证 MX 的优先级被发出。
//
// 与"非 MX 不发 priority"是一对：MX / SRV 缺了优先级会被 GoDaddy 拒绝。
func TestGoDaddyCreateRecordSendsPriorityForMX(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPatch, "/v1/domains/example.com/records/MX/@", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{}`)
	})

	rec, err := NewGoDaddy(srv.URL).CreateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{Name: "example.com", Type: dns.TypeMX, Content: "mail.example.com", TTL: 3600, Priority: 10})
	if err != nil {
		t.Fatal(err)
	}

	body := godaddyDecodeRecordBody(t, f.last().Body)
	if body["priority"].(float64) != 10 {
		t.Errorf("MX 的 priority = %v, 期望 10", body["priority"])
	}
	if rec.Priority != 10 {
		t.Errorf("返回记录的 Priority = %d, 期望 10", rec.Priority)
	}
}

func TestGoDaddyCreateRecordRejectsEmptyType(t *testing.T) {
	t.Parallel()

	_, srv := newFakeAPI(t)
	_, err := NewGoDaddy(srv.URL).CreateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{Name: "www.example.com", Content: "203.0.113.7"})
	if err == nil {
		t.Fatal("类型为空时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 修改记录
// ---------------------------------------------------------------------------

// TestGoDaddyUpdateRecordUsesPutOnFamilyPath 覆盖修改的全部要点。
//
// 用 PUT 是刻意的（接口约定提交的是完整记录），但它的真实语义是
// "替换掉该 (type, name) 下的**全部**记录" —— 请求路径因此只到
// (type, name)，没有也不可能有"某一条"的粒度。
func TestGoDaddyUpdateRecordUsesPutOnFamilyPath(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPut, "/v1/domains/example.com/records/A/www", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{}`)
	})

	// 模拟"从列表里读到一条记录，用户改了它的值"。
	updater := NewGoDaddy(srv.URL)
	id := encodeRecordID("A", "www", "203.0.113.7")
	rec, err := updater.UpdateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{ID: id, Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}

	if f.last().Method != http.MethodPut {
		t.Errorf("修改应当用 PUT，实际 %s", f.last().Method)
	}
	if f.last().Path != "/v1/domains/example.com/records/A/www" {
		t.Errorf("请求路径 = %q", f.last().Path)
	}

	body := godaddyDecodeRecordBody(t, f.last().Body)
	if body["data"] != "203.0.113.99" {
		t.Errorf("请求体里的记录值 = %v, 期望 203.0.113.99", body["data"])
	}
	// 新值写进去之后，旧 ID（含旧 data）就不再对应任何东西了 ——
	// 返回的必须是新 ID，否则调用方拿着它做下一次操作会失败。
	_, _, data := godaddyRecordID(t, rec)
	if data != "203.0.113.99" {
		t.Errorf("返回的合成 ID 里应当是**新**的值，得到 %q", data)
	}
	if rec.Content != "203.0.113.99" {
		t.Errorf("返回记录的内容 = %q", rec.Content)
	}
}

// TestGoDaddyUpdateWithoutIDIsRejected 验证缺少 ID 时不猜。
//
// GoDaddy 没有原生 ID，所以"没给 ID"意味着我们完全无法知道该改哪一条；
// 猜一个（例如按名字去搜）会在多条记录时改错。
func TestGoDaddyUpdateWithoutIDIsRejected(t *testing.T) {
	t.Parallel()

	_, srv := newFakeAPI(t)
	_, err := NewGoDaddy(srv.URL).UpdateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
	if err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if !strings.Contains(err.Error(), "ID") {
		t.Errorf("错误信息应当说明缺 ID，得到: %v", err)
	}
}

// TestGoDaddyUpdateRejectsMismatchedID 是"记录没有 ID"这个冲突的正面测试。
//
// 合成的 ID 里含 data，用户改了值之后 ID 就与记录对不上了。
// 如果不校验，我们会拿着一个旧 ID 去掉一条它并不指向的记录 ——
// 那是最糟的一类错误：用户以为改的是 A，实际被改的是 B，两边都不报错。
func TestGoDaddyUpdateRejectsMismatchedID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		id   string
		rec  dns.Record
	}{
		{
			// 界面上的陈旧条目：ID 还是上一次读到的那个域名/名字。
			name: "ID 指向别的名字",
			id:   encodeRecordID("A", "home", "203.0.113.7"),
			rec:  dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"},
		},
		{
			// 用户在同一个下拉里换了类型。
			name: "ID 指向别的类型",
			id:   encodeRecordID("AAAA", "www", "2001:db8::1"),
			rec:  dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, srv := newFakeAPI(t)
			tc.rec.ID = tc.id

			_, err := NewGoDaddy(srv.URL).UpdateRecord(context.Background(), godaddyCred(), godaddyZone(), tc.rec)
			if err == nil {
				t.Fatal("ID 与记录不一致时应当报错")
			}
			// 关键：**一个请求都不能发出去**，否则就已经改错记录了。
			if f.count() != 0 {
				t.Errorf("校验失败时不该发出任何请求，实际 %d 次", f.count())
			}
		})
	}
}

func TestGoDaddyUpdateRejectsUnparsableID(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	// 用户手工填的、或来自别家服务商的 ID。
	_, err := NewGoDaddy(srv.URL).UpdateRecord(context.Background(), godaddyCred(), godaddyZone(),
		dns.Record{ID: "r1", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
	if err == nil {
		t.Fatal("无法解析的 ID 应当报错")
	}
	if f.count() != 0 {
		t.Errorf("校验失败时不该发出任何请求，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 删除记录
// ---------------------------------------------------------------------------

// TestGoDaddyDeleteRecordTargetsNameTypeFamily 记录 DeleteRecord 的真实语义。
//
// 接口写的是"删除一条记录"，但 GoDaddy 只有
// DELETE /records/{type}/{name} —— 它删掉的是该**组合下的全部**记录。
// 这个测试把实际发出的请求钉下来，好让读代码的人一眼看到：
// 请求路径里没有、也不可能有"某一条"的粒度。
func TestGoDaddyDeleteRecordTargetsNameTypeFamily(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/v1/domains/example.com/records/A/www", func(w http.ResponseWriter, _ *http.Request) {
		// GoDaddy 删除成功回 204 且无响应体。
		w.WriteHeader(http.StatusNoContent)
	})

	// 注意这个 ID 是"两条 A 记录里的第二条"—— 删除时两条都会没。
	id := encodeRecordID("A", "www", "203.0.113.8")
	err := NewGoDaddy(srv.URL).DeleteRecord(context.Background(), godaddyCred(), godaddyZone(), id)
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	if f.last().Method != http.MethodDelete {
		t.Errorf("应当用 DELETE，实际 %s", f.last().Method)
	}
	// 路径只到 (type, name)：data 段被丢掉是**设计如此**，
	// 因为 GoDaddy 无法只删一条值。这正是必须如实告知用户的后果。
	if f.last().Path != "/v1/domains/example.com/records/A/www" {
		t.Errorf("请求路径 = %q（应当只到 type/name，没有单条粒度）", f.last().Path)
	}
	if strings.Contains(f.last().Path, "203.0.113.8") {
		t.Error("请求路径里不该出现记录值 —— GoDaddy 的删除没有单条粒度")
	}
}

func TestGoDaddyDeleteRequiresID(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	err := NewGoDaddy(srv.URL).DeleteRecord(context.Background(), godaddyCred(), godaddyZone(), "")
	if err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("校验失败时不该发出请求，实际 %d 次", f.count())
	}
}

// TestGoDaddyDeleteNotFoundIsExplained 验证 404 被翻译成人话。
//
// 对一次删除来说，"已不存在"其实是成功，但 GoDaddy 回的是 404。
// 用户需要知道"没删成是因为它本来就不在了"，而不是一句 HTTP 404。
func TestGoDaddyDeleteNotFoundIsExplained(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/v1/domains/example.com/records/A/gone", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 404, `{"code":"NOT_FOUND","message":"Resource not found"}`)
	})

	err := NewGoDaddy(srv.URL).DeleteRecord(context.Background(), godaddyCred(), godaddyZone(),
		encodeRecordID("A", "gone", "203.0.113.7"))
	if err == nil {
		t.Fatal("期望报错")
	}

	// 错误类型仍然可被上层识别（%w 保留了原始错误）——
	// 界面靠它区分"记录不在了"与"限流了/凭据失效了"。
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当仍可解析成 *APIError，得到 %T: %v", err, err)
	}
	if !apiErr.IsNotFound() {
		t.Errorf("IsNotFound 应当为 true，状态码 %d", apiErr.Status)
	}
	// 本地化的说明 + 服务商的原始说明都要在。
	if !strings.Contains(err.Error(), "未执行任何删除") {
		t.Errorf("错误信息应当说明没删成，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "Resource not found") {
		t.Errorf("错误信息应当带上 GoDaddy 的原始说明，得到: %v", err)
	}
}

// TestGoDaddyRateLimitedIsReported 验证 429 被如实转达。
//
// GoDaddy 的限流很紧（多数接口约 60 次/分钟），429 是会真实发生的。
// 用户需要看到"被限流了，等一会儿"而不是一句无解的"操作失败"。
func TestGoDaddyRateLimitedIsReported(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains/example.com/records", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		writeJSONBody(w, 429, `{"code":"TOO_MANY_REQUESTS","message":"Too many requests"}`)
	})

	_, err := NewGoDaddy(srv.URL).ListRecords(context.Background(), godaddyCred(),
		godaddyZone(), dns.RecordFilter{})
	if err == nil {
		t.Fatal("期望报错")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Status != http.StatusTooManyRequests {
		t.Errorf("状态码 = %d, 期望 429", apiErr.Status)
	}
	if !strings.Contains(err.Error(), "Too many requests") {
		t.Errorf("错误信息应当带上 GoDaddy 的原始说明，得到: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 响应与错误体的结构
// ---------------------------------------------------------------------------

func TestGoDaddyMalformedResponseIsReported(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v1/domains", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `this is not json`)
	})

	_, err := NewGoDaddy(srv.URL).ListZones(context.Background(), godaddyCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	// 错误信息不该原样带上整个响应体（它可能很大，也可能含账号信息）。
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长，可能把响应体整个塞了进去: %d 字符", len(err.Error()))
	}
}

// TestGoDaddyErrorTextUsesStringCode 钉住 GoDaddy 错误体的形态。
//
// code 是**字符串**（"UNABLE_TO_AUTHENTICATE"），不是数字。
// 按数字解析会得到空串，而空 code 会让用户失去唯一的排查线索。
func TestGoDaddyErrorTextUsesStringCode(t *testing.T) {
	t.Parallel()

	got := godaddyErrorText([]byte(
		`{"code":"INVALID_BODY","message":"Request body is invalid"}`))
	if !strings.Contains(got, "INVALID_BODY") || !strings.Contains(got, "Request body is invalid") {
		t.Errorf("错误文本 = %q，应当同时带上 code 与 message", got)
	}

	// 没有 code 时只给 message，不要拼出一个空的方括号。
	got = godaddyErrorText([]byte(`{"message":"Record name is invalid"}`))
	if got != "Record name is invalid" {
		t.Errorf("错误文本 = %q", got)
	}

	// 完全解析不出来时给一句可读的兜底，而不是空串 ——
	// 空错误信息在界面上就是一片空白，用户无从下手。
	got = godaddyErrorText([]byte(`<html>gateway timeout</html>`))
	if strings.TrimSpace(got) == "" {
		t.Error("无法解析的错误体也应当有兜底文案")
	}
}
