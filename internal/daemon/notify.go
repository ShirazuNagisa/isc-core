package daemon

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ShirazuNagisa/isc-core/internal/event"
	"github.com/ShirazuNagisa/isc-core/internal/i18n"
	"github.com/ShirazuNagisa/isc-core/internal/notify"
)

// 本文件把事件总线上的事件翻译成通知。
//
// # 哪些事件值得通知
//
// 原则是"**用户需要采取行动或需要知道**"，而不是"发生了什么"。
// 全都要通知的话，用户会在一天之内关掉它 —— 而那之后真正重要的
// 那条他也看不到了。
//
//	值得通知    解析失败、地址变化、证书签发/失败、变更失败且回滚失败、
//	            被中断的变更
//	不值得通知  每次解析成功、作业进度、日志写入
//
// # 去重键怎么取
//
// 键里只放"这件事是什么"（任务 ID、证书名），**不放会变的部分**
//（IP 地址、错误文本）。放了后者的话地址一变就是一条新消息，
// 去重完全失效 —— 而事件风暴正是它要挡的东西。

// notifyEventMap 把事件类型映射成通知的构造方式。
//
// 用表而不是一长串 switch：事件类型会持续增加，而表让"哪些事件会
// 产生通知"一眼可见，也便于将来做成用户可配的。
var notifyEvents = map[string]func(payload map[string]any) (notify.Message, bool){
	event.TypeDNSUpdateFailed: func(p map[string]any) (notify.Message, bool) {
		taskID := str(p, "task_id")
		label := str(p, "label")
		if label == "" {
			label = taskID
		}
		return notify.Message{
			Event:    event.TypeDNSUpdateFailed,
			Severity: notify.SeverityError,
			Title:    fmt.Sprintf("动态解析失败：%s", label),
			Body:     str(p, "error"),
			// 键里只有任务 ID —— 错误文本会变，放进去会让去重失效。
			DedupKey: "dns.update_failed:" + taskID,
			Data:     p,
		}, true
	},

	event.TypeDNSRecordUpdated: func(p map[string]any) (notify.Message, bool) {
		taskID := str(p, "task_id")
		label := str(p, "label")
		if label == "" {
			label = taskID
		}
		return notify.Message{
			Event:    event.TypeDNSRecordUpdated,
			Severity: notify.SeverityInfo,
			Title:    fmt.Sprintf("动态解析已更新：%s", label),
			Body:     fmt.Sprintf("新地址 %s", str(p, "ip")),
			DedupKey: "dns.record_updated:" + taskID,
			Data:     p,
		}, true
	},

	// 前缀变化**不**逐个地址通知，而是按接口去重。
	//
	// 一次重拨会产生多条地址变化事件，逐个通知就是刷屏。
	event.TypeIPPrefixChanged: func(p map[string]any) (notify.Message, bool) {
		iface := str(p, "iface")
		return notify.Message{
			Event:    event.TypeIPPrefixChanged,
			Severity: notify.SeverityWarning,
			Title:    fmt.Sprintf("IPv6 前缀已变化（%s）", iface),
			Body:     "新前缀 " + str(p, "prefix"),
			DedupKey: "ip.prefix_changed:" + iface,
			Data:     p,
		}, true
	},

	event.TypeCertIssued: func(p map[string]any) (notify.Message, bool) {
		name := str(p, "name")
		return notify.Message{
			Event:    event.TypeCertIssued,
			Severity: notify.SeverityInfo,
			Title:    fmt.Sprintf("证书已签发：%s", name),
			DedupKey: "cert.issued:" + name,
			Data:     p,
		}, true
	},

	event.TypeCertFailed: func(p map[string]any) (notify.Message, bool) {
		name := str(p, "name")
		return notify.Message{
			Event:    event.TypeCertFailed,
			Severity: notify.SeverityError,
			Title:    fmt.Sprintf("证书签发失败：%s", name),
			Body:     str(p, "error") + "\n" + i18n.T("notify.cert_hint"),
			DedupKey: "cert.failed:" + name,
			Data:     p,
		}, true
	},

	event.TypeChangeFailed: func(p map[string]any) (notify.Message, bool) {
		title := str(p, "title")
		planID := str(p, "plan_id")

		severity := notify.SeverityWarning
		body := str(p, "error")

		// 回滚失败是**最严重**的结果：系统既不是原状、也不是目标状态。
		// 它必须比普通的"变更失败"更醒目。
		if rbErr := str(p, "rollback_error"); rbErr != "" {
			severity = notify.SeverityError
			body = fmt.Sprintf(
				"变更失败，且自动回滚未能完成 —— 系统可能处于中间状态。\n%s\n%s",
				str(p, "error"), rbErr)
		}

		return notify.Message{
			Event:    event.TypeChangeFailed,
			Severity: severity,
			Title:    fmt.Sprintf("系统变更失败：%s", title),
			Body:     body,
			DedupKey: "change.failed:" + planID,
			Data:     p,
		}, true
	},

	event.TypeChangeRolledBack: func(p map[string]any) (notify.Message, bool) {
		return notify.Message{
			Event:    event.TypeChangeRolledBack,
			Severity: notify.SeverityInfo,
			Title:    fmt.Sprintf("系统变更已撤销：%s", str(p, "title")),
			DedupKey: "change.rolled_back:" + str(p, "plan_id"),
			Data:     p,
		}, true
	},
}

// startNotifier 订阅事件总线并分发通知。
//
// 它跑在自己的 goroutine 里：通知可能涉及网络请求，而事件总线
// 是**同步**分发的 —— 在总线里直接发通知会拖慢所有其它订阅者。
func (d *Daemon) startNotifier(ctx context.Context) {
	if d.notifier == nil || d.bus == nil {
		return
	}

	sub, err := d.bus.Subscribe(0)
	if err != nil {
		d.log.Warn("通知中心订阅事件失败", "err", err)
		return
	}

	go func() {
		defer sub.Close()

		// 投递循环。
		go d.notifier.Run(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-sub.C():
				if !ok {
					return
				}
				d.handleNotifyEvent(ev)
			}
		}
	}()
}

func (d *Daemon) handleNotifyEvent(ev event.Event) {
	build, ok := notifyEvents[ev.Type]
	if !ok {
		return
	}

	// 事件的载荷是原始 JSON，这里解码成通用结构。
	//
	// 用 map 而不是为每种事件定义一个结构体：通知的内容是**给人看的
	// 文本**，它只需要取出几个字段拼成标题与正文，而为此定义十几个
	// 结构体（还得跟着事件载荷的演进同步维护）不成比例。
	var payload map[string]any
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			d.log.Debug("事件载荷无法解析，跳过通知", "type", ev.Type, "err", err)
			return
		}
	}
	if payload == nil {
		payload = map[string]any{}
	}

	msg, ok := build(payload)
	if !ok {
		return
	}
	msg.At = ev.TS

	// Notify 是**非阻塞**的：事件总线是同步分发的，在这里等网络
	// 会把所有其它订阅者一起拖住。
	d.notifier.Notify(msg)
}

// str 从事件负载里取一个字符串。
func str(p map[string]any, key string) string {
	v, ok := p[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}
