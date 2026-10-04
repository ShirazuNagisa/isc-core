package remote

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
)

// 事件 → 通知的翻译。
//
// # 为什么它值得单独测
//
// 这里出错的症状是"用户收到一堆没用的通知"或者"该推的没推"。
// 前者的后果不是信息多，而是**用户关掉通知权限** —— 而一旦关掉，
// "站点挂了"这条真正重要的通知也一起没了。
//
// 因此除了"每类事件翻译成什么"，还要钉住"哪些事件**不该**被翻译"。

// pushStore 是设备存储的内存实现（仅测试用）。
type pushStore struct {
	mu      sync.Mutex
	devices map[string]Device
	seen    []PushDelivery
}

func newPushStore() *pushStore { return &pushStore{devices: map[string]Device{}} }

func (m *pushStore) CreateDevice(_ context.Context, d Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[d.ID] = d
	return nil
}

func (m *pushStore) ListDevices(context.Context) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.devices))
	for _, d := range m.devices {
		out = append(out, d)
	}
	return out, nil
}

func (m *pushStore) Device(_ context.Context, id string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return Device{}, ErrDeviceNotFound
	}
	return d, nil
}

func (m *pushStore) DeviceByTokenHash(context.Context, []byte) (Device, error) {
	return Device{}, ErrDeviceNotFound
}

func (m *pushStore) UpdateDevice(_ context.Context, id string, p DevicePatch) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return Device{}, ErrDeviceNotFound
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

func (m *pushStore) RevokeDevice(_ context.Context, id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[id]; !ok {
		return 0, ErrDeviceNotFound
	}
	for did, d := range m.devices {
		if did == id || d.ParentDeviceID == id {
			if d.RevokedAt.IsZero() {
				d.RevokedAt = at
				m.devices[did] = d
			}
		}
	}
	return 1, nil
}

func (m *pushStore) TouchDevice(context.Context, string, time.Time, string) error { return nil }

func (m *pushStore) SetPushToken(_ context.Context, id, token, env, topic string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return ErrDeviceNotFound
	}
	d.APNSToken, d.APNSEnvironment, d.APNSTopic = token, env, topic
	m.devices[id] = d
	return nil
}

func (m *pushStore) AppendPushDelivery(_ context.Context, d PushDelivery) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, d)
	return nil
}

func (m *pushStore) deliveries() []PushDelivery {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]PushDelivery(nil), m.seen...)
}

// notifierUnderTest 构造一个挂了真实 pusher（指向假服务器）的转发器。
func notifierUnderTest(t *testing.T, host string) (*Notifier, *Service, *pushStore) {
	t.Helper()

	dir := t.TempDir()
	store := newPushStore()
	_, pemText := testKey(t)

	credentials := NewCredentialStore(dir, identityCipher{})
	if err := credentials.Set(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "app.isc.mizar", PrivateKey: pemText,
	}); err != nil {
		t.Fatalf("保存凭据失败: %v", err)
	}

	service, err := New(Options{
		Dir:   dir,
		Store: store,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Name:  "Mac-mini",
	})
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	service.SetNotificationsEnabled(true)

	return NewNotifier(service, credentials, host), service, store
}

func TestNotifierTranslatesTheImportantEvents(t *testing.T) {
	t.Parallel()

	notifier, _, _ := notifierUnderTest(t, "")

	cases := []struct {
		name      string
		eventType string
		payload   map[string]any
		wantKey   string
	}{
		{
			name:      "站点启动失败",
			eventType: event.TypeAppStateChanged,
			payload:   map[string]any{"id": "app-1", "name": "博客", "state": "failed", "health_detail": "端口被占"},
			wantKey:   "app.failed:app-1",
		},
		{
			name:      "站点不健康",
			eventType: event.TypeAppHealthChanged,
			payload:   map[string]any{"id": "app-1", "name": "博客", "health": "unhealthy"},
			wantKey:   "app.unhealthy:app-1",
		},
		{
			name:      "证书签发失败",
			eventType: event.TypeCertFailed,
			payload:   map[string]any{"domain": "blog.example.com", "error": "DNS 校验超时"},
			wantKey:   "cert.failed:blog.example.com",
		},
		{
			name:      "解析更新失败",
			eventType: event.TypeDNSUpdateFailed,
			payload:   map[string]any{"task_id": "task-1", "label": "家里的 IPv6"},
			wantKey:   "ddns.failed:家里的 IPv6",
		},
		{
			name:      "公网地址变化",
			eventType: event.TypeIPChanged,
			payload:   map[string]any{"ipv4": "1.2.3.4"},
			wantKey:   "ip.changed",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(c.payload)
			if err != nil {
				t.Fatalf("构造载荷失败: %v", err)
			}
			notification, key, ok := notifier.translate(event.Event{
				Seq: 1, Type: c.eventType, Payload: raw,
			})
			if !ok {
				t.Fatalf("%s 应当被翻译", c.eventType)
			}
			if key != c.wantKey {
				t.Errorf("去重键 = %q，期望 %q", key, c.wantKey)
			}
			if notification.Title == "" {
				t.Error("通知没有标题")
			}
			if notification.Route == nil {
				t.Error("通知没有深链信息 —— 用户点开之后不知道该去哪儿")
			}
		})
	}
}

