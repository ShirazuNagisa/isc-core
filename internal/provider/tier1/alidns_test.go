package tier1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件只写阿里云专属的用例。
//
// 假服务与断言辅助（fakeAPI / recordedRequest / writeJSONBody / asAPIError）
// 来自 cloudflare_test.go —— 那是 Tier-1 的参考测试，这里直接复用，
// 不再定义第二份（重复定义会让整个包编译不过）。
//
// 云解析是 RPC 风格：所有操作都打在同一个路径上，靠 Action 参数区分，
// 因此路由用的是 fakeAPI 的兜底处理器 + 一次 Action 分发，而不是按路径注册。

// alidnsCred 是测试用的凭据。
func alidnsCred() dns.Credential {
	return dns.Credential{
		ID: "c1", Provider: "alidns",
		Fields: map[string]string{
			"access_key_id":     "test-access-key-id",
			"access_key_secret": "test-access-key-secret",
		},
	}
}

// newAlidnsFake 起一个假云解析服务，并返回指向它的实现。
func newAlidnsFake(t *testing.T) (*fakeAPI, *Alidns) {
	t.Helper()
	f, srv := newFakeAPI(t)
	return f, NewAlidns(srv.URL)
}

// alidnsOK 注册"按 Action 返回固定响应"的假服务。
func alidnsOK(f *fakeAPI, byAction map[string]string) {
	f.setFallback(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		body, ok := byAction[action]
		if !ok {
			writeJSONBody(w, http.StatusBadRequest,
				`{"Code":"UnsupportedOperation","Message":"fake: 未注册的 Action `+action+`"}`)
			return
		}
		writeJSONBody(w, http.StatusOK, body)
	})
}

// alidnsFail 注册"任何请求都返回同一个错误"的假服务。
func alidnsFail(f *fakeAPI, status int, body string) {
	f.setFallback(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, status, body)
	})
}

// alidnsRequest 找出请求过的某个 Action，找不到就让测试失败。
func alidnsRequest(t *testing.T, f *fakeAPI, action string) recordedRequest {
	t.Helper()
	for _, req := range f.all() {
		q, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatalf("查询串无法解析: %v", err)
		}
		if q.Get("Action") == action {
			return req
		}
	}
	t.Fatalf("没有发出 Action=%s 的请求，实际发了 %d 次", action, f.count())
	return recordedRequest{}
}

// alidnsParams 把一次请求的查询串解析成参数表。
func alidnsParams(t *testing.T, req recordedRequest) url.Values {
	t.Helper()
	q, err := url.ParseQuery(req.Query)
	if err != nil {
		t.Fatalf("查询串无法解析: %v", err)
	}
	return q
}

// ---------------------------------------------------------------------------
// 元信息与端点
// ---------------------------------------------------------------------------

func TestAlidnsMeta(t *testing.T) {
	t.Parallel()

	m := NewAlidns("").Meta()
	// Name 必须与 internal/provider/builtin.go 里登记的标识、以及 ddns-go
	// 配置导入用的名字完全一致，否则导入的凭据找不到实现。
	if m.Name != "alidns" {
		t.Errorf("Name = %q, 期望 alidns", m.Name)
	}
	if m.DisplayName != "阿里云 DNS" {
		t.Errorf("DisplayName = %q", m.DisplayName)
	}
	if m.Tier != 1 {
		t.Errorf("Tier = %d, 期望 1", m.Tier)
	}
}

// TestAlidnsDefaultEndpoint 钉住默认端点与 URL 形状。
//
// URL 形状（根路径 + 查询串）是 RPC 风格接口的一部分：多一个斜杠就会变成
// "//?Action=..."，真实服务端会把它当成另一个路径。
func TestAlidnsDefaultEndpoint(t *testing.T) {
	t.Parallel()

	cl := NewAlidns("").clientFor("")
	if got := cl.URL("/?Action=DescribeDomains"); got != "https://alidns.aliyuncs.com/?Action=DescribeDomains" {
		t.Errorf("默认端点拼出的 URL = %q", got)
	}

	_, srv := newFakeAPI(t)
	if got := NewAlidns(srv.URL).baseURL; got != srv.URL {
		t.Errorf("传入的基址被改动了: %q", got)
	}
}

// ---------------------------------------------------------------------------
// 记录名 ↔ RR 转换
// ---------------------------------------------------------------------------

