package tier1

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
)

// 本文件是 Tier-1 实现的**参考测试**。
//
// 其余几家（阿里云 / 腾讯云 / 华为云 / GoDaddy / DNSPod）的测试应当
// 照此结构编写：起一个假服务商，断言"发出的请求长什么样"与
// "响应被翻译成了什么"。这样测的是**契约**而不是实现细节 ——
// 请求体字段名写错、URL 拼错、响应字段解析错，都能被抓到。
//
// 用假服务器而不是打真实 API：真实调用需要凭据、会产生副作用、
// 而且 CI 上没有网络。假服务器能覆盖全部路径，包括真实 API 上
// 很难构造的错误分支（401 / 404 / 畸形响应）。

// fakeAPI 是一个记录请求并可编程响应的假服务商。
type fakeAPI struct {
	mu       sync.Mutex
	requests []recordedRequest
	// routes 按"METHOD 路径前缀"匹配处理器。
	routes map[string]http.HandlerFunc
	// fallback 在没有任何路由匹配时调用。
	fallback http.HandlerFunc
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
	Header http.Header
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f := &fakeAPI{routes: make(map[string]http.HandlerFunc)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Body:   string(body),
			Header: r.Header.Clone(),
		})
		handler := f.match(r.Method, r.URL.Path)
		f.mu.Unlock()

		if handler == nil {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":404,"message":"no route"}]}`))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) match(method, path string) http.HandlerFunc {
	if h, ok := f.routes[method+" "+path]; ok {
		return h
	}
	if f.fallback != nil {
		return f.fallback
	}
	return nil
}

func (f *fakeAPI) on(method, path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routes[method+" "+path] = h
}

func (f *fakeAPI) setFallback(h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fallback = h
}

func (f *fakeAPI) last() recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return recordedRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeAPI) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func writeJSONBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func cfCred() dns.Credential {
	return dns.Credential{
		ID: "c1", Provider: "cloudflare",
		Fields: map[string]string{"token": "test-token"},
	}
}

// ---------------------------------------------------------------------------
// 凭据校验
// ---------------------------------------------------------------------------

func TestCloudflareVerify(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{
			name: "有效令牌", status: 200,
			body: `{"success":true,"result":{"status":"active"}}`,
		},
		{
			name: "无效令牌", status: 200, wantErr: true,
			body: `{"success":false,"errors":[{"code":1000,"message":"Invalid API Token"}]}`,
		},
		{
			name: "鉴权失败", status: 401, wantErr: true,
			body: `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`,
		},
		{
			name: "令牌被停用", status: 200, wantErr: true,
			body: `{"success":true,"result":{"status":"disabled"}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, srv := newFakeAPI(t)
			f := &Cloudflare{meta: dns.Meta{Name: "cloudflare"}, baseURL: srv.URL}

			// 用一个专用假服务器返回固定响应。
			f2, srv2 := newFakeAPI(t)
			f2.on(http.MethodGet, "/user/tokens/verify", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONBody(w, tc.status, tc.body)
			})
			f.baseURL = srv2.URL

			err := f.Verify(context.Background(), cfCred())
			if tc.wantErr && err == nil {
				t.Fatal("期望报错，实际成功")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("期望成功，实际报错: %v", err)
			}
		})
	}
}

// TestCloudflareVerifySendsToken 验证鉴权头正确。
func TestCloudflareVerifySendsToken(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/user/tokens/verify", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"success":true,"result":{"status":"active"}}`)
	})

	c := NewCloudflare(srv.URL)
	if err := c.Verify(context.Background(), cfCred()); err != nil {
		t.Fatalf("校验失败: %v", err)
	}

	got := f.last().Header.Get("Authorization")
	if got != "Bearer test-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestCloudflareVerifyRequiresToken(t *testing.T) {
	t.Parallel()

	c := NewCloudflare("http://127.0.0.1:1")
	err := c.Verify(context.Background(), dns.Credential{Fields: map[string]string{}})
	if err == nil {
		t.Fatal("缺少令牌时应当报错")
	}
}

// ---------------------------------------------------------------------------
// 区域
// ---------------------------------------------------------------------------

