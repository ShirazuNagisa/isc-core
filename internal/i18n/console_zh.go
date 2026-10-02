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

	// --- 通用 ---
	"web.common.loading": "加载中…",
}