// TestAlidnsRRConversion 覆盖阿里云实现里最容易出错的一步。
func TestAlidnsRRConversion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		zone    string
		record  string
		want    string
		wantErr bool
	}{
		{name: "根记录", zone: "example.com", record: "example.com", want: "@"},
		{name: "根记录带尾点与大写", zone: "example.com", record: "EXAMPLE.COM.", want: "@"},
		{name: "根记录带空白", zone: " example.com ", record: " example.com ", want: "@"},
		{name: "一级子域", zone: "example.com", record: "www.example.com", want: "www"},
		{name: "多级子域", zone: "example.com", record: "a.b.example.com", want: "a.b"},
		{name: "保留调用方大小写", zone: "example.com", record: "WWW.example.com", want: "WWW"},
		{name: "子域带尾点", zone: "example.com", record: "www.example.com.", want: "www"},
		{name: "直接给 @", zone: "example.com", record: "@", want: "@"},
		{name: "ddns-go 风格的根记录", zone: "example.com", record: "@.example.com", want: "@"},

		{name: "别的区域", zone: "example.com", record: "www.other.com", wantErr: true},
		{name: "后缀相同但不是子域", zone: "example.com", record: "notexample.com", wantErr: true},
		{name: "只有主机记录", zone: "example.com", record: "www", wantErr: true},
		{name: "空前缀", zone: "example.com", record: ".example.com", wantErr: true},
		{name: "记录名为空", zone: "example.com", record: "", wantErr: true},
		{name: "区域名为空", zone: "", record: "www.example.com", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := alidnsRR(tc.zone, tc.record)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 RR=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望 RR=%q，实际报错: %v", tc.want, err)
			}
			if got != tc.want {
				t.Fatalf("RR = %q, 期望 %q", got, tc.want)
			}

			// 反过来必须还原成同一个完整记录名：RR 拆错和拼错都会让
			// "刚写完的记录"与"列表拉回来的记录"对不上。
			zone := alidnsTrimName(tc.zone)
			want := alidnsTrimName(tc.record)
			if tc.want == "@" {
				want = zone
			}
			if full := alidnsFullName(zone, got); full != want {
				t.Errorf("还原记录名 = %q, 期望 %q", full, want)
			}
		})
	}
}

// TestAlidnsFullName 覆盖读取方向：服务端返回的 RR 要拼回完整域名。
func TestAlidnsFullName(t *testing.T) {
	t.Parallel()

	cases := []struct{ rr, want string }{
		{"@", "example.com"}, // 根记录
		{"", "example.com"},  // 防御：服务端理论上不会这么返回
		{"www", "www.example.com"},
		{"a.b", "a.b.example.com"},
		{"example.com", "example.com"}, // 防御：有账号真的把域名填进 RR
	}
	for _, tc := range cases {
		if got := alidnsFullName("example.com", tc.rr); got != tc.want {
			t.Errorf("alidnsFullName(%q) = %q, 期望 %q", tc.rr, got, tc.want)
		}
	}
	// 区域名带尾点时也不能拼出 "www.example.com.."
	if got := alidnsFullName("example.com.", "www"); got != "www.example.com" {
		t.Errorf("区域名带尾点时拼出 %q", got)
	}
}

// ---------------------------------------------------------------------------
// 签名与公共参数
// ---------------------------------------------------------------------------

