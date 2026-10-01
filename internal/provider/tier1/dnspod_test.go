package tier1

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件是 DNSPod（dnsapi.cn 传统 API）实现的契约测试。
//
// 结构照 cloudflare_test.go：起一个假服务商，断言"发出的请求长什么样"
// 与"响应被翻译成了什么"。fakeAPI / recordedRequest / writeJSONBody /
// asAPIError 都在那个文件里，这里只写 DNSPod 专属的用例。
//
// 这套 API 与别家最大的差别是**请求体是表单而不是 JSON**，
// 所以下面几乎每条用例都会用 dnspodFormOf 取参数 —— 它顺带断言了
// Content-Type 是表单，而那是 DNSPod 唯一接受的编码方式。

// 测试凭据的两个字段值。
//
// 刻意取得足够特别：有专门一条用例断言它们不会出现在错误信息里
// （请求体里带凭据，一旦被回显进错误信息就是凭据泄漏）。
const (
	dnspodTestID    = "123456"
	dnspodTestToken = "dnspod-token-9f8e7d6c"
)

// dnspodCred 返回一份 DNSPod 凭据。
//
// 字段名就是 id / token（不是别家的 access_key_id / api_token），
// 这是 builtin.go 里 fieldDNSPodID / fieldDNSPodToken 声明的那两个。
func dnspodCred() dns.Credential {
	return dns.Credential{
		ID:       "cred-1",
		Provider: "dnspod",
		Fields: map[string]string{
			"id":    dnspodTestID,
			"token": dnspodTestToken,
		},
	}
}

// dnspodZone 返回测试用的区域。
func dnspodZone() dns.Zone {
	return dns.Zone{ID: "9842292", Name: "example.com"}
}

// dnspodFormOf 把假服务器记录下来的请求体解析成表单。
//
// 两个用途：取值，以及断言"请求体确实是表单"。
// DNSPod 只吃 application/x-www-form-urlencoded；换成 JSON 过去，
// 服务端一个参数都取不到，只会回一句"参数不合法"。
func dnspodFormOf(t *testing.T, req recordedRequest) url.Values {
	t.Helper()
	if ct := req.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
		t.Fatalf("Content-Type = %q，DNSPod 只接受表单", ct)
	}
	form, err := url.ParseQuery(req.Body)
	if err != nil {
		t.Fatalf("请求体不是合法表单: %v（body=%q）", err, req.Body)
	}
	return form
}

// ---------------------------------------------------------------------------
// 元信息与接口能力
// ---------------------------------------------------------------------------

func TestDnspodMeta(t *testing.T) {
	t.Parallel()

	d := NewDnspod("")
	meta := d.Meta()
	// Name 必须与 internal/provider/builtin.go 里的注册名一致，
	// 否则凭据的 provider 字段对不上，界面里找不到这家。
	if meta.Name != "dnspod" {
		t.Errorf("Name = %q，期望 dnspod", meta.Name)
	}
	if meta.DisplayName != "DNSPod" {
		t.Errorf("DisplayName = %q，期望 DNSPod", meta.DisplayName)
	}
	if meta.Tier != 1 {
		t.Errorf("Tier = %d，DNSPod 有完整记录 CRUD，应当是 1", meta.Tier)
	}
	// baseURL 为空时必须落到官方地址，否则线上会往空地址发请求。
	if d.baseURL != dnspodDefaultBase {
		t.Errorf("baseURL = %q，期望 %q", d.baseURL, dnspodDefaultBase)
	}

	// 五个能力接口都要在能力集合里 —— 界面靠它决定哪些按钮可点。
	set := dns.Capabilities(d)
	if !set.ZoneList || !set.RecordList || !set.RecordCreate || !set.RecordUpdate || !set.RecordDelete {
		t.Errorf("能力集合不完整: %+v", set)
	}
	// 本次改动没有实现 dns.Verifier，因此能力集合里不该出现"测试连接"。
	if set.Verify {
		t.Error("Dnspod 没有实现 dns.Verifier，能力集合里不该有 Verify")
	}
}

// ---------------------------------------------------------------------------
// 请求形态：表单、凭据、公共参数
// ---------------------------------------------------------------------------

