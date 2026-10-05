package remote

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// 接入点的选择。
//
// # 为什么这一条值得单独测
//
// 走错环境的代价不是"这条通知丢了"：Apple 回的是 400 BadDeviceToken，
// 而本包把这个错误理解成"令牌已失效"，于是会把这台设备**完全有效**的
// 令牌清掉（见 push.go 里的 Unregistered 分支）。用户看到的是"通知一直
// 收不到，而且每次都要重新登记一遍" —— 而那个症状指向的方向（令牌、
// 网络、凭据）全都是错的。
//
// 最容易走错的一步是"用 Debug 配置把应用装到真机上"：它的令牌只对沙箱
// 有效，而在这之前内核**默认发生产**，并且没有任何代码在读
// `Device.APNSEnvironment`。

// credentialsForTest 造一份可用的 APNs 凭据存储。
func credentialsForTest(t *testing.T) *CredentialStore {
	t.Helper()
	store := NewCredentialStore(t.TempDir(), identityCipher{})
	_, pemText := testKey(t)
	if err := store.Set(Credentials{
		TeamID: "T", KeyID: "K", BundleID: "app.isc.mizar", PrivateKey: pemText,
	}); err != nil {
		t.Fatalf("保存凭据失败: %v", err)
	}
	return store
}

// recordingPusher 记下它被构造时的接入点，并记录每一次发送。
//
// 用它而不是真 pusher：真 pusher 会去打 Apple。
type recordingPusher struct {
	mu       sync.Mutex
	devices  []string
	delivery Delivery
}

func (r *recordingPusher) Push(_ context.Context, device Device, _ Notification) Delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices = append(r.devices, device.ID)
	return r.delivery
}

func (r *recordingPusher) pushed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.devices...)
}

// recordingCache 造一个把接入点记下来的缓存。
func recordingCache(t *testing.T, host string) (*pusherCache, *[]string) {
	t.Helper()
	cache := newPusherCache(credentialsForTest(t), host)
	var mu sync.Mutex
	seen := []string{}
	cache.newPusher = func(_ Credentials, h string) (Pusher, error) {
		mu.Lock()
		seen = append(seen, h)
		mu.Unlock()
		return &recordingPusher{delivery: Delivery{Status: PushStatusSent}}, nil
	}
	return cache, &seen
}

// pusherHost 读出真 pusher 实际会打的那个接入点。
//
// 用类型断言而不是给 APNSPusher 加一个导出的 getter：这个值只在测试里
// 需要，而多一个导出成员就多一处要维护的对外承诺。
func pusherHost(t *testing.T, p Pusher) string {
	t.Helper()
	apns, ok := p.(*APNSPusher)
	if !ok {
		t.Fatalf("拿到的不是真 pusher：%T", p)
	}
	return apns.host
}

// 设备自报的环境决定接入点。
func TestPusherRoutesByTheDevicesRegisteredEnvironment(t *testing.T) {
	t.Parallel()

	cache := newPusherCache(credentialsForTest(t), "")

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"沙箱（Debug 配置装到真机上）", PushEnvSandbox, APNSHostSandbox},
		{"生产（TestFlight / App Store）", PushEnvProduction, APNSHostProduction},
		{"空串（这个字段存在之前登记的设备）", "", APNSHostProduction},
		{"认不出的值", "staging", APNSHostProduction},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			pusher, err := cache.get(Device{ID: "dev-1", APNSToken: "abc", APNSEnvironment: c.env})
			if err != nil {
				t.Fatalf("取 pusher 失败: %v", err)
			}
			if pusher == nil {
				t.Fatal("配了凭据却拿不到 pusher")
			}
			if got := pusherHost(t, pusher); got != c.want {
				t.Errorf("接入点 = %q，期望 %q", got, c.want)
			}
		})
	}
}