// TestAlidnsSignatureCoversSentParams 验证签名覆盖"实际发出去的每一个参数"。
//
// 假服务端不会替我们检查签名，而真实服务端只会回一句 SignatureDoesNotMatch ——
// 那是极难从现象反推原因的一类失败。这里用移植过来的签名函数对收到的参数
// 重算一次：算法本身不重写，重算的是"参数集合与请求方法"。
func TestAlidnsSignatureCoversSentParams(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomains": `{"TotalCount":0,"Domains":{"Domain":[]}}`,
	})

	if _, err := a.ListZones(context.Background(), alidnsCred()); err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	req := f.last()
	if req.Method != http.MethodGet {
		t.Errorf("请求方法 = %s, 期望 GET（签名就是按 GET 算的）", req.Method)
	}

	q := alidnsParams(t, req)
	if got := q.Get("Action"); got != "DescribeDomains" {
		t.Errorf("Action = %q", got)
	}
	if got := q.Get("Version"); got != "2015-01-09" {
		t.Errorf("Version = %q, 期望 2015-01-09", got)
	}
	if got := q.Get("Format"); got != "JSON" {
		t.Errorf("Format = %q", got)
	}
	if got := q.Get("SignatureMethod"); got != "HMAC-SHA1" {
		t.Errorf("SignatureMethod = %q", got)
	}
	if got := q.Get("SignatureVersion"); got != "1.0" {
		t.Errorf("SignatureVersion = %q", got)
	}
	if got := q.Get("AccessKeyId"); got != "test-access-key-id" {
		t.Errorf("AccessKeyId = %q", got)
	}
	if got := q.Get("Timestamp"); got == "" {
		t.Error("缺少 Timestamp")
	}
	if got := q.Get("SignatureNonce"); got == "" {
		t.Error("缺少 SignatureNonce")
	}

	sent := q.Get("Signature")
	if sent == "" {
		t.Fatal("请求里没有 Signature")
	}
	// 签名是对"不含 Signature 的整组参数"算出来的，因此去掉它再算一次应当一致。
	unsigned := url.Values{}
	for k, v := range q {
		if k == "Signature" {
			continue
		}
		unsigned[k] = v
	}
	want := ddnsgo.HmacSignToB64("HMAC-SHA1", http.MethodGet, "test-access-key-secret", unsigned)
	if sent != want {
		t.Errorf("签名与实际发出的参数不匹配：\n发出 %s\n重算 %s", sent, want)
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// alidnsDomainPage 拼一个 DescribeDomains 响应：fill 条填充数据 + 一条 extra。
func alidnsDomainPage(total, fill int, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `{"TotalCount":%d,"PageNumber":1,"PageSize":%d,"Domains":{"Domain":[`,
		total, alidnsDomainsPerPage)
	for i := 0; i < fill; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"DomainId":"d%d","DomainName":"host%d.example.com"}`, i, i)
	}
	if extra != "" {
		if fill > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(extra)
	}
	sb.WriteString(`]}}`)
	return sb.String()
}

func TestAlidnsListZones(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	f.setFallback(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("PageNumber") == "2" {
			// 第二页顺带覆盖中文域名的两种形式：原文优先，原文缺失才退回 punycode。
			writeJSONBody(w, 200,
				`{"TotalCount":102,"PageNumber":2,"Domains":{"Domain":[
					{"DomainId":"d-idn","DomainName":"例子.测试","PunyCode":"xn--fsqu00a.xn--0zwm56d"},
					{"DomainId":"d-puny","PunyCode":"xn--fsqu00a.xn--0zwm56d"}
				]}}`)
			return
		}
		// 第一页取满（100 条），最后一条既没有 DomainId，域名还带着空白 ——
		// 阿里云文档的响应示例里 DomainName 就带着一个换行。
		writeJSONBody(w, 200, alidnsDomainPage(102, 99, `{"DomainName":"  spaced.example.com\n"}`))
	})

	zones, err := a.ListZones(context.Background(), alidnsCred())
	if err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}
	if len(zones) != 102 {
		t.Fatalf("应当跟随分页取到 102 个域名，得到 %d", len(zones))
	}
	if f.count() != 2 {
		t.Errorf("应当请求 2 页，实际 %d 次", f.count())
	}

	spaced := zones[99]
	if spaced.Name != "spaced.example.com" {
		t.Errorf("域名没有被清理干净: %q", spaced.Name)
	}
	// 没有 DomainId 时退回用域名 —— 阿里云的一切记录操作本来就按域名定位。
	if spaced.ID != "spaced.example.com" {
		t.Errorf("DomainId 缺失时应当退回域名，得到 %q", spaced.ID)
	}
	if zones[100].Name != "例子.测试" {
		t.Errorf("中文域名应当用原文: %q", zones[100].Name)
	}
	if zones[101].Name != "xn--fsqu00a.xn--0zwm56d" {
		t.Errorf("原文缺失时应当退回 punycode: %q", zones[101].Name)
	}

	q := alidnsParams(t, f.all()[0])
	if q.Get("PageSize") != "100" {
		t.Errorf("PageSize = %q, 期望 100（服务端上限）", q.Get("PageSize"))
	}
}

// TestAlidnsListZonesError 验证错误信息带上服务商的原始说明。
func TestAlidnsListZonesError(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsFail(f, http.StatusBadRequest,
		`{"RequestId":"req-1","HostId":"alidns.aliyuncs.com","Code":"InvalidAccessKeyId.NotFound","Message":"Specified access key is not found."}`)

	_, err := a.ListZones(context.Background(), alidnsCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	// 光秃秃的 "HTTP 400" 没法告诉用户该去改什么。
	if !strings.Contains(err.Error(), "Specified access key is not found.") {
		t.Errorf("错误信息应当包含服务商说明，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "列出区域") {
		t.Errorf("错误信息应当说明在做什么，得到: %v", err)
	}

	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Code != "InvalidAccessKeyId.NotFound" {
		t.Errorf("错误码 = %q", apiErr.Code)
	}

	// 凭据绝不进错误信息：AccessKeyId 就在查询串里，一旦有人把 URL 塞进
	// 错误信息，它就会跟着进日志和审计。Secret 更是绝不能出现。
	for _, secret := range []string{"test-access-key-secret", "test-access-key-id"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("错误信息里出现了凭据: %v", err)
		}
	}
}

func TestAlidnsListZonesMalformedResponse(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	f.setFallback(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `this is not json`)
	})

	_, err := a.ListZones(context.Background(), alidnsCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	// 不能把整个响应体原样塞进错误信息：它可能很大，也可能含账号信息。
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长: %d 字符", len(err.Error()))
	}
}

// TestAlidnsMissingCredential 验证缺凭据时本地就报错，且不打印凭据。
func TestAlidnsMissingCredential(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	err := func() error {
		_, err := a.ListZones(context.Background(), dns.Credential{Fields: map[string]string{"access_key_id": "only-id"}})
		return err
	}()
	if err == nil {
		t.Fatal("缺少 Secret 时应当报错")
	}
	if !strings.Contains(err.Error(), "AccessKey") {
		t.Errorf("错误信息应当指明缺哪个字段: %v", err)
	}
	if f.count() != 0 {
		t.Errorf("凭据不全时不该发出请求，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 记录：列表
// ---------------------------------------------------------------------------

// alidnsRecordPage 拼一个 DescribeDomainRecords 响应：第一条是 extra，
// 后面跟 fill 条填充记录。
func alidnsRecordPage(total int, extra string, fill int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, `{"TotalCount":%d,"PageNumber":1,"PageSize":%d,"DomainRecords":{"Record":[`,
		total, alidnsRecordsPerPage)
	if extra != "" {
		sb.WriteString(extra)
		if fill > 0 {
			sb.WriteString(",")
		}
	}
	for i := 0; i < fill; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb,
			`{"RecordId":"f%d","DomainName":"example.com","RR":"host%d","Type":"A","Value":"203.0.113.7","TTL":600}`,
			i, i)
	}
	sb.WriteString(`]}}`)
	return sb.String()
}

func TestAlidnsListRecords(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecords": `{"TotalCount":3,"PageNumber":1,"PageSize":500,"DomainRecords":{"Record":[
			{"RecordId":"r1","DomainName":"example.com\n","RR":"@","Type":"A","Value":"203.0.113.7","TTL":600,"Line":"default","Status":"Enable","Remark":"家庭宽带"},
			{"RecordId":"r2","DomainName":"example.com","RR":"www","Type":"A","Value":"203.0.113.8","TTL":1800,"Line":"default"},
			{"RecordId":"r3","DomainName":"example.com","RR":"@","Type":"MX","Value":"mail.example.com","TTL":3600,"Priority":10,"Line":"default"}
		]}}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	records, err := a.ListRecords(context.Background(), alidnsCred(), zone, dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("应当得到 3 条记录，得到 %d", len(records))
	}

	// 根记录的 RR 是 "@"，对外要还原成完整域名。
	if records[0].Name != "example.com" {
		t.Errorf("根记录名 = %q, 期望 example.com", records[0].Name)
	}
	if records[1].Name != "www.example.com" {
		t.Errorf("子域记录名 = %q", records[1].Name)
	}
	if records[1].ID != "r2" || records[1].TTL != 1800 {
		t.Errorf("记录字段不符: %+v", records[1])
	}
	if records[2].Priority != 10 {
		t.Errorf("MX 优先级 = %d, 期望 10", records[2].Priority)
	}
	// 备注在写入接口里没有参数，但读得回来。
	if records[0].Comment != "家庭宽带" {
		t.Errorf("备注 = %q, 期望 家庭宽带", records[0].Comment)
	}
	// 阿里云没有 CDN 代理开关。
	if records[0].Proxied {
		t.Error("Proxied 应当恒为 false")
	}

	q := alidnsParams(t, f.last())
	if q.Get("DomainName") != "example.com" {
		t.Errorf("DomainName = %q", q.Get("DomainName"))
	}
	if q.Get("PageSize") != "500" {
		t.Errorf("PageSize = %q, 期望 500（服务端上限）", q.Get("PageSize"))
	}
	if q.Get("PageNumber") != "1" {
		t.Errorf("PageNumber = %q", q.Get("PageNumber"))
	}
}