func TestDnspodSendsFormNotJSON(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"Action completed successful"},"domains":[]}`)
	})

	if _, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred()); err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	req := f.last()
	// DNSPod 只支持 POST（"只支持POST方法请求数据，用其它方法会提示错误"）。
	if req.Method != http.MethodPost {
		t.Errorf("Method = %q，期望 POST", req.Method)
	}
	if body := strings.TrimSpace(req.Body); strings.HasPrefix(body, "{") {
		t.Fatalf("请求体看起来是 JSON: %q", body)
	}

	form := dnspodFormOf(t, req)

	// 鉴权：ID 与 Token 用英文逗号拼成**一个**参数。
	if got := form.Get("login_token"); got != dnspodTestID+","+dnspodTestToken {
		t.Errorf("login_token = %q，期望 \"<ID>,<Token>\"", got)
	}
	// format 必须显式传：DNSPod 的默认返回格式是 xml，而本实现全部按 JSON 解析。
	if got := form.Get("format"); got != "json" {
		t.Errorf("format = %q，必须显式要 json（默认是 xml）", got)
	}
	// lang=cn：用户可见的错误里会带上服务商的原文，英文说明对国内用户没用。
	if got := form.Get("lang"); got != "cn" {
		t.Errorf("lang = %q，期望 cn", got)
	}
	// error_on_empty=no：没有数据时返回空列表，而不是错误码。
	if got := form.Get("error_on_empty"); got != "no" {
		t.Errorf("error_on_empty = %q，期望 no", got)
	}
	// 分页参数从 0 开始。
	if got := form.Get("offset"); got != "0" {
		t.Errorf("offset = %q，期望 0（第一条记录是 0）", got)
	}
	if got := form.Get("length"); got == "" {
		t.Error("缺少 length 参数，分页就无从谈起")
	}

	// User-Agent：DNSPod 的接口规范要求按"名称/版本(联系方式)"声明，
	// 不设置或伪装成浏览器可能导致整个账号的 API 被封。
	if got := req.Header.Get("User-Agent"); got != dnspodUserAgent {
		t.Errorf("User-Agent = %q，期望 %q", got, dnspodUserAgent)
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// TestDnspodListZones 覆盖分页、状态过滤，以及"数字形态的域名 ID"。
//
// Domain.List 的官方示例里 id 是**数字**（"id": 2238269），
// 而 Domain.Info 里是字符串 —— 解析不出来就是拿不到 domain_id，
// 后面所有记录操作都会失准。
func TestDnspodListZones(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		if f.count() > 1 {
			// 第二页：一条正常域名（取不满一页 -> 结束）。
			writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
			  "domains":[{"id":10360095,"name":"second.com","punycode":"second.com","status":"enable"}]}`)
			return
		}
		// 第一页：必须**取满一页**才会触发翻页，因此程序化地拼 99 条，
		// 外加一条暂停解析的域名（它应当被过滤掉）。
		var b strings.Builder
		b.WriteString(`{"status":{"code":"1","message":"ok"},"info":{"domain_total":101},"domains":[`)
		for i := 0; i < 99; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, `{"id":%d,"name":"d%d.example.com","status":"enable"}`, 1000+i, i)
		}
		b.WriteString(`,{"id":2000,"name":"paused.example.com","status":"pause"}]}`)
		writeJSONBody(w, 200, b.String())
	})

	zones, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
	if err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	if f.count() != 2 {
		t.Errorf("应当请求 2 页，实际 %d 次", f.count())
	}
	if len(zones) != 100 {
		t.Fatalf("应当得到 100 个可用区域（99+1，pause 被滤掉），得到 %d", len(zones))
	}
	// 数字形态的 ID 必须原样变成字符串，不能变成 1e+03 之类的东西。
	if zones[0].ID != "1000" || zones[0].Name != "d0.example.com" {
		t.Errorf("第一个区域 = %+v，期望 ID=1000 Name=d0.example.com", zones[0])
	}
	if zones[99].Name != "second.com" {
		t.Errorf("最后一个区域 = %+v，期望 second.com（分页没跟上？）", zones[99])
	}
	for _, z := range zones {
		if z.Name == "paused.example.com" {
			// 暂停解析的域名改了记录不会生效，列出来只会让用户以为"我改了但没用"。
			t.Error("pause 状态的域名不该出现在列表里")
		}
	}
}

