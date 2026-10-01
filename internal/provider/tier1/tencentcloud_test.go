package tier1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件是腾讯云实现的测试，结构照 cloudflare_test.go 写：起一个假服务商，
// 断言"发出的请求长什么样"与"响应被翻译成了什么"。
//
// 两处腾讯云特有的地方：
//
//  1. 所有 action 都打在同一地址（POST /）上，靠 X-TC-Action 头区分，
//     因此假服务商按 action 分发，而不是像 Cloudflare 那样按路径。
//  2. 假服务商**不校验 TC3 签名**：签名串里的主机名由服务名拼出
//     （dnspod.tencentcloudapi.com），与本地假服务器的地址对不上，签名
//     必然是不匹配的。这里只断言"签名头按 TC3 的格式产生了、作用域里的
//     服务名是 dnspod"；真正的签名计算交给移植过来的
//     ddnsgo.TencentCloudSigner，它在上游已经跑了很久。

// tcFake 是一个按 X-TC-Action 分发的假腾讯云端点。
type tcFake struct {
	mu      sync.Mutex
	api     *fakeAPI
	actions map[string]http.HandlerFunc
}

func newTCFake(t *testing.T) (*tcFake, string) {
	t.Helper()
	api, srv := newFakeAPI(t)
	f := &tcFake{api: api, actions: map[string]http.HandlerFunc{}}

	api.on(http.MethodPost, "/", func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-TC-Action")
		f.mu.Lock()
		h := f.actions[action]
		f.mu.Unlock()
		if h == nil {
			// 没登记的 action 一律报错：静默返回空响应会让"发错了 action"
			// 变成一个看起来正常的成功。
			writeJSONBody(w, http.StatusOK, fmt.Sprintf(
				`{"Response":{"RequestId":"test","Error":{"Code":"InvalidAction","Message":"未登记的 action: %s"}}}`,
				action))
			return
		}
		h(w, r)
	})
	return f, srv.URL
}

// on 登记某个 action 的处理器。
func (f *tcFake) on(action string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions[action] = h
}

// json 登记一个固定内容的成功响应。
func (f *tcFake) json(action, body string) {
	f.on(action, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusOK, body)
	})
}

func (f *tcFake) last() recordedRequest { return f.api.last() }
func (f *tcFake) all() []recordedRequest {
	return f.api.all()
}
func (f *tcFake) count() int { return f.api.count() }

// tcLastInt 从最近一条被记录的请求里取整数字段；取不到返回 -1。
//
// 不能在处理器里读 r.Body：fakeAPI 在分发之前就把请求体读完并记录下来了，
// 处理器拿到的 r.Body 已经是空的。好在记录本身就发生在分发之前，
// 所以处理器里可以直接回看 f.last()。
func tcLastInt(f *tcFake, key string) int {
	var body map[string]any
	if err := json.Unmarshal([]byte(f.last().Body), &body); err != nil {
		return -1
	}
	v, ok := body[key].(float64)
	if !ok {
		return -1
	}
	return int(v)
}

// tcRequestBody 解出被记录的请求体，便于按字段名断言。
func tcRequestBody(t *testing.T, req recordedRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatalf("请求体不是 JSON: %v（%s）", err, req.Body)
	}
	return body
}

// 测试用的凭据。
//
// 密钥取一个不可能与真实值撞车的字面量，好在断言里直接搜它 ——
// "错误信息里不许出现密钥"这条要求需要一个能搜的标记。
const (
	tcTestSecretID  = "AKIDtest0000000000000000"
	tcTestSecretKey = "test-secret-key-must-never-leak"
)

func tcCred() dns.Credential {
	return dns.Credential{
		ID: "c1", Provider: "tencentcloud",
		Fields: map[string]string{
			"secret_id":  tcTestSecretID,
			"secret_key": tcTestSecretKey,
		},
	}
}

func tcZone() dns.Zone { return dns.Zone{ID: "12614766", Name: "example.com"} }