// TestAlidnsListRecordsFilterIsExact 验证过滤是精确匹配。
//
// 服务端的 RRKeyWord 是模糊匹配：查 www 会把 wwww 也带回来。
// 接口层的承诺是"Name 精确匹配"，因此客户端必须复核。
func TestAlidnsListRecordsFilterIsExact(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecords": `{"TotalCount":3,"DomainRecords":{"Record":[
			{"RecordId":"r1","DomainName":"example.com","RR":"www","Type":"A","Value":"203.0.113.7","TTL":600},
			{"RecordId":"r2","DomainName":"example.com","RR":"wwww","Type":"A","Value":"203.0.113.8","TTL":600},
			{"RecordId":"r3","DomainName":"example.com","RR":"www","Type":"AAAA","Value":"2001:db8::1","TTL":600}
		]}}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	records, err := a.ListRecords(context.Background(), alidnsCred(), zone,
		dns.RecordFilter{Name: "www.example.com", Type: dns.TypeA})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 1 || records[0].ID != "r1" {
		t.Fatalf("精确过滤后应当只剩 r1，得到 %+v", records)
	}

	q := alidnsParams(t, f.last())
	if q.Get("RRKeyWord") != "www" {
		t.Errorf("RRKeyWord = %q, 期望 www（服务端先粗筛）", q.Get("RRKeyWord"))
	}
	if q.Get("TypeKeyWord") != "A" {
		t.Errorf("TypeKeyWord = %q", q.Get("TypeKeyWord"))
	}
}