// TestDnspodListZonesEmptyAccount 覆盖"账号下没有域名"。
//
// DNSPod 对这种情况返回业务码 9（没有任何域名）。那不是故障，是事实，
// 报成错误会让刚注册的用户一打开界面就看到一条红色提示。
func TestDnspodListZonesEmptyAccount(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"9","message":"没有任何域名"}}`)
	})

	zones, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
	if err != nil {
		t.Fatalf("空账号不该报错: %v", err)
	}
	if len(zones) != 0 {
		t.Errorf("期望空列表，得到 %+v", zones)
	}
}

// TestDnspodListZonesErrorMessageIsSurfaced 验证用户能看到服务商的原始说明。
func TestDnspodListZonesErrorMessageIsSurfaced(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		// 凭据不对时 DNSPod 同样走 HTTP 200，失败写在 status.code 里。
		writeJSONBody(w, 200, `{"status":{"code":"-1","message":"登陆失败"}}`)
	})

	_, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	if !strings.Contains(err.Error(), "登陆失败") {
		t.Errorf("错误信息必须带上服务商的原始说明，得到: %v", err)
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Code != "-1" {
		t.Errorf("Code = %q，期望 -1", apiErr.Code)
	}
}

// TestDnspodStatusCodeIsComparedAsString 是本文件最要紧的一条。
//
// status.code 是**字符串**："1" 表示成功，其它任何值都是失败。
// 失败码（"-1"、"-2"、"6"、"8"…）也是字符串，一旦拿它做数值比较，
// 所有失败都会变成"成功"——界面显示已保存，记录其实没动。
//
// 数字形态的 1 也认：同一套 API 在不同接口之间换过字段类型
// （id / ttl 都换过），多认一种写法不会误判，而只认一种的代价是
// 整家服务商直接解析失败。
func TestDnspodStatusCodeIsComparedAsString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "字符串 1 是成功", body: `{"status":{"code":"1","message":"ok"},"domains":[]}`},
		{name: "数字 1 也当成功", body: `{"status":{"code":1,"message":"ok"},"domains":[]}`},
		{name: "字符串 -1 是失败", body: `{"status":{"code":"-1","message":"登陆失败"}}`, wantErr: true},
		{name: "字符串 0 是失败", body: `{"status":{"code":"0","message":"未知错误"}}`, wantErr: true},
		{name: "数字 0 是失败", body: `{"status":{"code":0,"message":"未知错误"}}`, wantErr: true},
		{name: "数字 -2 是失败", body: `{"status":{"code":-2,"message":"API使用超出限制"}}`, wantErr: true},
		{name: "code 缺失按失败处理", body: `{"status":{"message":"没有 code"}}`, wantErr: true},
		{name: "没有 status 块按失败处理", body: `{}`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, srv := newFakeAPI(t)
			f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONBody(w, 200, tc.body)
			})

			_, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
			if tc.wantErr && err == nil {
				t.Fatal("期望报错，实际成功 —— code 的判断方式错了")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望成功，实际报错: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 记录列表
// ---------------------------------------------------------------------------

func TestDnspodListRecords(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.List", func(w http.ResponseWriter, _ *http.Request) {
		// 照官方示例的形态：id / ttl / mx / enabled 都是字符串，
		// 名字是主机记录（根域名是 "@"）而不是完整域名。
		writeJSONBody(w, 200, `{
		  "status":{"code":"1","message":"Action completed successful"},
		  "domain":{"id":9842292,"name":"example.com","punycode":"example.com","ttl":600},
		  "info":{"sub_domains":"2","record_total":"3"},
		  "records":[
		    {"id":"44146112","name":"@","line":"默认","line_id":"0","type":"A",
		     "ttl":"600","value":"203.0.113.7","mx":"0","enabled":"1","remark":"官网"},
		    {"id":44146113,"name":"www","line":"默认","line_id":"0","type":"AAAA",
		     "ttl":"1200","value":"2001:db8::1","mx":"0","enabled":"0"},
		    {"id":"44146114","name":"@","line":"默认","line_id":"0","type":"MX",
		     "ttl":"600","value":"mail.example.com","mx":"10","enabled":"1"}
		  ]
		}`)
	})

	records, err := NewDnspod(srv.URL).ListRecords(context.Background(), dnspodCred(),
		dnspodZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("应当得到 3 条记录，得到 %d", len(records))
	}

	// 根域名：DNSPod 用 "@"，统一模型要的是完整记录名。
	if records[0].Name != "example.com" {
		t.Errorf("根记录的 Name = %q，期望 example.com（\"@\" 要翻译成区域名）", records[0].Name)
	}
	if records[0].TTL != 600 {
		t.Errorf("TTL = %d，期望 600（字符串形态的 \"600\" 要解析成整数）", records[0].TTL)
	}
	if records[0].Comment != "官网" {
		t.Errorf("备注 = %q，期望 官网（remark 对应 Comment）", records[0].Comment)
	}

	// 数字形态的 id 同样要保真。
	if records[1].ID != "44146113" || records[1].Name != "www.example.com" {
		t.Errorf("第二条记录 = %+v，期望 ID=44146113 Name=www.example.com", records[1])
	}
	if records[1].TTL != 1200 {
		t.Errorf("TTL = %d，期望 1200", records[1].TTL)
	}

	// mx 就是优先级。
	if records[2].Type != dns.TypeMX || records[2].Priority != 10 {
		t.Errorf("MX 记录 = %+v，期望 Priority=10", records[2])
	}
}

// TestDnspodListRecordsFilter 验证过滤：名字交给服务商，类型在本地做。
func TestDnspodListRecordsFilter(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{
		  "status":{"code":"1","message":"ok"},
		  "domain":{"name":"example.com","punycode":"example.com"},
		  "records":[
		    {"id":"1","name":"home","type":"A","ttl":"600","value":"203.0.113.9","mx":"0","enabled":"1"},
		    {"id":"2","name":"home","type":"AAAA","ttl":"600","value":"2001:db8::9","mx":"0","enabled":"1"}
		  ]
		}`)
	})

	records, err := NewDnspod(srv.URL).ListRecords(context.Background(), dnspodCred(),
		dnspodZone(), dns.RecordFilter{Name: "home.example.com", Type: dns.TypeAAAA})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 1 || records[0].Type != dns.TypeAAAA {
		t.Fatalf("类型过滤没有生效（Record.List 不支持按类型过滤，必须在本地做）: %+v", records)
	}

	form := dnspodFormOf(t, f.last())
	// 完整记录名要翻译成主机记录再交给服务商。
	if got := form.Get("sub_domain"); got != "home" {
		t.Errorf("sub_domain = %q，期望 home", got)
	}
	// record_type 不是 Record.List 的文档化参数，不该出现在请求里。
	if _, ok := form["record_type"]; ok {
		t.Error("Record.List 没有按类型过滤的参数，不该发送 record_type")
	}
	// 区域用 domain_id 定位（比名字稳定）。
	if got := form.Get("domain_id"); got != "9842292" {
		t.Errorf("domain_id = %q，期望 9842292", got)
	}
}