// 中间状态与无关事件**不该**变成通知。
//
// 全推的后果不是"信息多"，而是用户关掉通知权限 —— 而一旦关掉，
// 真正重要的那条也一起没了。
func TestNotifierIgnoresEverythingElse(t *testing.T) {
	t.Parallel()

	notifier, _, _ := notifierUnderTest(t, "")

	cases := []struct {
		name      string
		eventType string
		payload   map[string]any
	}{
		{"站点启动中", event.TypeAppStateChanged, map[string]any{"id": "app-1", "state": "starting"}},
		{"站点运行中", event.TypeAppStateChanged, map[string]any{"id": "app-1", "state": "running"}},
		{"站点健康了", event.TypeAppHealthChanged, map[string]any{"id": "app-1", "health": "healthy"}},
		{"任务进度", "job.progress", map[string]any{"id": "job-1", "progress": 0.5}},
		{"日志追加", "log.appended", map[string]any{"line": "hello"}},
		{"解析成功", event.TypeDNSRecordUpdated, map[string]any{"task_id": "task-1"}},
		{"未知事件", "something.new", map[string]any{"id": "x"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			raw, _ := json.Marshal(c.payload)
			if _, _, ok := notifier.translate(event.Event{Type: c.eventType, Payload: raw}); ok {
				t.Fatalf("%s 不该变成通知", c.eventType)
			}
		})
	}
}

// 去重：同一设备同一 key 在窗口内只放行一次。
//
// 它压住的是"一个站点反复启停"这类抖动 —— 那种情况下用户需要的是
// 一条"它不稳定"的通知，而不是四十条。
func TestNotifierDedupesWithinTheWindow(t *testing.T) {
	t.Parallel()

	notifier, _, _ := notifierUnderTest(t, "")

	if !notifier.allow("dev-1", "app.failed:app-1") {
		t.Fatal("第一次应当放行")
	}
	if notifier.allow("dev-1", "app.failed:app-1") {
		t.Fatal("窗口内的第二次应当被压掉")
	}
	// 另一台设备有它自己的额度：一台设备上的抖动不该让另一台收不到。
	if !notifier.allow("dev-2", "app.failed:app-1") {
		t.Fatal("另一个设备应当有自己的去重额度")
	}
	// 另一个 key 不受影响。
	if !notifier.allow("dev-1", "cert.failed:blog.example.com") {
		t.Fatal("不同的 key 不该互相影响")
	}

	// 清理之后再放行。
	notifier.mu.Lock()
	notifier.recent["dev-1\x00app.failed:app-1"] = time.Now().Add(-2 * pushDedupeWindow)
	notifier.mu.Unlock()
	notifier.sweep()
	if !notifier.allow("dev-1", "app.failed:app-1") {
		t.Fatal("过期之后应当重新放行")
	}
}

