package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/remote"
)

// 本文件是**远程面安全边界**的棘轮。
//
// # 它防的是什么
//
// 远程面与本地管理面用的是同一批处理器，区别只在外面那层白名单。
// 因此这个功能最可能的严重缺陷不是"某个处理器写错了"，而是
// **白名单里多了一条**：有人为了修一个手机上的问题，顺手把
// `GET /v1/settings` 或 `POST /v1/credentials` 加进去，而那条改动
// 看起来完全无害 —— 直到内核管理面因此暴露在局域网上。
//
// 所以这里逐条遍历白名单，并把一批"绝对不该出现"的路径钉死。

// testLocalToken 是本地面测试用的访问令牌。
const testLocalToken = "test-local-token"

// memStore 是设备存储的内存实现。
//
// 定义在测试里而不是生产代码里：远程面的其余部分只依赖接口，
// 因此测试不需要真的 SQLite（那条路径由 store 包自己的测试覆盖）。
type memStore struct {
	mu      sync.Mutex
	devices map[string]remote.Device
}

func newMemStore() *memStore {
	return &memStore{devices: map[string]remote.Device{}}
}

func (m *memStore) CreateDevice(_ context.Context, d remote.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.ID] = d
	return nil
}

func (m *memStore) ListDevices(context.Context) ([]remote.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]remote.Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	return out, nil
}

func (m *memStore) Device(_ context.Context, id string) (remote.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return remote.Device{}, remote.ErrDeviceNotFound
	}
	return d, nil
}

func (m *memStore) DeviceByTokenHash(_ context.Context, hash []byte) (remote.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.devices {
		// 与存储层同样的比较：逐字节。真实实现走的是索引查询，
		// 但两者对外都必须只有一个命中。
		if len(d.TokenHash) == len(hash) && string(d.TokenHash) == string(hash) {
			return d, nil
		}
	}
	return remote.Device{}, remote.ErrDeviceNotFound
}

func (m *memStore) UpdateDevice(_ context.Context, id string, p remote.DevicePatch) (remote.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return remote.Device{}, remote.ErrDeviceNotFound
	}
	if p.Label != nil {
		d.Label = *p.Label
	}
	if p.Role != nil {
		d.Role = *p.Role
	}
	if p.NotificationsEnabled != nil {
		d.NotificationsEnabled = *p.NotificationsEnabled
	}
	m.devices[id] = d
	return d, nil
}

func (m *memStore) RevokeDevice(_ context.Context, id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[id]; !ok {
		return 0, remote.ErrDeviceNotFound
	}
	n := 0
	for did, d := range m.devices {
		if did == id || d.ParentDeviceID == id {
			if d.RevokedAt.IsZero() {
				d.RevokedAt = at
				m.devices[did] = d
				n++
			}
		}
	}
	return n, nil
}

func (m *memStore) TouchDevice(_ context.Context, _ string, _ time.Time, _ string) error {
	return nil
}

func (m *memStore) SetPushToken(_ context.Context, id, token, env, topic string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return remote.ErrDeviceNotFound
	}
	d.APNSToken, d.APNSEnvironment, d.APNSTopic = token, env, topic
	m.devices[id] = d
	return nil
}

func (m *memStore) AppendPushDelivery(context.Context, remote.PushDelivery) error { return nil }

// testRemoteServer 构造一个挂着远程面的 API 服务。
func testRemoteServer(t *testing.T) (*Server, *remote.Service) {
	t.Helper()

	svc, err := remote.New(remote.Options{
		Dir:        t.TempDir(),
		Store:      newMemStore(),
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Name:       "test-kernel",
		Version:    "test",
		APIVersion: "v2",
	})
	if err != nil {
		t.Fatalf("构造远程服务失败: %v", err)
	}
	srv := New(Deps{
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Bus:    event.NewBus(64),
		Remote: svc,
		// 本地面要一个令牌：本地管理面**永远**强制鉴权（见 D09），
		// 因此测试也必须带着它才能碰到本地面。
		Token: testLocalToken,
	})
	return srv, svc
}

// issueDevice 走真实的配对流程签出一台设备。
//
// 刻意不提供"直接往存储里塞一条"的后门：那样测试就绕过了它要验证的
// 那条路径，而配对流程本身（会话 → 认领 → 签发）正是令牌的来源。
func issueDevice(t *testing.T, svc *remote.Service, role remote.Role) (remote.Device, string) {
	t.Helper()

	now := time.Now()
	session, err := svc.BeginPairing(role, "test-device", now)
	if err != nil {
		t.Fatalf("开启配对会话失败: %v", err)
	}
	claimed, err := svc.ClaimPairing(session.Secret, now)
	if err != nil {
		t.Fatalf("认领会话失败: %v", err)
	}
	device, token, err := svc.IssueDevice(context.Background(), claimed,
		remote.DeviceInfo{Name: "测试设备"}, now)
	if err != nil {
		t.Fatalf("签发设备失败: %v", err)
	}
	return device, token
}