// TestAlidnsListRecordsRootFilterSkipsKeyword 钉住根记录过滤的取舍。
//
// 根记录的 RR 是 "@" 这个占位符，而 RRKeyWord 是模糊匹配 —— 万一服务端不把
// "@" 当普通字面量检索，用户查根记录会拿到一个假的空列表。多翻几页可以接受，
// 骗人不行，所以根记录不下发 RRKeyWord。
func TestAlidnsListRecordsRootFilterSkipsKeyword(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecords": `{"TotalCount":1,"DomainRecords":{"Record":[
			{"RecordId":"r1","DomainName":"example.com","RR":"@","Type":"A","Value":"203.0.113.7","TTL":600}
		]}}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	records, err := a.ListRecords(context.Background(), alidnsCred(), zone,
		dns.RecordFilter{Name: "example.com"})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 1 || records[0].Name != "example.com" {
		t.Fatalf("根记录过滤结果不符: %+v", records)
	}
	if q := alidnsParams(t, f.last()); q.Has("RRKeyWord") {
		t.Errorf("根记录不该下发 RRKeyWord，实际 %q", q.Get("RRKeyWord"))
	}

	// 过滤器里写 "@"（阿里云/腾讯云/ddns-go 都在用的写法）时，
	// 要按完整域名去比，否则什么都匹配不上。
	records, err = a.ListRecords(context.Background(), alidnsCred(), zone,
		dns.RecordFilter{Name: "@"})
	if err != nil {
		t.Fatalf("用 @ 过滤失败: %v", err)
	}
	if len(records) != 1 || records[0].Name != "example.com" {
		t.Fatalf("用 @ 过滤根记录的结果不符: %+v", records)
	}
}

