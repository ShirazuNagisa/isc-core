package i18n

// consoleMessagesZh 是**控制台前端**的消息。
//
// # 为什么单独一层，而不是复用基础表
//
// 这一层会被**整个导出给浏览器**（见 ConsoleMessages）：页面在浏览器里运行，
// 拿不到 Go 的目录，只能先取一份 JSON 再替换。因此这一层必须只含
// **前端真正用得到的 key** —— 把整个目录倒给页面不仅浪费带宽，
// 还会把内部的错误文案（数据库失败、迁移校验和不匹配……）送到浏览器里。
//
// # 为什么值里允许出现 HTML
//
// 控制台的句子被 `<strong>` / `<code>` 切开，逐文本节点抽取会得到
// 不可翻译的碎片（试过：抽出 128 条，大半是"前缀，该前缀下的"这种）。
// 因此这里的值是**整句含内联标签**，前端用 innerHTML 注入。
//
// 前提写在这里：**这些值只来自本文件，绝不可来自用户输入**。
// 一旦有用户可控的内容混进来，innerHTML 就成了 XSS 入口。
// # 键名怎么取
//
// `web.<区块>.<名字>`。区块对应 index.html 里的 `<section id="tab-…">`，
// 因此"这句话该去哪儿找"有唯一答案。带内联标签的句子用 `_html` 后缀标记，
// 提醒改它的人别把它塞进 data-i18n（那会显示成字面量）。
var consoleMessagesZh = map[string]string{
	// --- 页头与标签栏 ---
	"web.title":           "ISC 验证控制台",
	"web.lang_tag":        "zh-CN",
	"web.subtitle":        "验证控制台",
	"web.conn.connecting": "连接中…",
	"web.tab.overview":    "概览",
	"web.tab.ip":          "IP 与前缀",
	"web.tab.credentials": "凭据",
	"web.tab.tasks":       "动态解析",
	"web.tab.records":     "DNS 记录",
	"web.tab.proxy":       "反向代理",
	"web.tab.certs":       "证书",
	"web.tab.notify":      "通知",
	"web.tab.service":     "系统服务",
	"web.tab.events":      "事件流",
	"web.tab.raw":         "原始接口",

	// --- 概览 ---
	"web.ov.title":     "内核状态",
	"web.ov.cap_title": "平台能力",

	// --- 本机地址与前缀 ---
	"web.ip.title":   "本机地址与 IPv6 前缀",
	"web.ip.refresh": "刷新",

	// --- 凭据 ---
	"web.cred.title":   "DNS 服务商凭据",
	"web.cred.refresh": "刷新",
	"web.cred.new":     "新建凭据",

	// --- 动态解析 ---
	"web.task.title":   "动态解析任务",
	"web.task.refresh": "刷新",
	"web.task.new":     "新建任务",

	// --- DNS 记录 ---
	"web.rec.title": "DNS 记录管理",
	"web.rec.load":  "加载记录",
	"web.rec.new":   "新增记录",

	// --- 反向代理 ---
	"web.px.title":   "反向代理",
	"web.px.save":    "保存",
	"web.px.status":  "刷新状态",
	"web.px.rules":   "转发规则",
	"web.px.refresh": "刷新",
	"web.px.new":     "新增规则",

	// --- 证书 ---
	"web.cert.title":   "TLS 证书",
	"web.cert.refresh": "刷新",
	"web.cert.renew":   "立即检查并续期",

	// --- 通知 ---
	"web.nt.title":   "通知通道",
	"web.nt.refresh": "刷新",
	"web.nt.new":     "新增通道",
	"web.nt.test":    "发送测试通知",
	"web.nt.recent":  "最近的投递结果",

	// --- 系统服务 ---
	"web.svc.title":     "系统服务",
	"web.svc.refresh":   "刷新",
	"web.svc.start":     "启动服务",
	"web.svc.stop":      "停止服务",
	"web.svc.install_h": "安装服务",
	"web.svc.install":   "安装为系统服务",
	"web.svc.uninstall": "卸载服务",

	// --- 事件流 ---
	"web.ev.title":      "事件流",
	"web.ev.connect":    "连接",
	"web.ev.disconnect": "断开",
	"web.ev.clear":      "清空",

	// --- 原始接口 ---
	"web.raw.title": "原始接口调用",
	"web.raw.send":  "发送",
	"web.raw.ready": "就绪",

	// --- 说明段落（含内联标签，用 data-i18n-html 注入）---
	//
	// 这些句子被 <strong> / <code> 切开，逐文本节点抽取会得到不可翻译的碎片。
	// 整句作为一条消息是唯一的办法 —— 译者需要看到完整的句子才知道
	// 那个 <strong> 圈的是哪一部分。
	"web.overview.p1":    "每一项都来自内核的<strong>代码事实</strong>（接口断言），不是配置。「引导模式」表示该平台后端尚未实现，相关功能会降级而不是假装成功。",
	"web.ip.p2":          "<strong>前缀</strong>是这套系统的核心概念：ISP 重拨后变化的是整个 <code>/64</code> 前缀，该前缀下的<strong>所有</strong> AAAA 记录都要重写，而不是只改一个地址。列表里只会出现 <code>/64</code> 及更粗的前缀 ——Windows 会把 IPv6 主机地址报成 <code>/128</code>，而隐私扩展地址每小时轮换，把它当委派前缀会造成每小时一次的无意义全量更新。",
	"web.credentials.p3": "凭据加密存储在主密钥保护的信封里。列表中只显示掩码值 ——内核<strong>不会</strong>把明文凭据发回界面。",
	"web.tasks.p4":       "一条任务 = 一组凭据 + 一组域名 + 一组地址来源。地址变化会被立即触发，另有定时轮询兜底。<br> 勾选任务的「立即执行」会<strong>清空防抖缓存</strong>，因此必定与服务商比对一次。",
	"web.records.p5":     "仅 Tier-1 服务商可用（Cloudflare / 阿里云 / 腾讯云 / DNSPod / 华为云 / GoDaddy）。<br> <strong>注意各家的记录模型不同</strong>：华为云的一条记录属于一个「记录集」，GoDaddy 的记录没有独立 ID ——它们的删除会波及同名的其它值。详见 <code>docs/PROVIDER-MATRIX.md</code>。",
	"web.proxy.p6":       "按域名把外部请求转发到本机的服务上。<strong>没有默认上游</strong> ——未配置的域名会返回 404，而不是回落到某个本地服务。<br> 上游只允许本机与内网地址：允许公网地址会让这个功能变成一个 <strong>开放代理</strong>，任何人都能借你的机器转发流量。",
	"web.proxy.p7":       "规则是<strong>整体保存</strong>的：同一个域名不能同时指向两个上游，而增量修改会让「检查冲突」变成一件跨多次调用才能完成的事。",
	"web.certs.p8":       "证书由内核自动申请与续期：到期前 <strong>1/3 寿命</strong>时进入续期窗口 （对 90 天的证书即提前 30 天）。一般情况下不需要手动干预。<br> 「需要续期」后面会给出<strong>理由</strong> ——一类是快过期了，另一类是 「现有证书不覆盖某个新加的域名」，而后者与剩余有效期无关。",
	"web.notify.p9":      "同一个事件在静默期内只发一条，防止地址抖动刷屏；静默期过后若期间有被抑制的消息，会补发一条汇总。<br> <strong>日志通道始终可用</strong> ——即使没有配置任何外部通道，通知也会出现在事件流与内核日志里。",
	"web.notify.p10":     "配置通道时最常被问到的问题是「我的通知到底发出去了没有」——这里是答案。",
	"web.service.p11":    "把内核注册为系统服务之后会开机自启、崩溃自动重启，不必一直开着终端。<br> <strong>安装与卸载需要管理员权限</strong>；在 Windows 上，连<strong>查询</strong>服务状态也需要 ——因此下面同时报出「内核是否可达」，那个问题不需要任何权限就能回答。",
	"web.service.p12":    "卸载<strong>不会</strong>删除数据目录 ——里面有你的凭据与配置。",
	"web.events.p13":     "通过 WebSocket 实时推送。浏览器无法为 WebSocket 设置请求头，因此令牌走 <code>Sec-WebSocket-Protocol</code> 子协议传递。<br> 断线重连时会带上 <code>lastEventId</code> 补发漏掉的事件；若补发链已断（环形缓冲被覆盖），会收到一条 <code>events.gap</code> 而不是静默丢事件。",
	"web.raw.p14":        "直接对内核发一次请求并看到<strong>原始响应</strong>。验证功能的最后一道手段 ——界面有 bug 时，这里仍然可信。<br> 契约原文：<a href=\"/v1/openapi.yaml\" target=\"_blank\" rel=\"noopener\">/v1/openapi.yaml</a>",

	// --- 混合内容标签 ---
	//
	// 这些标签里**既有文字又有表单控件**（`<label>凭据 <select>…`）。
	// 直接给 label 加 data-i18n 会把控件一起替换掉 —— 界面会少一个下拉框，
	// 而那种错误在页面上不显眼、在代码里也看不出来。因此把纯文本包进
	// <span data-i18n>，控件留在外面。
	"web.ov.th_cap":       "能力",
	"web.ov.th_avail":     "可用",
	"web.ov.th_backend":   "后端",
	"web.ov.th_note":      "说明",
	"web.rec.l_cred":      "凭据",
	"web.rec.l_zone":      "区域",
	"web.rec.pick_cred":   "（先选凭据）",
	"web.px.l_enable":     "启用反向代理",
	"web.px.l_port":       "端口",
	"web.px.l_https":      "使用 HTTPS",
	"web.svc.l_autostart": "开机自启",
	"web.svc.l_restart":   "崩溃后自动重启",
	"web.ev.l_follow":     "自动滚动",
	"web.raw.l_method":    "方法",
	"web.raw.l_path":      "路径",
	"web.raw.l_body":      "请求体（JSON，可留空）",

	// --- 由 JS 渲染的内容（panels.js）---
	//
	// 这一批走 t() / tf() 而不是 data-i18n 属性：它们是拼接生成的 HTML，
	// 页面上没有对应的静态元素。第二个参数是**兜底** —— 与 index.html
	// 不同，这些字符串只在这里出现一次，没有第二个来源。
	"web.px.err_state":      "无法读取代理状态",
	"web.px.not_running":    "未运行",
	"web.px.empty":          "还没有转发规则。",
	"web.px.edit_rule":      "编辑规则",
	"web.px.new_rule":       "新增规则",
	"web.px.l_domains":      "域名（每行一个）",
	"web.px.l_upstream":     "上游地址",
	"web.px.hint_private":   "只允许本机与内网地址。允许公网地址会让这个功能变成一个<strong>开放代理</strong>。",
	"web.px.l_tls":          "为此域名提供 HTTPS",
	"web.px.need_domain":    "至少填一个域名",
	"web.px.need_upstream":  "请填上游地址",
	"web.px.confirm_delete": "确定删除这条规则？",

	"web.cert.empty":         "还没有任何证书。",
	"web.cert.empty_hint":    "为一条路由启用 HTTPS 之后，内核会自动申请证书。",
	"web.cert.covers":        "覆盖：",
	"web.cert.needs_renewal": "需要续期：",
	"web.cert.last_error":    "上次失败：",
	"web.cert.checking":      "正在检查并续期，可能需要一两分钟…",

	"web.nt.empty":          "还没有配置任何通道。",
	"web.nt.empty_hint":     "（日志通道始终可用，通知会出现在事件流里。）",
	"web.nt.edit":           "编辑通道",
	"web.nt.kind_log":       "日志",
	"web.nt.l_url":          "目标地址",
	"web.nt.l_severity":     "最低级别",
	"web.nt.l_template":     "请求体模板（留空用默认 JSON）",
	"web.nt.no_deliveries":  "还没有投递记录。",
	"web.nt.need_name":      "请填名称",
	"web.nt.confirm_delete": "确定删除这个通道？",

	"web.th.domain":   "域名",
	"web.th.upstream": "上游",
	"web.th.name":     "名称",
	"web.th.kind":     "类型",
	"web.th.target":   "目标",
	"web.th.level":    "级别",
	"web.th.status":   "状态",
	"web.th.time":     "时间",
	"web.th.channel":  "通道",
	"web.th.result":   "结果",

	"web.common.edit":        "编辑",
	"web.common.delete":      "删除",
	"web.common.save":        "保存",
	"web.common.cancel":      "取消",
	"web.common.saved":       "已保存",
	"web.common.deleted":     "已删除",
	"web.common.enabled":     "启用",
	"web.common.disabled":    "停用",
	"web.common.read_failed": "读取失败：",
	"web.settings.saved":     "设置已保存",

	// --- 由 app.js 渲染的内容 ---
	"web.conn.down":        "未连接",
	"web.conn.up":          "已连接",
	"web.conn.auth_failed": "鉴权失败",
	"web.conn.failed":      "连接失败",

	"web.meta.version":     "版本",
	"web.meta.api_version": "接口版本",
	"web.meta.os":          "操作系统",
	"web.meta.started":     "启动时间",
	"web.meta.commit":      "提交",
	"web.meta.build_time":  "构建时间",

	"web.cap.firewall":        "防火墙编排",
	"web.cap.service_manager": "服务管理（自启）",
	"web.cap.ip_monitor":      "IP / 前缀监控",
	"web.cap.secret_store":    "密钥库",
	"web.cap.transport":       "本地传输",
	"web.cap.low_port":        "低端口绑定",
	"web.cap.available":       "可用",
	"web.cap.guided":          "引导模式",
	"web.cap.dynamic":         "动态解析",
	"web.cap.zones":           "记录管理",
	"web.cap.verify":          "可校验",
	"web.cap.unimplemented":   "未实现",

	"web.ip.primary_v6":     "主 IPv6",
	"web.ip.primary_prefix": "主前缀",
	"web.ip.primary_v4":     "主 IPv4",
	"web.ip.none":           "（无 —— 该机器没有可用的公网地址）",

	"web.cred.empty":      "还没有配置任何凭据。",
	"web.cred.provider":   "服务商",
	"web.cred.optional":   "（可选）",
	"web.cred.need_name":  "请填写名称",
	"web.cred.created":    "凭据已创建",
	"web.cred.testing":    "正在测试连接…",
	"web.cred.not_passed": "测试未通过",

	"web.common.unknown": "未知原因",

	"web.err.read_meta":      "读取内核信息失败：",
	"web.common.save_failed": "保存失败：",
	"web.common.test_failed": "测试失败：",
	"web.th.caps":            "能力",
	"web.th.credential":      "凭据",
	"web.cred.test":          "测试连接",
	"web.cred.name_ph":       "例如：我的 Cloudflare",

	"web.task.empty":        "还没有配置动态解析任务。",
	"web.task.never":        "从未执行",
	"web.task.updated":      "已更新",
	"web.task.failed":       "失败",
	"web.task.unchanged":    "无需改动",
	"web.task.th_task":      "任务",
	"web.task.th_sources":   "来源与域名",
	"web.task.th_last_addr": "上次地址",
	"web.task.need_cred":    "需要先创建一条凭据。",
	"web.task.new_full":     "新建动态解析任务",
	"web.task.label_ph":     "例如：家里的 IPv6",
	"web.task.l_getter":     "获取方式",
	"web.task.getter_iface": "网卡",
	"web.task.getter_url":   "外部接口",
	"web.cred.ok":           "连接正常",

	"web.task.run":            "立即执行",
	"web.task.getter_cmd":     "命令",
	"web.task.l_value":        "取值",
	"web.task.value_ph":       "网卡名（如 WLAN / eth0）",
	"web.task.l_selector":     "地址选择器（可选）",
	"web.task.selector_ph":    "@1 或正则",
	"web.task.save_run":       "保存并执行",
	"web.task.need_name":      "请填写任务名称",
	"web.task.created":        "任务已创建，正在执行首次解析…",
	"web.task.trigger_failed": "触发失败：",
	"web.task.accepted":       "已受理，正在执行…",

	"web.rec.no_cred":          "（没有支持记录管理的凭据）",
	"web.rec.zones_failed":     "读取区域失败：",
	"web.rec.no_zones":         "（该账号下没有活跃域名）",
	"web.rec.records_failed":   "读取记录失败：",
	"web.rec.empty":            "该区域下没有记录。",
	"web.rec.proxied":          "代理",
	"web.rec.priority":         "优先级",
	"web.rec.count_pre":        "共",
	"web.rec.th_content":       "内容",
	"web.rec.edit":             "编辑记录",
	"web.rec.l_name":           "名称（完整域名）",
	"web.rec.l_ttl":            "TTL（秒，0 = 服务商默认）",
	"web.rec.l_prio":           "优先级（MX / SRV）",
	"web.rec.l_cdn":            "CDN 代理",
	"web.rec.l_note":           "备注",
	"web.rec.need_name":        "请填写记录名",
	"web.rec.updated":          "记录已更新",
	"web.rec.created":          "记录已创建",
	"web.rec.deleted":          "记录已删除",
	"web.common.delete_failed": "删除失败：",

	// --- 通用 ---
	"web.common.loading": "加载中…",
}
