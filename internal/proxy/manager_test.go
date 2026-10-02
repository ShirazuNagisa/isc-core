package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖代理的**生命周期**：什么时候监听、路由从哪来、保存与生效
// 的顺序。Server 的路由与安全逻辑由 proxy_test.go 覆盖，这里只管编排。

// memStore 是内存版的路由仓储。
type memStore struct {
	mu     sync.Mutex
	routes []Route
	err    error
}

func (m *memStore) List(context.Context) ([]Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	return append([]Route(nil), m.routes...), nil
}

func (m *memStore) Replace(_ context.Context, routes []Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.routes = append([]Route(nil), routes...)
	return nil
}

func (m *memStore) set(routes ...Route) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes = routes
}

// TestSaveRoutesWorksWhileStopped 验证"先配置后启用"这条顺序能走通。
//
// 早先的版本在代理未运行时返回"代理未在运行"，而那让最自然的配置
// 顺序走不通 —— 用户会以为是自己填错了什么。
func TestSaveRoutesWorksWhileStopped(t *testing.T) {
	t.Parallel()

	store := &memStore{}
	m := NewManager(store, testLogger())

	if m.Status().Running {
		t.Fatal("初始状态不该是运行中")
	}

	err := m.SaveRoutes(context.Background(), []Route{{
		ID: "r1", Hosts: []string{"home.example.com"}, Upstream: "127.0.0.1:8096",
	}})
	if err != nil {
		t.Fatalf("代理未运行时也应当能保存路由: %v", err)
	}

	// 数据必须真的落库了 —— 否则"下次启动时生效"是空话。
	saved, _ := store.List(context.Background())
	if len(saved) != 1 {
		t.Fatalf("路由没有被保存，存储里有 %d 条", len(saved))
	}
}

// TestSaveValidatesBeforePersisting 验证不合法的路由不会被写进存储。
//
// 反过来的话，一份不合法数据会被写入而接口返回失败 —— 用户以为
// "没保存成功"，但内核下次启动时会因为这份数据而带着空路由表运行。
func TestSaveValidatesBeforePersisting(t *testing.T) {
	t.Parallel()

	store := &memStore{}
	m := NewManager(store, testLogger())

	bad := [][]Route{
		{{ID: "r1", Hosts: []string{"a.example.com"}, Upstream: "8.8.8.8:80"}},
		{{ID: "r2", Hosts: []string{"a.example.com"}, Upstream: "127.0.0.1"}},
		{{ID: "r3", Hosts: []string{"bad host"}, Upstream: "127.0.0.1:80"}},
		{{ID: "", Hosts: []string{"a.example.com"}, Upstream: "127.0.0.1:80"}},
	}

	for i, routes := range bad {
		if err := m.SaveRoutes(context.Background(), routes); err == nil {
			t.Errorf("第 %d 组应当被拒绝", i+1)
		}
	}

	saved, _ := store.List(context.Background())
	if len(saved) != 0 {
		t.Errorf("不合法路由不该被写入存储，实际有 %d 条", len(saved))
	}
}

// TestSaveRejectsDuplicateDomain 验证跨路由的冲突检查。
//
// 同一个域名指向两个上游时，请求打到哪一条取决于路由表的顺序，
// 而那个顺序对用户是不可见的。
func TestSaveRejectsDuplicateDomain(t *testing.T) {
	t.Parallel()

	store := &memStore{}
	m := NewManager(store, testLogger())

	err := m.SaveRoutes(context.Background(), []Route{
		{ID: "r1", Hosts: []string{"home.example.com"}, Upstream: "127.0.0.1:8001"},
		{ID: "r2", Hosts: []string{"home.example.com"}, Upstream: "127.0.0.1:8002"},
	})
	if err == nil {
		t.Fatal("同一个域名出现在两条路由里应当被拒绝")
	}

	// 错误信息必须说明白问题与后果。
	if !strings.Contains(err.Error(), "home.example.com") {
		t.Errorf("错误信息应当点名冲突的域名: %v", err)
	}
	if !strings.Contains(err.Error(), "顺序") {
		t.Errorf("错误信息应当说明后果（打到哪一条取决于顺序）: %v", err)
	}

	// 大小写不同也算冲突 —— HTTP 的 Host 大小写不敏感。
	err = m.SaveRoutes(context.Background(), []Route{
		{ID: "r1", Hosts: []string{"home.example.com"}, Upstream: "127.0.0.1:8001"},
		{ID: "r2", Hosts: []string{"HOME.Example.COM"}, Upstream: "127.0.0.1:8002"},
	})
	if err == nil {
		t.Error("大小写不同的同一域名也应当算冲突")
	}
}

