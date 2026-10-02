package i18n

// notifyMessagesEn 是**通知中心**的消息。
//
// 必须与 notifyMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var notifyMessagesEn = map[string]string{
	// --- channel configuration validation ---
	"notify.cfg.no_id":         "notify: the channel has no ID",
	"notify.cfg.no_name":       "notify: channel %s has no name",
	"notify.cfg.bad_kind":      "notify: unsupported channel type %q",
	"notify.cfg.no_url":        "notify: webhook channel %s has no target URL",
	"notify.cfg.bad_min_level": "notify: unsupported minimum severity %q",
	"notify.cfg.read_failed":   "notify: failed to read the channel configuration: %w",
	"notify.cfg.build_failed":  "notification channel %q could not be built and was skipped: %v",
	"notify.cfg.loaded":        "notification channels loaded: %d in effect",

	// --- webhook ---
	"notify.webhook.need_url":     "notify: a webhook channel needs a target URL",
	"notify.webhook.bad_scheme":   "notify: the webhook URL must start with http:// or https://, got %q",
	"notify.webhook.bad_template": "notify: the webhook body template has a syntax error: %w",
	"notify.webhook.build_failed": "notify: failed to build the request: %w",
	"notify.webhook.req_failed":   "notify: the request failed: %w",
	"notify.webhook.bad_status":   "notify: the target returned HTTP %d: %s",
	"notify.webhook.marshal":      "notify: failed to serialise the message: %w",
	"notify.webhook.render":       "notify: failed to render the request body: %w",
	"notify.webhook.channel_name": "log",

	// --- messages and merging ---
	"notify.msg.no_title":     "notify: the message has no title",
	"notify.msg.bad_severity": "notify: unsupported severity %q",
	"notify.msg.merged":       "%d more event(s) of the same kind were merged in",
	"notify.msg.dedup_key":    "Trigger key:",
}
