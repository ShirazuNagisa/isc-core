package i18n

// apiMessagesZh 是 HTTP 接口层的消息（错误说明与操作名）。
//
// # 为什么单独一个文件
//
// 这一层有近百条文案，全部塞进 messages_zh.go 会让那个文件难以浏览。
// 按**层**分文件而不是按语言分文件：找"接口层这句话怎么翻"时，
// 只需要打开一个文件。
//
// 新增 key 时同样要补 apiMessagesEn。
var apiMessagesZh = map[string]string{
	// --- 通用 ---
	"api.empty_body":       "请求体为空",
	"api.encode_failed":    "响应序列化失败",
	"api.internal_failed":  "%s失败",
	"api.provider_missing": "平台后端不可用",
	"api.platform_missing": "platform 未装配",

	// --- 认证与中间件 ---
	"api.token_empty":    "访问令牌为空，拒绝所有请求（这是配置错误）",
	"api.handler_panic":  "请求处理 panic",
	"api.http_request":   "http 请求",
	"api.console_index":  "读取控制台首页失败",
	"api.console_host":   "拒绝非回环 Host 的控制台引导请求",
	"api.console_rebind": "拒绝非本机 Host 的请求（可能是 DNS rebinding 尝试）",

	// --- 证书 ---
	"api.cert.list_failed":    "读取证书状态失败",
	"api.cert.issue_failed":   "证书签发失败",
	"api.cert.none_needed":    "全部证书均无需续期",
	"api.cert.issued":         "已签发 %d 张证书",
	"api.cert.issued_partial": "签发失败：",

	// --- 变更编排 ---
	"api.change.plan_failed":        "登记变更计划失败",
	"api.change.apply_failed":       "变更执行失败",
	"api.change.result_failed":      "读取变更结果失败",
	"api.change.rollback_failed":    "读取撤销结果失败",
	"api.change.query_failed":       "查询变更记录失败",
	"api.change.read_failed":        "读取变更记录失败",
	"api.change.interrupted_failed": "检查中断的变更失败",

	// --- 配置导入导出 ---
	"api.config.export_failed": "导出配置失败",
	"api.config.redacted":      "已脱敏",
	"api.config.plaintext":     "包含明文凭据",
	"api.config.import_long": "dry_run 默认为 true：先返回将会发生什么，确认后再以 dry_run=false " +
		"提交。凭据按 (服务商, 标签) 匹配：已存在则更新，否则新建。",
	"api.config.too_large":     "导入内容超过 1 MB 上限",
	"api.config.import_failed": "导入配置失败",
	"api.config.preview":       "预览",
	"api.config.applied":       "已应用",
	"api.config.cred_added":    "；凭据 +",
	"api.config.skipped":       " 跳过 ",

	// --- 凭据 ---
	"api.credential.list_failed": "查询凭据列表失败",
	"api.credential.in_use":      "该凭据仍被 %d 个任务使用",
	"api.credential.op_failed":   "凭据操作失败",

	// --- 凭据校验 ---
	"api.verify.no_service":  "记录管理服务不可用",
	"api.verify.no_dns":      "DNS 服务未装配",
	"api.verify.save_failed": "记录凭据校验结果失败",
	"api.verify.ok":          "连接正常",
	"api.verify.unsupported": "该服务商不支持凭据校验（它没有只读的校验端点）。" +
		"这不代表凭据有问题 —— 可以用「DNS 记录」面板列一次区域来确认",
	"api.verify.hint_auth": "\n\n这可能不是凭据填错了，而是**权限不足**：" +
		"该凭据需要目标区域的 DNS 编辑权限。" +
		"以 Cloudflare 为例，Token 至少要开 Zone:DNS:Edit；" +
		"注意 Zone 的资源范围也要包含目标域名。",
	"api.verify.hint_network": "\n\n这看起来是**网络问题**而不是凭据问题 —— " +
		"请确认这台机器能访问服务商的 API 地址" +
		"（部分服务商的接口在国内网络下可能不稳定）。",
	"api.verify.mark_auth":        "签名",
	"api.verify.mark_auth2":       "鉴权",
	"api.verify.mark_perm":        "权限",
	"api.verify.mark_timeout":     "超时",
	"api.verify.mark_unreachable": "连不上",

	// --- 任务 ---
	"api.job.list_failed":   "查询任务列表失败",
	"api.job.get_failed":    "查询任务失败",
	"api.job.cancel_failed": "取消任务失败",
	"api.job.noop_failed":   "提交空转任务失败",
	"api.job.noop_required": "fail_at_step 参数要求在第 N 步失败",

	// --- DNS 区域与记录 ---
	"api.dns.list_zones":   "列出区域",
	"api.dns.list_records": "列出记录",
	"api.dns.get_record":   "读取记录",
	"api.dns.create":       "新增记录",
	"api.dns.update":       "修改记录",
	"api.dns.delete":       "删除记录",
	"api.dns.op_failed":    "DNS 操作失败",

	// --- 动态解析 ---
	"api.ddns.snapshot_failed": "读取网卡快照失败",
	"api.ddns.list_failed":     "查询任务列表失败",
	"api.ddns.op_failed":       "任务操作失败",

	// --- 反代与可达性 ---
	"api.proxy.read_failed":  "读取代理路由失败",
	"api.reach.probe_failed": "可达性探测失败",

	// --- 通知 ---
	"api.notify.no_center":  "通知中心未初始化",
	"api.notify.test_title": "ISC 测试通知",
	"api.notify.test_body":  "如果你看到这条消息，说明这个通道配置正确。",

	// --- 事件流 ---
	"api.events.handshake": "事件流握手失败",
	"api.events.gap":       "推送 events.gap 失败",
	"api.events.heartbeat": "事件流心跳失败，关闭连接",
	"api.events.write":     "事件流写入失败，关闭连接",
	"api.events.closed":    "事件流订阅被断开",

	// --- 设置与审计 ---
	"api.settings.proxy_failed": "按设置启动反向代理失败",
	"api.audit.query_failed":    "查询审计失败",
	"api.verify_stop_failed":    "停止验证会话失败",

	// --- 系统服务 ---
	"api.service.installed":     "服务已安装",
	"api.service.uninstalled":   "服务已卸载",
	"api.service.started":       "服务已启动",
	"api.service.stopped":       "服务已停止",
	"api.service.no_self_path":  "无法确定当前可执行文件的路径：",
	"api.service.needs_admin":   "需要管理员权限",
	"api.service.needs_root":    "需要 root 权限",
	"api.service.access_denied": "拒绝访问",

	// --- 远程访问（ISC Mizar）---
	"api.remote_unwired":           "remote: 远程访问子系统未装配",
	"api.remote_settings_changed":  "远程访问已%s，监听端口 %d",
	"api.remote_pairing_started":   "已开始一次配对会话（角色 %s）",
	"api.remote_pairing_canceled":  "配对会话已取消",
	"api.remote_pairing_failed":    "配对失败",
	"api.remote_paired":            "已配对设备 %s（角色 %s）",
	"api.remote_derived":           "已从 %s 派生设备 %s（角色 %s）",
	"api.remote_revoked":           "已吊销 %d 台设备",
	"api.remote_devices_failed":    "设备操作失败",
	"api.remote_push_registered":   "已登记推送令牌（%s）",
	"api.remote_push_unregistered": "已注销推送令牌",

	"api.events_bus_missing":            "事件总线未装配",
	"api.events_subscribe_failed":       "订阅事件流失败",
	"api.remote_apns_set":               "已保存 APNs 凭据",
	"api.remote_apns_cleared":           "已删除 APNs 凭据",
	"api.remote_push_sent":              "已发送。若手机上没有出现，请检查系统设置里的通知权限。",
	"api.remote_push_failed":            "发送失败（HTTP %d）",
	"api.remote_push_test":              "测试推送：%s",
	"api.remote_public_synced":          "已同步公网子域名的 DNS 记录",
	"api.remote_public_removed":         "已删除公网子域名",
	"api.remote_public_teardown_failed": "删除公网子域名时出错（记录可能还留在你的 DNS 区域里）",
	"api.remote_public_bad_family":      "地址族只能是 ipv4 或 ipv6",
	"api.remote_public_same_lan":        "手机与内核在同一个局域网里，这次探测不作数",
	"api.remote_public_reachable":       "外部客户端连上了",
	"api.remote_public_unreachable":     "外部客户端连不上",
	"api.remote_public_check_failed":    "记录可达性自检结果失败",
	"api.preset.runtime_unavailable":    "这一版没有内置 %s 运行时，装了也跑不起来。",
}
