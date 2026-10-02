package i18n

// verifyMessagesZh 是**外部验证会话**的消息。
//
// 这一层里有一件别处没有的东西：`page.go` 生成一张**发给手机的 HTML 页面**。
// 它是整个项目里唯一会被**外部设备**看到的界面，因此有两条额外约束：
//
//   - 页面必须极小、无外部资源。多一次请求就多一次失败机会，而失败的表现是
//     "一片空白"——用户会以为验证没成功；
//   - 结论必须在第一屏用最大的字号说清楚。用户此刻只关心"到底通没通"。
//
// 页面语言跟随**内核**的语言设置（不是手机的 Accept-Language）：拿手机的是
// 同一个用户，而他在 ISC 里已经选过语言了。
//
// 另一组文案解释的是**为什么这次访问不算数**。IPv6 没有 NAT，所以从本机访问
// 自己的公网地址是**直连**的、不经过运营商 —— 成功了也证明不了任何事。
// 这一点反直觉，因此每条都要说透，否则用户会拿着一个假阳性结果以为通了。
var verifyMessagesZh = map[string]string{
	// --- 结果页 ---
	"verify.page.headline_ok":   "链路是通的",
	"verify.page.headline_fail": "这次访问不能作为凭据",
	"verify.page.body_ok": "这台机器能从公网被访问到。回到 ISC 控制台即可看到结果，" +
		"并可以关闭这个临时端口。",
	"verify.page.label_source": "本次来源",
	"verify.page.label_addr":   "来源地址",
	"verify.page.footer":       "ISC · 一次性验证页面，可以关闭",
	"verify.page.title":        "ISC 外部验证",

	// --- 来源分类 ---
	"verify.page.kind_public":    "公网地址（有效凭据）",
	"verify.page.kind_self":      "本机自己的地址（无效凭据）",
	"verify.page.kind_loopback":  "本机回环（无效凭据）",
	"verify.page.kind_linklocal": "链路本地（无效凭据）",
	"verify.page.kind_private":   "内网或运营商级 NAT（无效凭据）",
	"verify.page.kind_unknown":   "无法识别（无效凭据）",

	// --- 诊断结论 ---
	"verify.verdict.reachable": "已收到来自公网的访问，链路确实可用",
	"verify.verdict.blocked": "外部验证超时，且一次公网访问都没有收到 —— " +
		"本机监听正常但流量没有到达，说明上游挡住了这个端口",

	// --- 发夹（本机访问自己的公网地址）---
	//
	// IPv6 没有 NAT，因此这条路径是**直连**的 —— 这正是它证明不了任何事的原因。
	"verify.hairpin.self": "这次访问来自这台机器自己 —— IPv6 没有 NAT，" +
		"本机访问自己的公网地址是直连的，不经过运营商，" +
		"因此什么也证明不了。请用手机（关闭 Wi-Fi，走 4G/5G）重新打开。",
	"verify.hairpin.loopback": "只收到了来自本机的访问 —— 那是回环路径，不经过网络，" +
		"什么也证明不了。请用手机（关闭 Wi-Fi）重新打开。",
	"verify.hairpin.linklocal": "只收到了链路本地地址的访问 —— 不经过运营商。" +
		"请用手机（关闭 Wi-Fi，走 4G/5G）重新打开。",
	"verify.hairpin.private": "只收到了来自内网的访问 —— 手机可能还连着 Wi-Fi，" +
		"或者这是个运营商级 NAT 地址。请关闭 Wi-Fi、走移动数据重新打开。",

	// --- 会话与状态 ---
	"verify.err.listen": "verify: 无法监听端口 %d：%w" +
		"（该端口可能已被其它程序占用）",
	"verify.err.not_tcp":     "verify: 监听地址不是 TCP 地址",
	"verify.err.no_session":  "verify: 验证会话不存在",
	"verify.err.in_progress": "verify: 已有正在进行的验证",
	"verify.err.gen_token":   "verify: 生成验证令牌失败: %w",
	"verify.err.gen_session": "verify: 生成会话 ID 失败: %w",

	"verify.msg.waiting":      "等待外部访问。请用手机（关闭 Wi-Fi，走 4G/5G）打开下面的地址。",
	"verify.msg.started":      "外部验证会话已开始：端口 %d，地址 %s",
	"verify.msg.listen_ended": "外部验证监听异常结束：%v",
	"verify.msg.success":      "外部访问成功 —— 链路是通的。",
	"verify.msg.stopped":      "验证已被手动停止。",
	"verify.msg.timeout":      "在有效期内没有收到任何外部访问。",

	// 超时且本机检测全过 → 指向**上游封禁**。这句刻意写明"本机已经没得改了"：
	// 否则用户会继续在本机上找问题，而那不会有结果。
	"verify.msg.no_visit": "在有效期内没有收到任何外部访问。若本机检测全部通过，" +
		"这一条指向**上游封禁**（运营商或路由器防火墙）—— 本机已经没得改了。",
}