// 端到端：一条事件真的变成一次 HTTP 请求。
func TestNotifierDeliversThroughTheRealPusher(t *testing.T) {
	t.Parallel()

	fake := newFakeAPNS(t, nil, nil)
	notifier, _, store := notifierUnderTest(t, fake.server.URL)

	if err := store.CreateDevice(context.Background(), Device{
		ID: "dev-1", Label: "iPhone", Role: RoleViewer,
		APNSToken: "tok-1", NotificationsEnabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("创建设备失败: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{
		"id": "app-1", "name": "博客", "state": "failed", "health_detail": "端口被占",
	})
	notifier.handle(context.Background(), event.Event{
		Seq: 1, Type: event.TypeAppStateChanged, Payload: raw,
	})

	requests, bodies := fake.snapshot()
	if len(requests) != 1 {
		t.Fatalf("请求数 = %d，期望 1", len(requests))
	}
	if !strings.HasSuffix(requests[0].URL.Path, "/3/device/tok-1") {
		t.Fatalf("发给了错误的令牌：%s", requests[0].URL.Path)
	}
	var payload map[string]any
	_ = json.Unmarshal(bodies[0], &payload)
	aps, _ := payload["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if title, _ := alert["title"].(string); !strings.Contains(title, "博客") {
		t.Fatalf("标题里没有站点名：%v", alert)
	}

	// 投递流水必须留下：没有它，"为什么没收到推送"就只能靠猜。
	deliveries := store.deliveries()
	if len(deliveries) != 1 || deliveries[0].Status != PushStatusSent {
		t.Fatalf("投递流水不对：%+v", deliveries)
	}
}

// 收到 410 之后必须清掉那个令牌。
func TestNotifierClearsUnregisteredTokens(t *testing.T) {
	t.Parallel()

	fake := newFakeAPNS(t, []int{410}, []string{"Unregistered"})
	notifier, _, store := notifierUnderTest(t, fake.server.URL)

	if err := store.CreateDevice(context.Background(), Device{
		ID: "dev-1", Label: "旧手机", Role: RoleViewer,
		APNSToken: "stale-token", NotificationsEnabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("创建设备失败: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"id": "app-1", "name": "博客", "state": "failed"})
	notifier.handle(context.Background(), event.Event{Type: event.TypeAppStateChanged, Payload: raw})

	device, err := store.Device(context.Background(), "dev-1")
	if err != nil {
		t.Fatalf("读取设备失败: %v", err)
	}
	if device.APNSToken != "" {
		t.Fatal("令牌已失效却还留着 —— 用户只会觉得推送坏了，而设备端不会知道")
	}
}

// 关闭总开关之后什么都不发。
func TestNotifierRespectsTheGlobalSwitch(t *testing.T) {
	t.Parallel()

	fake := newFakeAPNS(t, nil, nil)
	notifier, service, store := notifierUnderTest(t, fake.server.URL)
	service.SetNotificationsEnabled(false)

	if err := store.CreateDevice(context.Background(), Device{
		ID: "dev-1", Label: "iPhone", Role: RoleViewer,
		APNSToken: "tok-1", NotificationsEnabled: true,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("创建设备失败: %v", err)
	}

	raw, _ := json.Marshal(map[string]any{"id": "app-1", "state": "failed"})
	notifier.handle(context.Background(), event.Event{Type: event.TypeAppStateChanged, Payload: raw})

	if requests, _ := fake.snapshot(); len(requests) != 0 {
		t.Fatal("总开关关着时不该发出任何请求")
	}
}

// 没登记推送令牌的设备不该收到通知。
func TestNotifierSkipsDevicesThatDidNotOptIn(t *testing.T) {
	t.Parallel()

	fake := newFakeAPNS(t, nil, nil)
	notifier, _, store := notifierUnderTest(t, fake.server.URL)

	for _, d := range []Device{
		{ID: "no-token", Label: "A", NotificationsEnabled: true},
		{ID: "not-enabled", Label: "B", APNSToken: "tok"},
		{ID: "revoked", Label: "C", APNSToken: "tok", NotificationsEnabled: true, RevokedAt: time.Now()},
	} {
		d.CreatedAt, d.UpdatedAt = time.Now(), time.Now()
		if err := store.CreateDevice(context.Background(), d); err != nil {
			t.Fatalf("创建设备失败: %v", err)
		}
	}

	raw, _ := json.Marshal(map[string]any{"id": "app-1", "state": "failed"})
	notifier.handle(context.Background(), event.Event{Type: event.TypeAppStateChanged, Payload: raw})

	if requests, _ := fake.snapshot(); len(requests) != 0 {
		t.Fatalf("不该给未开启推送 / 已吊销的设备发通知，实际发了 %d 条", len(requests))
	}
}
