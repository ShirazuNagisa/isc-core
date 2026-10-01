package tier1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/ddnsgo"
	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件只放华为云专属的用例。
//
// 假服务器、请求录制、响应写入这些辅助都在 cloudflare_test.go 里，
// 这里直接用，不再定义一遍（重复定义会直接编译失败）。
//
// 重点覆盖三类东西：
//
//  1. **记录集与单条记录的语义差** —— 一个记录集展开成多条 dns.Record、
//     它们共用同一个 ID、写入时值数组只有一个元素。这是与其它服务商
//     差别最大、也最容易做错的地方。
//  2. **签名与请求形态** —— 端点版本（域名 v2、记录集 v2.1）、
//     SDK-HMAC-SHA256 的 Authorization 头覆盖了什么、凭据不外泄。
//  3. **名字结尾的点** —— 华为云收发都带点，这一层必须翻译掉。

// 测试用凭据。这两个字符串只在本文件里当字面量用，不会被发到任何地方。
const (
	hwTestAK = "test-ak"
	hwTestSK = "test-sk"
)

func hwCred() dns.Credential {
	return dns.Credential{
		ID: "c-hw", Provider: "huaweicloud",
		Fields: map[string]string{
			"access_key_id":     hwTestAK,
			"access_key_secret": hwTestSK,
		},
	}
}

// hwTestZone 是测试里反复用到的区域。
//
// 名字带 Test 前缀是为了避开生产代码里的 hwZone 类型（同一个包）。
func hwTestZone() dns.Zone { return dns.Zone{ID: "z1", Name: "example.com"} }

// ---------------------------------------------------------------------------
// 元信息 / 构造
// ---------------------------------------------------------------------------

func TestHuaweicloudMeta(t *testing.T) {
	t.Parallel()

	// baseURL 为空时必须用官方地址；传了就用传进来的（测试要能打到假服务器）。
	h := NewHuaweicloud("")
	if h.Meta().Name != "huaweicloud" {
		t.Errorf("Name = %q，必须与 builtin.go 里登记的凭据查找键一致", h.Meta().Name)
	}
	if h.Meta().DisplayName != "华为云 DNS" {
		t.Errorf("DisplayName = %q", h.Meta().DisplayName)
	}
	if h.Meta().Tier != 1 {
		t.Errorf("Tier = %d，华为云是完整记录管理（Tier-1）", h.Meta().Tier)
	}
	if h.baseURL != "https://dns.myhuaweicloud.com" {
		t.Errorf("默认基址 = %q", h.baseURL)
	}
	if NewHuaweicloud("http://127.0.0.1:1").baseURL != "http://127.0.0.1:1" {
		t.Error("显式传入的基址没有被使用")
	}

	// 五个能力必须都实现了 —— 界面上"哪些按钮可点"就是照这个结果来的。
	caps := dns.Capabilities(NewHuaweicloud(""))
	if !caps.ZoneList || !caps.RecordList || !caps.RecordCreate || !caps.RecordUpdate || !caps.RecordDelete {
		t.Errorf("能力集不完整: %+v", caps)
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

// hwZonesJSON 拼一页域名响应（页数多时手写 JSON 不现实）。
func hwZonesJSON(start, count, total int) string {
	var b strings.Builder
	b.WriteString(`{"zones":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"z%d","name":"d%d.example.com.","status":"ACTIVE"}`, start+i, start+i)
	}
	fmt.Fprintf(&b, `],"metadata":{"total_count":%d}}`, total)
	return b.String()
}

func TestHuaweicloudListZones(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2/zones", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "0" {
			writeJSONBody(w, 200, hwZonesJSON(huaweicloudPerPage, 1, huaweicloudPerPage+1))
			return
		}
		// 第一页故意给满 500 条：不满一页会被当成"最后一页"。
		writeJSONBody(w, 200, hwZonesJSON(0, huaweicloudPerPage, huaweicloudPerPage+1))
	})

	zones, err := NewHuaweicloud(srv.URL).ListZones(context.Background(), hwCred())
	if err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}
	if len(zones) != huaweicloudPerPage+1 {
		t.Fatalf("应当把两页都拉回来（%d 条），得到 %d 条", huaweicloudPerPage+1, len(zones))
	}

	// 华为云的区域名带结尾的点，对外必须去掉：这个值会被拿去和用户输入、
	// 记录名做比较，多一个点会让所有比较静默失败。
	if zones[0].Name != "d0.example.com" {
		t.Errorf("区域名应当去掉结尾的点，得到 %q", zones[0].Name)
	}
	for _, z := range zones {
		if strings.HasSuffix(z.Name, ".") {
			t.Fatalf("区域名仍带结尾的点: %q", z.Name)
		}
	}

	// 分页：第二页的 offset 必须等于第一页的**原始**条数。
	reqs := f.all()
	if len(reqs) != 2 {
		t.Fatalf("应当请求 2 次，实际 %d 次", len(reqs))
	}
	if !strings.Contains(reqs[0].Query, "limit=500") || !strings.Contains(reqs[0].Query, "offset=0") {
		t.Errorf("第一页的查询串不符: %s", reqs[0].Query)
	}
	if !strings.Contains(reqs[1].Query, fmt.Sprintf("offset=%d", huaweicloudPerPage)) {
		t.Errorf("第二页的 offset 不符: %s", reqs[1].Query)
	}
	// 域名列表走 v2（记录集那一族才走 v2.1），两个版本并存不是笔误。
	if reqs[0].Path != "/v2/zones" {
		t.Errorf("路径 = %q，期望 /v2/zones", reqs[0].Path)
	}
}

