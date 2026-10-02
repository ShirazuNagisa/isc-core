package i18n

// messagesZh 是简体中文消息目录（默认语言）。
//
// 命名约定：<领域>.<具体项>，全部小写、下划线分词。
// 新增 key 时必须同时补齐 messagesEn，否则 i18n 完整性测试会失败。
var messagesZh = map[string]string{
	// --- 通用错误 ---
	"error.unauthorized":       "未授权：缺少或无效的访问令牌",
	"error.forbidden":          "禁止访问",
	"error.not_found":          "请求的资源不存在",
	"error.method_not_allowed": "不支持的请求方法",
	"error.invalid_request":    "请求参数不合法",
	"error.internal":           "内核内部错误",
	"error.not_implemented":    "该功能在当前平台尚未实现，已降级为引导模式",
	"error.timeout":            "操作超时",
	"error.conflict":           "当前状态下不允许该操作",

	// --- 任务 ---
	"error.job_not_found":      "任务不存在",
	"error.job_not_cancelable": "任务已结束，无法取消",
	"job.noop.running":         "空转任务执行中（第 %d/%d 步）",
	"job.noop.done":            "空转任务完成",
	"job.noop.failed":          "空转任务在第 %d 步按预期失败",
	"job.canceled":             "任务已取消",

	// --- 事件流 ---
	"error.websocket_upgrade": "无法升级为 WebSocket 连接：%s",
	"events.gap":              "请求的事件序号 %d 已超出保留范围（最早为 %d），请重新拉取全量状态",

	// --- 守护进程 ---
	"daemon.starting":             "ISC 内核启动中",
	"daemon.started":              "ISC 内核已启动",
	"daemon.stopping":             "ISC 内核正在关闭",
	"daemon.stopped":              "ISC 内核已停止",
	"daemon.already_running":      "检测到内核已在运行（PID %d），请勿重复启动",
	"daemon.stale_runtime_file":   "发现残留的运行时文件（PID %d 已不存在），已清理",
	"daemon.runtime_write_failed": "写入运行时文件失败：%s",
	"daemon.transport_failed":     "本地管理通道建立失败：%s",

	// --- 传输 ---
	"transport.named_pipe": "命名管道",
	"transport.unix_sock":  "Unix 域套接字",
	"transport.loopback":   "回环 TCP",
	"transport.desc":       "本地管理通道：%s（%s）",

	// --- 数据目录 ---
	"paths.windows":       "Windows",
	"paths.linux":         "Linux",
	"paths.darwin":        "macOS",
	"paths.not_supported": "当前平台（%s）不受支持，无法确定数据目录",

	// --- 平台能力 ---
	"platform.unsupported":      "当前平台的该后端尚未实现，将降级为引导模式",
	"platform.not_implemented":  "该平台后端尚未实现：%s",
	"platform.low_port_denied":  "缺少 CAP_NET_BIND_SERVICE，无法绑定 <1024 端口",
	"platform.low_port_granted": "可绑定低端口",

	// --- CLI ---
	"cli.daemon_not_running": "内核未运行。请先执行 'isc daemon run' 或安装为系统服务。",
	"cli.connecting":         "正在连接内核",
	"cli.connected":          "已连接内核",
	"cli.status_header":      "ISC 内核状态",
	"cli.version_header":     "ISC 版本信息",
	"cli.unknown_command":    "未知命令：%s",

	// --- 凭据字段（服务商注册表使用）---
	"provider.field.access_key_id":     "Access Key ID",
	"provider.field.access_key_secret": "Access Key Secret",
	"provider.field.secret_id":         "SecretId",
	"provider.field.secret_key":        "SecretKey",
	"provider.field.api_token":         "API 令牌",
	"provider.field.api_key":           "API Key",
	"provider.field.api_secret":        "API Secret",
	"provider.field.dnspod_id":         "DNSPod ID",
	"provider.field.dnspod_token":      "DNSPod Token",
	"provider.field.id":                "ID",
	"provider.field.secret":            "密钥",
	"provider.field.ext_param":         "扩展参数",

	"provider.help.access_key_id": "在云厂商控制台的访问控制页面创建，建议只授予 DNS 相关权限",
	"provider.help.api_token": "在 Cloudflare 控制台「我的个人资料 → API 令牌」创建，" +
		"建议使用「编辑区域 DNS」模板并限定到具体域名",
	"provider.help.dnspod_id":    "在 DNSPod 控制台「用户中心 → 安全设置 → API 密钥」查看",
	"provider.help.dnspod_token": "与 DNSPod ID 成对出现，创建后只显示一次，请务必保存",
	"provider.help.tier2_id":     "该项目前仅供配置导入使用，具体字段含义将在实现接入后明确",
	"provider.help.tier2_secret": "该项目前仅供配置导入使用，具体字段含义将在实现接入后明确",
	"provider.help.tier2_ext_param": "部分服务商需要的额外参数（例如 Vercel 的 teamId），" +
		"多数服务商留空即可",

	// --- 凭据 ---
	"credential.created":            "凭据已创建",
	"credential.updated":            "凭据已更新",
	"credential.deleted":            "凭据已删除",
	"credential.not_found":          "凭据不存在",
	"credential.duplicate":          "同一服务商下已存在同名凭据",
	"credential.in_use":             "该凭据仍被使用，无法删除",
	"credential.verify.running":     "正在校验凭据",
	"credential.verify.ok":          "凭据有效",
	"credential.verify.failed":      "凭据校验失败",
	"credential.verify.unsupported": "该服务商的实现尚未就绪，无法校验凭据",

	// --- 设置 ---
	"settings.updated": "设置已更新",

	// --- 动态解析任务 ---
	"ddns.task_not_found":    "任务不存在",
	"ddns.task_created":      "任务已创建",
	"ddns.task_updated":      "任务已更新",
	"ddns.task_deleted":      "任务已删除",
	"ddns.credential_in_use": "该凭据仍被 %d 个任务使用，无法删除",
	"ddns.triggered":         "已触发执行",
	"ddns.no_address":        "未能获取 %s 地址",
	"ddns.updated_count":     "已更新 %d 条记录",
	"ddns.unchanged":         "记录已是目标值，无需改动",
	"ddns.skipped":           "地址未变化，本次未与服务商比对",
	"ddns.detected_change":   "检测到地址变化，触发动态解析",

	// --- 导入导出 ---
	"config.export.empty":               "没有可导出的配置",
	"config.import.invalid":             "无法解析导入内容",
	"config.import.dry_run":             "预览模式：未写入任何改动",
	"config.import.applied":             "导入已完成",
	"config.import.ddnsgo.bad":          "这不是一份可识别的 ddns-go 配置",
	"config.import.ddnsgo.none":         "配置中没有找到任何 ddns-go 条目",
	"config.import.skipped":             "跳过第 %d 条：%s",
	"config.import.webhook_unsupported": "ddns-go 的 webhook 配置暂未迁移（通知中心将在 M4 接入）",

	// --- DNS 记录管理 ---
	"dns.unsupported":       "该服务商不支持此操作",
	"dns.record_not_found":  "DNS 记录不存在",
	"dns.upstream_error":    "服务商拒绝了这次操作",
	"dns.zone_not_found":    "DNS 区域不存在",
	"dns.invalid_record":    "记录内容不合法",
	"dns.too_many_requests": "服务商限流，请稍后再试",

	// --- 验证控制台 ---
	"console.title":            "ISC 验证控制台",
	"console.host_not_allowed": "请求的 Host 不是本机地址，已拒绝",
	"console.token_missing":    "未能取得访问令牌，请确认通过 127.0.0.1 打开控制台",

	// --- 可达性与系统变更 ---
	"reach.provider_not_found": "没有这种可达方式",
	"change.not_found":         "变更记录不存在",
	"change.interrupted_found": "发现 %d 条上次未走完的系统变更，请用 isc doctor 查看",

	// --- 外部验证 ---
	"verify.start_failed":      "无法开始外部验证",
	"verify.session_not_found": "验证会话不存在",

	// --- 系统变更 ---
	"change.plan_expired":    "计划不存在或已过期，请重新生成",
	"change.rollback_failed": "撤销失败",
	"reach.plan_failed":      "无法生成变更计划",

	// --- 反向代理 ---
	"proxy.invalid_routes": "转发规则不合法",

	// --- 证书 ---
	"cert.no_tls_routes": "还没有配置启用 HTTPS 的路由，无处可用证书",

	// --- 通知 ---
	"notify.cert_hint": "请用 isc cert list 查看详情；DNS-01 校验失败通常与凭据权限或域名归属有关。",

	// --- 通知 ---
	"notify.invalid_channels": "通知通道配置不合法",

	// --- 系统服务 ---
	"service.install_failed": "安装系统服务失败",
	"service.action_failed":  "系统服务操作失败",

	// --- 存储 ---
	"store.open_failed":    "打开数据库失败：%s",
	"store.migrate_failed": "数据库迁移失败：%s",
	"store.closed":         "数据库已关闭",
}