// TestDnspodListRecordsForeignNameIsNotSentAsSubDomain 验证对不上的名字不会
// 被当成 sub_domain 发出去。
//
// 那种情况下 DNSPod 会用"22 子域名不合法"把整次查询拒掉，
// 而一个过滤条件不该让整次查询失败：正确的结果是"没有匹配项"。
func TestDnspodListRecordsForeignNameIsNotSentAsSubDomain(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
		  "domain":{"name":"example.com","punycode":"example.com"},
		  "records":[{"id":"1","name":"www","type":"A","ttl":"600","value":"203.0.113.7","mx":"0","enabled":"1"}]}`)
	})

	records, err := NewDnspod(srv.URL).ListRecords(context.Background(), dnspodCred(),
		dnspodZone(), dns.RecordFilter{Name: "www.other.com"})
	if err != nil {
		t.Fatalf("对不上的名字不该让整次查询失败: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("期望没有匹配项，得到 %+v", records)
	}
	if _, ok := dnspodFormOf(t, f.last())["sub_domain"]; ok {
		t.Error("不在区域之下的名字不该作为 sub_domain 发给服务商")
	}
}

// ---------------------------------------------------------------------------
// 新增记录
// ---------------------------------------------------------------------------

func TestDnspodCreateRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Create", func(w http.ResponseWriter, _ *http.Request) {
		// 官方示例：新增的响应只有 id / name / status。
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"Action completed successful"},
		  "record":{"id":"16894439","name":"www","status":"enable"}}`)
	})

	rec, err := NewDnspod(srv.URL).CreateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600,
		})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if rec.ID != "16894439" {
		t.Errorf("返回的记录 ID = %q，期望 16894439", rec.ID)
	}
	// 响应里没有完整记录，只能回显提交的内容（不这么做只会返回一条零值记录）。
	if rec.Name != "www.example.com" || rec.Content != "203.0.113.7" || rec.TTL != 600 {
		t.Errorf("回显的记录不符: %+v", rec)
	}

	form := dnspodFormOf(t, f.last())
	want := map[string]string{
		"domain_id":   "9842292",
		"sub_domain":  "www",
		"record_type": "A",
		"value":       "203.0.113.7",
		"ttl":         "600",
		"record_line": "默认",
		"status":      "enable",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q，期望 %q", k, got, v)
		}
	}
	// A 记录不该带 mx。
	if _, ok := form["mx"]; ok {
		t.Error("A 记录不该发送 mx")
	}
}