// tokenRing 在多台设备的令牌之间轮换。
//
// 存在的理由：限流是按**设备**分桶的（burst 30），而下面两个测试要发
// 三十多个请求。绕过它的正确做法不是放宽生产限流（那会让真正失控的
// 客户端失去约束），而是用多台设备 —— 这也更接近真实情形：每台手机
// 各有各的额度。
type tokenRing struct {
	tokens []string
	next   int
}

func newTokenRing(t *testing.T, svc *remote.Service, n int, role remote.Role) *tokenRing {
	t.Helper()
	ring := &tokenRing{}
	for i := 0; i < n; i++ {
		_, token := issueDevice(t, svc, role)
		ring.tokens = append(ring.tokens, token)
	}
	return ring
}

func (r *tokenRing) take() string {
	token := r.tokens[r.next%len(r.tokens)]
	r.next++
	return token
}

// concretePath 把白名单里的模式换成一个具体的路径。
func concretePath(pattern string) string {
	path := pattern
	for _, param := range []string{"{id}", "{zoneId}", "{recordId}", "{kind}", "{name}", "{planId}"} {
		path = strings.ReplaceAll(path, param, "x")
	}
	_, rest, _ := strings.Cut(pattern, " ")
	return rest
}

// TestEveryRemoteRouteIsReachable 逐条遍历白名单，确认它**真的**通到处理器。
//
// 断言的不是状态码，而是"没有被白名单挡掉"。处理器本身可能因为
// 依赖缺失而返回 500，那是另一回事 —— 但 403 一定意味着白名单与
// 生成的路由对不上（例如模式写错了一个参数名）。
func TestEveryRemoteRouteIsReachable(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	ring := newTokenRing(t, svc, 3, remote.RoleOperator)
	handler := srv.RemoteRoutes()

	for _, rt := range remoteRoutes {
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = concretePath(rt.pattern)

		// 长轮询会挂住 25 秒；测试里只要它立刻返回。
		if strings.HasSuffix(path, "/v1/events/poll") {
			path += "?timeout_ms=0"
		}

		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if rt.role != "" {
			req.Header.Set("Authorization", "Bearer "+ring.take())
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusForbidden {
			t.Errorf("%s 被白名单挡掉了 —— 模式与生成的路由对不上", rt.pattern)
		}
	}
}

// TestRemoteDeniedPaths 钉住一批**绝对不能**出现在远程面上的路径。
//
// 每一条都有具体的理由，不是随手挑的：
//
//	/v1/settings            改语言、改代理端口、改 ACME 配置
//	/v1/audit               审计日志里有全机的操作史
//	POST /v1/credentials    写入一个能重写整个 DNS 区域的凭据
//	/v1/console/bootstrap   免鉴权返回本地访问令牌（核弹级）
//	/console/*              控制台静态资源
//	/v1/config/export       导出里可能含明文凭据
//	/v1/service/install     把内核装成系统服务（需要管理员权限）
//	PATCH /v1/ddns-tasks/{id}  能改域名与取址来源，权限面远大于启停
//	PATCH /v1/settings      同上
//	/v1/jobs                内核任务（含供给、部署）不属于手机端
func TestRemoteDeniedPaths(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	ring := newTokenRing(t, svc, 3, remote.RoleOperator)
	handler := srv.RemoteRoutes()

	denied := []struct{ method, path string }{
		{"GET", "/v1/settings"},
		{"PATCH", "/v1/settings"},
		{"GET", "/v1/audit"},
		{"POST", "/v1/credentials"},
		{"GET", "/v1/console/bootstrap"},
		{"GET", "/console/"},
		{"GET", "/"},
		{"GET", "/v1/config/export"},
		{"POST", "/v1/service/install"},
		{"POST", "/v1/service/uninstall"},
		{"PATCH", "/v1/ddns-tasks/x"},
		{"POST", "/v1/ddns-tasks"},
		{"DELETE", "/v1/ddns-tasks/x"},
		{"GET", "/v1/jobs"},
		{"POST", "/v1/jobs/x/cancel"},
		{"POST", "/v1/apps"},
		{"DELETE", "/v1/apps/x"},
		{"POST", "/v1/apps/x/deploy"},
		{"PUT", "/v1/proxy/routes"},
		{"GET", "/v1/proxy/routes"},
		{"PUT", "/v1/notify/channels"},
		{"POST", "/v1/notify/test"},
		{"POST", "/v1/changes/x/apply"},
		{"POST", "/v1/changes/x/rollback"},
		{"POST", "/v1/reach/providers/x/plan"},
		{"POST", "/v1/runtimes/provision"},
		{"DELETE", "/v1/runtimes/x"},
		{"POST", "/v1/sources/inspect"},
		{"POST", "/v1/remote/status"},
		{"GET", "/v1/remote/devices"},
		{"POST", "/v1/remote/pairing"},
		{"POST", "/v1/remote/apns"},
		{"GET", "/v1/openapi.yaml"},
	}

	for _, c := range denied {
		req := httptest.NewRequest(c.method, c.path, nil)
		req.Header.Set("Authorization", "Bearer "+ring.take())
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s 的响应是 %d，期望 403（这条路径不该出现在远程面上）",
				c.method, c.path, rec.Code)
		}
	}
}