// TestHwNextOffset 钉住翻页的终止条件。
//
// 这段逻辑写错的后果很不对称：少一个终止条件就是一直翻到 maxPages
// （白打 100 次请求，还会把重复的记录并进结果），
// 多一个终止条件就是**静默漏数据**。所以这里把两个方向的边界都列出来。
func TestHwNextOffset(t *testing.T) {
	t.Parallel()

	const perPage = huaweicloudPerPage
	cases := []struct {
		name       string
		offset     int
		got        int
		total      int
		wantOffset int
		wantMore   bool
	}{
		{"空页结束", 0, 0, 0, 0, false},
		{"还有下一页", 0, perPage, 0, perPage, true},
		{"还有下一页（知道总数）", 0, perPage, perPage * 2, perPage, true},
		{"取够总数", perPage, perPage, perPage * 2, 0, false},
		{"最后一页（总数对得上）", 0, 3, 3, 0, false},
		{"服务商忽略 limit 一次全给", 0, perPage + 1, 0, 0, false},
		{"总数未知但满页", perPage, perPage, 0, perPage * 2, true},
		// 总数未知且不满一页时**继续**翻：多打一次请求（下一页是空的）
		// 换来的是"服务商把 limit 截小了也不会漏数据"。
		{"总数未知且不满一页", 0, 3, 0, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, more := hwNextOffset(tc.offset, tc.got, tc.total)
			if more != tc.wantMore {
				t.Fatalf("继续翻页 = %v，期望 %v", more, tc.wantMore)
			}
			if more && got != tc.wantOffset {
				t.Errorf("下一个 offset = %d，期望 %d", got, tc.wantOffset)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 记录：记录集 → 多条 dns.Record
// ---------------------------------------------------------------------------

func TestHuaweicloudListRecordsExpandsRecordsets(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{
		  "recordsets": [
		    {"id":"rs1","name":"www.example.com.","type":"A","ttl":300,
		     "records":["203.0.113.7","203.0.113.8","203.0.113.9"],
		     "description":"www 的解析","zone_id":"z1","zone_name":"example.com."},
		    {"id":"rs2","name":"example.com.","type":"TXT","ttl":3600,
		     "records":["v=spf1 -all"],"default":true},
		    {"id":"rs3","name":"mail.example.com.","type":"MX","ttl":300,
		     "records":["10 mail.example.com."]}
		  ],
		  "metadata": {"total_count": 3}
		}`)
	})

	records, err := NewHuaweicloud(srv.URL).ListRecords(
		context.Background(), hwCred(), hwTestZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}

	// 3 + 1 + 1：每个记录集按 values 的个数展开成多条"记录"。
	if len(records) != 5 {
		t.Fatalf("应当展开出 5 条记录，得到 %d: %+v", len(records), records)
	}

	// 一个记录集展开出的 3 条共享同一个 ID —— 这正是接口与华为云模型
	// 对不上的地方，调用方**不能**把 ID 当唯一键。这条断言是为了让
	// "ID 会不会重复"这个疑问永远有答案，而不是靠注释。
	for i := 0; i < 3; i++ {
		if records[i].ID != "rs1" {
			t.Errorf("第 %d 条记录的 ID = %q，同一记录集必须共用 rs1", i, records[i].ID)
		}
	}
	if records[0].Name != "www.example.com" {
		t.Errorf("记录名应当去掉结尾的点，得到 %q", records[0].Name)
	}
	if records[0].TTL != 300 {
		t.Errorf("TTL = %d，期望原样的 300", records[0].TTL)
	}
	// description 是**记录集级**的，展开出的每条都会看到它。
	if records[0].Comment != "www 的解析" {
		t.Errorf("备注应当来自记录集的 description，得到 %q", records[0].Comment)
	}

	// MX / SRV / CAA 的优先级、权重、flag 都编在值字符串里，原样保留：
	// 拆开再拼回去需要按类型的语法表，写错一个字就把用户的值改坏了。
	if records[4].Content != "10 mail.example.com." {
		t.Errorf("MX 的值应当原样保留，得到 %q", records[4].Content)
	}
	if records[4].Priority != 0 {
		t.Errorf("Priority 恒为 0（优先级在值里），得到 %d", records[4].Priority)
	}

	// 端点必须是 v2.1 的记录集接口，且带上分页参数。
	if got := f.last().Path; got != "/v2.1/zones/z1/recordsets" {
		t.Errorf("路径 = %q", got)
	}
	if q := f.last().Query; !strings.Contains(q, "limit=500") || !strings.Contains(q, "offset=0") {
		t.Errorf("分页参数缺失: %s", q)
	}
}

// TestHuaweicloudListRecordsNameFilterIsExact 覆盖最容易踩的一个坑。
//
// 华为云的 name 参数是**模糊**匹配（文档原话："域名中包含此 name"），
// 所以服务商一定会多回一些"包含"目标名的记录集，精确匹配只能本地做。
func TestHuaweicloudListRecordsNameFilterIsExact(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{
		  "recordsets": [
		    {"id":"rs1","name":"www.example.com.","type":"A","ttl":300,"records":["203.0.113.1"]},
		    {"id":"rs9","name":"www.example.com.cn.","type":"A","ttl":300,"records":["203.0.113.9"]},
		    {"id":"rs8","name":"other.example.com.","type":"A","ttl":300,"records":["203.0.113.8"]}
		  ],
		  "metadata": {"total_count": 3}
		}`)
	})

	// 名字故意写成"大写 + 结尾带点"：用户输入和读回来的值都可能是这样。
	records, err := NewHuaweicloud(srv.URL).ListRecords(context.Background(), hwCred(), hwTestZone(),
		dns.RecordFilter{Name: "WWW.Example.com.", Type: dns.TypeA})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 1 || records[0].ID != "rs1" {
		t.Fatalf("模糊匹配的结果必须再按名字精确过滤，得到 %+v", records)
	}

	q := f.last().Query
	if !strings.Contains(q, "name=WWW.Example.com") {
		t.Errorf("名字过滤没有发出（应当去掉结尾的点），查询串: %s", q)
	}
	if !strings.Contains(q, "type=A") {
		t.Errorf("类型过滤没有发出，查询串: %s", q)
	}
}

// TestHuaweicloudListRecordsPaginatesByRawCount 是一个专门为"分页 + 本地过滤"
// 组合写的用例。
//
// offset 必须按服务商返回的**原始**条数推进。按过滤后的条数推进会跳过
// 剩下的记录集 —— 而且恰好是"过滤器用得越多、漏得越多"，
// 用户只会看到"我的记录少了一些"，几乎不可能定位。
func TestHuaweicloudListRecordsPaginatesByRawCount(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			var b strings.Builder
			b.WriteString(`{"recordsets":[`)
			b.WriteString(`{"id":"rs-hit-1","name":"www.example.com.","type":"A","ttl":300,"records":["203.0.113.1"]}`)
			// 另外 499 个都不匹配过滤器，会在本地被丢掉。
			for i := 0; i < huaweicloudPerPage-1; i++ {
				fmt.Fprintf(&b, `,{"id":"rs-other-%d","name":"n%d.example.com.","type":"A","ttl":300,"records":["198.51.100.1"]}`, i, i)
			}
			fmt.Fprintf(&b, `],"metadata":{"total_count":%d}}`, huaweicloudPerPage+1)
			writeJSONBody(w, 200, b.String())
			return
		}
		writeJSONBody(w, 200, `{"recordsets":[
		  {"id":"rs-hit-2","name":"www.example.com.","type":"A","ttl":300,"records":["203.0.113.2"]}
		],"metadata":{"total_count":501}}`)
	})

	records, err := NewHuaweicloud(srv.URL).ListRecords(context.Background(), hwCred(), hwTestZone(),
		dns.RecordFilter{Name: "www.example.com"})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("两页各命中一条，应当得到 2 条，得到 %d: %+v", len(records), records)
	}

	reqs := f.all()
	if len(reqs) != 2 {
		t.Fatalf("应当请求 2 页，实际 %d 次", len(reqs))
	}
	if !strings.Contains(reqs[1].Query, fmt.Sprintf("offset=%d", huaweicloudPerPage)) {
		t.Errorf("第二页 offset 必须是原始条数 %d（不是过滤后的 1），实际查询串: %s",
			huaweicloudPerPage, reqs[1].Query)
	}
}