// TestDnspodCreateRecordRootAndMX 覆盖根域名（"@"）与 MX 的优先级。
func TestDnspodCreateRecordRootAndMX(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Create", func(w http.ResponseWriter, _ *http.Request) {
		// 数字形态的 id：解析不出来就定位不到刚建的记录。
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
		  "record":{"id":16894440,"name":"@","status":"enable"}}`)
	})

	rec, err := NewDnspod(srv.URL).CreateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			Name: "example.com", Type: dns.TypeMX, Content: "mail.example.com",
			// TTL 0 = 交给服务商默认，优先级没填。
		})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if rec.ID != "16894440" {
		t.Errorf("数字形态的记录 ID 没解析对: %q", rec.ID)
	}

	form := dnspodFormOf(t, f.last())
	// 根域名在 DNSPod 里是 "@"。
	if got := form.Get("sub_domain"); got != "@" {
		t.Errorf("sub_domain = %q，期望 @（根域名）", got)
	}
	// 缺了 mx 会被 DNSPod 用"30 MX 值错误"拒绝，这里兜底成 10。
	if got := form.Get("mx"); got != "10" {
		t.Errorf("mx = %q，期望 10（MX 记录的优先级必填）", got)
	}
	// TTL 为 0 表示"交给服务商默认"，不传这个参数。
	if _, ok := form["ttl"]; ok {
		t.Errorf("TTL=0 时不该发送 ttl，实际 %q", form.Get("ttl"))
	}
}

// TestDnspodCreateRecordRejectsOutOfRangeTTL 验证越界 TTL 在本地就被拦下。
//
// 服务商那边只会回一句"32 记录的TTL值超出了限制"，用户不知道边界在哪。
func TestDnspodCreateRecordRejectsOutOfRangeTTL(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	_, err := NewDnspod(srv.URL).CreateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7",
			TTL: 700000,
		})
	if err == nil {
		t.Fatal("越界的 TTL 应当报错")
	}
	if !strings.Contains(err.Error(), "604800") {
		t.Errorf("错误信息应当说明允许的范围，得到: %v", err)
	}
	if f.count() != 0 {
		t.Errorf("本地就能判定为非法的请求不该发出去，实际发了 %d 次", f.count())
	}
}

// TestDnspodCreateRecordRequiresZoneName 是一条防"改错地方"的用例。
//
// DNSPod 把"没有 sub_domain 参数"解释成 "@"。也就是说，如果区域名缺失时
// 我们退化成不发送 sub_domain，一条 www 的记录会被**静默地加到根域名上** ——
// 用户完全看不到这件事发生。所以这里必须报错。
func TestDnspodCreateRecordRequiresZoneName(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	_, err := NewDnspod(srv.URL).CreateRecord(context.Background(), dnspodCred(),
		dns.Zone{ID: "9842292"}, // 没有名字
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
	if err == nil {
		t.Fatal("缺少区域名时应当报错，而不是把记录加到根域名上")
	}
	if f.count() != 0 {
		t.Errorf("不该发出任何请求，实际发了 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 修改记录
// ---------------------------------------------------------------------------

// dnspodCurrentRecord 是 Record.Info 返回的"当前记录"。
//
// 刻意放在一条**非默认线路**（联通 / record_line_id=10=1）上：
// 修改记录时线路必须原样带回，否则用户的联通线路记录会被搬到默认线路上。
const dnspodCurrentRecord = `{
  "status":{"code":"1","message":"Action completed successful"},
  "domain":{"id":9842292,"domain":"example.com","domain_grade":"DP_Free"},
  "record":{"id":"44146112","sub_domain":"www","record_type":"A",
            "record_line":"联通","record_line_id":"10=1","value":"203.0.113.7",
            "weight":null,"mx":"0","ttl":"600","enabled":"1","remark":"旧备注"}
}`

func TestDnspodUpdateRecordPreservesLineAndCurrentFields(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, dnspodCurrentRecord)
	})
	f.on(http.MethodPost, "/Record.Modify", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
		  "record":{"id":44146112,"name":"www","value":"203.0.113.99","status":"enable"}}`)
	})

	rec, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			ID: "44146112", Name: "www.example.com", Type: dns.TypeA,
			Content: "203.0.113.99",
			// TTL 0 = "交给服务商默认"：这里必须理解成"保留当前值"，
			// 而不是把记录重置成某个我们并不知道的默认值。
		})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}

	// 先读后写：两次请求。
	if f.count() != 2 {
		t.Fatalf("应当先 Record.Info 再 Record.Modify（共 2 次请求），实际 %d 次", f.count())
	}
	modify := f.last()
	if modify.Path != "/Record.Modify" {
		t.Fatalf("最后一次请求打到了 %s", modify.Path)
	}

	// record_line_id 形如 "10=1"，其中的 '=' 必须被百分号编码
	//（官方文档专门提醒过这一点）。
	if !strings.Contains(modify.Body, "record_line_id=10%3D1") {
		t.Errorf("线路 ID 必须原样带回并转义：%s", modify.Body)
	}

	form := dnspodFormOf(t, modify)
	want := map[string]string{
		"record_id":      "44146112",
		"sub_domain":     "www",
		"record_type":    "A",
		"value":          "203.0.113.99",
		"ttl":            "600",  // 保留当前值
		"record_line_id": "10=1", // 保留当前线路
		"status":         "enable",
	}
	for k, v := range want {
		if got := form.Get(k); got != v {
			t.Errorf("%s = %q，期望 %q", k, got, v)
		}
	}
	// 用 record_line_id 定位线路时不该再混一个中文线路名。
	if _, ok := form["record_line"]; ok {
		t.Error("已经带了 record_line_id，不该再发送 record_line")
	}

	// 回显实际提交的值。
	if rec.ID != "44146112" || rec.Name != "www.example.com" ||
		rec.Content != "203.0.113.99" || rec.TTL != 600 {
		t.Errorf("回显的记录不符: %+v", rec)
	}
}