func TestAlidnsListRecordsRequiresZoneName(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	// 阿里云按域名定位记录，没有域名就无从查起 —— 不能悄悄返回空列表。
	_, err := a.ListRecords(context.Background(), alidnsCred(), dns.Zone{ID: "d1"}, dns.RecordFilter{})
	if err == nil {
		t.Fatal("区域名为空时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("不该发出请求，实际 %d 次", f.count())
	}
}

// TestAlidnsListRecordsPagination 验证记录列表也跟随分页。
func TestAlidnsListRecordsPagination(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	f.setFallback(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("PageNumber") == "2" {
			writeJSONBody(w, 200, `{"TotalCount":501,"PageNumber":2,"DomainRecords":{"Record":[
				{"RecordId":"r-last","DomainName":"example.com","RR":"last","Type":"A","Value":"203.0.113.9","TTL":600}
			]}}`)
			return
		}
		writeJSONBody(w, 200, alidnsRecordPage(501, "", 500))
	})

	records, err := a.ListRecords(context.Background(), alidnsCred(),
		dns.Zone{ID: "d1", Name: "example.com"}, dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 501 {
		t.Fatalf("应当跟随分页取到 501 条记录，得到 %d", len(records))
	}
	if records[500].ID != "r-last" {
		t.Errorf("第二页的数据不对: %+v", records[500])
	}
	if f.count() != 2 {
		t.Errorf("应当请求 2 页，实际 %d 次", f.count())
	}
}

// TestAlidnsMorePages 覆盖分页结束条件。
//
// 阿里云返回的是总数而不是总页数，两个信号（本页是否取满、总数）都要看：
// 只看总数会在 TotalCount 缺失时漏掉后面的页，只看页大小会在服务端把
// PageSize 截小时多跑一次空请求。
func TestAlidnsMorePages(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                            string
		pageItems, seen, total, perPage int
		want                            bool
	}{
		{name: "空页", pageItems: 0, seen: 0, total: 100, perPage: 100, want: false},
		{name: "取满且还没到总数", pageItems: 100, seen: 100, total: 250, perPage: 100, want: true},
		{name: "取满且刚好到总数", pageItems: 100, seen: 100, total: 100, perPage: 100, want: false},
		{name: "总数缺失时按页大小继续", pageItems: 100, seen: 100, total: 0, perPage: 100, want: true},
		{name: "服务端截小了页大小", pageItems: 3, seen: 3, total: 250, perPage: 100, want: false},
		{name: "没过半也继续", pageItems: 100, seen: 200, total: 0, perPage: 100, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := alidnsMorePages(tc.pageItems, tc.seen, tc.total, tc.perPage); got != tc.want {
				t.Errorf("alidnsMorePages(%d,%d,%d,%d) = %v, 期望 %v",
					tc.pageItems, tc.seen, tc.total, tc.perPage, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 记录：新增
// ---------------------------------------------------------------------------

func TestAlidnsCreateRecord(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"AddDomainRecord": `{"RequestId":"req-1","RecordId":"new-1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	rec, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7",
		TTL: 600, Proxied: true, Comment: "不该被发送",
	})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	req := alidnsRequest(t, f, "AddDomainRecord")
	if req.Method != http.MethodGet {
		t.Errorf("请求方法 = %s, 期望 GET", req.Method)
	}
	q := alidnsParams(t, req)
	want := map[string]string{
		"DomainName": "example.com",
		"RR":         "www",
		"Type":       "A",
		"Value":      "203.0.113.7",
		"TTL":        "600",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, 期望 %q", k, got, v)
		}
	}
	// 线路不传：缺省就是默认线路，替用户猜一个反而会出错。
	if q.Has("Line") {
		t.Errorf("新增时不该传 Line，实际 %q", q.Get("Line"))
	}
	// 调用方没设优先级就不发这个参数。
	if q.Has("Priority") {
		t.Errorf("A 记录不该传 Priority，实际 %q", q.Get("Priority"))
	}
	// 阿里云的写接口没有备注参数。
	if q.Has("Remark") {
		t.Errorf("阿里云没有 Remark 参数，不该发送")
	}

	// 写接口只回 RecordId，其余字段按调用方给的回显。
	if rec.ID != "new-1" {
		t.Errorf("返回的记录 ID = %q", rec.ID)
	}
	if rec.Name != "www.example.com" {
		t.Errorf("返回的记录名 = %q", rec.Name)
	}
	if rec.TTL != 600 {
		t.Errorf("返回的 TTL = %d", rec.TTL)
	}
	// 阿里云没有代理开关与备注，回显时不能假装写进去了。
	if rec.Proxied || rec.Comment != "" {
		t.Errorf("不该回显未生效的字段: %+v", rec)
	}
}

// TestAlidnsCreateRecordRootDomain 是最容易出错的一条：根记录的 RR 必须是 "@"。
//
// 填成空串或完整域名，阿里云要么报错、要么（更糟）在 example.com 下建一条
// 永远解析不到的 www.example.com.example.com。
func TestAlidnsCreateRecordRootDomain(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"AddDomainRecord": `{"RequestId":"req-1","RecordId":"root-1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	rec, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		Name: "example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600,
	})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	q := alidnsParams(t, alidnsRequest(t, f, "AddDomainRecord"))
	if got := q.Get("RR"); got != "@" {
		t.Fatalf("根记录的 RR = %q, 必须是 @", got)
	}
	if got := q.Get("DomainName"); got != "example.com" {
		t.Errorf("DomainName = %q", got)
	}
	if rec.Name != "example.com" {
		t.Errorf("回显的记录名 = %q, 期望完整域名 example.com", rec.Name)
	}
}

// TestAlidnsCreateRecordDefaultTTL 验证 TTL=0 被翻译成阿里云的默认值 600。
func TestAlidnsCreateRecordDefaultTTL(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"AddDomainRecord": `{"RequestId":"req-1","RecordId":"new-1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	rec, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 0,
	})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	if got := alidnsParams(t, f.last()).Get("TTL"); got != "600" {
		t.Errorf("TTL = %q, 期望 600（阿里云文档的默认值）", got)
	}
	// 回显的是实际写进去的值，不是 0：否则"刚创建的记录"与"列表拉回来的
	// 记录"会凭空多出一处差异。
	if rec.TTL != 600 {
		t.Errorf("回显的 TTL = %d, 期望 600", rec.TTL)
	}
}

// TestAlidnsCreateRecordTTLIsNotClamped 验证用户明确给的 TTL 原样透传。
//
// 不同版本的最小 TTL 不同（免费版 600 秒、企业版 1 秒），客户端猜一个下限
// 只会把"服务端能说清楚的一次拒绝"变成"用户设了 60，实际写进去 600"。
func TestAlidnsCreateRecordTTLIsNotClamped(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"AddDomainRecord": `{"RequestId":"req-1","RecordId":"new-1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	if _, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 1,
	}); err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if got := alidnsParams(t, f.last()).Get("TTL"); got != "1" {
		t.Errorf("TTL = %q, 期望原样透传 1", got)
	}
}

// TestAlidnsMXPriority 验证 MX 的优先级只在调用方给了正值时才发送。
func TestAlidnsMXPriority(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		priority int
		want     string
	}{
		{name: "有优先级", priority: 10, want: "10"},
		{name: "没给优先级", priority: 0, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, a := newAlidnsFake(t)
			alidnsOK(f, map[string]string{
				"AddDomainRecord": `{"RequestId":"req-1","RecordId":"mx-1"}`,
			})

			zone := dns.Zone{ID: "d1", Name: "example.com"}
			if _, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
				Name: "example.com", Type: dns.TypeMX, Content: "mail.example.com",
				TTL: 600, Priority: tc.priority,
			}); err != nil {
				t.Fatalf("新增失败: %v", err)
			}
			if got := alidnsParams(t, f.last()).Get("Priority"); got != tc.want {
				t.Errorf("Priority = %q, 期望 %q", got, tc.want)
			}
		})
	}
}