// TestHuaweicloudListRecordsKeepsEmptyRecordset：值为空的记录集也要列出来。
//
// 丢掉它的后果是用户既看不到、也删不掉这个记录集 —— 那比显示一条空记录糟糕。
func TestHuaweicloudListRecordsKeepsEmptyRecordset(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"recordsets":[
		  {"id":"rs-empty","name":"gone.example.com.","type":"A","ttl":300,"records":[]}
		],"metadata":{"total_count":1}}`)
	})

	records, err := NewHuaweicloud(srv.URL).ListRecords(
		context.Background(), hwCred(), hwTestZone(), dns.RecordFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "rs-empty" {
		t.Fatalf("空记录集也应当出现（否则删不掉），得到 %+v", records)
	}
	if f.count() != 1 {
		t.Errorf("应当只请求一页，实际 %d 次", f.count())
	}
}

// ---------------------------------------------------------------------------
// 新增
// ---------------------------------------------------------------------------

const hwCreatedJSON = `{
  "id":"rs-new","name":"www.example.com.","description":"www 主机",
  "type":"A","ttl":600,"records":["203.0.113.7"],
  "status":"PENDING_CREATE","zone_id":"z1","zone_name":"example.com.",
  "default":false,"weight":1
}`

// hwDecodeBody 把最后一次请求的 JSON 体解出来。
func hwDecodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("请求体不是 JSON: %v（%s）", err, body)
	}
	return out
}

func TestHuaweicloudCreateRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwCreatedJSON)
	})

	rec, err := NewHuaweicloud(srv.URL).CreateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7",
			TTL: 600, Comment: "www 主机"})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}

	if f.last().Method != http.MethodPost {
		t.Errorf("应当用 POST，实际 %s", f.last().Method)
	}
	if f.last().Path != "/v2.1/zones/z1/recordsets" {
		t.Errorf("路径 = %q", f.last().Path)
	}

	body := hwDecodeBody(t, f.last().Body)
	// 记录名必须是 FQDN：华为云要求以点结尾，少一个点会被直接拒绝。
	if body["name"] != "www.example.com." {
		t.Errorf("name = %v，必须是带结尾点的 FQDN", body["name"])
	}
	if body["type"] != "A" {
		t.Errorf("type = %v", body["type"])
	}
	// 值必须是**数组**，哪怕只有一条 —— 华为云的单位是记录集。
	values, ok := body["records"].([]any)
	if !ok || len(values) != 1 || values[0] != "203.0.113.7" {
		t.Errorf("records = %v，期望只有一个元素的数组", body["records"])
	}
	if body["ttl"].(float64) != 600 {
		t.Errorf("ttl = %v，期望 600", body["ttl"])
	}
	if body["description"] != "www 主机" {
		t.Errorf("description = %v，应当来自 Comment", body["description"])
	}
	// weight 要显式发 1（主用）：0 在华为云是"备用"记录，
	// 万一服务商的默认值不是 1，用户会得到一个不参与解析的记录集。
	if body["weight"].(float64) != 1 {
		t.Errorf("weight = %v，新增时必须显式发 1（主用）", body["weight"])
	}

	// 返回给调用方的是"服务商实际存下来的样子"，名字带点这一层已经翻译掉。
	if rec.ID != "rs-new" || rec.Name != "www.example.com" || rec.Content != "203.0.113.7" {
		t.Errorf("返回的记录不符: %+v", rec)
	}
	if rec.TTL != 600 {
		t.Errorf("返回的 TTL = %d", rec.TTL)
	}
}

// TestHuaweicloudCreateRecordDefaultTTL：TTL=0 表示"交给服务商默认值"。
//
// 华为云没有 auto 哨兵值，所以翻译成文档写明的默认值 300，而不是把 0
// 原样发过去（0 会被服务商拒绝）。
func TestHuaweicloudCreateRecordDefaultTTL(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwCreatedJSON)
	})

	if _, err := NewHuaweicloud(srv.URL).CreateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}

	body := hwDecodeBody(t, f.last().Body)
	if body["ttl"].(float64) != huaweicloudDefaultTTL {
		t.Errorf("ttl = %v，期望默认值 %d", body["ttl"], huaweicloudDefaultTTL)
	}
	// 备注为空时不发这个字段：华为云的语义是"为空表示维持原值"。
	if _, ok := body["description"]; ok {
		t.Error("备注为空时不该发送 description")
	}
}

// TestHuaweicloudCreateRecordZoneApex：根域名写法的兼容。
//
// dns.DomainResult 里根域名用 "@" 表示，而华为云不认这个写法，
// 直接发过去只会得到一句"域名格式不正确"。
func TestHuaweicloudCreateRecordZoneApex(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"id":"rs-apex","name":"example.com.","type":"TXT","ttl":300,
		  "records":["v=spf1 -all"]}`)
	})

	if _, err := NewHuaweicloud(srv.URL).CreateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{Name: "@", Type: dns.TypeTXT, Content: "v=spf1 -all"}); err != nil {
		t.Fatal(err)
	}
	if body := hwDecodeBody(t, f.last().Body); body["name"] != "example.com." {
		t.Errorf("name = %v，\"@\" 应当被补成区域名的 FQDN", body["name"])
	}
}

