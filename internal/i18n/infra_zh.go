package i18n

// infraMessagesZh 覆盖 **DNS 服务层、任务引擎、运行时文件、配置导入导出、
// 服务商注册表** 五块。
//
// 它们的共同点是**都出现在"配置或装配出了问题"的时刻**：DNS 记录操作被拒、
// 任务引擎关闭、运行时文件写不进去、导入的配置版本太新、服务商没注册。
// 用户此时需要的是"哪一环没接上"，而不是一个笼统的失败。
var infraMessagesZh = map[string]string{
	// --- DNS 服务层 ---
	"dns.err.unsupported": "dns: 服务商 %s 不支持%s",
	"dns.err.not_found":   "dns: 记录不存在",
	// 这三个是**能力名**：它们会被拼进上面那条 "不支持%s" 里，
	// 因此必须与 dns 包里的能力标识一一对应。
	"dns.op.record_mgmt":  "记录管理",
	"dns.op.list_zones":   "列出区域",
	"dns.op.list_records": "列出记录",
	"dns.op.create":       "新增记录",
	"dns.op.update":       "修改记录",
	"dns.op.delete":       "删除记录",
	"dns.err.no_zone_id":  "dns: 缺少区域 ID",
	"dns.err.no_type":     "dns: 记录类型不能为空",
	"dns.err.no_name":     "dns: 记录名不能为空",
	"dns.err.no_verify":   "dns: 该服务商不支持凭据校验",

	// --- 任务引擎 ---
	"job.err.closed":             "job: 引擎已关闭，拒绝新任务",
	"job.err.persist":            "job: 持久化新任务: %w",
	"job.err.drain_timeout":      "job: 等待在途任务收尾超时: %w",
	"job.err.not_found":          "job: 任务不存在",
	"job.err.finished":           "job: 任务已结束，无法取消",
	"job.err.gen_id":             "job: 生成任务 ID 失败: %w",
	"job.err.unknown_kind":       "job: 未登记的任务类型 %q",
	"job.err.interrupted":        "内核在任务执行期间退出",
	"job.err.interrupted_detail": "任务没有跑完：内核在这次任务执行期间停止运行。请重新执行。",

	// --- 应用托管 ---
	"apps.error.docker_required": "这个部署方式需要 Docker Desktop，但本机没有检测到。请先安装并启动 Docker Desktop，或改用其他方式。",

	// --- 运行时供给（D28） ---
	"runtime.msg.downloading":  "正在下载 %s %s%s",
	"runtime.msg.extracting":   "正在解压 %s %s",
	"runtime.msg.ready":        "%s %s 已就绪",
	"runtime.msg.using_system": "使用系统已安装的 %s %s",

	// --- 运行时文件 ---
	"runtime.err.mkdir":    "runtimeinfo: 创建运行时目录: %w",
	"runtime.err.marshal":  "runtimeinfo: 序列化: %w",
	"runtime.err.tempfile": "runtimeinfo: 创建临时文件: %w",
	"runtime.err.chmod":    "runtimeinfo: 设置权限: %w",
	"runtime.err.write":    "runtimeinfo: 写入: %w",
	"runtime.err.sync":     "runtimeinfo: 落盘: %w",
	"runtime.err.close":    "runtimeinfo: 关闭临时文件: %w",
	"runtime.err.rename":   "runtimeinfo: 替换 %s: %w",
	"runtime.err.parse":    "runtimeinfo: 解析 %s: %w",

	// --- 配置导入导出 ---
	"configio.err.marshal":         "configio: 序列化导出内容失败: %w",
	"configio.err.too_new":         "%w: 文件格式版本为 %d，本内核最高支持 %d（请升级内核）",
	"configio.err.no_provider":     "缺少 provider 或 label",
	"configio.err.unknown_prov":    "未知的服务商 %q",
	"configio.err.unrecognised":    "configio: 无法识别的配置文档",
	"configio.ddnsgo.no_provider":  "该条目未指定服务商",
	"configio.ddnsgo.unknown_prov": "内核不认识服务商 %q",
	"configio.ddnsgo.migrated": "识别到 %d 条启用中的动态解析配置；" +
		"解析任务的迁移与调度将在 M2 接入后生效",
	"configio.ddnsgo.not_ddnsgo": "configio: 这不是一份 ddns-go 配置（缺少 dnsconf 段）",

	// --- 服务商注册表 ---
	"provider.name.alidns":       "阿里云 DNS",
	"provider.name.tencentcloud": "腾讯云 DNS",
	"provider.name.huaweicloud":  "华为云 DNS",
	"provider.name.ali_esa":      "阿里云 ESA",
	"provider.name.baiducloud":   "百度云 DNS",
	"provider.name.callback":     "Callback（自定义回调）",
	"provider.name.edgeone":      "腾讯 EdgeOne",
	"provider.name.rainyun":      "雨云",
	"provider.name.volcengine":   "火山引擎 TrafficRoute",
	"provider.err.dup":           "provider: 服务商 %q 被重复登记",
	"provider.err.no_impl":       "provider: %s 未注册实现",
	"provider.err.no_ip":         "provider: 未提供 IP",

	// --- 校验（Cloudflare 的独立实现）---
	"provider.cf.need_token":     "provider: Cloudflare 需要 API Token",
	"provider.cf.build_failed":   "provider: 构造校验请求失败: %w",
	"provider.cf.connect_failed": "provider: 连接 Cloudflare 失败: %w",
	"provider.cf.read_failed":    "provider: 读取 Cloudflare 响应失败: %w",
	"provider.cf.bad_body":       "provider: Cloudflare 返回了无法解析的响应（HTTP %d）",
	"provider.cf.rejected":       "provider: Cloudflare 拒绝了该凭据：%s",
	"provider.cf.bad_status":     "provider: Cloudflare 返回 HTTP %d",
	"provider.cf.not_active":     "provider: Cloudflare 令牌状态为 %q，不是 active",
	"provider.cf.no_detail":      "未提供错误详情",
}