func TestCloudflareListZones(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "2" {
			writeJSONBody(w, 200, `{
			  "success": true,
			  "result": [{"id":"z2","name":"second.com","status":"active"}],
			  "result_info": {"page":2,"total_pages":2}
			}`)
			return
		}
		writeJSONBody(w, 200, `{
		  "success": true,
		  "result": [
		    {"id":"z1","name":"example.com","status":"active"},
		    {"id":"z3","name":"pending.com","status":"pending"}
		  ],
		  "result_info": {"page":1,"total_pages":2}
		}`)
	})

	zones, err := NewCloudflare(srv.URL).ListZones(context.Background(), cfCred())
	if err != nil {
		t.Fatalf("列出区域失败: %v", err)
	}

	// 分页应当被跟随：第一页 2 条（其中 1 条 pending 被滤掉）+ 第二页 1 条。
	if len(zones) != 2 {
		t.Fatalf("应当得到 2 个活跃区域，得到 %d: %+v", len(zones), zones)
	}
	if zones[0].Name != "example.com" || zones[1].Name != "second.com" {
		t.Errorf("区域列表不符: %+v", zones)
	}

	// pending 区域必须被过滤：在它下面改记录不会生效，
	// 列出来只会让用户以为"我改了但没用"。
	for _, z := range zones {
		if z.Name == "pending.com" {
			t.Error("pending 状态的区域不应出现在列表里")
		}
	}
	if f.count() != 2 {
		t.Errorf("应当请求 2 页，实际 %d 次", f.count())
	}
}

func TestCloudflareListZonesError(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 403, `{"success":false,"errors":[{"code":9109,"message":"Invalid access token"}]}`)
	})

	_, err := NewCloudflare(srv.URL).ListZones(context.Background(), cfCred())
	if err == nil {
		t.Fatal("期望报错")
	}
	// 错误信息必须带上服务商的说明 —— 一个光秃秃的"HTTP 403"
	// 无法告诉用户该去改什么权限。
	if !strings.Contains(err.Error(), "Invalid access token") {
		t.Errorf("错误信息应当包含服务商说明，得到: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 记录
// ---------------------------------------------------------------------------

func TestCloudflareListRecords(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones/z1/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{
		  "success": true,
		  "result": [
		    {"id":"r1","name":"www.example.com","type":"A","content":"203.0.113.7","ttl":1,"proxied":true},
		    {"id":"r2","name":"example.com","type":"MX","content":"mail.example.com","ttl":600,"priority":10},
		    {"id":"r3","name":"example.com","type":"TXT","content":"v=spf1 -all","ttl":3600}
		  ],
		  "result_info": {"page":1,"total_pages":1}
		}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	records, err := NewCloudflare(srv.URL).ListRecords(context.Background(), cfCred(), zone, dns.RecordFilter{})
	if err != nil {
		t.Fatalf("列出记录失败: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("应当得到 3 条记录，得到 %d", len(records))
	}

	// TTL=1 在 Cloudflare 里表示 auto，对外统一表达为 0。
	if records[0].TTL != 0 {
		t.Errorf("ttl=1 应当被翻译成 0（auto），得到 %d", records[0].TTL)
	}
	if !records[0].Proxied {
		t.Error("proxied 未被正确解析")
	}
	// MX 的优先级。
	if records[1].Priority != 10 {
		t.Errorf("MX 优先级 = %d, 期望 10", records[1].Priority)
	}
}

func TestCloudflareListRecordsSendsFilter(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones/z1/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"success":true,"result":[],"result_info":{"page":1,"total_pages":1}}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	_, err := NewCloudflare(srv.URL).ListRecords(context.Background(), cfCred(), zone,
		dns.RecordFilter{Type: dns.TypeAAAA, Name: "home.example.com"})
	if err != nil {
		t.Fatal(err)
	}

	q := f.last().Query
	if !strings.Contains(q, "type=AAAA") {
		t.Errorf("类型过滤未发出，查询串: %s", q)
	}
	if !strings.Contains(q, "name=home.example.com") {
		t.Errorf("名称过滤未发出，查询串: %s", q)
	}
}

func TestCloudflareCreateRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/zones/z1/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{
		  "success": true,
		  "result": {"id":"new1","name":"www.example.com","type":"A",
		             "content":"203.0.113.7","ttl":600,"proxied":false}
		}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	rec, err := NewCloudflare(srv.URL).CreateRecord(context.Background(), cfCred(), zone,
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 600})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if rec.ID != "new1" {
		t.Errorf("返回的记录 ID = %q", rec.ID)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(f.last().Body), &body); err != nil {
		t.Fatalf("请求体不是 JSON: %v", err)
	}
	if body["type"] != "A" || body["name"] != "www.example.com" || body["content"] != "203.0.113.7" {
		t.Errorf("请求体不符: %v", body)
	}
	if body["ttl"].(float64) != 600 {
		t.Errorf("TTL = %v, 期望 600", body["ttl"])
	}
	// A 记录可代理，应当显式发送 proxied。
	if _, ok := body["proxied"]; !ok {
		t.Error("A 记录应当发送 proxied 字段")
	}
	// 非 MX/SRV 不应发送 priority。
	if _, ok := body["priority"]; ok {
		t.Error("A 记录不该发送 priority")
	}
}

