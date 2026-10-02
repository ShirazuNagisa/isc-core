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

	"cli.error.with_detail": "%s（HTTP %d）：%s",
	"cli.error.title_only":  "%s（HTTP %d）",

	"cli.ip.short": "查看当前网卡地址与 IPv6 前缀",
	"cli.ip.long": "查看当前可用于解析的地址。\n\n" +
		"IPv6 前缀是本产品的核心概念：ISP 重拨后变化的是整个 /64 前缀，\n" +
		"该前缀下的所有 AAAA 记录都要重写，而不是只改一个地址。",
	"cli.ip.no_interface": "没有找到可用于解析的网卡。",

	"cli.ddns.short": "管理动态解析任务",
	"cli.ddns.long": "管理动态解析任务。\n\n" +
		"一条任务 = 一组凭据 + 一组域名 + 一组地址来源。调度器会在地址变化时\n" +
		"立刻执行，并按固定周期兜底重试。",
	"cli.ddns.list_short":    "列出全部动态解析任务",
	"cli.ddns.list_empty":    "还没有配置任何动态解析任务。",
	"cli.ddns.field_status":  "    状态      %s\n",
	"cli.ddns.field_message": "    说明      %s\n",
	"cli.ddns.run_use":       "run <任务ID>",
	"cli.ddns.run_short":     "立即执行一次任务（忽略防抖）",
	"cli.ddns.accepted":      "已受理（任务 %s）。执行结果请用 'isc ddns list' 查看。\n",
	"cli.ddns.state_on":      "[启用]",
	"cli.ddns.state_off":     "[停用]",
	"cli.ddns.never_run":     "从未执行",

	"cli.cert.short": "查看与管理 TLS 证书",
	"cli.cert.long": "查看 TLS 证书的状态，或手动触发一次续期。\n\n" +
		"证书由内核自动申请与续期：到期前 1/3 寿命时进入续期窗口\n" +
		"（对 90 天的证书即提前 30 天）。因此正常情况下不需要手动干预。\n\n" +
		"需要先配置 ACME：isc settings set --acme-email … --acme-dns-credential-id …",
	"cli.cert.list_short":  "列出证书与续期状态",
	"cli.cert.renew_short": "立即检查并为全部 HTTPS 路由申请（或续期）证书",
	"cli.cert.renew_long": "立即触发一次证书检查与签发。\n\n" +
		"它是**幂等**的：已经有效的证书不会被重新签发 —— 那会白白消耗\n" +
		"ACME 的失败配额（生产环境每小时 5 次）。",
	"cli.cert.renew_triggered": "已触发一次证书检查。",
	"cli.cert.list_empty":      "还没有任何证书。",
	"cli.cert.list_empty_hint": "为一条路由启用 HTTPS（isc proxy add ... --tls）之后，内核会自动申请证书。",
	"cli.cert.list_title":      "证书（%d）\n",
	"cli.cert.covers":          "      覆盖: %s\n",
	"cli.cert.valid_until":     "      有效期至: %s（还剩 %d 天）\n",
	"cli.cert.staging_warn":    "      ⚠ 这张证书来自 ACME **测试环境**，浏览器不会信任它。",
	"cli.cert.staging_hint":    "        要拿到正式证书，请把 acme_directory 清空后重新续期。",
	"cli.cert.needs_renewal":   "      需要续期: %s\n",
	"cli.cert.last_failure":    "      上次失败: %s\n",

	"cli.error.with_detail_short": "%s（%s）",

	"cli.ddns.add_short": "创建一条动态解析任务",
	"cli.ddns.add_long": "创建一条动态解析任务。\n\n" +
		"一条任务 = 一组凭据 + 一组域名 + 一组地址来源。\n\n" +
		"例（IPv6，从网卡读取）：\n\n" +
		"  isc ddns add --label 家里的IPv6 --credential <凭据ID> \\\n" +
		"      --domain home.example.com --type AAAA --source ipv6\n\n" +
		"例（IPv4，通过外部接口查询）：\n\n" +
		"  isc ddns add --label 家里的IPv4 --credential <凭据ID> \\\n" +
		"      --domain home.example.com --type A --source ipv4 \\\n" +
		"      --get-type url --value https://api.ipify.org\n\n" +
		"--source ipv6 时可以用 --selector 在多地址中挑一个：\n\n" +
		"  --selector \"@2\"        取第 2 个（从 1 开始）\n" +
		"  --selector \"^240e:.*\"  正则筛选，取第一个匹配的",
	"cli.ddns.need_label":      "必须用 --label 给任务起一个名字",
	"cli.ddns.need_credential": "必须用 --credential 指定凭据 ID（用 isc credential list 查看）",
	"cli.ddns.need_domain":     "至少要用 --domain 指定一个域名",
	"cli.ddns.bad_type":        "--type 只能是 A 或 AAAA，收到 %q",
	"cli.ddns.bad_get_type":    "--get-type 只能是 netInterface / url / cmd，收到 %q",
	"cli.ddns.need_value":      "--get-type %s 时必须用 --value 给出%s",
	"cli.ddns.value_url":       "接口地址",
	"cli.ddns.value_cmd":       "要执行的命令",
	"cli.ddns.created":         "✅ 任务已创建（%s）\n",
	"cli.ddns.created_hint":    "\n立即跑一次看看：isc ddns run %s",
	"cli.ddns.rm_use":          "rm <任务ID>",
	"cli.ddns.rm_short":        "删除一条动态解析任务",
	"cli.ddns.rm_long": "删除一条动态解析任务。\n\n" +
		"它只删除**任务**，不会动 DNS 里已有的记录 —— 记录会保持最后一次\n" +
		"解析出来的值。",
	"cli.ddns.rm_confirm": "将删除任务 %s。确认请加 --yes。\n" +
		"（DNS 里的记录会保持最后一次解析出来的值，不会被删掉。）\n",
	"cli.ddns.removed":  "✅ 任务 %s 已删除\n",
	"cli.ddns.yes_flag": "跳过确认",

	"cli.ddns.flag_label":      "任务名称（必填）—— 会出现在通知与日志里",
	"cli.ddns.flag_credential": "凭据 ID（必填）",
	"cli.ddns.flag_domain": "要更新的域名，可重复；" +
		"支持 www:example.com 显式指定根域名",
	"cli.ddns.flag_type":     "记录类型：A 或 AAAA",
	"cli.ddns.flag_source":   "地址来源：ipv6 或 ipv4（用于默认的取值方式）",
	"cli.ddns.flag_get_type": "取值方式：netInterface（从网卡读，推荐）/ url / cmd",
	"cli.ddns.flag_selector": "仅 IPv6：地址选择器，如 @2 或 ^240e:.*",
	"cli.ddns.flag_ttl":      "记录 TTL 秒数；留空用服务商默认值",
	"cli.ddns.flag_disabled": "创建后先停用",

	"cli.notify.short": "查看与测试通知通道",
	"cli.notify.long": "查看通知通道与最近的投递结果，或发一条测试通知。\n\n" +
		"配置通道用接口（PUT /v1/notify/channels）—— 通道的字段较多\n" +
		"（地址、请求头、请求体模板），命令行不适合编辑它们。\n" +
		"Web 控制台里有完整的表单。",
	"cli.notify.list_short":       "列出通知通道",
	"cli.notify.list_empty":       "还没有配置任何通知通道。",
	"cli.notify.log_always":       "（日志通道始终可用，通知会出现在 isc daemon 的日志里。）",
	"cli.notify.enabled":          "启用",
	"cli.notify.disabled":         "停用",
	"cli.notify.min_severity":     "    仅在 %s 及以上时发送\n",
	"cli.notify.custom_body":      "    （使用自定义请求体模板）",
	"cli.notify.deliveries_short": "列出最近的通知投递结果",
	"cli.notify.test_short":       "向全部通道发送一条测试通知",
	"cli.notify.test_long": "立刻向全部通道发一条测试消息。\n\n" +
		"它**绕过去重与队列**：你点了之后应当立刻看到结果，\n" +
		"而不是等下一个投递循环。",
	"cli.notify.sent":          "测试通知已发送：",
	"cli.notify.no_deliveries": "还没有任何投递记录。",

	"cli.console.short": "显示（或打开）验证控制台地址",
	"cli.console.long": "显示验证控制台的本机地址。\n\n" +
		"控制台监听回环 TCP，端口由内核启动时随机分配，因此每次启动都不同。\n" +
		"用本命令取得当前地址，或加 --open 直接用浏览器打开。\n\n" +
		"注意：浏览器访问控制台必须使用 127.0.0.1（或 localhost）。\n" +
		"内核会拒绝 Host 头不是本机地址的请求 —— 那是为了防 DNS rebinding，\n" +
		"用局域网 IP 或自定义主机名都会得到 403。",
	"cli.console.not_running": "%w（提示：先运行 'isc daemon run' 启动内核）",
	"cli.console.no_tcp":      "内核没有提供回环 TCP 通道，浏览器无法访问控制台",
	"cli.console.not_tcp":     "备用通道不是 TCP（%s），浏览器无法访问控制台",
	"cli.console.open_hint": "\n提示：加 --open 可直接用浏览器打开。\n" +
		"      控制台必须通过 127.0.0.1 访问 —— 用其它主机名会被内核\n" +
		"      以 403 拒绝（DNS rebinding 防护）。\n",
	"cli.console.open_flag":   "用默认浏览器打开控制台",
	"cli.console.open_failed": "无法打开浏览器：%w",

	"cli.service.short": "把内核注册为系统服务（开机自启、崩溃重启）",
	"cli.service.long": "把内核注册为系统服务。\n\n注册之后内核会：\n" +
		"  · 开机自动启动（Windows 使用延迟自启，等网络就绪后再启动）\n" +
		"  · 崩溃后自动重启（5 秒 / 30 秒 / 60 秒三档递增延迟）\n\n" +
		"**安装与卸载需要管理员权限**：\n" +
		"  Windows  右键终端 → 以管理员身份运行\n" +
		"  Linux    用 sudo\n" +
		"  macOS    用 sudo\n\n" +
		"没有管理员权限时它不会失败得很含糊，而是明确告诉你缺什么。\n\n" +
		"各平台的服务机制不同：\n" +
		"  Windows  服务控制管理器（SCM），可在 services.msc 里看到\n" +
		"  Linux    systemd（/etc/systemd/system/isc-core.service）\n" +
		"  macOS    launchd（/Library/LaunchDaemons/com.isc.core.plist）",
	"cli.service.install_short": "安装系统服务",
	"cli.service.install_long": "安装并注册系统服务。\n\n" +
		"已经安装过时会**更新配置**而不是报错 —— 重新运行安装命令是常态\n" +
		"（换了路径、想改自启设置），而报「服务已存在」只会让你去手工卸载。",
	"cli.service.installed":       "✅ 服务已安装（%s）\n",
	"cli.service.exe_path":        "   可执行文件: %s\n",
	"cli.service.data_dir":        "   数据目录:   %s\n",
	"cli.service.autostart_yes":   "   开机自启:   是（延迟自启，等网络就绪）",
	"cli.service.autostart_no":    "   开机自启:   否（手动启动）",
	"cli.service.restart_yes":     "   崩溃重启:   是（5s / 30s / 60s 递增延迟）",
	"cli.service.next_steps":      "\n用 isc service start 启动它，或用 isc service status 查看状态。",
	"cli.service.flag_autostart":  "开机自动启动（Windows 上使用延迟自启，等网络就绪后再启动）",
	"cli.service.flag_no_restart": "不在崩溃后自动重启",
	"cli.service.flag_exe":        "要注册的可执行文件路径（默认用当前运行的这一个）",
	"cli.service.uninstall_short": "停止并删除系统服务",
	"cli.service.uninstall_long": "停止并删除系统服务。\n\n" +
		"它是**幂等**的：服务本来就不存在时返回成功。报错会让「先卸再装」\n" +
		"这类部署脚本失败，而那是最常见的写法。\n\n" +
		"数据目录**不会**被删除 —— 里面有你的凭据与配置。",
	"cli.service.uninstall_busy": "暂不支持「只删服务、不停进程」；请先 isc service stop",
	"cli.service.uninstalled":    "✅ 服务已卸载",
	"cli.service.data_kept": "   数据目录仍然保留：%s\n" +
		"   如需彻底清除，请手工删除它。",
	"cli.service.keep_running": "只删除服务注册，不停掉正在运行的进程（当前平台可能不支持）",
	"cli.service.status_short": "查看系统服务状态",
	"cli.service.status_long": "查看系统服务状态。\n\n它同时报出两件事：\n\n" +
		"\t操作系统里的服务   是否已安装、是否在运行（**需要管理员权限**）\n" +
		"\t内核本身           现在是否真的能连通（不需要任何权限）\n\n" +
		"两者分开报是有原因的。真机上确认过：Windows 上**查询**服务状态同样\n" +
		"需要管理员（打开服务控制管理器要完全访问权），因此普通用户跑这条\n" +
		"命令会失败。但他真正想知道的往往是「内核在跑吗」—— 而那个问题看\n" +
		"一眼运行时文件就能回答。",
	"cli.service.kernel_running": "▶  内核：运行中（本地接口可连通）",
	"cli.service.kernel_stopped": "⏹  内核：未运行",
	"cli.service.query_failed":   "   系统服务：无法查询（%s）\n",
	"cli.service.state_stopped":  "未运行",
	"cli.service.state_running":  "运行中",
	"cli.service.state_line":     "%s 系统服务：%s（%s）\n",
	"cli.service.start_short":    "启动系统服务",
	"cli.service.start_long": "启动系统服务。\n\n" +
		"服务**已经在运行**时返回成功 —— 报错会让「确保它在跑」这类脚本失败，\n" +
		"而那正是最常见的用法。",
	"cli.service.started":      "✅ 服务已启动",
	"cli.service.stop_short":   "停止系统服务",
	"cli.service.stopped":      "✅ 服务已停止",
	"cli.service.no_self_path": "无法确定当前可执行文件的路径：%w。请用 --exe 显式指定",
	"cli.service.not_abs":      "无法解析为绝对路径 %q：%w",
	"cli.service.not_found":    "可执行文件不存在或无法访问：%s：%w",
	"cli.service.is_dir":       "可执行文件路径指向一个目录：%s",

	"cli.daemon.short": "管理内核守护进程",
	"cli.daemon.long": "管理内核守护进程。\n\n" +
		"内核是一个无 GUI 的常驻进程，CLI、验证控制台与下游 GUI 都通过\n" +
		"本地接口与它通信。它必须常驻才能保证动态解析的定时任务可靠执行。",
	"cli.daemon.run_short": "在前台运行内核",
	"cli.daemon.run_long": "在前台运行内核，直到收到中断信号。\n\n" +
		"生产环境应当使用 isc service install 安装为系统服务，\n" +
		"这样内核能在无人登录时运行、开机自启，并以足够的权限\n" +
		"修改防火墙与绑定低端口。",
	"cli.daemon.flag_listen":   "回环监听地址（默认 127.0.0.1:0，即自动分配端口）",
	"cli.daemon.flag_no_tcp":   "关闭回环监听（注意：浏览器控制台将无法连接）",
	"cli.daemon.flag_origins":  "允许的 WebSocket Origin 模式（仅用于本地开发调试控制台）",
	"cli.daemon.stub_prefix":   "将在 %s 实现（见 docs/PLAN.md）",
	"cli.daemon.install_short": "安装为系统服务（需要管理员权限）",
	"cli.daemon.install_long": "把内核安装为系统服务。\n\n" +
		"为什么必须是系统服务（见 docs/DECISIONS.md D20）：\n" +
		"服务能在无人登录时运行、开机自启，并以足够权限修改防火墙、\n" +
		"绑定低端口 —— 用户级进程在 Windows 上每次改防火墙都要弹 UAC。",
	"cli.daemon.backend_install":   "服务安装",
	"cli.daemon.uninstall_short":   "卸载系统服务（需要管理员权限）",
	"cli.daemon.backend_uninstall": "服务卸载",

	"cli.verify.short": "外部验证：用手机确认能不能从公网访问",
	"cli.verify.long": "开始一次外部验证，确认服务能不能从公网访问。\n\n流程：\n" +
		"  1. 内核在本机临时监听一个端口（默认随机分配）；\n" +
		"  2. 命令给出一个带随机路径的地址；\n" +
		"  3. 用手机**关闭 Wi-Fi、走 4G/5G** 打开那个地址；\n" +
		"  4. 内核根据**来源地址**判断这次访问算不算数。\n\n" +
		"判断分三档，其中第二档是最容易被误解的：\n\n" +
		"  公网地址    → 链路确实通\n" +
		"  本机地址    → **不能作为凭据**（可能是 NAT 回环，也可能你忘了关 Wi-Fi）\n" +
		"  没有访问    → 上游挡住了，而这一项本机无法自测",
	"cli.verify.flag_port":       "监听的端口（0 = 由内核分配空闲端口）",
	"cli.verify.flag_wait":       "等待外部访问的时长（0 = 不等待，只打印地址）",
	"cli.verify.no_address_hint": "  请先运行 isc doctor 确认本机的 IPv6 状态。",
	"cli.verify.started":         "外部验证已开始",
	"cli.verify.no_address": "⚠ 没有找到可用的公网地址，无法生成验证链接。\n" +
		"  请先运行 isc doctor 确认本机的 IPv6 状态。",
	"cli.verify.open_hint":        "\n请用手机（关闭 Wi-Fi，走 4G/5G）打开：",
	"cli.verify.listen_port":      "监听端口：%d\n",
	"cli.verify.allow_first":      "\n在内核里先放行这个端口：",
	"cli.verify.timeout":          "\n等待超时。",
	"cli.verify.reachable":        "✅ 外部访问成功 —— 链路是通的。",
	"cli.verify.next_step":        "   现在可以放行你真正要用的服务端口了。",
	"cli.verify.hairpin":          "⚠️  这次访问**不能作为凭据**。",
	"cli.verify.hairpin_hint":     "\n   请确认手机已关闭 Wi-Fi、走的是移动数据。",
	"cli.verify.unreachable":      "❌ 在有效期内没有收到任何外部访问。",
	"cli.verify.unreachable_hint": "\n   若 isc doctor 的本机检测全部通过，那么问题不在本机：",
	"cli.verify.cause_router":     "     · 路由器防火墙没有放行该端口（检查路由器的 IPv6 防火墙设置）",
	"cli.verify.cause_isp":        "     · 运营商封禁了入站连接（部分省份确实如此）",
	"cli.verify.hits":             "\n收到的访问：",

	"cli.doctor.short": "自检：到底哪一环断了",
	"cli.doctor.long": "逐层检查「服务能否从公网访问」这条链路。\n\n" +
		"检查按从下到上的顺序进行：\n\n" +
		"  1. 本机是否有全局 IPv6 地址\n" +
		"  2. 是否有 IPv6 委派前缀\n" +
		"  3. 本机防火墙后端是否可用\n" +
		"  4. 上游可达性（本机无法自测，必须用手机流量验证）\n\n" +
		"失败项会附带「该怎么办」。按顺序逐项处理即可 —— 一次修一个，\n" +
		"比同时动五个设置更容易定位问题。\n\n" +
		"注意：命令**不会**产生任何系统变更，只读取现状。",
	"cli.doctor.none":          "没有可用的可达方式。",
	"cli.doctor.unknown":       "没有找到名为 %q 的可达方式",
	"cli.doctor.read_failed":   "（提示：读取未完成的系统变更失败：%v）`n",
	"cli.doctor.flag_provider": "只检查指定的可达方式",
	"cli.doctor.title":         "ISC 自检",
	"cli.doctor.pending":       "\n⚠ 发现 %d 条上次未走完的系统变更：\n",
	"cli.doctor.pending_state": "      状态：%s\n",
	"cli.doctor.pending_hint": "    这些变更可能只生效了一部分。请确认它们是否符合预期，\n" +
		"    必要时用 'isc changes' 查看详情后手动撤销。\n",
	"cli.doctor.needs_server":     "\n  （需要一台外部服务器）\n",
	"cli.doctor.local_section":    "\n  本机检测\n",
	"cli.doctor.upstream_section": "\n  外部验证（本机无法自测）\n",
	"cli.doctor.conclusion_blocked": "结论：本机配置没有问题，但**上游挡住了**。\n" +
		"      这不是你能在本机修复的 —— 请换用其它可达方式，\n" +
		"      或联系运营商确认是否封禁了入站连接。\n",
	"cli.doctor.conclusion_ok": "结论：本机已具备条件。但**本机自测通过不等于外网能连上** ——\n" +
		"      请务必用手机 4G/5G 打开验证地址确认。\n",
	"cli.doctor.conclusion":            "结论：%s\n",
	"cli.doctor.conclusion_next":       "      先处理这一项：%s\n",
	"cli.doctor.state_running":         "执行中（内核可能异常退出）",
	"cli.doctor.state_applied":         "已生效",
	"cli.doctor.state_failed":          "失败（已自动回滚）",
	"cli.doctor.state_rolledback":      "已撤销",
	"cli.doctor.state_rollback_failed": "回滚失败（系统处于中间状态）",

	"cli.proxy.short": "管理反向代理",
	"cli.proxy.long": "管理反向代理的转发规则。\n\n规则把域名映射到本机服务，例如：\n\n" +
		"    home.example.com   →  127.0.0.1:8096\n" +
		"    *.lab.example.com  →  127.0.0.1:3000\n\n" +
		"上游**必须是本机或内网地址**。反代监听在公网上，若允许任意上游，\n" +
		"任何人都能拿它当跳板 —— 而所有流量都记在你头上。\n\n" +
		"注意通配规则：*.example.com 也会匹配 example.com 本身。",
	"cli.proxy.status_short": "查看反向代理状态",
	"cli.proxy.running":      "运行中：监听端口 %d，%d 条规则\n",
	"cli.proxy.stopped":      "未运行。",
	"cli.proxy.stopped_hint": "在设置中开启「反向代理」并指定监听端口即可启动。",
	"cli.proxy.last_error":   "\n最近一次失败：%s\n",
	"cli.proxy.routes_short": "列出全部转发规则",
	"cli.proxy.add_use":      "add <域名> [域名...] --to <上游>",
	"cli.proxy.add_short":    "添加一条转发规则",
	"cli.proxy.add_long": "把一组域名指向一个本机服务。\n\n" +
		"上游要写全 host:port，例如 127.0.0.1:8096。\n" +
		"不写端口会被拒绝 —— 猜端口会转发到意想不到的服务上，\n" +
		"而那种问题很难被发现。",
	"cli.proxy.updated":         "已更新：%s → %s\n",
	"cli.proxy.added":           "已添加：%s → %s\n",
	"cli.proxy.flag_to":         "上游地址，例如 127.0.0.1:8096（必填）",
	"cli.proxy.flag_label":      "规则的可读名称",
	"cli.proxy.flag_tls":        "该域名使用 HTTPS",
	"cli.proxy.rm_use":          "rm <域名或规则ID>",
	"cli.proxy.rm_short":        "删除一条转发规则",
	"cli.proxy.rm_notfound":     "没有找到匹配 %q 的转发规则",
	"cli.proxy.removed":         "已删除 %d 条规则。\n",
	"cli.proxy.list_empty":      "还没有配置任何转发规则。",
	"cli.proxy.list_empty_hint": "用 'isc proxy add home.example.com --to 127.0.0.1:8096' 添加一条。",
	"cli.proxy.list_title":      "转发规则（%d）\n",

	"cli.doctor.pending_hint_a":       "    这些变更可能只生效了一部分。请确认它们是否符合预期，\n",
	"cli.doctor.pending_hint_b":       "    必要时用 'isc changes' 查看详情后手动撤销。\n",
	"cli.doctor.conclusion_blocked_a": "结论：本机配置没有问题，但**上游挡住了**。\n",
	"cli.doctor.conclusion_blocked_b": "      这不是你能在本机修复的 —— 请换用其它可达方式，\n",
	"cli.doctor.conclusion_blocked_c": "      或联系运营商确认是否封禁了入站连接。\n",
	"cli.doctor.conclusion_ok_a":      "结论：本机已具备条件。但**本机自测通过不等于外网能连上** ——\n",
	"cli.doctor.conclusion_ok_b":      "      请务必用手机 4G/5G 打开验证地址确认。\n",

	"cli.expose.short": "在防火墙中放行一个端口（先生成计划、可预览、可撤销）",
	"cli.expose.long": "生成一份「放行端口」的变更计划，确认后应用。\n\n" +
		"命令会先把要做的改动打印出来再等你确认。放行端口是修改系统状态，\n" +
		"你会看到：\n\n" +
		"  · 当前已有的 ISC 规则（不会动它们）\n" +
		"  · 本次要新增的规则\n" +
		"  · 风险等级与注意事项\n\n" +
		"应用后可以用 'isc changes' 查看历史、用 'isc rollback <计划ID>' 撤销。\n\n" +
		"注意：\n" +
		"  · 需要管理员权限（Windows 上非管理员无法创建防火墙规则）；\n" +
		"  · 放行的是**本机**防火墙 —— 路由器那一层要另行设置。",
	"cli.expose.no_change":         "\n无需改动 —— 规则已经存在。",
	"cli.expose.confirm":           "确认应用这份变更？",
	"cli.expose.cancelled":         "已取消。",
	"cli.expose.flag_port":         "要放行的端口（必填）",
	"cli.expose.flag_protocol":     "协议：tcp 或 udp",
	"cli.expose.flag_label":        "服务名，会出现在系统防火墙界面里",
	"cli.expose.flag_provider":     "可达方式",
	"cli.expose.flag_yes":          "跳过确认直接应用",
	"cli.expose.plan_title":        "变更计划",
	"cli.expose.plan_risk":         "风险：%s\n",
	"cli.expose.plan_id":           "计划 ID：%s\n",
	"cli.expose.plan_diff":         "\n将要发生的改动：",
	"cli.expose.plan_irreversible": "  ⚠ 这一步不可撤销\n",
	"cli.expose.plan_notes":        "\n注意：",
	"cli.expose.plan_details":      "\n说明：",
	"cli.expose.applied":           "✅ 变更已生效。",
	"cli.expose.applied_undo":      "   撤销：isc rollback %s\n",
	"cli.expose.failed":            "❌ 变更失败，已自动回滚到执行前的状态。",
	"cli.expose.rollback_failed":   "‼️  变更失败，**且自动回滚未能完成**。",
	"cli.expose.rollback_hint_a":   "   系统可能处于中间状态，请用 'isc doctor' 检查，\n",
	"cli.expose.rollback_hint_b":   "   必要时在系统防火墙中手动清理以 isc- 开头的规则。",
	"cli.expose.state":             "状态：%s\n",
	"cli.expose.failed_step":       "   失败步骤：%s\n",
	"cli.expose.failed_reason":     "     原因：%s\n",

	"cli.changes.short":        "查看系统变更历史与待确认的计划",
	"cli.changes.flag_limit":   "历史记录条数",
	"cli.changes.pending":      "待确认的计划（%d）\n",
	"cli.changes.pending_line": "      风险 %s，%s 过期\n",
	"cli.changes.pending_hint": "\n  这些计划还没有生效。应用：isc expose --port ... 或经接口确认。",
	"cli.changes.empty":        "还没有任何系统变更记录。",
	"cli.changes.title":        "变更历史",
	"cli.changes.plan_id_line": "      计划 ID %s",
	"cli.changes.undo":         "   撤销：isc rollback %s",

	"cli.rollback.use":   "rollback <计划ID>",
	"cli.rollback.short": "撤销一次已生效的系统变更",
	"cli.rollback.long": "撤销一次此前应用过的变更。\n\n" +
		"它跨越内核重启依然可用 —— 撤销所需的信息随变更一起持久化了。\n" +
		"用 'isc changes' 可以查到计划 ID。\n\n" +
		"撤销是幂等的：已经撤销过的再撤一次会成功返回。",
	"cli.rollback.done":     "✅ 已撤销：%s\n",
	"cli.rollback.state":    "状态：%s\n",
	"cli.rollback.no_stdin": "\n（无法读取输入，已取消；自动化场景请加 --yes）",
	"cli.rollback.yes":      "是",
	"cli.risk.high":         "高 —— 可能影响你与这台机器的连接",
	"cli.risk.medium":       "中 —— 会开放一个端口",
	"cli.risk.low":          "低",

	"cli.init.short": "首次使用引导：探测环境并给出下一步命令",
	"cli.init.long": "首次使用引导。\n\n" +
		"它会检查这台机器能不能对外提供服务（公网 IPv6、委派前缀、防火墙权限、\n" +
		"低端口绑定能力），然后打印一份**填好了具体值**的下一步清单。\n\n" +
		"它不修改任何系统状态 —— 只会告诉你该做什么。真正的变更都在后续的\n" +
		"命令里，而那些变更都可以撤销（见 isc changes / isc rollback）。",
	"cli.init.flag_port":   "打算对外开放的端口（用于检查能否绑定与是否有服务在监听）",
	"cli.init.flag_domain": "打算使用的域名（用于生成可直接复制的命令）",
	"cli.init.title":       "ISC 首次使用引导",
	"cli.init.env_section": "【环境】",
	"cli.init.ipv6_ok":     "  ✅ 公网 IPv6：%s\n",
	"cli.init.iface":       "     网卡：%s\n",
	"cli.init.ipv6_missing": "  ❌ 没有找到公网 IPv6 地址\n" +
		"     这是本产品最核心的前提。请先确认：\n" +
		"       · 路由器里开启了 IPv6，并且**开启了前缀委派（DHCPv6-PD）**\n" +
		"       · 光猫是桥接模式（路由模式下常常拿不到委派前缀）\n" +
		"       · 系统里 IPv6 没有被禁用\n" +
		"     用 isc ip 可以看到每块网卡的详细情况。",
	"cli.init.prefix":      "  ℹ️  委派前缀：%s\n",
	"cli.init.prefix_hint": "     运营商重拨后这个前缀会变，ISC 会自动跟进并更新 DNS。",
	"cli.init.ipv4_public": "  ℹ️  检测到公网 IPv4（本产品的重点在 IPv6，但 A 记录同样可用）",
	"cli.init.ipv4_cgnat":  "  ℹ️  没有公网 IPv4 —— 这在国内家宽上是常态，不影响使用",
	"cli.init.fw_ok":       "  ✅ 防火墙后端可用（放行端口时需要管理员权限）",
	"cli.init.fw_bad": "  ⚠️  防火墙后端不可用：%s\n" +
		"     你仍然可以用它，但需要手工在系统防火墙里放行端口。",
	"cli.init.lowport_ok": "  ✅ 可以绑定低端口（443 等）—— %s\n",
	"cli.init.lowport_bad": "  ⚠️  不能绑定低端口（443 等）：%s\n" +
		"     用 443 需要 root / setcap，或者改用 ≥1024 的端口。",
	"cli.init.kernel_section":  "【内核】",
	"cli.init.kernel_running":  "  ✅ 正在运行",
	"cli.init.kernel_stopped":  "  ⏹  未运行",
	"cli.init.start_now":       "     现在跑起来：isc daemon run",
	"cli.init.install_service": "     装成系统服务（推荐，需管理员）：isc service install && isc service start",
	"cli.init.steps_section":   "【下一步】",
	"cli.init.footer_console":  "     控制台（图形界面）用 isc console 打开。",

	"cli.init.step_run_desc":  "把内核跑起来",
	"cli.init.step_run_note":  "（前台运行，Ctrl-C 停止。长期使用请用 isc service install）",
	"cli.init.step_cred_desc": "添加 DNS 服务商凭据",
	"cli.init.step_cred_cmd":  "isc credential add cloudflare --label 我的CF --field token=<API-TOKEN>",
	"cli.init.step_cred_note": "Token 只开 Zone:DNS:Edit 权限即可 —— 内核只需要改 DNS 记录。" +
		"用 isc credential fields cloudflare 可以看到需要哪些字段。",
	"cli.init.step_ddns_desc": "创建动态解析任务",
	"cli.init.step_ddns_cmd": "isc ddns add --label 我的域名 --credential <凭据ID> " +
		"--domain %s --type %s --source %s",
	"cli.init.step_ddns_note":   "凭据 ID 从上一步的输出里取（或 isc credential list）。",
	"cli.init.step_cred_note2":  "（把 cloudflare 换成你实际用的服务商）",
	"cli.init.step_ddns_note2":  "加 --domain 参数重跑 isc init 可以得到填好域名的版本。",
	"cli.init.step_expose_desc": "在防火墙中放行端口（会先生成计划供你确认）",
	"cli.init.step_expose_cmd":  "isc expose --port %d --label 我的服务",
	"cli.init.step_expose_note": "这一步需要管理员权限；应用之后可以用 isc rollback 撤销。",
	"cli.init.step_verify_desc": "用手机确认能从公网访问",
	"cli.init.step_verify_note": "**这一步不能省。** 在电脑上访问自己的域名成功\n" +
		"不能说明什么 —— 路由器可能在做 NAT 回环，\n" +
		"只有来自公网的请求才证明得了。",

	"cli.init.ipv6_missing_a": "  ❌ 没有找到公网 IPv6 地址",
	"cli.init.ipv6_missing_b": "     这是本产品最核心的前提。请先确认：",
	"cli.init.ipv6_missing_c": "       · 路由器里开启了 IPv6，并且**开启了前缀委派（DHCPv6-PD）**",
	"cli.init.ipv6_missing_d": "       · 光猫是桥接模式（路由模式下常常拿不到委派前缀）",
	"cli.init.ipv6_missing_e": "       · 系统里 IPv6 没有被禁用",
	"cli.init.fw_bad_a":       "  ⚠️  防火墙后端不可用：%s",
	"cli.init.fw_bad_b":       "     你仍然可以用它，但需要手工在系统防火墙里放行端口。",
	"cli.init.lowport_bad_a":  "  ⚠️  不能绑定低端口（443 等）：%s",
	"cli.init.lowport_bad_b":  "     用 443 需要 root / setcap，或者改用 ≥1024 的端口。",
	"cli.init.footer_undo":    "提示：任何系统变更都可以撤销。用 isc changes 看历史，isc rollback <ID> 撤销。",

	"cli.init.step_verify_note_a": "**这一步不能省。** 在电脑上访问自己的域名成功\n",
	"cli.init.step_verify_note_b": "不能说明什么 —— 路由器可能在做 NAT 回环，\n",
	"cli.init.step_verify_note_c": "只有来自公网的请求才证明得了。",
	"cli.init.step_ddns_cmd2":     "isc ddns add --label 我的域名 --credential <凭据ID> --domain home.example.com --type AAAA --source ipv6",

	"cli.init.ipv6_missing_f":   "     用 isc ip 可以看到每块网卡的详细情况。",
	"cli.init.step_cred_note_a": "Token 只开 Zone:DNS:Edit 权限即可 —— 内核只需要改 DNS 记录。",
	"cli.init.step_cred_note_b": "用 isc credential fields cloudflare 可以看到需要哪些字段。",

	"cli.err.not_running":  "cli: 内核未运行",
	"cli.err.unreachable":  "cli: 内核不可达",
	"cli.err.runtime_read": "cli: 读取运行时文件失败: %w",
	"cli.err.no_endpoint":  "运行时文件中没有可用的通道",
	"cli.err.http_status":  "cli: 内核返回 HTTP %d",
	"cli.flag.json":        "以 JSON 输出，便于脚本消费",
	"cli.flag.lang":        "输出语言：zh-CN 或 en",
	"cli.flag.verbose":     "输出调试日志",
	"cli.flag.data_dir":    "覆盖数据目录（默认取平台标准位置，也可用 %s 环境变量）",
	"cli.status.short":     "查看运行中的内核状态",
	"cli.status.long": "连接运行中的内核并打印其状态。\n\n" +
		"若内核未运行，命令以非零码退出并给出如何启动的提示 ——\n" +
		"这使它可以直接用于脚本中的健康检查。",
	"cli.status.backends": "\n平台后端可用性：",
	"cli.version.short":   "打印版本信息",

	"settings.bad_lang":   "settings: 不支持的语言 %q",
	"settings.bad_level":  "settings: 不支持的日志级别 %q",
	"settings.bad_buffer": "settings: 事件缓冲容量 %d 超出允许范围 [%d, %d]",
	"settings.bad_port":   "settings: 代理端口 %d 不合法（0-65535）",
	"settings.need_port":  "settings: 开启反向代理时必须指定监听端口",
	"settings.need_dns01": "settings: 启用 HTTPS 前必须先指定用于 DNS-01 校验的凭据",

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
