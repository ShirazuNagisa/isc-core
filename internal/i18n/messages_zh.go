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

	"cli.zones.short": "列出某个凭据可管理的 DNS 区域",
	"cli.zones.long": "列出某个凭据可管理的 DNS 区域。\n\n" +
		"用 isc credential list 拿到凭据 ID。\n\n" +
		"并非所有服务商都支持 —— Tier-2（只做动态解析的那 30 家）没有列区域的\n" +
		"能力。遇到时这条命令会明确说明，而不是给你一个空列表。",
	"cli.zones.empty": "该凭据下没有可管理的区域。",
	"cli.zones.empty_hint": "常见原因：凭据的权限范围不包含任何域名，" +
		"或该服务商不支持列出区域（Tier-2）。",
	"cli.zones.title": "区域（%d）",
	"cli.zones.next":  "下一步：isc records list %s <区域ID>",

	"cli.records.short": "管理 DNS 记录（仅 Tier-1 服务商）",
	"cli.records.long": "浏览与编辑 DNS 记录。\n\n" +
		"**仅 Tier-1 服务商可用**：Cloudflare / 阿里云 / 腾讯云 / DNSPod /\n" +
		"华为云 / GoDaddy。Tier-2（只做动态解析的那 30 家）没有记录管理能力。\n\n" +
		"注意各家的记录模型不同：华为云的一条记录属于一个「记录集」，\n" +
		"GoDaddy 的记录没有独立 ID —— 它们的删除会波及同名的其它值。\n" +
		"详见 docs/PROVIDER-MATRIX.md。",
	"cli.records.list_short":  "列出区域内的记录",
	"cli.records.list_empty":  "该区域下没有匹配的记录。",
	"cli.records.list_title":  "记录（%d）",
	"cli.records.col_type":    "类型",
	"cli.records.col_name":    "名称",
	"cli.records.col_content": "内容",
	"cli.records.ttl_default": "默认",
	"cli.records.filter_type": "只看某个类型（A / AAAA / CNAME / MX / TXT …）",
	"cli.records.filter_name": "只看某个名字",
	"cli.records.add_short":   "新增一条记录",
	"cli.records.add_long": "新增一条 DNS 记录。\n\n" +
		"记录名用**完整名字**（www.example.com），而不是相对名（www）——\n" +
		"各家对相对名的处理不一致，而完整名字在六家上含义相同。",
	"cli.records.need_type":    "必须用 --type 指定记录类型",
	"cli.records.need_content": "必须用 --content 指定记录内容",
	"cli.records.added":        "✅ 已新增 %s %s → %s",
	"cli.records.flag_type":    "记录类型（必填）：A / AAAA / CNAME / MX / TXT …",
	"cli.records.flag_content": "记录内容（必填）",
	"cli.records.flag_ttl":     "TTL 秒数（0 = 用服务商默认值）",
	"cli.records.rm_short":     "删除一条记录",
	"cli.records.rm_long": "删除一条 DNS 记录。\n\n" +
		"**注意部分服务商的语义差异**：GoDaddy 的记录没有独立 ID，删一条\n" +
		"同名记录会波及该名字下的**全部**同类型值。华为云的一条记录属于一个\n" +
		"「记录集」，删除的粒度与其它家不同。详见 docs/PROVIDER-MATRIX.md。",
	"cli.records.rm_confirm": "将删除记录 %s。确认请加 --yes。",
	"cli.records.removed":    "✅ 记录 %s 已删除",
	"cli.records.yes_flag":   "跳过确认",

	"cli.settings.short":     "查看与修改内核设置",
	"cli.settings.long":      "查看与修改内核设置。\n\n不带子命令时打印当前的全部设置。",
	"cli.settings.set_short": "修改设置",
	"cli.settings.set_long": "修改设置。**只提交你显式给出的字段**，其余保持不变。\n\n" +
		"例：\n" +
		"  isc settings set --acme-email you@example.com --acme-dns-credential-id <凭据ID>\n" +
		"  isc settings set --proxy-enabled --proxy-port 443 --proxy-tls\n\n" +
		"ACME 设置是 HTTPS 的前置条件：启用 proxy-tls 之前必须先指定\n" +
		"DNS-01 凭据，否则证书签不出来，而症状是「浏览器报证书错误」。",
	"cli.settings.conflict_proxy":  "--proxy-enabled 与 --proxy-disabled 不能同时给出",
	"cli.settings.conflict_tls":    "--proxy-tls 与 --no-proxy-tls 不能同时给出",
	"cli.settings.nothing":         "没有给出任何要修改的字段。用 isc settings 查看当前值。",
	"cli.settings.updated":         "✅ 设置已更新",
	"cli.settings.flag_lang":       "界面语言：zh-CN 或 en",
	"cli.settings.flag_log_level":  "日志级别：debug / info / warn / error",
	"cli.settings.flag_proxy_on":   "启用反向代理",
	"cli.settings.flag_proxy_off":  "停用反向代理",
	"cli.settings.flag_proxy_port": "反向代理监听端口",
	"cli.settings.flag_proxy_tls":  "反向代理使用 HTTPS",
	"cli.settings.flag_no_tls":     "反向代理改回明文 HTTP",
	"cli.settings.flag_acme_email": "ACME 账户邮箱（续期失败时 CA 用它提醒你）",
	"cli.settings.flag_acme_dir": "ACME 目录地址，留空用生产环境；" +
		"测试环境签的证书浏览器不信任",
	"cli.settings.flag_acme_cred": "做 DNS-01 校验用的凭据 ID",

	"cli.settings.render_title":   "内核设置",
	"cli.settings.render_lang":    "  界面语言      %s",
	"cli.settings.render_level":   "  日志级别      %s",
	"cli.settings.render_buffer":  "  事件缓冲      %d",
	"cli.settings.render_off":     "停用",
	"cli.settings.render_on":      "启用",
	"cli.settings.render_proxy":   "  反向代理      %s",
	"cli.settings.render_port":    "（端口 %d",
	"cli.settings.render_https":   "，HTTPS",
	"cli.settings.render_http":    "，明文 HTTP",
	"cli.settings.render_close":   "）",
	"cli.settings.render_acme":    "\n  ACME（HTTPS 的前置条件）",
	"cli.settings.render_unset":   "（未设置）",
	"cli.settings.render_email":   "    邮箱        %s",
	"cli.settings.render_prod":    "生产环境",
	"cli.settings.render_staging": "  ⚠ 测试环境签的证书浏览器不信任",
	"cli.settings.render_dir":     "    目录        %s",
	"cli.settings.render_cred":    "    DNS-01 凭据 %s",
	"cli.settings.render_no_cred": "    ⚠ 未设置凭据时无法签发证书，也就无法启用 HTTPS",

	"cli.root.short": "ISC —— 把没有公网 IPv4 的电脑接入公网",
	"cli.root.long": "ISC（接入编排器）让一台只有动态 IPv6 的普通电脑可以从公网访问。\n\n" +
		"它跟踪 IPv6 前缀变化、更新动态域名解析、编排防火墙、签发证书，\n" +
		"并通过反向代理把本地服务发布到一个可用的公网端口上。\n\n" +
		"本命令同时是 CLI 客户端与内核守护进程的入口：\n" +
		"  在终端里执行 isc status 是与运行中的内核通信；\n" +
		"  执行 isc daemon run 则是把当前进程变成内核本身。",

	// --- 存储 ---
	"store.open_failed":    "打开数据库失败：%s",
	"store.migrate_failed": "数据库迁移失败：%s",
	"store.closed":         "数据库已关闭",
	// --- 凭据管理（CLI） ---
	"cli.credential.short":      "管理 DNS 服务商凭据",
	"cli.credential.list_short": "列出已保存的凭据",
	"cli.credential.add_short":  "添加一个凭据",
	"cli.credential.rm_short":   "删除一个凭据",
	"cli.credential.long": "管理 DNS 服务商凭据。\n\n" +
		"凭据加密存储在主密钥保护的信封里，而主密钥在系统密钥库里\n" +
		"（Windows DPAPI / macOS 钥匙串 / Linux Secret Service）。\n" +
		"接口只返回敏感字段的**掩码值**，明文永远不会被发回来。\n\n" +
		"用 'isc credential fields <服务商>' 查看某家需要哪些字段。",
	"cli.credential.list_empty":       "还没有任何凭据。",
	"cli.credential.list_empty_hint":  "用 isc credential add <服务商> 添加一个；isc credential list 的子命令 fields 可以看到支持哪些服务商。",
	"cli.credential.list_title":       "凭据（%d）",
	"cli.credential.not_implemented":  "该服务商的实现尚未完成",
	"cli.credential.verify_failed_at": "上次校验未通过（%s）",

	"cli.credential.add_long": "添加一个 DNS 服务商凭据。\n\n" +
		"字段用 --field 传入，可以重复：\n\n" +
		"  isc credential add cloudflare --label 我的CF --field token=<API-TOKEN>\n" +
		"  isc credential add dnspod --label 主域名 \\\n" +
		"      --field id=<ID> --field secret=<TOKEN>\n\n" +
		"用 'isc credential fields <服务商>' 查看它需要哪些字段名。\n\n" +
		"**最小权限**：只需 DNS 记录的编辑权限。以 Cloudflare 为例，\n" +
		"Token 只开 Zone:DNS:Edit 即可 —— 内核不会碰其它任何设置。",
	"cli.credential.provider_empty": "服务商名不能为空",
	"cli.credential.label_required": "必须用 --label 给凭据起一个名字 —— " +
		"同一家服务商可以有多组凭据，而名字是界面上区分它们的方式",
	"cli.credential.added":       "✅ 凭据已添加（%s）",
	"cli.credential.added_hint":  "下一步：用这个 ID 创建动态解析任务，或在控制台里管理 DNS 记录。",
	"cli.credential.fields_hint": "用 'isc credential fields %s' 查看它需要哪些字段",

	"cli.credential.fields_short":   "查看某家服务商需要哪些凭据字段",
	"cli.credential.fields_long":    "查看某家服务商需要哪些凭据字段。\n\n不带参数时列出全部服务商。",
	"cli.credential.fields_unknown": "没有名为 %q 的服务商。不带参数运行可以看到全部",
	"cli.credential.no_fields":      "（无需凭据字段）",
	"cli.credential.field_required": "（必填）",
	"cli.credential.field_secret":   " [敏感]",
	"cli.credential.field_example":  "  例如 %s",
	"cli.credential.capabilities":   "    能力：%s",
	"cli.credential.cap_dynamic":    "动态解析",
	"cli.credential.cap_zones":      "列区域",
	"cli.credential.cap_create":     "新增记录",
	"cli.credential.cap_update":     "修改记录",
	"cli.credential.cap_delete":     "删除记录",
	"cli.credential.cap_dns01":      "DNS-01 证书",
	"cli.credential.tier":           "  [Tier-%d]",
	"cli.credential.unavailable":    "  ⚠ 尚未实现",

	"cli.credential.rm_confirm": "删除凭据 %s 会让引用它的动态解析任务全部失效。\n确认请加 --yes。",
	"cli.credential.removed":    "✅ 凭据 %s 已删除",

	"cli.credential.verify_short": "校验凭据是否可用",
	"cli.credential.verify_long": "校验凭据是否可用（\"测试连接\"）。\n\n" +
		"**不是所有服务商都支持**：阿里云 / 腾讯云 / 华为云 / GoDaddy 没有只读的\n" +
		"校验端点，用\"列一次域名\"来冒充会要求额外的权限，把只有 DNS 编辑权限的\n" +
		"最小权限账号误判为无效。\n\n" +
		"不支持时这条命令会明确说明，而不是给你一个假的\"失败\"。",
	"cli.credential.verify_ok":  "✅ 凭据可用",
	"cli.credential.verify_bad": "❌ 凭据不可用",

	"cli.credential.field_format": "--field 的格式是 name=value，收到 %q",
	"cli.credential.field_noname": "--field 缺少字段名：%q",
	"cli.credential.field_none":   "至少要用 --field 提供一个字段。用 'isc credential fields <服务商>' 查看需要哪些",
	"cli.credential.yes_flag":     "跳过确认",
	"cli.credential.label_flag":   "凭据的可读名称（必填）—— 同一家服务商可以有多组凭据",
	"cli.credential.field_flag":   "字段，形如 name=value，可重复",
}
