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

	// --- explanatory paragraphs (contain inline markup; use data-i18n-html) ---
	//
	// The Chinese values live in console_zh.go. These are whole sentences with
	// their inline tags kept intact — the translator needs to see where the
	// <strong> sits, which per-text-node extraction destroys.
	"web.overview.p1": "Every item here comes from a <strong>code fact</strong> in the kernel " +
		"(an interface assertion), not from configuration. \"Guided mode\" means that platform " +
		"backend is not implemented yet, so the related features degrade rather than pretend to work.",
	"web.ip.p2": "The <strong>prefix</strong> is the central concept here: what changes after an ISP " +
		"redial is the whole <code>/64</code> prefix, and <strong>every</strong> AAAA record under it " +
		"has to be rewritten — not just one address. " +
		"Only prefixes of <code>/64</code> or coarser appear in the list: Windows reports IPv6 host " +
		"addresses as <code>/128</code>, and privacy-extension addresses rotate hourly, so treating one " +
		"as a delegated prefix would cause a pointless full update every hour.",
	"web.credentials.p3": "Credentials are stored encrypted in an envelope protected by the master key. " +
		"The list shows masked values only — the kernel <strong>never</strong> sends plaintext credentials back to the UI.",
	"web.tasks.p4": "One task = one set of credentials + one set of domains + one set of address sources. " +
		"An address change triggers it immediately, with periodic polling as a backstop.<br>" +
		"Ticking \"run now\" on a task <strong>clears the debounce cache</strong>, so it is guaranteed to compare against the provider once.",
	"web.records.p5": "Available for Tier-1 providers only (Cloudflare / Alibaba Cloud / Tencent Cloud / DNSPod / Huawei Cloud / GoDaddy).<br>" +
		"<strong>Their record models differ</strong>: on Huawei Cloud a record belongs to a \"record set\", " +
		"and GoDaddy records have no independent ID — deleting either affects other values with the same name. " +
		"See <code>docs/PROVIDER-MATRIX.md</code>.",
	"web.proxy.p6": "Forwards external requests to services on this machine by domain. " +
		"<strong>There is no default upstream</strong> — an unconfigured domain returns 404 rather than " +
		"falling back to some local service.<br>" +
		"Upstreams may only be loopback or private addresses: allowing public ones would turn this " +
		"feature into an <strong>open proxy</strong> that anyone could use to relay traffic through your machine.",
	"web.proxy.p7": "Rules are saved <strong>as a whole</strong>: a domain cannot point at two upstreams at once, " +
		"and incremental edits would make \"check for conflicts\" span several calls.",
	"web.certs.p8": "The kernel issues and renews certificates on its own: the renewal window opens at " +
		"<strong>1/3 of the lifetime</strong> remaining (30 days ahead for a 90-day certificate). " +
		"Normally no manual action is needed.<br>" +
		"\"Needs renewal\" is followed by a <strong>reason</strong> — one kind is nearing expiry, the other " +
		"is \"the existing certificate does not cover a newly added domain\", which has nothing to do with " +
		"how much validity is left.",
	"web.notify.p9": "The same event is sent only once within the quiet period, so address flapping cannot flood you; " +
		"if messages were suppressed during the quiet period, a summary follows once it ends.<br>" +
		"<strong>The log channel is always available</strong> — even with no external channel configured, " +
		"notifications still appear in the event stream and the kernel log.",
	"web.notify.p10": "The question asked most often when configuring a channel is \"did my notification actually go out?\" " +
		"— this is the answer.",
	"web.service.p11": "Registering the kernel as a system service gives you start-on-boot and automatic restart after " +
		"a crash, without keeping a terminal open.<br>" +
		"<strong>Installing and uninstalling need administrator rights</strong>; on Windows, so does merely " +
		"<strong>querying</strong> the service state — which is why \"is the kernel reachable\" is reported " +
		"separately below, since that question needs no privileges at all.",
	"web.service.p12": "Uninstalling does <strong>not</strong> delete the data directory — your credentials and configuration live there.",
	"web.events.p13": "Pushed in real time over WebSocket. A browser cannot set request headers on a WebSocket, " +
		"so the token travels in the <code>Sec-WebSocket-Protocol</code> subprotocol.<br>" +
		"On reconnect it sends <code>lastEventId</code> to replay missed events; if the replay chain is broken " +
		"(the ring buffer was overwritten) you get an <code>events.gap</code> instead of silently losing events.",
	"web.raw.p14": "Send one request straight at the kernel and see the <strong>raw response</strong>. " +
		"The last resort for verification — when the UI has a bug, this is still trustworthy.<br>" +
		"Contract source: <a href=\"/v1/openapi.yaml\" target=\"_blank\" rel=\"noopener\">/v1/openapi.yaml</a>",

	// --- labels that mix text with a form control ---
	//
	// These labels hold **both text and a control** (`<label>Credential <select>…`).
	// Putting data-i18n on the label itself would replace the control too — the
	// page would silently lose a dropdown, which is easy to miss both on screen
	// and in the code. So the plain text is wrapped in <span data-i18n> and the
	// control stays outside.
	"web.ov.th_cap":       "Capability",
	"web.ov.th_avail":     "Available",
	"web.ov.th_backend":   "Backend",
	"web.ov.th_note":      "Notes",
	"web.rec.l_cred":      "Credential",
	"web.rec.l_zone":      "Zone",
	"web.rec.pick_cred":   "(pick a credential first)",
	"web.px.l_enable":     "Enable the reverse proxy",
	"web.px.l_port":       "Port",
	"web.px.l_https":      "Use HTTPS",
	"web.svc.l_autostart": "Start on boot",
	"web.svc.l_restart":   "Restart automatically after a crash",
	"web.ev.l_follow":     "Follow",
	"web.raw.l_method":    "Method",
	"web.raw.l_path":      "Path",
	"web.raw.l_body":      "Request body (JSON, optional)",

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
