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
	"web.ov.title": "Kernel status",
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