// TestDnspodUpdateRecordSkipsNoChange 覆盖"零变动"的拦阻。
//
// 官方文档：一小时内提交超过 5 次**没有任何变动**的修改请求，记录会被锁定
// 一小时；接口规范里更把"记录内容没有任何改变的刷新"直接列为滥用行为。
// 既然已经读了当前记录，就该比对一次再决定发不发。
func TestDnspodUpdateRecordSkipsNoChange(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, dnspodCurrentRecord)
	})
	f.on(http.MethodPost, "/Record.Modify", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("记录没有任何变动，不该发出 Record.Modify")
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"}}`)
	})

	rec, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			ID: "44146112", Name: "www.example.com", Type: dns.TypeA,
			Content: "203.0.113.7", TTL: 600,
		})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if f.count() != 1 {
		t.Errorf("零变动的修改不该发出去，实际请求 %d 次", f.count())
	}
	// 返回的应当是服务商那边的当前值。
	if rec.TTL != 600 || rec.Content != "203.0.113.7" || rec.Name != "www.example.com" {
		t.Errorf("返回的记录不符: %+v", rec)
	}
}

// TestDnspodUpdateRecordKeepsDisabledRecordsDisabled 验证不会把暂停的记录
// 悄悄启用。
//
// dns.Record 里没有"启用/暂停"的位置，于是"修改记录时该发什么 status"
// 就成了一个必须自己决定的问题：发 enable 会破坏用户暂停掉的记录。
func TestDnspodUpdateRecordKeepsDisabledRecordsDisabled(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
		  "record":{"id":"44146112","sub_domain":"www","record_type":"A",
		            "record_line":"默认","record_line_id":"0","value":"203.0.113.7",
		            "mx":"0","ttl":"600","enabled":"0"}}`)
	})
	f.on(http.MethodPost, "/Record.Modify", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},"record":{"id":44146112}}`)
	})

	_, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			ID: "44146112", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99",
		})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if got := dnspodFormOf(t, f.last()).Get("status"); got != "disable" {
		t.Errorf("status = %q，期望 disable（当前记录是暂停状态，不能被改回启用）", got)
	}
}

