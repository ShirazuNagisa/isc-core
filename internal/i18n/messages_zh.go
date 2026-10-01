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
}