// TestViewerCannotWrite 钉住两级角色的分界。
//
// 这是"手表只读"那条约束在服务端的落点：客户端可以被改，
// 而这里的判定改不了。
func TestViewerCannotWrite(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	_, viewer := issueDevice(t, svc, remote.RoleViewer)
	handler := srv.RemoteRoutes()

	// operator 独有的路径，用 viewer 令牌必须被拒。
	writes := []struct{ method, path string }{
		{"POST", "/v1/apps/x/start"},
		{"POST", "/v1/apps/x/stop"},
		{"POST", "/v1/apps/x/restart"},
		{"POST", "/v1/certs/renew"},
		{"POST", "/v1/ddns-tasks/x/run"},
		{"POST", "/v1/ddns-tasks/x/enable"},
		{"POST", "/v1/ddns-tasks/x/disable"},
		{"POST", "/v1/credentials/x/zones/y/records"},
		{"PUT", "/v1/credentials/x/zones/y/records/z"},
		{"DELETE", "/v1/credentials/x/zones/y/records/z"},
	}
	for _, c := range writes {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+viewer)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("viewer 访问 %s %s 得到 %d，期望 403", c.method, c.path, rec.Code)
		}
	}

	// 只读路径对 viewer 必须放行。
	for _, path := range []string{"/v1/health", "/v1/meta", "/v1/apps", "/v1/remote/self"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+viewer)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusForbidden {
			t.Errorf("viewer 访问只读路径 %s 被拒了", path)
		}
	}
}

// TestUnauthenticatedIsRejected 钉住"除配对之外一律要有令牌"。
func TestUnauthenticatedIsRejected(t *testing.T) {
	t.Parallel()

	srv, _ := testRemoteServer(t)
	handler := srv.RemoteRoutes()

	paths := []string{
		"/v1/health", "/v1/metrics", "/v1/apps", "/v1/remote/self",
		"/v1/events/poll?timeout_ms=0", "/v1/credentials",
	}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("不带令牌访问 %s 得到 %d，期望 401", path, rec.Code)
		}
	}

	// 伪造的令牌同样是 401。
	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("伪造令牌得到 %d，期望 401", rec.Code)
	}

	// 令牌放在查询串里**不认**：多一种载体就多一处会进日志的地方。
	req = httptest.NewRequest(http.MethodGet, "/v1/metrics?token=whatever", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("查询串里的令牌不该被接受，得到 %d", rec.Code)
	}
}

// TestRevokedDeviceIsRejected 钉住吊销的即时性。
func TestRevokedDeviceIsRejected(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	device, token := issueDevice(t, svc, remote.RoleOperator)
	handler := srv.RemoteRoutes()

	req := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("刚签发的令牌不该被拒")
	}

	if _, err := svc.RevokeDevice(context.Background(), device.ID, time.Now()); err != nil {
		t.Fatalf("吊销失败: %v", err)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("吊销之后应当立刻 401，得到 %d", rec.Code)
	}

	// 客户端要能区分"令牌不认识"与"我被断开了"：
	// 前者是"扫错了服务器"，后者是"去重新配对"。
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("响应不是 problem+json: %v", err)
	}
	if problem.Detail == "" {
		t.Fatal("吊销的响应缺少说明，客户端无法给出正确的下一步")
	}
}

// TestPairingIsTheOnlyPublicPath 钉住免鉴权面的**唯一性**。
//
// 一条新的免鉴权路径意味着一个新的攻击面，而它看起来往往很无辜
// （"只是让客户端探测一下版本"）。因此这里把集合写死。
func TestPairingIsTheOnlyPublicPath(t *testing.T) {
	t.Parallel()

	srv, svc := testRemoteServer(t)
	handler := srv.RemoteRoutes()

	// 没有任何令牌，但用一个**真的**配对码 —— 它必须走通。
	//
	// 刻意不断言"不是 401"：配对处理器自己对错误的码就返回 401
	//（契约如此），因此那种断言分不清"被鉴权挡下"与"码不对"。
	// 走通一次 200 才能证明这条路径真的是免鉴权的。
	session, err := svc.BeginPairing(remote.RoleViewer, "watch", time.Now())
	if err != nil {
		t.Fatalf("开启配对会话失败: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"code":   session.Code,
		"device": map[string]string{"name": "x"},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/remote/pair", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("不带令牌的配对请求得到 %d，期望 200（配对路径必须是免鉴权的）：%s",
			rec.Code, rec.Body.String())
	}

	// 同一个码再用一次必须失败：配对码是一次性的。
	req = httptest.NewRequest(http.MethodPost, "/v1/remote/pair", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("用过的配对码不该还能用 —— 那是一次静默的重放")
	}

	// 其余每一条都要令牌。
	for _, path := range []string{"/v1/health", "/v1/meta", "/v1/apps", "/v1/remote/self"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s 没有令牌却得到 %d —— 它不该是公开路径", path, rec.Code)
		}
	}
}
