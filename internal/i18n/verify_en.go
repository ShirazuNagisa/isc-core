package i18n

// verifyMessagesEn 是**外部验证会话**的消息。
//
// 必须与 verifyMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var verifyMessagesEn = map[string]string{
	// --- result page ---
	"verify.page.headline_ok":   "The path is open",
	"verify.page.headline_fail": "This visit does not count as proof",
	"verify.page.body_ok": "This machine is reachable from the public internet. " +
		"Go back to the ISC console to see the result; you can close this temporary port now.",
	"verify.page.label_source": "This visit came from",
	"verify.page.label_addr":   "Source address",
	"verify.page.footer":       "ISC · a one-off verification page; you can close it",
	"verify.page.title":        "ISC external verification",

	// --- source classification ---
	"verify.page.kind_public":    "a public address (valid proof)",
	"verify.page.kind_self":      "this machine's own address (not valid proof)",
	"verify.page.kind_loopback":  "the local loopback (not valid proof)",
	"verify.page.kind_linklocal": "a link-local address (not valid proof)",
	"verify.page.kind_private":   "a private or carrier-grade NAT address (not valid proof)",
	"verify.page.kind_unknown":   "unrecognised (not valid proof)",

	// --- diagnosis ---
	"verify.verdict.reachable": "A visit from the public internet arrived; the path really is open",
	"verify.verdict.blocked": "External verification timed out and not one public visit arrived — " +
		"the local listener is fine but the traffic never got here, which points at the upstream blocking this port",

	// --- hairpin (reaching your own public address from this machine) ---
	//
	// IPv6 has no NAT, so this path is direct — which is exactly why it proves nothing.
	"verify.hairpin.self": "This visit came from the machine itself — IPv6 has no NAT, so reaching your own " +
		"public address from here goes straight out without passing your ISP. That proves nothing. " +
		"Open it again on a phone with Wi-Fi turned off, over 4G/5G.",
	"verify.hairpin.loopback": "Only a visit from the machine itself arrived — that is the loopback path and never " +
		"touches the network, so it proves nothing. Open it again on a phone with Wi-Fi turned off.",
	"verify.hairpin.linklocal": "Only a visit from a link-local address arrived — it never passed your ISP. " +
		"Open it again on a phone with Wi-Fi turned off, over 4G/5G.",
	"verify.hairpin.private": "Only a visit from the local network arrived — the phone may still be on Wi-Fi, " +
		"or this is a carrier-grade NAT address. Turn Wi-Fi off and open it again over mobile data.",

	// --- session and status ---
	"verify.err.listen": "verify: failed to listen on port %d: %w " +
		"(the port may already be in use by another program)",
	"verify.err.not_tcp":     "verify: the listen address is not a TCP address",
	"verify.err.no_session":  "verify: the verification session does not exist",
	"verify.err.in_progress": "verify: a verification is already in progress",
	"verify.err.gen_token":   "verify: failed to generate the verification token: %w",
	"verify.err.gen_session": "verify: failed to generate the session ID: %w",

	"verify.msg.waiting":      "Waiting for an external visit. Open the address below on a phone with Wi-Fi turned off, over 4G/5G.",
	"verify.msg.started":      "The external verification session has started: port %d, address %s",
	"verify.msg.listen_ended": "The external verification listener ended abnormally: %v",
	"verify.msg.success":      "An external visit succeeded — the path is open.",
	"verify.msg.stopped":      "Verification was stopped by hand.",
	"verify.msg.timeout":      "No external visit arrived within the validity period.",

	// Timed out with every local check passing → this points at the upstream.
	// The last sentence is deliberate: without it the user keeps looking for
	// a problem on this machine, where there is none left to find.
	"verify.msg.no_visit": "No external visit arrived within the validity period. If every local check passed, " +
		"this points at the **upstream** (your ISP or the router's firewall) — there is nothing left to change here.",
}