// TestAlidnsCreateRecordRejectsForeignName 验证不会把外域记录名写进本区域。
func TestAlidnsCreateRecordRejectsForeignName(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"AddDomainRecord": `{"RequestId":"req-1","RecordId":"new-1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	_, err := a.CreateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		Name: "www.other.com", Type: dns.TypeA, Content: "203.0.113.7",
	})
	if err == nil {
		t.Fatal("记录名不在该区域下时应当报错")
	}
	// 绝不能"猜一个前缀"写出去：那会静默建出一条错误但看不出来的记录。
	if f.count() != 0 {
		t.Errorf("参数不合法时不该发出请求，实际 %d 次", f.count())
	}
	for _, want := range []string{"www.other.com", "example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息应当同时点出记录名与区域名（缺 %q）: %v", want, err)
		}
	}
}

// ---------------------------------------------------------------------------
// 记录：修改
// ---------------------------------------------------------------------------

func TestAlidnsUpdateRecord(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecordInfo": `{"RecordId":"r1","DomainName":"example.com","RR":"www","Type":"A",
			"Value":"203.0.113.7","TTL":600,"Line":"default","Status":"Enable"}`,
		"UpdateDomainRecord": `{"RequestId":"req-2","RecordId":"r1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	rec, err := a.UpdateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		ID: "r1", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600,
	})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}

	// 先读回原记录（为了线路），再写。
	if f.count() != 2 {
		t.Fatalf("应当先读后写，共 2 次请求，实际 %d 次", f.count())
	}
	if got := alidnsParams(t, alidnsRequest(t, f, "DescribeDomainRecordInfo")).Get("RecordId"); got != "r1" {
		t.Errorf("读回记录时 RecordId = %q", got)
	}

	q := alidnsParams(t, alidnsRequest(t, f, "UpdateDomainRecord"))
	want := map[string]string{
		"RecordId": "r1",
		"RR":       "www",
		"Type":     "A",
		"Value":    "203.0.113.99",
		"TTL":      "600",
		"Line":     "default",
	}
	for k, v := range want {
		if got := q.Get(k); got != v {
			t.Errorf("%s = %q, 期望 %q", k, got, v)
		}
	}
	if rec.ID != "r1" || rec.Content != "203.0.113.99" || rec.Name != "www.example.com" {
		t.Errorf("返回的记录不符: %+v", rec)
	}
}

