package i18n

// notifyMessagesZh 是**通知中心**的消息。
//
// 这一层分两种用途，措辞要求不同：
//
//   - **配置校验**（config / webhook）：用户填错了通道配置，报错要指出
//     是哪个通道、哪个字段、以及期望什么；
//   - **投递时的合并说明**：同一件事在短时间内反复发生时会被合并成一条，
//     正文里必须**如实说明合并了多少次** —— 否则用户会以为事件只发生了一次。
var notifyMessagesZh = map[string]string{
	// --- 通道配置校验 ---
	"notify.cfg.no_id":         "notify: 通道缺少 ID",
	"notify.cfg.no_name":       "notify: 通道 %s 缺少名称",
	"notify.cfg.bad_kind":      "notify: 不支持的通道类型 %q",
	"notify.cfg.no_url":        "notify: Webhook 通道 %s 缺少目标地址",
	"notify.cfg.bad_min_level": "notify: 不支持的最低级别 %q",
	"notify.cfg.read_failed":   "notify: 读取通道配置失败: %w",
	"notify.cfg.build_failed":  "通知通道 %q 构造失败，已跳过: %v",
	"notify.cfg.loaded":        "通知通道已加载：%d 个生效",

	// --- Webhook ---
	"notify.webhook.need_url":     "notify: Webhook 通道需要目标地址",
	"notify.webhook.bad_scheme":   "notify: Webhook 地址必须以 http:// 或 https:// 开头，得到 %q",
	"notify.webhook.bad_template": "notify: Webhook 请求体模板语法错误: %w",
	"notify.webhook.build_failed": "notify: 构造请求失败: %w",
	"notify.webhook.req_failed":   "notify: 请求失败: %w",
	"notify.webhook.bad_status":   "notify: 目标返回 HTTP %d: %s",
	"notify.webhook.marshal":      "notify: 序列化消息失败: %w",
	"notify.webhook.render":       "notify: 渲染请求体失败: %w",
	"notify.webhook.channel_name": "日志",

	// --- 消息与合并 ---
	"notify.msg.no_title":     "notify: 消息缺少标题",
	"notify.msg.bad_severity": "notify: 不支持的重要程度 %q",
	"notify.msg.merged":       "另有 %d 次同类事件被合并",
	"notify.msg.dedup_key":    "触发键：",
}