// ---------------------------------------------------------------------------
// 修改 / 删除
// ---------------------------------------------------------------------------

const hwUpdatedJSON = `{
  "id":"rs1","name":"www.example.com.","description":"改过的",
  "type":"A","ttl":600,"records":["203.0.113.99"],
  "status":"PENDING_UPDATE","zone_id":"z1","zone_name":"example.com.","weight":1
}`

func TestHuaweicloudUpdateRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPut, "/v2.1/zones/z1/recordsets/rs1", func(w http.ResponseWriter, _ *http.Request) {
		// 修改返回 202（不是 200）。把它当成错误是最容易犯的错之一：
		// 用户会看到"修改失败"，而实际上已经改完了。
		writeJSONBody(w, http.StatusAccepted, hwUpdatedJSON)
	})

	rec, err := NewHuaweicloud(srv.URL).UpdateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{ID: "rs1", Name: "www.example.com", Type: dns.TypeA,
			Content: "203.0.113.99", TTL: 600, Comment: "改过的"})
	if err != nil {
		t.Fatalf("修改失败（202 应当算成功）: %v", err)
	}

	if f.last().Method != http.MethodPut {
		t.Errorf("应当用 PUT，实际 %s", f.last().Method)
	}
	if f.last().Path != "/v2.1/zones/z1/recordsets/rs1" {
		t.Errorf("路径 = %q，修改走的是 v2.1 的记录集端点", f.last().Path)
	}

	body := hwDecodeBody(t, f.last().Body)
	if body["name"] != "www.example.com." || body["type"] != "A" {
		t.Errorf("name/type 不符: %v", body)
	}
	// 只发一个值：整个记录集会被改成这一个值（丢数据的风险见文件头）。
	values, _ := body["records"].([]any)
	if len(values) != 1 || values[0] != "203.0.113.99" {
		t.Errorf("records = %v，期望只有一个元素的数组", body["records"])
	}
	if body["ttl"].(float64) != 600 {
		t.Errorf("ttl = %v", body["ttl"])
	}
	// 修改时不发 weight：文档写明"为空表示维持原值"，
	// 我们不该悄悄改掉用户设过的权重。
	if _, ok := body["weight"]; ok {
		t.Error("修改时不该发送 weight")
	}

	if rec.Content != "203.0.113.99" || rec.ID != "rs1" {
		t.Errorf("返回的记录不符: %+v", rec)
	}
}

