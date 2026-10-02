package i18n

// reachMessagesZh 是可达性检查（`isc doctor` 的正文）的消息。
//
// 这一层的特点是**它是给用户看的诊断**：每条检查都带一个状态、一句说明、
// 以及"该怎么办"。措辞里刻意保留了"这不是你的配置问题"这类判断 ——
// 用户看到一句失败时最需要知道的正是"这该不该我来修"。
var reachMessagesZh = map[string]string{
	// --- 检查名（同一个名字会出现在检查项与其提示里）---
	"reach.check.global_v6":  "全局 IPv6 地址",
	"reach.check.prefix":     "IPv6 委派前缀",
	"reach.check.firewall":   "本机防火墙后端",
	"reach.check.upstream":   "上游可达性",
	"reach.check.port_range": "端口范围",
	"reach.check.listening":  "服务监听",

	"reach.port.confirm_hint": "请自行确认该端口上的服务已启动。",
	"reach.check.portmap":     "端口映射",
	"reach.check.lowport":     "低端口权限",
	"reach.check.unimpl":      "未实现",

	// --- IPv6 直连方式 ---
	"reach.v6.name": "IPv6 直连",
	"reach.v6.desc": "直接用本机的公网 IPv6 地址对外提供服务。\n" +
		"不需要公网 IPv4、不需要中转服务器、流量不经过任何第三方。\n" +
		"需要运营商下发了 IPv6 前缀，且路由器放行了入站连接。",

	// --- 全局 IPv6 地址 ---
	"reach.v6.found":     "在 %s 上找到 %d 个可用于公网访问的 IPv6 地址",
	"reach.v6.not_found": "没有找到可用于公网访问的 IPv6 地址",
	"reach.v6.not_found_hint": "（链路本地 fe80:: 与私有 fd00:: 不算）\n" +
		"确认运营商已开通 IPv6（多数家宽默认开通，可打客服确认）；\n" +
		"再进路由器的 IPv6 设置，确认已开启且为「Native / 原生」模式而非隧道模式",

	// --- 委派前缀 ---
	"reach.prefix.found":   "检测到 %d 个委派前缀（/64 或更粗）",
	"reach.prefix.no_addr": "没有全局 IPv6 地址，无法判断前缀",
	"reach.prefix.no_prefix": "有全局 IPv6 地址，但没有检测到 /64 或更粗的委派前缀。" +
		"这通常意味着地址是运营商逐台分配的（/128），重启或换设备后地址会变",
	"reach.prefix.no_prefix_hint": "这种情形下动态解析仍然可用，但地址变化会更频繁。" +
		"建议把检测周期调短一些",

	// --- 防火墙后端 ---
	"reach.fw.unimplemented": "当前平台（%s）的防火墙后端尚未实现，内核无法自动放行端口",
	"reach.fw.unimpl_hint": "这是内核的能力缺口，不是你的配置问题。" +
		"请手动在系统防火墙中放行需要的端口，或等待后续版本",
	"reach.fw.ready": "已接入 %s；放行操作会先生成可预览的计划，并在失败时自动回滚",
	"reach.fw.manual_allow": "reach: 本平台（%s）的防火墙后端尚未实现，无法自动放行端口；" +
		"请手动在系统防火墙中放行 %d/%s",

	// --- 结论 ---
	"reach.summary.ready":   "本机已具备 IPv6 直连的条件；能否从外网访问需要用手机流量验证",
	"reach.summary.pending": "本机还差一步：",

	// --- 计划与撤销 ---
	"reach.plan.title":        "放行 %d/%s 的入站连接",
	"reach.plan.notes_a":      "规则只放行这一个端口，不会改动其它规则。",
	"reach.plan.notes_b":      "撤销时会恢复成添加之前的状态。",
	"reach.plan.irreversible": "该后端报告此变更不可撤销；应用后需要手动清理。",
	"reach.plan.diff":         "入站 %s %s（来源：任意）",
	"reach.diff_failed":       "reach: 计算防火墙差异失败: %w",
	"reach.rollback.no_backend": "reach: 无法自动撤销 %s：当前平台的防火墙后端不可用。" +
		"请在系统防火墙中手动删除以 %q 开头的规则",
	"reach.rollback.no_payload": "reach: 无法自动撤销 %s：这条变更没有保存回滚数据" +
		"（可能由更早的内核版本创建）。请在系统防火墙中手动删除以 %q 开头的规则",
	"reach.rollback.failed": "reach: 撤销防火墙变更 %s 失败: %w",

	// --- 上游可达性 ---
	"reach.upstream.blocked": "这不是你能在本机修复的。可以尝试：" +
		"换一个端口（运营商常常只封特定端口）、换用其它可达方式、或联系运营商确认",
	"reach.upstream.detail": "本机无法自测：从本机访问自己的公网地址通常走回环，" +
		"因此无论上游是否放行都会显示成功",
	"reach.upstream.unknown": "用手机 4G/5G 打开验证地址进行确认。" +
		"这一步不能省 —— 它是区分「本机没配好」与「运营商封了」的唯一手段",

	// --- 参数校验 ---
	"reach.bad_port":       "端口 %d 不在合法范围内",
	"reach.bad_port_range": "端口 %d 不在 1-65535 范围内",
	"reach.bad_port_hint":  "请填一个合法端口。",
	"reach.bad_proto":      "reach: 不支持的协议 %q（只支持 tcp / udp）",
	"reach.udp_no_probe":   "%s 端口无法用连接探测（UDP 没有连接的概念）",

	// --- 端口探测 ---
	"reach.port.listening":     "端口 %d 上有服务在监听",
	"reach.port.not_listening": "端口 %d 上没有服务在监听",
	"reach.port.interrupted":   "探测被中断",
	"reach.port.no_listener":   "reach: 该端口上没有服务在监听",
	"reach.port.no_listener_hint": "规则开好了但本机没有服务，从外面访问仍然什么都不通，" +
		"而那时很难想到问题出在本机上。" +
		"如果服务还没启动，请先启动它；如果只是还没部署，可以先放行。",

	// --- 端口映射 ---
	"reach.portmap.passthrough": "外部 %d → 内部 %d（直通）",
	"reach.portmap.mapped":      "外部 %d → 内部 %d",
	"reach.portmap.hint": "非标端口入口：访问时要显式带端口号，" +
		"例如 https://example.com:8443",

	// --- 低端口 ---
	"reach.lowport.not_privileged": "%d 不是特权端口，无需额外权限",
	"reach.lowport.ok":             "可以绑定 %d（%s）",
	"reach.lowport.denied": "绑定 %d 需要额外权限。三种做法：\n" +
		"  · 换一个 ≥1024 的端口（最简单，代价是访问时要带端口号）\n" +
		"  · 给可执行文件授权：sudo setcap 'cap_net_bind_service=+ep' /path/to/isc\n" +
		"  · 以 root 运行内核",

	// --- 不可用 ---
	"reach.unavailable": "reach: 当前方式不可用",
}