// TestAlidnsUpdateRecordPreservesLine 是本文件里最要紧的一条断言。
//
// dns.Record 里没有 Line 字段，而阿里云的修改接口是整条覆盖写、文档只说
// 省略 Line 时"默认为 default"。若真的按默认线路写回去，用户把记录挂在
// "境外"这类线路上时，一次"只改 IP"的操作就会把解析线路悄悄改掉 ——
// 而且没有任何人会立刻发现。
func TestAlidnsUpdateRecordPreservesLine(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecordInfo": `{"RecordId":"r1","DomainName":"example.com","RR":"www","Type":"A",
			"Value":"203.0.113.7","TTL":600,"Line":"cn_mobile_anhui","Status":"Enable"}`,
		"UpdateDomainRecord": `{"RequestId":"req-2","RecordId":"r1"}`,
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	if _, err := a.UpdateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		ID: "r1", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600,
	}); err != nil {
		t.Fatalf("修改失败: %v", err)
	}

	if got := alidnsParams(t, alidnsRequest(t, f, "UpdateDomainRecord")).Get("Line"); got != "cn_mobile_anhui" {
		t.Errorf("Line = %q, 期望把原线路 cn_mobile_anhui 原样送回", got)
	}
}

// TestAlidnsUpdateRecordFailsWhenLineLookupFails 钉住"宁可失败也不静默损坏"。
//
// 读不回原线路时，我们无法知道这条记录挂在哪条线路上。此时如果照写不误，
// 就可能把用户的线路改回默认；让这次修改失败并把服务端原话交给用户，
// 用户至少知道发生了什么，重试一次就行。
func TestAlidnsUpdateRecordFailsWhenLineLookupFails(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DescribeDomainRecordInfo": "",
	})
	f.setFallback(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Action") == "DescribeDomainRecordInfo" {
			writeJSONBody(w, http.StatusBadRequest,
				`{"Code":"InvalidRecordId.NotFound","Message":"The specified record id does not exist."}`)
			return
		}
		writeJSONBody(w, 200, `{"RequestId":"req-2","RecordId":"r1"}`)
	})

	zone := dns.Zone{ID: "d1", Name: "example.com"}
	_, err := a.UpdateRecord(context.Background(), alidnsCred(), zone, dns.Record{
		ID: "r1", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600,
	})
	if err == nil {
		t.Fatal("读不回原记录时应当报错")
	}
	if !strings.Contains(err.Error(), "The specified record id does not exist.") {
		t.Errorf("错误信息应当带上服务端原话: %v", err)
	}
	// 关键：一次写请求都不能发出去。
	if f.count() != 1 {
		t.Fatalf("应当只发了读回记录那一次请求，实际 %d 次", f.count())
	}
	if alidnsRequest(t, f, "DescribeDomainRecordInfo").Method == "" {
		t.Fatal("读回记录的请求没有发出")
	}
	for _, req := range f.all() {
		if alidnsParams(t, req).Get("Action") == "UpdateDomainRecord" {
			t.Error("读不回原线路时不该继续修改记录")
		}
	}
}

func TestAlidnsUpdateRequiresID(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	_, err := a.UpdateRecord(context.Background(), alidnsCred(),
		dns.Zone{ID: "d1", Name: "example.com"},
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
	if err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("不该发出请求，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 记录：删除
// ---------------------------------------------------------------------------

func TestAlidnsDeleteRecord(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsOK(f, map[string]string{
		"DeleteDomainRecord": `{"RequestId":"req-3","RecordId":"r1"}`,
	})

	// 刻意只给 ID、不给区域名：阿里云按 RecordId 删除，不需要域名，
	// 而"只知道 ID 却删不掉"没有任何好处。
	if err := a.DeleteRecord(context.Background(), alidnsCred(), dns.Zone{ID: "d1"}, "r1"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	q := alidnsParams(t, alidnsRequest(t, f, "DeleteDomainRecord"))
	if q.Get("RecordId") != "r1" {
		t.Errorf("RecordId = %q", q.Get("RecordId"))
	}
}

func TestAlidnsDeleteRequiresID(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	if err := a.DeleteRecord(context.Background(), alidnsCred(), dns.Zone{ID: "d1"}, "  "); err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("不该发出请求，实际 %d 次", f.count())
	}
}

// TestAlidnsDeleteNotFound 验证"记录不存在"的错误能带着服务商原话传上来。
//
// 注意阿里云把这类错误表达成 HTTP 400 + Code（而不是 404），所以接口层
// 要按 Code 判断，不能只看 APIError.IsNotFound()。
func TestAlidnsDeleteNotFound(t *testing.T) {
	t.Parallel()

	f, a := newAlidnsFake(t)
	alidnsFail(f, http.StatusBadRequest,
		`{"Code":"InvalidRecordId.NotFound","Message":"The specified record id does not exist."}`)

	err := a.DeleteRecord(context.Background(), alidnsCred(), dns.Zone{ID: "d1"}, "gone")
	if err == nil {
		t.Fatal("期望报错")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Code != "InvalidRecordId.NotFound" {
		t.Errorf("错误码 = %q", apiErr.Code)
	}
	if !strings.Contains(err.Error(), "The specified record id does not exist.") {
		t.Errorf("错误信息应当带上服务商原话: %v", err)
	}
}

// TestAlidnsResponseIsJSON 做一次形状检查：假服务返回的每个响应体都能被
// 我们自己的结构体解析。字段名写错时（例如把 RecordId 写成 RecordID），
// 上面的用例会因为取到空值而失败，这里再兜一层，直接指出是哪个响应。
func TestAlidnsResponseIsJSON(t *testing.T) {
	t.Parallel()

	samples := []struct {
		name string
		body string
		into any
	}{
		{name: "DescribeDomains", into: &alidnsDomainsResp{},
			body: `{"TotalCount":2,"Domains":{"Domain":[{"DomainId":"d1","DomainName":"example.com","PunyCode":""}]}}`},
		{name: "DescribeDomainRecords", into: &alidnsRecordsResp{},
			body: `{"TotalCount":1,"DomainRecords":{"Record":[{"RecordId":"r1","RR":"@","Type":"A","Value":"1.1.1.1","TTL":600,"Line":"default"}]}}`},
		{name: "DescribeDomainRecordInfo", into: &alidnsRecord{},
			body: `{"RecordId":"r1","RR":"www","Type":"A","Value":"1.1.1.1","TTL":600,"Line":"default","Priority":5}`},
		{name: "写接口", into: &alidnsRecordIDResp{},
			body: `{"RequestId":"req-1","RecordId":"r1"}`},
	}
	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := json.Unmarshal([]byte(tc.body), tc.into); err != nil {
				t.Fatalf("响应体无法解析: %v", err)
			}
		})
	}

	// 解析出来的字段必须真的落在结构体上。
	var resp alidnsRecordsResp
	if err := json.Unmarshal([]byte(samples[1].body), &resp); err != nil {
		t.Fatal(err)
	}
	got := resp.DomainRecords.Record
	if len(got) != 1 || got[0].RecordID != "r1" || got[0].RR != "@" || got[0].TTL != 600 {
		t.Errorf("记录字段没解析对: %+v", got)
	}
}