// TestCloudflareCreateRecordOmitsProxiedForTXT 是最容易出错的一条。
//
// 对 TXT / MX 这类记录发送 proxied 会被 Cloudflare 直接拒绝 ——
// 而那是一个"用户什么都没做错却收到报错"的场景。
func TestCloudflareCreateRecordOmitsProxiedForTXT(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/zones/z1/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"t1","name":"example.com","type":"TXT","content":"v=spf1 -all","ttl":3600}}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	_, err := NewCloudflare(srv.URL).CreateRecord(context.Background(), cfCred(), zone,
		dns.Record{Name: "example.com", Type: dns.TypeTXT, Content: "v=spf1 -all", TTL: 3600, Proxied: true})
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(f.last().Body), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["proxied"]; ok {
		t.Error("TXT 记录不该发送 proxied —— Cloudflare 会直接拒绝这个请求")
	}
}

// TestCloudflareCreateRecordAutoTTL 验证 TTL=0 被翻译成 Cloudflare 的 auto。
func TestCloudflareCreateRecordAutoTTL(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPost, "/zones/z1/dns_records", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"a1","name":"www.example.com","type":"A","content":"203.0.113.7","ttl":1}}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	if _, err := NewCloudflare(srv.URL).CreateRecord(context.Background(), cfCred(), zone,
		dns.Record{Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.7", TTL: 0}); err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	_ = json.Unmarshal([]byte(f.last().Body), &body)
	if body["ttl"].(float64) != 1 {
		t.Errorf("TTL=0（auto）应当被翻译成 Cloudflare 的 1，得到 %v", body["ttl"])
	}
}

func TestCloudflareUpdateRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodPut, "/zones/z1/dns_records/r1", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"r1","name":"www.example.com","type":"A","content":"203.0.113.99","ttl":600}}`)
	})

	zone := dns.Zone{ID: "z1", Name: "example.com"}
	rec, err := NewCloudflare(srv.URL).UpdateRecord(context.Background(), cfCred(), zone,
		dns.Record{ID: "r1", Name: "www.example.com", Type: dns.TypeA, Content: "203.0.113.99", TTL: 600})
	if err != nil {
		t.Fatalf("修改失败: %v", err)
	}
	if rec.Content != "203.0.113.99" {
		t.Errorf("返回内容 = %q", rec.Content)
	}
	if f.last().Method != http.MethodPut {
		t.Errorf("应当用 PUT，实际 %s", f.last().Method)
	}
}

func TestCloudflareUpdateRequiresID(t *testing.T) {
	t.Parallel()

	_, srv := newFakeAPI(t)
	_, err := NewCloudflare(srv.URL).UpdateRecord(context.Background(), cfCred(),
		dns.Zone{ID: "z1"}, dns.Record{Name: "www.example.com", Type: dns.TypeA})
	if err == nil {
		t.Fatal("缺少记录 ID 时应当报错")
	}
}

func TestCloudflareDeleteRecord(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/zones/z1/dns_records/r1", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `{"success":true,"result":{"id":"r1"}}`)
	})

	err := NewCloudflare(srv.URL).DeleteRecord(context.Background(), cfCred(),
		dns.Zone{ID: "z1"}, "r1")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if f.last().Method != http.MethodDelete {
		t.Errorf("应当用 DELETE，实际 %s", f.last().Method)
	}
}

func TestCloudflareDeleteNotFound(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodDelete, "/zones/z1/dns_records/gone", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 404, `{"success":false,"errors":[{"code":81044,"message":"Record does not exist"}]}`)
	})

	err := NewCloudflare(srv.URL).DeleteRecord(context.Background(), cfCred(),
		dns.Zone{ID: "z1"}, "gone")
	if err == nil {
		t.Fatal("期望报错")
	}

	// 错误类型必须能被上层识别 —— 靠错误文本判断"记录不存在"是脆弱的。
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if !apiErr.IsNotFound() {
		t.Errorf("IsNotFound 应当为 true，状态码 %d", apiErr.Status)
	}
}

// TestCloudflareUnauthorizedIsClassified 验证鉴权失败可被识别。
//
// 界面靠它把"凭据过期了，请重新授权"与"服务商那边出错了"区分开 ——
// 二者的处置方式完全不同。
func TestCloudflareUnauthorizedIsClassified(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 401, `{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`)
	})

	_, err := NewCloudflare(srv.URL).ListZones(context.Background(), cfCred())
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		t.Fatalf("错误应当是 *APIError，得到 %T", err)
	}
	if !apiErr.IsUnauthorized() {
		t.Error("IsUnauthorized 应当为 true")
	}
}

// TestCloudflareMalformedResponseIsReported 验证畸形响应不会让内核崩溃。
func TestCloudflareMalformedResponseIsReported(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 200, `this is not json`)
	})

	_, err := NewCloudflare(srv.URL).ListZones(context.Background(), cfCred())
	if err == nil {
		t.Fatal("畸形响应应当被报成错误")
	}
	// 错误信息不该原样带上整个响应体（它可能很大，也可能含账号信息）。
	if len(err.Error()) > 500 {
		t.Errorf("错误信息过长，可能把响应体整个塞了进去: %d 字符", len(err.Error()))
	}
}

// asAPIError 是 errors.As 的薄封装，避免本文件到处 import errors。
func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Global API Key 误填
// ---------------------------------------------------------------------------

// 把 Global API Key 当令牌发出去时，Cloudflare 回 6003 Invalid request
// headers —— 用户完全看不出问题在哪。这里断言我们把它翻译成了
// 指向「API 令牌」页面的说明。
func TestCloudflareVerifyExplainsGlobalAPIKey(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/user/tokens/verify", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 400,
			`{"success":false,"errors":[{"code":6003,"message":"Invalid request headers"}]}`)
	})

	c := NewCloudflare(srv.URL)
	err := c.Verify(context.Background(), dns.Credential{
		Fields: map[string]string{"token": "0123456789abcdef0123456789abcdef01234"},
	})
	if err == nil {
		t.Fatal("应当报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "Global API Key") {
		t.Fatalf("错误信息没点明 Global API Key: %s", msg)
	}
	if strings.Contains(msg, "6003") {
		t.Fatalf("不该把原始错误码甩给用户: %s", msg)
	}
}

// 形状本身就是 Global API Key 时，即使服务商回的是别的错误码也要提示。
func TestCloudflareGlobalAPIKeyHintByShape(t *testing.T) {
	t.Parallel()

	// 37 位十六进制 = Global API Key。
	const globalKey = "0123456789abcdef0123456789abcdef01234"
	if len(globalKey) != 37 {
		t.Fatalf("测试样本长度应为 37，实际 %d", len(globalKey))
	}
	if !CloudflareGlobalAPIKeyHint(globalKey) {
		t.Error("37 位十六进制应当被认作 Global API Key")
	}
	// 40 位的 API Token（含 - 与 _）不能被误判。
	const apiToken = "abcdefghijklmnopqrstuvwxyz0123456789_-AB"
	if len(apiToken) != 40 {
		t.Fatalf("测试样本长度应为 40，实际 %d", len(apiToken))
	}
	if CloudflareGlobalAPIKeyHint(apiToken) {
		t.Error("40 位 API Token 被误判成了 Global API Key")
	}
	// 有 6003 时无条件判定。
	if !CloudflareGlobalAPIKeyHint(apiToken, 6003) {
		t.Error("错误码 6003 应当触发该提示")
	}
	if CloudflareGlobalAPIKeyHint(apiToken, 1000) {
		t.Error("错误码 1000（令牌无效）不该触发 Global API Key 提示")
	}
}

// 直接进 DNS 分区（没先点"校验"）时，也要给出同样的指引。
func TestCloudflareListZonesExplainsGlobalAPIKey(t *testing.T) {
	t.Parallel()

	f, srv := newFakeAPI(t)
	f.on(http.MethodGet, "/zones", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONBody(w, 400,
			`{"success":false,"errors":[{"code":6003,"message":"Invalid request headers"}]}`)
	})

	c := NewCloudflare(srv.URL)
	_, err := c.ListZones(context.Background(), dns.Credential{
		Fields: map[string]string{"token": "0123456789abcdef0123456789abcdef01234"},
	})
	if err == nil {
		t.Fatal("应当报错")
	}
	if !strings.Contains(err.Error(), "Global API Key") {
		t.Fatalf("列出区域失败时没点明 Global API Key: %s", err)
	}
}