// TestDnspodUpdateRecordPreservesMXPriority 验证调用方没给优先级时保留当前值。
//
// MX 的 mx 是必填，但"调用方没填"不等于"把优先级改成 10"——
// 用户原来的 20 得留着。
func TestDnspodUpdateRecordPreservesMXPriority(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},
		  "record":{"id":"44146114","sub_domain":"@","record_type":"MX",
		            "record_line":"默认","record_line_id":"0","value":"mail.example.com",
		            "mx":"20","ttl":"600","enabled":"1"}}`)
	})
	f.on(http.MethodPost, "/Record.Modify", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"},"record":{"id":44146114}}`)
	})

	rec, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			ID: "44146114", Name: "example.com", Type: dns.TypeMX, Content: "mail2.example.com",
		})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	form := dnspodFormOf(t, f.last())
	if got := form.Get("mx"); got != "20" {
		t.Errorf("mx = %q，期望保留当前值 20", got)
	}
	if got := form.Get("sub_domain"); got != "@" {
		t.Errorf("sub_domain = %q，期望 @（不传会被 DNSPod 当成 @，但显式发送才是对的）", got)
	}
	if rec.Priority != 20 {
		t.Errorf("回显的优先级 = %d，期望 20", rec.Priority)
	}
}

func TestDnspodUpdateRecordRequiresID(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	_, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{Name: "www.example.com", Type: dns.TypeA})
	if err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("不该发出任何请求，实际 %d 次", f.count())
	}
}

// TestDnspodUpdateRecordInfoFailureIsExplained 验证"先读后写"里的读失败
// 不会退化成"不读直接改"。
//
// 读不到当前记录就不知道它在哪条线路上；此时硬改只会把用户的线路记录
// 搬到默认线路，或者被服务商以"26 记录线路错误"拒绝。正确做法是停下。
func TestDnspodUpdateRecordInfoFailureIsExplained(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Info", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"8","message":"记录ID错误"}}`)
	})
	f.on(http.MethodPost, "/Record.Modify", func(w http.ResponseWriter, _ *http.Request) {
		t.Error("读不到当前记录时不该继续修改")
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"ok"}}`)
	})

	_, err := NewDnspod(srv.URL).UpdateRecord(context.Background(), dnspodCred(),
		dnspodZone(), dns.Record{
			ID: "44146112", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99",
		})
	if err == nil {
		t.Fatal("期望报错")
	}
	if !strings.Contains(err.Error(), "记录ID错误") {
		t.Errorf("错误信息应当带上服务商的原始说明，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "线路") {
		t.Errorf("错误信息应当说明为什么要先读一次，得到: %v", err)
	}
	if f.count() != 1 {
		t.Errorf("应当只发了一次 Record.Info，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 删除记录
// ---------------------------------------------------------------------------

func TestDnspodDeleteRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Remove", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"1","message":"Action completed successful"}}`)
	})

	if err := NewDnspod(srv.URL).DeleteRecord(context.Background(), dnspodCred(),
		dnspodZone(), "44146112"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	form := dnspodFormOf(t, f.last())
	if got := form.Get("record_id"); got != "44146112" {
		t.Errorf("record_id = %q，期望 44146112", got)
	}
	if got := form.Get("domain_id"); got != "9842292" {
		t.Errorf("domain_id = %q，期望 9842292", got)
	}
}

