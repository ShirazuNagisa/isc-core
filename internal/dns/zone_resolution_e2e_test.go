// Package dns_test 是 dns 的外部测试包。
//
// 用外部包而不是 dns 包：这里要把**领域服务**和**真实的服务商实现**接在
// 一起跑，而 tier1 已经 import 了 dns —— 放在 dns 包内会形成循环。
package dns_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ShirazuNagisa/isc-core/internal/dns"
	"github.com/ShirazuNagisa/isc-core/internal/provider/tier1"
)

// 本文件复现并锁住一个真实故障。
//
// 用户点开 DNS 分区，区域列出来了，但记录列表报：
//
//	HTTP 404: Could not route to /client/v4/zones/shirazu-nagisa.com/dns_records,
//	perhaps your object identifier is invalid?
//
// 原因是 Cloudflare 的记录端点要的是**区域 ID**，而用户手里（以及界面
// 传下来的）是**区域名**。这里用一个"只认 ID"的假 Cloudflare 把整条
// 链路（dns.Service → tier1.Cloudflare → HTTP）跑一遍：给名字时要能成功，
// 否则就是又退回了那个 bug。

// fakeCloudflare 模仿 Cloudflare 的路由规则：区域路径里只认 32 位十六进制 ID。
type fakeCloudflare struct {
	zoneID   string
	zoneName string
	// sawPaths 记录收到的请求路径，用于断言真的打到了 ID 那条路径。
	sawPaths []string
}

const cfRecordJSON = `{
  "success": true,
  "result": [
    {"id":"rec-1","name":"www.example.com","type":"A","content":"192.0.2.1","ttl":300,"proxied":false}
  ],
  "result_info": {"page":1,"per_page":100,"total_pages":1,"count":1,"total_count":1}
}`

func (f *fakeCloudflare) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.sawPaths = append(f.sawPaths, r.URL.Path)

		switch {
		case r.URL.Path == "/zones":
			writeJSON(w, map[string]any{
				"success": true,
				"result": []map[string]any{
					{"id": f.zoneID, "name": f.zoneName, "status": "active"},
				},
				"result_info": map[string]any{
					"page": 1, "per_page": 50, "total_pages": 1, "count": 1, "total_count": 1,
				},
			})
		case r.URL.Path == "/zones/"+f.zoneID+"/dns_records":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cfRecordJSON))
		default:
			// Cloudflare 对"路径里的标识符不是合法 ID"就是这个回答。
			writeJSONStatus(w, http.StatusNotFound, map[string]any{
				"success": false,
				"errors": []map[string]any{{
					"code":    7003,
					"message": "Could not route to " + r.URL.Path + ", perhaps your object identifier is invalid?",
				}},
			})
		}
	})
}

func writeJSON(w http.ResponseWriter, body any) { writeJSONStatus(w, http.StatusOK, body) }

func writeJSONStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type creds struct{}

func (creds) Resolve(context.Context, string) (dns.Credential, error) {
	return dns.Credential{
		ID: "cred", Provider: "cloudflare",
		Fields: map[string]string{"token": "test-token"},
	}, nil
}

func newService(t *testing.T, srv *httptest.Server) *dns.Service {
	t.Helper()
	impl := tier1.NewCloudflare(srv.URL)
	return dns.NewService(creds{}, func(string) (dns.Provider, bool) { return impl, true })
}

const testZoneID = "023e105f4ecef8ad9ca31a8372d0c353"
const testZoneName = "shirazu-nagisa.com"

// 用户实际走的那条路：界面把**区域名**放进路径。
func TestListRecordsByZoneNameReachesCloudflareWithZoneID(t *testing.T) {
	fake := &fakeCloudflare{zoneID: testZoneID, zoneName: testZoneName}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	records, err := newService(t, srv).ListRecords(context.Background(), "cred", testZoneName, dns.RecordFilter{})
	if err != nil {
		t.Fatalf("按区域名列记录失败（这正是用户遇到的那个 404）: %v", err)
	}
	if len(records) != 1 || records[0].Content != "192.0.2.1" {
		t.Fatalf("记录没解析出来: %+v", records)
	}

	// 关键断言：真正打到服务商的是带区域 ID 的那条路径，
	// 而不是把区域名当成 ID 发过去。
	want := "/zones/" + testZoneID + "/dns_records"
	found := false
	for _, p := range fake.sawPaths {
		if p == want {
			found = true
		}
		if strings.Contains(p, testZoneName) && strings.Contains(p, "dns_records") {
			t.Fatalf("区域名被当成 ID 拼进了路径: %s", p)
		}
	}
	if !found {
		t.Fatalf("没有请求 %s，实际路径: %v", want, fake.sawPaths)
	}
}

// 按契约传区域 ID 同样要能用。
func TestListRecordsByZoneID(t *testing.T) {
	fake := &fakeCloudflare{zoneID: testZoneID, zoneName: testZoneName}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	records, err := newService(t, srv).ListRecords(context.Background(), "cred", testZoneID, dns.RecordFilter{})
	if err != nil {
		t.Fatalf("按区域 ID 列记录失败: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("记录数不对: %+v", records)
	}
}

// 区域名对不上时必须给一句人能懂的话，而不是把服务商的
// "object identifier is invalid" 冒泡上去。
func TestListRecordsUnknownZoneIsExplained(t *testing.T) {
	fake := &fakeCloudflare{zoneID: testZoneID, zoneName: testZoneName}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	_, err := newService(t, srv).ListRecords(context.Background(), "cred", "other.example", dns.RecordFilter{})
	if err == nil {
		t.Fatal("不存在的区域应当报错")
	}
	if !strings.Contains(err.Error(), "other.example") {
		t.Fatalf("错误信息里没点名区域: %v", err)
	}
	if strings.Contains(err.Error(), "object identifier") {
		t.Fatalf("不该把服务商的原话直接抛给用户: %v", err)
	}
}