// ---------------------------------------------------------------------------
// 元信息与能力
// ---------------------------------------------------------------------------

func TestTencentCloudMeta(t *testing.T) {
	t.Parallel()

	c := NewTencentCloud("")
	m := c.Meta()
	if m.Name != "tencentcloud" {
		t.Errorf("Name = %q，必须与 provider 注册表里的一致", m.Name)
	}
	if m.DisplayName != "腾讯云 DNS" {
		t.Errorf("DisplayName = %q", m.DisplayName)
	}
	if m.Tier != 1 {
		t.Errorf("Tier = %d，腾讯云是完整的 Tier-1 实现", m.Tier)
	}

	// 能力集合是界面决定"哪些按钮可点"的依据，必须五项俱全。
	caps := dns.Capabilities(c)
	if !caps.ZoneList || !caps.RecordList || !caps.RecordCreate || !caps.RecordUpdate || !caps.RecordDelete {
		t.Errorf("能力集合不完整: %+v", caps)
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

func TestTencentCloudListZones(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.on(tcActionDescribeDomainList, func(w http.ResponseWriter, _ *http.Request) {
		if offset := tcLastInt(f, "Offset"); offset == 0 {
			// 第一页返回满页，逼出第二次请求。
			var b strings.Builder
			b.WriteString(`{"Response":{"RequestId":"r1","DomainList":[`)
			for i := 0; i < tcZonesPerPage; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"DomainId":%d,"Name":"z%d.example.com","Status":"ENABLE"}`, 1000+i, i)
			}
			b.WriteString(`]}}`)
			writeJSONBody(w, http.StatusOK, b.String())
			return
		}
		writeJSONBody(w, http.StatusOK, `{"Response":{"RequestId":"r2","DomainList":[
		  {"DomainId":2000,"Name":"second.com","Status":"ENABLE"},
		  {"DomainId":2001,"Name":"paused.com","Status":"PAUSE"},
		  {"DomainId":2002,"Name":"spam.com","Status":"SPAM"}
		]}}`)
	})

	zones, err := NewTencentCloud(base).ListZones(context.Background(), tcCred())
	if err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	// 分页应当被跟随：第一页 100 条 + 第二页 1 条（另两条被滤掉）。
	if len(zones) != tcZonesPerPage+1 {
		t.Fatalf("应当得到 %d 个区域，得到 %d", tcZonesPerPage+1, len(zones))
	}
	if zones[0].Name != "z0.example.com" || zones[len(zones)-1].Name != "second.com" {
		t.Errorf("区域列表不符: 首 %q 末 %q", zones[0].Name, zones[len(zones)-1].Name)
	}
	// 编号是数字，对外表达成字符串。
	if zones[0].ID != "1000" {
		t.Errorf("区域 ID = %q，期望 \"1000\"", zones[0].ID)
	}

	// 暂停 / 封禁的域名必须被滤掉：在它们下面改记录不会生效，
	// 列出来只会让用户以为"我改了但没用"。
	for _, z := range zones {
		if z.Name == "paused.com" || z.Name == "spam.com" {
			t.Errorf("非 ENABLE 的域名不应出现: %q", z.Name)
		}
	}

	if f.count() != 2 {
		t.Fatalf("应当请求 2 页，实际 %d 次", f.count())
	}
	// 第二页的偏移必须是**服务商返回的条数**（100），而不是过滤后的条数。
	if offset := tcLastInt(f, "Offset"); offset != tcZonesPerPage {
		t.Errorf("第二页 Offset = %d，期望 %d", offset, tcZonesPerPage)
	}
	if limit := tcLastInt(f, "Limit"); limit != tcZonesPerPage {
		t.Errorf("Limit = %d，期望 %d", limit, tcZonesPerPage)
	}
}

func TestTencentCloudListZonesRequiresCredential(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	_, err := NewTencentCloud(base).ListZones(context.Background(), dns.Credential{Fields: map[string]string{}})
	if err == nil {
		t.Fatal("缺少凭据时应当报错")
	}
	// 报错要说清缺的是哪个字段，但绝不能把值回显出来。
	if !strings.Contains(err.Error(), "secret_id") {
		t.Errorf("错误信息应当点明缺失的字段，得到: %v", err)
	}
	if f.count() != 0 {
		t.Error("凭据不全时不该发出请求")
	}
}

// TestTencentCloudUnauthorizedIsClassified 验证鉴权失败可被识别。
//
// 界面靠它把"凭据过期了，请重新授权"与"服务商那边出错了"区分开。
func TestTencentCloudUnauthorizedIsClassified(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.on(tcActionDescribeDomainList, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusForbidden,
			`{"Response":{"RequestId":"r1","Error":{"Code":"AuthFailure.SignatureFailure","Message":"签名验证失败，请检查 SecretId 与 SecretKey"}}}`)
	})

	_, err := NewTencentCloud(base).ListZones(context.Background(), tcCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if !apiErr.IsUnauthorized() {
		t.Errorf("IsUnauthorized 应当为 true，状态码 %d", apiErr.Status)
	}
	// 错误信息必须带上服务商的说明 —— 一句光秃秃的"HTTP 403"
	// 无法告诉用户该去改什么。
	if !strings.Contains(err.Error(), "签名验证失败") {
		t.Errorf("错误信息应当包含服务商说明，得到: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 记录列表
// ---------------------------------------------------------------------------

func TestTencentCloudListRecords(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDescribeRecordList, `{"Response":{"RequestId":"r1","RecordList":[
	  {"RecordId":556507778,"Name":"www","Type":"A","Value":"203.0.113.7","TTL":600,"MX":0,"Remark":"官网"},
	  {"RecordId":556507779,"Name":"@","Type":"MX","Value":"mail.example.com.","TTL":600,"MX":10},
	  {"RecordId":556507780,"Name":"_acme-challenge","Type":"TXT","Value":"v=spf1 -all","TTL":600,"MX":0}
	]}}`)

	records, err := NewTencentCloud(base).ListRecords(context.Background(), tcCred(), tcZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("应当得到 3 条记录，得到 %d", len(records))
	}

	// RecordId 是数字、dns.Record.ID 是字符串，转换必须无损。
	if records[0].ID != "556507778" {
		t.Errorf("记录 ID = %q", records[0].ID)
	}
	// 腾讯云的主机记录要拼成完整域名。
	if records[0].Name != "www.example.com" {
		t.Errorf("记录名 = %q，期望 www.example.com", records[0].Name)
	}
	if records[0].Comment != "官网" {
		t.Errorf("备注 = %q", records[0].Comment)
	}
	// "@" 表示根域名。
	if records[1].Name != "example.com" {
		t.Errorf("根记录名 = %q，期望 example.com", records[1].Name)
	}
	if records[1].Priority != 10 {
		t.Errorf("MX 优先级 = %d，期望 10", records[1].Priority)
	}
	if records[2].Name != "_acme-challenge.example.com" {
		t.Errorf("记录名 = %q", records[2].Name)
	}

	// 没有名称过滤时不该发送 SubDomain。
	body := tcRequestBody(t, f.last())
	if _, ok := body["SubDomain"]; ok {
		t.Errorf("没有名称过滤时不该发送 SubDomain: %v", body)
	}
}

func TestTencentCloudListRecordsSendsFilter(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDescribeRecordList, `{"Response":{"RequestId":"r1","RecordList":[]}}`)

	zone := tcZone()
	records, err := NewTencentCloud(base).ListRecords(context.Background(), tcCred(), zone,
		dns.RecordFilter{Type: dns.TypeAAAA, Name: "home.example.com"})
	if err != nil {
		t.Fatalf("空结果不该被当成错误: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("应当得到 0 条记录，得到 %d", len(records))
	}

	body := tcRequestBody(t, f.last())
	if body["Domain"] != "example.com" {
		t.Errorf("Domain = %v，腾讯云按域名而不是编号定位", body["Domain"])
	}
	if body["SubDomain"] != "home" {
		t.Errorf("SubDomain = %v，期望把完整域名转成主机记录 home", body["SubDomain"])
	}
	if body["RecordType"] != "AAAA" {
		t.Errorf("RecordType = %v", body["RecordType"])
	}
	// 这个参数的默认值是 "yes"（查不到就报错），对"列出记录"是错的语义：
	// 一个还没有记录的区域应当得到空列表。
	if body["ErrorOnEmpty"] != "no" {
		t.Errorf("ErrorOnEmpty = %v，必须显式传 no", body["ErrorOnEmpty"])
	}
	if body["Limit"] != float64(tcRecordsPerPage) {
		t.Errorf("Limit = %v", body["Limit"])
	}
}

func TestTencentCloudListRecordsFollowsPaging(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.on(tcActionDescribeRecordList, func(w http.ResponseWriter, _ *http.Request) {
		if offset := tcLastInt(f, "Offset"); offset == 0 {
			var b strings.Builder
			b.WriteString(`{"Response":{"RequestId":"r1","RecordList":[`)
			for i := 0; i < tcRecordsPerPage; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, `{"RecordId":%d,"Name":"h%d","Type":"A","Value":"203.0.113.1","TTL":600}`, 100+i, i)
			}
			b.WriteString(`]}}`)
			writeJSONBody(w, http.StatusOK, b.String())
			return
		}
		writeJSONBody(w, http.StatusOK,
			`{"Response":{"RequestId":"r2","RecordList":[{"RecordId":999,"Name":"last","Type":"A","Value":"203.0.113.9","TTL":600}]}}`)
	})

	records, err := NewTencentCloud(base).ListRecords(context.Background(), tcCred(), tcZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != tcRecordsPerPage+1 {
		t.Fatalf("应当得到 %d 条记录，得到 %d", tcRecordsPerPage+1, len(records))
	}
	if records[len(records)-1].Name != "last.example.com" {
		t.Errorf("最后一条 = %q", records[len(records)-1].Name)
	}
	if f.count() != 2 {
		t.Fatalf("应当请求 2 页，实际 %d 次", f.count())
	}
	if offset := tcLastInt(f, "Offset"); offset != tcRecordsPerPage {
		t.Errorf("第二页 Offset = %d，期望 %d", offset, tcRecordsPerPage)
	}
}

// TestTencentCloudRequiresZoneName 钉住一个很容易踩的坑：
// 腾讯云的记录接口按**域名**定位，不认数字的 DomainId。只给编号时必须
// 直接报错，而不是拿编号去当域名发请求。
func TestTencentCloudRequiresZoneName(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	c := NewTencentCloud(base)
	ctx := context.Background()
	zone := dns.Zone{ID: "12614766"} // 只有编号

	if _, err := c.ListRecords(ctx, tcCred(), zone, dns.RecordFilter{}); err == nil {
		t.Error("只有区域编号时列出记录应当报错")
	}
	if _, err := c.CreateRecord(ctx, tcCred(), zone,
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"}); err == nil {
		t.Error("只有区域编号时新增记录应当报错")
	}
	if err := c.DeleteRecord(ctx, tcCred(), zone, "556507778"); err == nil {
		t.Error("只有区域编号时删除记录应当报错")
	}
	if f.count() != 0 {
		t.Errorf("参数不全时不该发出请求，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 记录名 / 编号的翻译
// ---------------------------------------------------------------------------

func TestTencentCloudSubDomain(t *testing.T) {
	t.Parallel()

	cases := []struct {
		zone string
		name string
		want string
	}{
		{"example.com", "www.example.com", "www"},
		{"example.com", "a.b.example.com", "a.b"},
		{"example.com", "example.com", "@"},
		{"example.com", "Example.COM", "@"},
		{"example.com", "www.example.com.", "www"},
		{"example.com", "WWW.example.com", "WWW"},
		{"example.com", "_acme-challenge.example.com", "_acme-challenge"},
		{"example.com", "@", "@"},
		{"example.com", "", "@"},
		// 不属于本区域的名称原样透传：腾讯云的 SubDomain 永远相对 Domain，
		// 写错也只会落到本区域内，判成错误会误伤 "*" 这类合法写法。
		{"example.com", "*", "*"},
		{"example.com", "other.net", "other.net"},
		// 没有区域名时不猜。
		{"", "www.example.com", "www.example.com"},
		// 后缀相同但不在分隔点上，不能被误当成子域名。
		{"example.com", "notexample.com", "notexample.com"},
	}

	for _, tc := range cases {
		if got := tcSubDomain(tc.zone, tc.name); got != tc.want {
			t.Errorf("tcSubDomain(%q, %q) = %q，期望 %q", tc.zone, tc.name, got, tc.want)
		}
	}

	// 反向拼接。
	if got := tcFullName("example.com", "@"); got != "example.com" {
		t.Errorf("tcFullName 根域名 = %q", got)
	}
	if got := tcFullName("example.com", "www"); got != "www.example.com" {
		t.Errorf("tcFullName = %q", got)
	}
}

// ---------------------------------------------------------------------------
// 新增
// ---------------------------------------------------------------------------

func TestTencentCloudCreateRecord(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionCreateRecord, `{"Response":{"RequestId":"r1","RecordId":556507778}}`)

	rec, err := NewTencentCloud(base).CreateRecord(context.Background(), tcCred(), tcZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if rec.ID != "556507778" {
		t.Errorf("返回的记录 ID = %q", rec.ID)
	}
	if rec.Name != "www.example.com" {
		t.Errorf("返回的记录名 = %q", rec.Name)
	}

	last := f.last()
	if last.Header.Get("X-TC-Action") != tcActionCreateRecord {
		t.Errorf("X-TC-Action = %q", last.Header.Get("X-TC-Action"))
	}
	body := tcRequestBody(t, last)
	if body["Domain"] != "example.com" || body["SubDomain"] != "www" {
		t.Errorf("域名/主机记录不符: %v", body)
	}
	if body["RecordType"] != "A" || body["Value"] != "203.0.113.7" {
		t.Errorf("类型/记录值不符: %v", body)
	}
	// 线路是必填参数，而 dns.Record 里没有线路字段，只能填默认。
	if body["RecordLine"] != tcDefaultLine {
		t.Errorf("RecordLine = %v，期望 %q", body["RecordLine"], tcDefaultLine)
	}
	if body["TTL"] != float64(600) {
		t.Errorf("TTL = %v", body["TTL"])
	}
	// 新建时带上 RecordId（0）只会换来"记录编号错误"。
	if _, ok := body["RecordId"]; ok {
		t.Errorf("新增请求不该带 RecordId: %v", body)
	}
	// A 记录没有优先级。
	if _, ok := body["MX"]; ok {
		t.Errorf("A 记录不该发送 MX: %v", body)
	}
	// 备注必须显式发送：ModifyRecord 靠"传空"来清空备注。
	if _, ok := body["Remark"]; !ok {
		t.Errorf("请求体应当始终带 Remark 字段: %v", body)
	}
}

func TestTencentCloudCreateRootMXRecord(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionCreateRecord, `{"Response":{"RequestId":"r1","RecordId":42}}`)

	rec, err := NewTencentCloud(base).CreateRecord(context.Background(), tcCred(), tcZone(),
		dns.Record{Name: "example.com", Type: dns.TypeMX, Content: "mail.example.com.", Priority: 10, TTL: 0})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if rec.Name != "example.com" {
		t.Errorf("根记录名 = %q", rec.Name)
	}
	if rec.Priority != 10 {
		t.Errorf("返回的优先级 = %d", rec.Priority)
	}

	body := tcRequestBody(t, f.last())
	// 根域名的主机记录是 "@"。
	if body["SubDomain"] != "@" {
		t.Errorf("SubDomain = %v，根域名必须是 @", body["SubDomain"])
	}
	// MX 记录的优先级放在 MX 字段里，且是必填项。
	if body["MX"] != float64(10) {
		t.Errorf("MX = %v，期望 10", body["MX"])
	}
	// TTL=0 表示交给服务商默认值：不能发一个 0 过去（会被拒）。
	if _, ok := body["TTL"]; ok {
		t.Errorf("TTL=0 时不该发送 TTL 字段: %v", body)
	}
}

func TestTencentCloudCreateRejectsEmptyTypeOrName(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	c := NewTencentCloud(base)
	ctx := context.Background()

	if _, err := c.CreateRecord(ctx, tcCred(), tcZone(),
		dns.Record{Name: "www.example.com", Content: "203.0.113.7"}); err == nil {
		t.Error("记录类型为空时应当报错")
	}
	if _, err := c.CreateRecord(ctx, tcCred(), tcZone(),
		dns.Record{Type: dns.TypeA, Content: "203.0.113.7"}); err == nil {
		t.Error("记录名为空时应当报错")
	}
	if f.count() != 0 {
		t.Errorf("参数不全时不该发出请求，实际 %d 次", f.count())
	}
}

// TestTencentCloudCreateWithoutRecordIDIsReported 验证"成功但没有编号"
// 不会被当成成功：返回一条 ID 为空的记录，会让失败延后到下一次操作，
// 而那时已经完全看不出问题出在哪。
func TestTencentCloudCreateWithoutRecordIDIsReported(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionCreateRecord, `{"Response":{"RequestId":"r1"}}`)

	_, err := NewTencentCloud(base).CreateRecord(context.Background(), tcCred(), tcZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600})
	if err == nil {
		t.Fatal("服务商没返回记录编号时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 修改
// ---------------------------------------------------------------------------

// TestTencentCloudUpdateRecordKeepsLine 是这份实现里最要紧的一条。
//
// ModifyRecord 的 RecordLine 是必填参数，而 dns.Record 里没有线路字段。
// 如果固定填"默认"，一条挂在"电信"这类线路上的记录（多线路负载均衡）
// 会被连线路一起改掉，而用户在界面上看不到任何异常。
func TestTencentCloudUpdateRecordKeepsLine(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDescribeRecord, `{"Response":{"RequestId":"r1","RecordInfo":{
	  "Id":556507778,"SubDomain":"www","RecordType":"A","RecordLine":"电信",
	  "Value":"203.0.113.7","TTL":600,"MX":0
	}}}`)
	f.json(tcActionModifyRecord, `{"Response":{"RequestId":"r2","RecordId":556507778}}`)

	rec, err := NewTencentCloud(base).UpdateRecord(context.Background(), tcCred(), tcZone(),
		dns.Record{ID: "556507778", Name: "www.example.com", Type: dns.TypeA,
			Content: "203.0.113.99", TTL: 600})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if rec.Content != "203.0.113.99" {
		t.Errorf("返回内容 = %q", rec.Content)
	}

	if f.count() != 2 {
		t.Fatalf("应当在修改前先读一次原记录，实际请求 %d 次", f.count())
	}
	reqs := f.all()
	if reqs[0].Header.Get("X-TC-Action") != tcActionDescribeRecord {
		t.Errorf("第一次请求应当是读取记录，实际 %q", reqs[0].Header.Get("X-TC-Action"))
	}
	// 读线路用的也必须是记录编号。
	if id := tcRequestBody(t, reqs[0])["RecordId"]; id != float64(556507778) {
		t.Errorf("读取记录用的 RecordId = %v", id)
	}

	last := f.last()
	if last.Header.Get("X-TC-Action") != tcActionModifyRecord {
		t.Errorf("最后一次请求应当是修改记录，实际 %q", last.Header.Get("X-TC-Action"))
	}
	body := tcRequestBody(t, last)
	if body["RecordLine"] != "电信" {
		t.Errorf("RecordLine = %v，必须带回原记录所在的线路", body["RecordLine"])
	}
	// 编号是数字：传字符串会被服务商当成参数类型错误。
	if body["RecordId"] != float64(556507778) {
		t.Errorf("RecordId = %v（%T），必须是数字", body["RecordId"], body["RecordId"])
	}
	if body["SubDomain"] != "www" || body["Value"] != "203.0.113.99" {
		t.Errorf("请求体不符: %v", body)
	}
}

// TestTencentCloudUpdateFallsBackToDefaultLine 验证查线路失败不会挡住修改。
//
// 有些 CAM 策略只授予写权限，一次辅助查询的失败不该让本来能成功的修改失败。
func TestTencentCloudUpdateFallsBackToDefaultLine(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDescribeRecord,
		`{"Response":{"RequestId":"r1","Error":{"Code":"UnauthorizedOperation","Message":"未授权操作。"}}}`)
	f.json(tcActionModifyRecord, `{"Response":{"RequestId":"r2","RecordId":7}}`)

	_, err := NewTencentCloud(base).UpdateRecord(context.Background(), tcCred(), tcZone(),
		dns.Record{ID: "7", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600})
	if err != nil {
		t.Fatalf("辅助查询失败不该让修改失败: %v", err)
	}
	if body := tcRequestBody(t, f.last()); body["RecordLine"] != tcDefaultLine {
		t.Errorf("RecordLine = %v，读不到原线路时应当退回默认", body["RecordLine"])
	}
}

// TestTencentCloudUpdateRejectsBadID 验证编号转换失败时直接报错。
//
// 把 "abc" 静默当成 0 发出去，要么得到一条"记录编号错误"，
// 要么更糟 —— 命中编号为 0 的某条记录。
func TestTencentCloudUpdateRejectsBadID(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	c := NewTencentCloud(base)
	ctx := context.Background()

	for _, id := range []string{"", "   ", "abc", "12.5", "-1"} {
		_, err := c.UpdateRecord(ctx, tcCred(), tcZone(),
			dns.Record{ID: id, Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600})
		if err == nil {
			t.Errorf("记录 ID %q 不合法时应当报错", id)
		}
	}
	if f.count() != 0 {
		t.Errorf("编号不合法时不该发出请求，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 删除
// ---------------------------------------------------------------------------

func TestTencentCloudDeleteRecord(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDeleteRecord, `{"Response":{"RequestId":"r1"}}`)

	if err := NewTencentCloud(base).DeleteRecord(context.Background(), tcCred(), tcZone(), "556507778"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}

	last := f.last()
	if last.Method != http.MethodPost {
		t.Errorf("腾讯云一律用 POST，实际 %s", last.Method)
	}
	if last.Header.Get("X-TC-Action") != tcActionDeleteRecord {
		t.Errorf("X-TC-Action = %q", last.Header.Get("X-TC-Action"))
	}
	body := tcRequestBody(t, last)
	if body["Domain"] != "example.com" || body["RecordId"] != float64(556507778) {
		t.Errorf("请求体不符: %v", body)
	}
}

// TestTencentCloudBusinessErrorIsNotSuccess 验证业务错误不会被当成成功。
//
// 腾讯云的**业务错误同样返回 HTTP 200**，错误藏在 Response.Error 里。
// 只看状态码会把"记录编号错误"当成删除成功 —— 那是最危险的一类误报。
func TestTencentCloudBusinessErrorIsNotSuccess(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDeleteRecord,
		`{"Response":{"RequestId":"ab4f1426","Error":{"Code":"InvalidParameter.RecordIdInvalid","Message":"记录编号错误。"}}}`)

	err := NewTencentCloud(base).DeleteRecord(context.Background(), tcCred(), tcZone(), "556507778")
	if err == nil {
		t.Fatal("HTTP 200 + Response.Error 必须被当成失败")
	}
	// 错误信息要带上服务商的原始说明，以及便于报障的 RequestId。
	if !strings.Contains(err.Error(), "记录编号错误。") {
		t.Errorf("错误信息应当包含服务商说明，得到: %v", err)
	}
	if !strings.Contains(err.Error(), "ab4f1426") {
		t.Errorf("错误信息应当包含 RequestId，得到: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 签名与保密
// ---------------------------------------------------------------------------

// TestTencentCloudSignsWithTC3 钉住签名契约。
//
// 服务名必须是 dnspod 而不是 tencentcloud：它由签名作用域体现出来，
// 而 ddns-go 那边这个常量是未导出的，这里只能以字面量重复一次 ——
// 这条断言就是为了防止它被改成别的值。
func TestTencentCloudSignsWithTC3(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.json(tcActionDescribeDomainList, `{"Response":{"RequestId":"r1","DomainList":[]}}`)

	if _, err := NewTencentCloud(base).ListZones(context.Background(), tcCred()); err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	last := f.last()
	// 所有 action 都打在端点根路径上，签名串里的路径也被写死成 "/"。
	if last.Path != "/" {
		t.Errorf("请求路径 = %q，期望 /", last.Path)
	}
	if ct := last.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if v := last.Header.Get("X-TC-Version"); v != tcVersion {
		t.Errorf("X-TC-Version = %q，期望 %q", v, tcVersion)
	}
	if v := last.Header.Get("X-TC-Action"); v != tcActionDescribeDomainList {
		t.Errorf("X-TC-Action = %q", v)
	}
	if _, err := strconv.ParseInt(last.Header.Get("X-TC-Timestamp"), 10, 64); err != nil {
		t.Errorf("X-TC-Timestamp 应当是 Unix 秒: %q", last.Header.Get("X-TC-Timestamp"))
	}

	auth := last.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "TC3-HMAC-SHA256 Credential="+tcTestSecretID+"/") {
		t.Errorf("Authorization 前缀不符: %q", auth)
	}
	if !strings.Contains(auth, "/"+tcService+"/tc3_request") {
		t.Errorf("签名作用域里的服务名应当是 %q: %q", tcService, auth)
	}
	if !strings.Contains(auth, "SignedHeaders=content-type;host;x-tc-action") {
		t.Errorf("被签名的头不符: %q", auth)
	}
}

// TestTencentCloudNeverLeaksSecretKey 验证密钥不会被写进用户可见的错误信息，
// 也不会随请求发出去。
//
// 注意 SecretId 会出现在 Authorization 头里（TC3 的凭证串用它标识身份），
// 那是公开部分，不算泄漏；SecretKey 则绝不能离开进程。
func TestTencentCloudNeverLeaksSecretKey(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.on(tcActionDescribeDomainList, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusForbidden,
			`{"Response":{"RequestId":"r1","Error":{"Code":"AuthFailure.SignatureFailure","Message":"签名验证失败"}}}`)
	})

	_, err := NewTencentCloud(base).ListZones(context.Background(), tcCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	if strings.Contains(err.Error(), tcTestSecretKey) {
		t.Errorf("错误信息里出现了 SecretKey: %v", err)
	}
	if strings.Contains(err.Error(), "Authorization") {
		t.Errorf("错误信息里不该出现鉴权头（签名串在里面）: %v", err)
	}

	for i, req := range f.all() {
		if strings.Contains(req.Header.Get("Authorization"), tcTestSecretKey) {
			t.Errorf("第 %d 个请求的 Authorization 头里出现了 SecretKey", i+1)
		}
		if strings.Contains(req.Body, tcTestSecretKey) {
			t.Errorf("第 %d 个请求的请求体里出现了 SecretKey", i+1)
		}
	}
}

// TestTencentCloudMalformedResponseIsReported 验证畸形响应不会让内核崩溃。
func TestTencentCloudMalformedResponseIsReported(t *testing.T) {
	t.Parallel()

	f, base := newTCFake(t)
	f.on(tcActionDescribeDomainList, func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusOK, `this is not json`)
	})

	_, err := NewTencentCloud(base).ListZones(context.Background(), tcCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	// 错误信息不该原样带上整个响应体（它可能很大，也可能含账号信息）。
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长，可能把响应体整个塞了进去: %d 字符", len(err.Error()))
	}
}