func TestDnspodDeleteRequiresID(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	if err := NewDnspod(srv.URL).DeleteRecord(context.Background(), dnspodCred(),
		dnspodZone(), "  "); err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("不该发出任何请求，实际 %d 次", f.count())
	}
}

// TestDnspodDeleteErrorIsClassified 验证业务失败（HTTP 200 + code 8）
// 也能被上层识别，并带上服务商的说明。
func TestDnspodDeleteErrorIsClassified(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Record.Remove", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"8","message":"记录ID错误"}}`)
	})

	err := NewDnspod(srv.URL).DeleteRecord(context.Background(), dnspodCred(),
		dnspodZone(), "gone")
	if err == nil {
		t.Fatal("期望报错")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Code != "8" {
		t.Errorf("Code = %q，期望 8", apiErr.Code)
	}
	if !strings.Contains(err.Error(), "记录ID错误") {
		t.Errorf("错误信息应当带上服务商的原始说明，得到: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 边界与安全
// ---------------------------------------------------------------------------

// TestDnspodCredentialNeverLeaksIntoError 是一条硬性要求的用例。
//
// 凭据就在请求体里，因此请求体一旦被回显进错误信息，凭据就进了日志与审计。
func TestDnspodCredentialNeverLeaksIntoError(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"status":{"code":"-1","message":"登陆失败"}}`)
	})

	_, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
	if err == nil {
		t.Fatal("期望报错")
	}

	// 先确认请求里确实带了凭据，否则这条用例什么也没验证。
	//（逗号在表单里会被编码成 %2C，所以解析后再比。）
	if got := dnspodFormOf(t, f.last()).Get("login_token"); got != dnspodTestID+","+dnspodTestToken {
		t.Fatalf("请求体里没有凭据，用例失去意义: %q", f.last().Body)
	}
	for _, secret := range []string{dnspodTestToken, dnspodTestID + "," + dnspodTestToken, "login_token"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("错误信息里出现了凭据相关内容 %q: %v", secret, err)
		}
	}
}

func TestDnspodCredentialIsRequired(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		fields map[string]string
	}{
		{name: "两个字段都空", fields: map[string]string{}},
		{name: "缺 token", fields: map[string]string{"id": dnspodTestID}},
		{name: "缺 id", fields: map[string]string{"token": dnspodTestToken}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			f, srv := newFakeAPI(t)
			_, err := NewDnspod(srv.URL).ListZones(context.Background(),
				dns.Credential{Provider: "dnspod", Fields: tc.fields})
			if err == nil {
				t.Fatal("缺少凭据时应当报错")
			}
			if !strings.Contains(err.Error(), "API ID 与 API Token") {
				t.Errorf("错误信息应当说清缺了什么，得到: %v", err)
			}
			if f.count() != 0 {
				t.Errorf("凭据不全时不该发请求，实际 %d 次", f.count())
			}
		})
	}
}

// TestDnspodMalformedResponseIsReported 验证畸形响应不会让内核崩溃，
// 也不会把整个响应体原样带进错误信息。
func TestDnspodMalformedResponseIsReported(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/Domain.List", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `this is not json`)
	})

	_, err := NewDnspod(srv.URL).ListZones(context.Background(), dnspodCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长，可能把响应体整个塞了进去: %d 字符", len(err.Error()))
	}
}

// TestDnspodSubDomain 覆盖"完整记录名 -> 主机记录"的翻译规则。
func TestDnspodSubDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		full, zone, want string
	}{
		{full: "www.example.com", zone: "example.com", want: "www"},
		{full: "example.com", zone: "example.com", want: "@"},
		{full: "a.b.example.com", zone: "example.com", want: "a.b"},
		{full: "WWW.Example.COM", zone: "example.com", want: "WWW"},
		{full: "www.example.com.", zone: "example.com.", want: "www"},
		{full: "www.other.com", zone: "example.com", want: ""},
		{full: "example.com", zone: "", want: ""},
		{full: "", zone: "example.com", want: ""},
	}
	for _, tc := range cases {
		if got := dnspodSubDomain(tc.full, tc.zone); got != tc.want {
			t.Errorf("dnspodSubDomain(%q, %q) = %q，期望 %q", tc.full, tc.zone, got, tc.want)
		}
	}
}
