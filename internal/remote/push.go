package remote

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
)

// 本文件把**内核事件**翻译成**手机通知**。
//
// # 为什么是白名单而不是"什么都推"
//
// 事件总线上跑着几十种事件，其中大部分（`job.progress`、`log.appended`、
// `app.state_changed` 的每一次中间状态）对手机用户毫无意义。全推的
// 后果不是"信息多"，而是**用户关掉通知权限** —— 而一旦关掉，
// "站点挂了"这条真正重要的通知也一起没了。
//
// 因此这里只留五类，每一类都对应一个"用户现在就该知道"的时刻。

// pushDedupeWindow 是同一设备同一事件的最短推送间隔。
//
// 五分钟：与 ISC Phecda 的桌面通知用的是同一个窗口。
// 它的作用是压住"一个站点反复启停"这类抖动 —— 那种情况下用户需要的
// 是一条"它不稳定"的通知，而不是四十条。
const pushDedupeWindow = 5 * time.Minute

// Notifier 订阅事件总线并把该推的事件推出去。
type Notifier struct {
	service *Service
	host    string

	pushers *pusherCache

	mu     sync.Mutex
	recent map[string]time.Time
}

// NewNotifier 构造推送转发器。
//
// host 是 APNs 的接入点（生产 / 沙箱）。它由调用方给，因为
// "给沙箱设备发生产"是这个功能里最常见的配置错误，而把它写死
// 只会让那个错误变成一个改不掉的默认值。
func NewNotifier(service *Service, store *CredentialStore, host string) *Notifier {
	return &Notifier{
		service: service,
		host:    host,
		pushers: newPusherCache(store, host),
		recent:  map[string]time.Time{},
	}
}

// Run 订阅事件总线，直到 ctx 结束。
//
// 放在自己的 goroutine 里跑（与内核的其它后台任务一致）。
func (n *Notifier) Run(ctx context.Context, bus *event.Bus) {
	if bus == nil {
		return
	}

	// 从"现在"开始订阅：内核启动时回放上次运行的事件，只会让用户在
	// 开机的一瞬间收到一堆早已处理过的通知。
	sub, err := bus.Subscribe(bus.LatestSeq())
	if err != nil {
		return
	}
	defer sub.Close()

	// 定期清理去重表：不清理的话它会随着事件数量无限增长，而每一条
	// 只占几十字节 —— 那种泄漏在几个月之后才会显形。
	ticker := time.NewTicker(pushDedupeWindow)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.sweep()
		case ev, ok := <-sub.C():
			if !ok {
				return
			}
			n.handle(ctx, ev)
		}
	}
}

// handle 处理一条事件。
func (n *Notifier) handle(ctx context.Context, ev event.Event) {
	notification, key, ok := n.translate(ev)
	if !ok {
		return
	}
	if !n.service.NotificationsEnabled() {
		// 总开关关着时**什么都不做**，但也不报错：用户关掉推送之后
		// 事件总线照常跑，这是正常的。
		return
	}

	devices, err := n.targets(ctx)
	if err != nil || len(devices) == 0 {
		return
	}

	for _, device := range devices {
		if !n.allow(device.ID, key) {
			continue
		}
		n.deliver(ctx, device, notification, key)
	}
}

// deliver 把一条通知发给一台设备，并记录结果。
func (n *Notifier) deliver(ctx context.Context, device Device, notification Notification, key string) {
	// 接入点按**这台设备**登记的环境选：开发者用 Debug 装到真机上时，
	// 它的令牌只对沙箱有效，而发到生产会被 Apple 回一个 BadDeviceToken
	// —— 那个错误在这里被理解成"令牌失效"，于是令牌会被清掉。
	pusher, err := n.pushers.get(device)
	if err != nil || pusher == nil {
		// 没配凭据：记一条"跳过"，让"为什么没收到推送"有一个可查的答案。
		n.service.RecordPushDelivery(ctx, PushDelivery{
			TS: time.Now().UTC(), DeviceID: device.ID, Kind: "push",
			DedupeKey: key, Status: PushStatusSkipped,
			Reason: i18n.T("remote.msg.push_not_configured"),
		})
		return
	}

	notification.ThreadID = "isc-" + n.service.Name()
	result := pusher.Push(ctx, device, notification)

	n.service.RecordPushDelivery(ctx, PushDelivery{
		TS: time.Now().UTC(), DeviceID: device.ID, Kind: "push",
		DedupeKey: key, Status: result.Status,
		HTTPStatus: result.HTTPStatus, Reason: result.Reason, APNSID: result.APNSID,
	})

	// 令牌失效时清掉它：继续给一个失效的令牌发通知不会有任何效果，
	// 而设备端也不会知道 —— 用户只会觉得"推送坏了"。
	if result.Unregistered {
		_ = n.service.SetPushToken(ctx, device.ID, "", "", "")
	}
}

