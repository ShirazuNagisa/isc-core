package i18n

// consoleMessagesEn 是**控制台前端**的消息。
//
// 必须与 consoleMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var consoleMessagesEn = map[string]string{
	// --- header and tab bar ---
	"web.title":           "ISC verification console",
	"web.lang_tag":        "en",
	"web.subtitle":        "verification console",
	"web.conn.connecting": "Connecting…",
	"web.tab.overview":    "Overview",
	"web.tab.ip":          "IP & prefixes",
	"web.tab.credentials": "Credentials",
	"web.tab.tasks":       "Dynamic DNS",
	"web.tab.records":     "DNS records",
	"web.tab.proxy":       "Reverse proxy",
	"web.tab.certs":       "Certificates",
	"web.tab.notify":      "Notifications",
	"web.tab.service":     "System service",
	"web.tab.events":      "Event stream",
	"web.tab.raw":         "Raw API",

	// --- overview ---
	"web.ov.title":     "Kernel status",
	"web.ov.cap_title": "Platform capabilities",

	// --- IP and prefixes ---
	"web.ip.title":   "Local addresses and IPv6 prefixes",
	"web.ip.refresh": "Refresh",

	// --- credentials ---
	"web.cred.title":   "DNS provider credentials",
	"web.cred.refresh": "Refresh",
	"web.cred.new":     "New credential",

	// --- dynamic DNS ---
	"web.task.title":   "Dynamic DNS tasks",
	"web.task.refresh": "Refresh",
	"web.task.new":     "New task",

	// --- DNS records ---
	"web.rec.title": "DNS record management",
	"web.rec.load":  "Load records",
	"web.rec.new":   "New record",

	// --- reverse proxy ---
	"web.px.title":   "Reverse proxy",
	"web.px.save":    "Save",
	"web.px.status":  "Refresh status",
	"web.px.rules":   "Forwarding rules",
	"web.px.refresh": "Refresh",
	"web.px.new":     "New rule",

	// --- certificates ---
	"web.cert.title":   "TLS certificates",
	"web.cert.refresh": "Refresh",
	"web.cert.renew":   "Check and renew now",

	// --- notifications ---
	"web.nt.title":   "Notification channels",
	"web.nt.refresh": "Refresh",
	"web.nt.new":     "New channel",
	"web.nt.test":    "Send a test notification",
	"web.nt.recent":  "Recent deliveries",

	// --- system service ---
	"web.svc.title":     "System service",
	"web.svc.refresh":   "Refresh",
	"web.svc.start":     "Start service",
	"web.svc.stop":      "Stop service",
	"web.svc.install_h": "Install the service",
	"web.svc.install":   "Install as a system service",
	"web.svc.uninstall": "Uninstall the service",

	// --- event stream ---
	"web.ev.title":      "Event stream",
	"web.ev.connect":    "Connect",
	"web.ev.disconnect": "Disconnect",
	"web.ev.clear":      "Clear",

	// --- raw API ---
	"web.raw.title": "Raw API call",
	"web.raw.send":  "Send",
	"web.raw.ready": "Ready",

	// --- shared ---
	"web.common.loading": "Loading…",
}

// ConsoleMessages 返回**控制台前端**在指定语言下的全部消息。
//
// 它导出的是一个副本：调用方要把它序列化成 JSON 发给浏览器，
// 而让外部拿到可变的地图会埋下一个很难查的问题 —— 页面 A 改了之后
// 页面 B 的文案跟着变。
//
// 这一层刻意**不含**基础表与其它分层：那些是内核自己的错误文案
// （数据库失败、迁移校验和不匹配……），前端一条都用不到，
// 送过去只是把它们暴露在浏览器里。
func ConsoleMessages(lang Lang) map[string]string {
	src := consoleMessagesZh
	if lang == En {
		src = consoleMessagesEn
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}