// 同一个接入点上必须复用同一把 pusher。
//
// 每次发通知都新建一个的话 JWT 的缓存就没了，而 APNs 会因为我们频繁
// 换 token 而拒绝连接（TooManyProviderTokenUpdates）—— 那是一个
// "偶尔漏几条通知"的故障，极难查。
//
// 而两个接入点**必须**是两把：混在一起会让"这次拿到哪一把"变成调用
// 顺序的函数，也就是一个只在两台设备都活跃时才出现的 bug。
func TestPusherCacheReusesOnePusherPerHost(t *testing.T) {
	t.Parallel()

	cache := newPusherCache(credentialsForTest(t), "")

	sandboxFirst, err := cache.get(Device{ID: "a", APNSEnvironment: PushEnvSandbox})
	if err != nil {
		t.Fatalf("取沙箱 pusher 失败: %v", err)
	}
	sandboxAgain, err := cache.get(Device{ID: "b", APNSEnvironment: PushEnvSandbox})
	if err != nil {
		t.Fatalf("再取一次沙箱 pusher 失败: %v", err)
	}
	production, err := cache.get(Device{ID: "c", APNSEnvironment: PushEnvProduction})
	if err != nil {
		t.Fatalf("取生产 pusher 失败: %v", err)
	}

	if sandboxFirst != sandboxAgain {
		t.Error("同一个接入点上重复构造了 pusher —— JWT 的缓存会失效")
	}
	if sandboxFirst == production {
		t.Error("沙箱与生产共用了一把 pusher")
	}
}

// 换凭据之后要重新构造：否则会继续用旧私钥签的 JWT。
func TestPusherCacheRebuildsWhenCredentialsChange(t *testing.T) {
	t.Parallel()

	store := credentialsForTest(t)
	cache := newPusherCache(store, "")

	before, err := cache.get(Device{ID: "a", APNSEnvironment: PushEnvSandbox})
	if err != nil {
		t.Fatalf("取 pusher 失败: %v", err)
	}

	_, otherKey := testKey(t)
	if err := store.Set(Credentials{
		TeamID: "T2", KeyID: "K2", BundleID: "app.isc.mizar", PrivateKey: otherKey,
	}); err != nil {
		t.Fatalf("换凭据失败: %v", err)
	}

	after, err := cache.get(Device{ID: "a", APNSEnvironment: PushEnvSandbox})
	if err != nil {
		t.Fatalf("换凭据后取 pusher 失败: %v", err)
	}
	if before == after {
		t.Error("凭据换了却还在用旧 pusher —— 会用旧私钥签的 JWT 去发通知")
	}
}

// 没有凭据时返回 (nil, nil)：调用方据此记一条"跳过"，
// 而不是把一次失败伪装成一次发送。
func TestPusherCacheWithoutCredentialsReturnsNothing(t *testing.T) {
	t.Parallel()

	cache := newPusherCache(NewCredentialStore(t.TempDir(), identityCipher{}), "")
	pusher, err := cache.get(Device{ID: "a", APNSEnvironment: PushEnvSandbox})
	if err != nil {
		t.Fatalf("没配凭据不该报错: %v", err)
	}
	if pusher != nil {
		t.Fatalf("没配凭据却拿到了 pusher：%T", pusher)
	}
}

// 显式覆盖对**每一台**设备生效，无论它自报什么环境。
//
// 验收脚本用一个本地的假 APNs 替换真实端点（见
// scripts/acceptance-remote.sh 里的 ISC_APNS_HOST）。按环境分流会把一半
// 设备发到 Apple 的真实接入点 —— 那既让那条脚本失去意义，也破坏了
// "测试不许碰真实端点"这条约束。
func TestExplicitHostOverrideWinsForEveryDevice(t *testing.T) {
	t.Parallel()

	cache, seen := recordingCache(t, "http://127.0.0.1:1")
	for _, env := range []string{PushEnvSandbox, PushEnvProduction, ""} {
		if _, err := cache.get(Device{ID: "d-" + env, APNSEnvironment: env}); err != nil {
			t.Fatalf("环境 %q 下取 pusher 失败: %v", env, err)
		}
	}

	hosts := *seen
	if len(hosts) == 0 {
		t.Fatal("一次都没构造 pusher")
	}
	for _, h := range hosts {
		if h != "http://127.0.0.1:1" {
			t.Errorf("接入点 = %q，期望覆盖值 —— 这一发会打到 Apple", h)
		}
	}
}