// targets 返回该接收推送的设备。
func (n *Notifier) targets(ctx context.Context) ([]Device, error) {
	devices, err := n.service.ListDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(devices))
	for _, device := range devices {
		if device.Active() && device.NotificationsEnabled && device.APNSToken != "" {
			out = append(out, device)
		}
	}
	return out, nil
}

// allow 去重：同一设备同一 key 在窗口内只放行一次。
func (n *Notifier) allow(deviceID, key string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()

	id := deviceID + "\x00" + key
	now := time.Now()
	if last, ok := n.recent[id]; ok && now.Sub(last) < pushDedupeWindow {
		return false
	}
	n.recent[id] = now
	return true
}

// sweep 清掉过期的去重记录。
func (n *Notifier) sweep() {
	n.mu.Lock()
	defer n.mu.Unlock()

	cutoff := time.Now().Add(-pushDedupeWindow)
	for id, at := range n.recent {
		if at.Before(cutoff) {
			delete(n.recent, id)
		}
	}
}

// translate 把一条内核事件翻译成通知。
//
// 返回的 key 是去重键：它必须**只由事件内容决定**，而不是由时间或
// 随机数决定 —— 否则去重永远不会命中。
func (n *Notifier) translate(ev event.Event) (Notification, string, bool) {
	var payload map[string]any
	if len(ev.Payload) > 0 {
		_ = json.Unmarshal(ev.Payload, &payload)
	}
	str := func(key string) string {
		if payload == nil {
			return ""
		}
		if value, ok := payload[key].(string); ok {
			return value
		}
		return ""
	}

	switch ev.Type {
	case event.TypeAppStateChanged:
		if str("state") != "failed" {
			return Notification{}, "", false
		}
		name := str("name")
		if name == "" {
			name = str("id")
		}
		return Notification{
			Title:         i18n.T("remote.push.app_failed_title", name),
			Body:          firstNonEmpty(str("health_detail"), str("last_error")),
			Route:         map[string]string{"kind": "app", "id": str("id")},
			CollapseID:    "app-" + str("id"),
			TimeSensitive: true,
		}, "app.failed:" + str("id"), true

	case event.TypeAppHealthChanged:
		if str("health") != "unhealthy" {
			return Notification{}, "", false
		}
		name := firstNonEmpty(str("name"), str("id"))
		return Notification{
			Title:      i18n.T("remote.push.app_unhealthy_title", name),
			Body:       str("health_detail"),
			Route:      map[string]string{"kind": "app", "id": str("id")},
			CollapseID: "app-health-" + str("id"),
		}, "app.unhealthy:" + str("id"), true

	case event.TypeCertFailed:
		domain := firstNonEmpty(str("domain"), str("name"))
		return Notification{
			Title:      i18n.T("remote.push.cert_failed_title", domain),
			Body:       firstNonEmpty(str("error"), str("reason")),
			Route:      map[string]string{"kind": "cert", "id": domain},
			CollapseID: "cert-" + domain,
		}, "cert.failed:" + domain, true

	case event.TypeDNSUpdateFailed:
		label := firstNonEmpty(str("label"), str("task_id"), str("id"))
		return Notification{
			Title:      i18n.T("remote.push.ddns_failed_title", label),
			Body:       firstNonEmpty(str("message"), str("error")),
			Route:      map[string]string{"kind": "ddns", "id": str("task_id")},
			CollapseID: "ddns-" + label,
		}, "ddns.failed:" + label, true

	case event.TypeIPChanged:
		address := firstNonEmpty(str("ipv4"), str("ipv6"), str("address"))
		return Notification{
			Title:      i18n.T("remote.push.ip_changed_title"),
			Body:       address,
			Route:      map[string]string{"kind": "ip"},
			CollapseID: "ip-changed",
		}, "ip.changed", true
	}
	return Notification{}, "", false
}

// firstNonEmpty 返回第一个非空值。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