// TestHuaweicloudUpdateOmittedTTLKeepsProviderValue：rec.TTL 为 0 时不发 ttl。
//
// 把 TTL 重置成默认的 300 会悄悄改掉用户设过的值，而用户只是改了个 IP。
func TestHuaweicloudUpdateOmittedTTLKeepsProviderValue(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPut, "/v2.1/zones/z1/recordsets/rs1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusAccepted, hwUpdatedJSON)
	})

	if _, err := NewHuaweicloud(srv.URL).UpdateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{ID: "rs1", Name: "www.example.com", Type: dns.TypeA,
			Content: "203.0.113.99"}); err != nil {
		t.Fatal(err)
	}

	body := hwDecodeBody(t, f.last().Body)
	if _, ok := body["ttl"]; ok {
		t.Errorf("TTL=0 时不该发送 ttl（华为云语义是维持原值），请求体: %v", body)
	}
}

func TestHuaweicloudDeleteRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/v2.1/zones/z1/recordsets/rs1", func(w http.ResponseWriter, _ *http.Request) {
		// 删除返回 202 + 空响应体。空体不能被当成"解析失败"。
		w.WriteHeader(http.StatusAccepted)
	})

	err := NewHuaweicloud(srv.URL).DeleteRecord(context.Background(), hwCred(),
		hwTestZone(), "rs1")
	if err != nil {
		t.Fatalf("删除失败（202 + 空体应当算成功）: %v", err)
	}
	if f.last().Method != http.MethodDelete {
		t.Errorf("应当用 DELETE，实际 %s", f.last().Method)
	}
	if f.last().Path != "/v2.1/zones/z1/recordsets/rs1" {
		t.Errorf("路径 = %q", f.last().Path)
	}
}