// TestPush 必须把**那台设备**传给路由。
//
// 这是这条链路上最容易漏的一步：把 `get(device)` 写回 `get(Device{})`，
// 编译通过、单元测试全绿，而所有沙箱设备都会被打到生产。用记录用的
// pusher 把整条路径跑一遍，从而不必碰真实端点。
func TestTestPushRoutesByTheTargetDevicesEnvironment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := newPushStore()
	device := testDevice()
	if err := store.CreateDevice(context.Background(), device); err != nil {
		t.Fatalf("建设备失败: %v", err)
	}

	service, err := New(Options{
		Dir:       dir,
		Store:     store,
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Name:      "Mac-mini",
		APNSStore: credentialsForTest(t),
	})
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	if service.apns == nil {
		t.Fatal("配了凭据存储却没有 pusher 缓存")
	}

	cache, seen := recordingCache(t, "")
	service.apns = cache

	delivery, err := service.TestPush(context.Background(), device.ID)
	if err != nil {
		t.Fatalf("测试推送失败: %v", err)
	}
	if delivery.Status != PushStatusSent {
		t.Errorf("状态 = %q，期望 %q", delivery.Status, PushStatusSent)
	}

	hosts := *seen
	if len(hosts) != 1 {
		t.Fatalf("构造了 %d 把 pusher，期望 1", len(hosts))
	}
	// testDevice() 登记的是沙箱。
	if hosts[0] != APNSHostSandbox {
		t.Errorf("接入点 = %q，期望 %q（这台设备登记的是沙箱）", hosts[0], APNSHostSandbox)
	}
}

// 通知转发同样按设备分流。
//
// 与上面那条分开：两者是**两条**调用路径（TestPush 与 Notifier.deliver），
// 而"改了一处忘了另一处"正是这种重构最常见的错。
func TestNotifierRoutesByEachDevicesEnvironment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	store := newPushStore()

	sandbox := testDevice()
	sandbox.ID, sandbox.APNSEnvironment = "dev-sandbox", PushEnvSandbox
	production := testDevice()
	production.ID, production.APNSEnvironment = "dev-prod", PushEnvProduction
	for _, d := range []Device{sandbox, production} {
		if err := store.CreateDevice(context.Background(), d); err != nil {
			t.Fatalf("建设备失败: %v", err)
		}
	}

	service, err := New(Options{
		Dir: dir, Store: store,
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Name: "Mac-mini",
	})
	if err != nil {
		t.Fatalf("构造服务失败: %v", err)
	}
	service.SetNotificationsEnabled(true)

	cache, seen := recordingCache(t, "")
	notifier := NewNotifier(service, credentialsForTest(t), "")
	notifier.pushers = cache

	for _, device := range []Device{sandbox, production} {
		notifier.deliver(context.Background(), device,
			Notification{Title: "站点挂了"}, "app.failed:"+device.ID)
	}

	hosts := *seen
	if len(hosts) != 2 {
		t.Fatalf("构造了 %d 把 pusher，期望 2（两个环境各一把）", len(hosts))
	}
	joined := strings.Join(hosts, ",")
	if !strings.Contains(joined, APNSHostSandbox) {
		t.Errorf("没有为沙箱设备选中沙箱接入点：%v", hosts)
	}
	if !strings.Contains(joined, APNSHostProduction) {
		t.Errorf("没有为生产设备选中生产接入点：%v", hosts)
	}
}