// TestStartServesSavedRoutes 验证启动时会把存储里的路由加载进来。
func TestStartServesSavedRoutes(t *testing.T) {
	t.Parallel()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)

	store := &memStore{}
	store.set(Route{
		ID: "r1", Hosts: []string{"home.example.com"},
		Upstream: strings.TrimPrefix(backend.URL, "http://"),
	})

	m := NewManager(store, testLogger())
	port := freePort(t)

	if err := m.Start(context.Background(), port); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	st := m.Status()
	if !st.Running || st.Port != port {
		t.Fatalf("状态不对: %+v", st)
	}
	if st.Routes != 1 {
		t.Errorf("应当加载 1 条路由，得到 %d", st.Routes)
	}

	// 真的能转发。
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	req.Host = "home.example.com"
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("经代理请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("响应体 = %q", body)
	}
}

// TestStartIsIdempotentOnPortChange 验证改端口时先释放旧的。
//
// 不先释放的话，新监听会因为端口占用而失败 —— 而那个错误在用户看来
// 是"改端口之后代理起不来了"。
func TestStartIsIdempotentOnPortChange(t *testing.T) {
	t.Parallel()

	m := NewManager(&memStore{}, testLogger())

	p1 := freePort(t)
	if err := m.Start(context.Background(), p1); err != nil {
		t.Fatalf("第一次启动失败: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	p2 := freePort(t)
	if err := m.Start(context.Background(), p2); err != nil {
		t.Fatalf("改端口重启失败: %v", err)
	}

	if got := m.Status().Port; got != p2 {
		t.Errorf("端口 = %d，期望 %d", got, p2)
	}
	// 旧端口必须已经被释放。
	if !waitPortReleased(t, p1) {
		t.Error("旧端口仍然在被监听 —— 改端口时没有释放")
	}
}

func TestStartRejectsOccupiedPort(t *testing.T) {
	t.Parallel()

	// 先占住一个端口。
	//
	// **必须绑在所有接口上**（":port"），不能只用 httptest.Server
	//（它绑 127.0.0.1）。Windows 允许 0.0.0.0:port 与 127.0.0.1:port
	// 同时存在，因此用后者占端口时管理器仍能绑成功，测试的前提就不成立。
	port := freePort(t)
	blocker, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("无法占用端口 %d: %v", port, err)
	}
	t.Cleanup(func() { _ = blocker.Close() })

	m := NewManager(&memStore{}, testLogger())
	err = m.Start(context.Background(), port)
	if err == nil {
		_ = m.Stop(context.Background())
		t.Fatal("端口被占用时应当报错")
	}

	// 错误信息要指向用户能处理的事。
	if !strings.Contains(err.Error(), "占用") {
		t.Errorf("错误信息应当提示端口被占用: %v", err)
	}

	// 失败原因必须留在状态里 —— 只写日志的话，用户在界面上
	// 只看到"代理没开"，不知道为什么。
	if st := m.Status(); st.Error == "" {
		t.Error("失败原因应当留在状态里供界面展示")
	}
}

// TestStartSucceedsDespiteBadStoredRoutes 验证坏数据不阻止启动。
//
// 带着空路由表启动（返回 404）比"代理根本没起来"更容易诊断，
// 而且用户还能通过界面把路由修好。直接失败的话，用户得先修好
// 数据才能启动服务 —— 而他可能正是想通过界面去修。
func TestStartSucceedsDespiteBadStoredRoutes(t *testing.T) {
	t.Parallel()

	store := &memStore{}
	store.set(Route{
		ID: "bad", Hosts: []string{"a.example.com"},
		Upstream: "8.8.8.8:80", // 公网地址，非法
	})

	m := NewManager(store, testLogger())
	if err := m.Start(context.Background(), freePort(t)); err != nil {
		t.Fatalf("坏数据不该阻止启动: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	st := m.Status()
	if !st.Running {
		t.Error("应当仍然在运行（带空路由表）")
	}
	if st.Routes != 0 {
		t.Errorf("非法路由不该生效，得到 %d 条", st.Routes)
	}
	if st.Error == "" {
		t.Error("应当记录下加载失败的原因")
	}
}

func TestStartLoadErrorDoesNotBlockListen(t *testing.T) {
	t.Parallel()

	store := &memStore{err: errors.New("数据库暂时不可用")}
	m := NewManager(store, testLogger())

	if err := m.Start(context.Background(), freePort(t)); err != nil {
		t.Fatalf("读取失败不该阻止监听: %v", err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	if !m.Status().Running {
		t.Error("应当仍然在运行")
	}
}

func TestStopIsIdempotent(t *testing.T) {
	t.Parallel()

	m := NewManager(&memStore{}, testLogger())

	// 从未启动过就停止：不该报错。
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("未运行时停止不该报错: %v", err)
	}

	port := freePort(t)
	if err := m.Start(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Errorf("重复停止不该报错: %v", err)
	}
	if m.Status().Running {
		t.Error("停止后状态应当是不在运行")
	}
	// 端口必须被释放。
	//
	// 轮询而不是立即检查：监听关闭与端口在操作系统层面变为可用
	// 之间有极短的时间差，立即检查会在负载高时偶发失败 ——
	// 而那种失败看起来像是"停止没有释放端口"，很难定位。
	if !waitPortReleased(t, port) {
		t.Error("停止后端口仍在被监听")
	}
}

func TestReloadWithoutStartFails(t *testing.T) {
	t.Parallel()

	m := NewManager(&memStore{}, testLogger())
	if err := m.Reload(context.Background()); err == nil {
		t.Error("未运行时重新加载应当报错")
	}
}

// TestReloadIsHot 验证改路由不需要重启监听。
//
// 重启会切断在途请求 —— 那会让正在看视频的人莫名其妙地断流。
func TestReloadIsHot(t *testing.T) {
	t.Parallel()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "第一个上游")
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "第二个上游")
	}))
	t.Cleanup(second.Close)

	store := &memStore{}
	store.set(Route{
		ID: "r1", Hosts: []string{"a.example.com"},
		Upstream: strings.TrimPrefix(first.URL, "http://"),
	})

	m := NewManager(store, testLogger())
	port := freePort(t)
	if err := m.Start(context.Background(), port); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Stop(context.Background()) }() //nolint:errcheck // 测试清理

	if got := fetch(t, port, "a.example.com"); got != "第一个上游" {
		t.Fatalf("首次响应 = %q", got)
	}

	// 换上游并保存：应当立即生效，且监听端口不变。
	err := m.SaveRoutes(context.Background(), []Route{{
		ID: "r1", Hosts: []string{"a.example.com"},
		Upstream: strings.TrimPrefix(second.URL, "http://"),
	}})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	if got := fetch(t, port, "a.example.com"); got != "第二个上游" {
		t.Errorf("改上游后响应 = %q，期望「第二个上游」—— 热更新没生效", got)
	}
	if m.Status().Port != port {
		t.Error("热更新不该改变监听端口")
	}
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

func fetch(t *testing.T, port int, host string) string {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // 测试清理

	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

// portOf 从 httptest.Server 的 URL 里取出端口。
func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	var port int
	if _, err := fmt.Sscanf(rawURL, "http://127.0.0.1:%d", &port); err != nil {
		t.Fatalf("解析端口失败: %v", err)
	}
	return port
}

// waitPortReleased 等待端口不再被监听，最多等 3 秒。
//
// 用轮询而不是一次检查：监听关闭与端口在操作系统层面变为可用之间有
// 极短的时间差，立即检查会在负载高时偶发失败。
func waitPortReleased(t *testing.T, port int) bool {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp",
			fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