// hwPendingServer 起一个"通配"的假服务器：所有请求都回 200 空对象。
//
// 只用于"不该发出请求"的用例 —— 断言 f.count() == 0 时，
// 有没有路由并不重要。
func hwPendingServer(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f, srv := newFakeAPI(t)
	f.setFallback(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{}`)
	})
	return f, srv
}

// TestHuaweicloudPreflightErrors：本地就能判断的输入错误，不该发给服务商。
//
// 服务商对空值的报错往往是一句与用户操作对不上的话；而且这些错误
// 一旦发出去，用户还得等一次网络往返才看到提示。
func TestHuaweicloudPreflightErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		want string
		call func(*Huaweicloud) error
	}{
		{
			name: "列出记录缺区域 ID", want: "区域 ID",
			call: func(h *Huaweicloud) error {
				_, err := h.ListRecords(context.Background(), hwCred(), dns.Zone{}, dns.RecordFilter{})
				return err
			},
		},
		{
			name: "新增缺区域 ID", want: "区域 ID",
			call: func(h *Huaweicloud) error {
				_, err := h.CreateRecord(context.Background(), hwCred(), dns.Zone{},
					dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
				return err
			},
		},
		{
			name: "新增缺记录类型", want: "记录类型不能为空",
			call: func(h *Huaweicloud) error {
				_, err := h.CreateRecord(context.Background(), hwCred(), hwTestZone(),
					dns.Record{Name: "www.example.com", Content: "203.0.113.7"})
				return err
			},
		},
		{
			name: "新增缺记录值", want: "记录值不能为空",
			call: func(h *Huaweicloud) error {
				_, err := h.CreateRecord(context.Background(), hwCred(), hwTestZone(),
					dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "  "})
				return err
			},
		},
		{
			name: "新增缺记录名且区域名为空", want: "记录名不能为空",
			call: func(h *Huaweicloud) error {
				_, err := h.CreateRecord(context.Background(), hwCred(), dns.Zone{ID: "z1"},
					dns.Record{Type: dns.TypeA, Content: "203.0.113.7"})
				return err
			},
		},
		{
			name: "修改缺记录 ID", want: "记录 ID",
			call: func(h *Huaweicloud) error {
				_, err := h.UpdateRecord(context.Background(), hwCred(), hwTestZone(),
					dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"})
				return err
			},
		},
		{
			name: "删除缺记录 ID", want: "记录 ID",
			call: func(h *Huaweicloud) error {
				return h.DeleteRecord(context.Background(), hwCred(), hwTestZone(), "")
			},
		},
		{
			name: "删除缺区域 ID", want: "区域 ID",
			call: func(h *Huaweicloud) error {
				return h.DeleteRecord(context.Background(), hwCred(), dns.Zone{}, "rs1")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, srv := hwPendingServer(t)
			err := tc.call(NewHuaweicloud(srv.URL))
			if err == nil {
				t.Fatal("期望报错，实际成功")
			}
			// 用户看到的必须是中文说明，而不是英文的字段校验信息。
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误信息应当包含 %q，实际: %v", tc.want, err)
			}
			if f.count() != 0 {
				t.Errorf("本地就能判断的错误不该发出 %d 个请求", f.count())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 凭据
// ---------------------------------------------------------------------------

func TestHuaweicloudRequiresCredential(t *testing.T) {
	t.Parallel()

	f, srv := hwPendingServer(t)
	h := NewHuaweicloud(srv.URL)

	cases := []map[string]string{
		{},                              // 两个都缺
		{"access_key_id": hwTestAK},     // 缺 SK
		{"access_key_secret": hwTestSK}, // 缺 AK
		// 只有空白：用户从控制台复制时经常带上尾随空格 / 换行，
		// Credential.Field 会去掉它们，因此这等价于没填。
		{"access_key_id": "  ", "access_key_secret": "\t\n"},
	}
	for i, fields := range cases {
		_, err := h.ListZones(context.Background(), dns.Credential{Fields: fields})
		if err == nil {
			t.Fatalf("第 %d 组：缺凭据时应当报错", i)
		}
		// 报错要指名道姓说缺哪个字段，用户才知道去填什么。
		if !strings.Contains(err.Error(), "access_key_secret") {
			t.Errorf("第 %d 组：错误信息应当点出字段名，实际: %v", i, err)
		}
		// 凭据本身绝不能出现在错误信息里（它会进日志与审计）。
		if strings.Contains(err.Error(), hwTestSK) || strings.Contains(err.Error(), hwTestAK) {
			t.Errorf("第 %d 组：错误信息里出现了凭据内容", i)
		}
	}
	if f.count() != 0 {
		t.Errorf("凭据不全时不该发出任何请求，实际 %d 次", f.count())
	}
}

// TestHuaweicloudNeverSendsSecret：SK 绝不能出现在请求的任何地方。
//
// Authorization 里会出现 AK —— 那是标识符，本来就随请求发出；
// 而 SK 只参与 HMAC 计算，任何形式的出现都意味着凭据在裸奔。
func TestHuaweicloudNeverSendsSecret(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwZonesJSON(0, 1, 1))
	})
	f.on(http.MethodPost, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwCreatedJSON)
	})

	h := NewHuaweicloud(srv.URL)
	if _, err := h.ListZones(context.Background(), hwCred()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.CreateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}

	for _, req := range f.all() {
		if strings.Contains(hwRequestText(req), hwTestSK) {
			t.Errorf("%s %s 的请求里出现了 SK", req.Method, req.Path)
		}
	}
}

// hwRequestText 把一个录到的请求摊平成文本，方便整块搜索。
func hwRequestText(r recordedRequest) string {
	var b strings.Builder
	b.WriteString(r.Method + " " + r.Path + "?" + r.Query + "\n" + r.Body + "\n")
	for k, vs := range r.Header {
		b.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// 签名
// ---------------------------------------------------------------------------

// hwAuthParts 拆开 Authorization 头：
// "SDK-HMAC-SHA256 Access=<AK>, SignedHeaders=<h1;h2>, Signature=<hex>"
func hwAuthParts(t *testing.T, auth string) (access string, signed []string, signature string) {
	t.Helper()

	const prefix = "SDK-HMAC-SHA256 Access="
	if !strings.HasPrefix(auth, prefix) {
		t.Fatalf("Authorization 头格式不对: %q", auth)
	}
	rest := strings.TrimPrefix(auth, prefix)

	parts := strings.SplitN(rest, ", SignedHeaders=", 2)
	if len(parts) != 2 {
		t.Fatalf("Authorization 头缺少 SignedHeaders: %q", auth)
	}
	access = parts[0]

	parts = strings.SplitN(parts[1], ", Signature=", 2)
	if len(parts) != 2 {
		t.Fatalf("Authorization 头缺少 Signature: %q", auth)
	}
	return access, strings.Split(parts[0], ";"), parts[1]
}

func TestHuaweicloudSignsRequests(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwZonesJSON(0, 1, 1))
	})

	if _, err := NewHuaweicloud(srv.URL).ListZones(context.Background(), hwCred()); err != nil {
		t.Fatal(err)
	}
	req := f.last()

	access, signed, signature := hwAuthParts(t, req.Header.Get("Authorization"))
	if access != hwTestAK {
		t.Errorf("Access = %q，应当是 AK", access)
	}

	// X-Sdk-Date 必须参与签名：它是防重放的时间戳，服务商按它算有效期。
	if !hwContains(signed, "x-sdk-date") {
		t.Errorf("SignedHeaders 必须包含 x-sdk-date，实际 %v", signed)
	}
	if req.Header.Get("X-Sdk-Date") == "" {
		t.Error("缺少 X-Sdk-Date 头")
	} else if _, err := time.Parse(ddnsgo.BasicDateFormat, req.Header.Get("X-Sdk-Date")); err != nil {
		t.Errorf("X-Sdk-Date 格式不符（%q）: %v", req.Header.Get("X-Sdk-Date"), err)
	}
	// 在签名之前设置的公共头（Accept）会进 SignedHeaders —— 这是有意的：
	// 签名覆盖哪些头由 SignedHeaders 显式声明，服务商按这份声明校验。
	if !hwContains(signed, "accept") {
		t.Errorf("签名前设置的 Accept 应当被签进去，实际 %v", signed)
	}
	// Content-Type 是在签名**之后**设置的（照搬移植实现的顺序），
	// 因此不在签名范围内。这条断言把这个有意的顺序固定下来。
	if hwContains(signed, "content-type") {
		t.Errorf("Content-Type 不应出现在 SignedHeaders 里（它是在签名之后设置的），实际 %v", signed)
	}
	// Authorization 自己不能参与签名计算（先有鸡还是先有蛋）。
	if hwContains(signed, "authorization") {
		t.Errorf("Authorization 不能出现在 SignedHeaders 里，实际 %v", signed)
	}

	// 签名是 64 位小写十六进制（HMAC-SHA256 的 hex 表达）。
	if len(signature) != 64 || strings.Trim(signature, "0123456789abcdef") != "" {
		t.Errorf("Signature 不是 64 位十六进制: %q", signature)
	}
}

func hwContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestHuaweicloudSignatureCoversSentRequest 用假服务器**实际收到**的内容
// 重算一次签名，两边必须一致。
//
// 这不是在重复测签名算法（那是移植代码的事），而是验证本实现**怎么用它**：
// 只要签的路径、查询串、请求体或签名头集合与真正发出去的东西有一点点不一致，
// 重算结果就对不上。而真实环境里这种不一致的表现只是服务商回一句
// "签名不匹配"，几乎无法定位。
func TestHuaweicloudSignatureCoversSentRequest(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/v2.1/zones/z1/recordsets", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, hwCreatedJSON)
	})

	if _, err := NewHuaweicloud(srv.URL).CreateRecord(context.Background(), hwCred(), hwTestZone(),
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7",
			TTL: 600}); err != nil {
		t.Fatal(err)
	}

	sent := f.last()
	_, signed, _ := hwAuthParts(t, sent.Header.Get("Authorization"))

	urlStr := srv.URL + sent.Path
	if sent.Query != "" {
		urlStr += "?" + sent.Query
	}
	replay, err := http.NewRequest(sent.Method, urlStr, strings.NewReader(sent.Body))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range signed {
		if name == "host" {
			// Go 的服务端把 Host 从 Header 挪到了 r.Host，这里取不到；
			// 值就是假服务器的地址，与原始请求一致。
			replay.Header.Set("Host", replay.Host)
			continue
		}
		replay.Header.Set(name, sent.Header.Get(name))
	}

	signer := ddnsgo.Signer{Key: hwTestAK, Secret: hwTestSK}
	if err := signer.Sign(replay); err != nil {
		t.Fatalf("重算签名失败: %v", err)
	}
	if got, want := replay.Header.Get("Authorization"), sent.Header.Get("Authorization"); got != want {
		t.Errorf("签名与实际发出的内容对不上：\n实际发出: %s\n按收到的内容重算: %s", want, got)
	}
}

// ---------------------------------------------------------------------------
// 错误
// ---------------------------------------------------------------------------

// TestHuaweicloudErrorKeepsProviderMessage：错误信息必须带上服务商的原话。
//
// 一个光秃秃的"HTTP 400"没法告诉用户该去改什么；而"系统默认记录集不能删除"
// 这句话用户拿去搜就能搜到答案。
func TestHuaweicloudErrorKeepsProviderMessage(t *testing.T) {
	t.Parallel()

	const providerMsg = "Record set is default, can not be deleted."

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/v2.1/zones/z1/recordsets/rs-soa", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusBadRequest,
			`{"error_code":"DNS.0302","error_msg":"`+providerMsg+`"}`)
	})

	err := NewHuaweicloud(srv.URL).DeleteRecord(context.Background(), hwCred(),
		hwTestZone(), "rs-soa")
	if err == nil {
		t.Fatal("期望报错")
	}

	// 错误类型要能被上层识别 —— 靠错误文本判断"哪里出错"是脆弱的。
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if apiErr.Code != "DNS.0302" {
		t.Errorf("Code = %q，应当取出华为云的 error_code", apiErr.Code)
	}
	if !strings.Contains(err.Error(), providerMsg) {
		t.Errorf("错误信息必须带上服务商的原话，实际: %v", err)
	}
	// 操作名是中文的，用户才知道是哪一步失败。
	if !strings.Contains(err.Error(), "删除记录") {
		t.Errorf("错误信息应当点出中文操作名，实际: %v", err)
	}
}

func TestHuaweicloudUnauthorizedIsClassified(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusUnauthorized,
			`{"error_code":"DNS.0301","error_msg":"Authentication failed."}`)
	})

	_, err := NewHuaweicloud(srv.URL).ListZones(context.Background(), hwCred())
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	// 界面靠它把"凭据过期了，请重新授权"与"服务商那边出错了"区分开。
	if !apiErr.IsUnauthorized() {
		t.Errorf("IsUnauthorized 应当为 true，状态码 %d", apiErr.Status)
	}
	if !strings.Contains(err.Error(), "Authentication failed.") {
		t.Errorf("错误信息应当带上服务商的原话，实际: %v", err)
	}
}

// TestHuaweicloudMalformedResponseIsReported 验证畸形响应不会让内核崩溃，
// 也不会把整个响应体塞进错误信息（它可能很大、也可能含账号信息）。
func TestHuaweicloudMalformedResponseIsReported(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/v2/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `this is not json`)
	})

	_, err := NewHuaweicloud(srv.URL).ListZones(context.Background(), hwCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长，可能把响应体整个塞了进去: %d 字符", len(err.Error()))
	}
}

// TestHuaweicloudDeleteErrorIsNotFound：404 要能被识别成"资源不存在"。
//
// 用户在界面上删一条已经被别处删掉的记录时，这决定了界面提示
// "记录不存在，请刷新"还是"服务商出错了"。
func TestHuaweicloudDeleteNotFound(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/v2.1/zones/z1/recordsets/gone", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, http.StatusNotFound,
			`{"error_code":"DNS.0303","error_msg":"Record set does not exist."}`)
	})

	err := NewHuaweicloud(srv.URL).DeleteRecord(context.Background(), hwCred(),
		hwTestZone(), "gone")
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if !apiErr.IsNotFound() {
		t.Errorf("IsNotFound 应当为 true，状态码 %d", apiErr.Status)
	}
	if !strings.Contains(err.Error(), "Record set does not exist.") {
		t.Errorf("错误信息应当带上服务商的原话，实际: %v", err)
	}
}
