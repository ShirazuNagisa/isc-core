package i18n

// remoteMessagesZh 是**远程管理面**（ISC Mizar）的消息。
//
// 它单独成层而不是并进 api 层，原因是这一块有一条自己的安全语义：
// 这里的每一条文案都对应一种**可以被利用的状态**（配对被锁定、
// 令牌被吊销、角色不足），而用户看到的那句话往往决定了他是去
// "再试一次"还是"去重新配对"。
//
// 措辞上有一条反复出现的原则：**区分"你没权限"与"你的凭据不作数了"**。
// 前者要用户去改授权，后者要用户去重新配对 —— 两者长得像，
// 但下一步动作完全相反。
var remoteMessagesZh = map[string]string{
	// --- 证书与私钥 ---
	"remote.err.cert_dir":     "remote: 创建证书目录失败: %w",
	"remote.err.key_gen":      "remote: 生成私钥失败: %w",
	"remote.err.key_marshal":  "remote: 编码私钥失败: %w",
	"remote.err.key_write":    "remote: 写入私钥失败: %w",
	"remote.err.serial":       "remote: 生成证书序列号失败: %w",
	"remote.err.cert_sign":    "remote: 签发证书失败: %w",
	"remote.err.cert_write":   "remote: 写入证书失败: %w",
	"remote.err.cert_parse":   "remote: 证书无法解析，将重新签发",
	"remote.err.random":       "remote: 随机源不可用",
	"remote.err.qr_payload":   "remote: 生成二维码内容失败: %w",
	"remote.err.port_range":   "remote: 端口 %d 不在 1-65535 之间",
	"remote.err.no_handler":   "remote: 路由处理器尚未注入",
	"remote.err.listen":       "remote: 监听端口失败: %v",
	"remote.err.serve":        "remote: 监听异常退出: %v",
	"remote.err.no_store":     "remote: 设备存储未装配",
	"remote.err.role_invalid": "remote: 未知的角色",
	// 这一条永远不该出现。它出现就意味着派生逻辑本身有缺陷，
	// 因此文案要写得像一次内部错误，而不是一句用户提示。
	"remote.err.role_escalation": "remote: 派生设备的权限高于父设备，已拒绝",

	// --- 运行日志 ---
	"remote.msg.listening":       "远程访问已监听 :%d（公钥指纹 %s）",
	"remote.msg.shutdown":        "remote: 关闭监听超时，连接将被强制断开",
	"remote.msg.failed":          "remote: 远程访问不可用",
	"remote.msg.touch_failed":    "remote: 记录设备最后访问时间失败",
	"remote.msg.push_log_failed": "remote: 记录推送投递失败",

	// --- 设备 ---
	"remote.device.unnamed": "未命名设备",

	// --- 接口层的说明文案（标题复用 error.* 那几条通用标题）---
	"remote.api.disabled":           "远程访问未开启，请先在 Phecda 的「远程访问」页打开它。",
	"remote.api.pairing_conflict":   "已经有一个配对会话在进行中，请先在 Phecda 上取消它或等它过期。",
	"remote.api.pairing_locked":     "配对因连续失败被临时锁定，请稍后再试。",
	"remote.api.pairing_mismatch":   "配对码不正确，或该会话已过期。",
	"remote.api.pairing_none":       "没有进行中的配对会话。",
	"remote.api.rate_limited":       "请求过于频繁，请稍后再试。",
	"remote.api.credential_missing": "必须提供配对密钥（来自二维码或配对链接）。",
	"remote.api.device_missing":     "缺少设备信息。",
	"remote.api.role_invalid":       "角色不合法。",
	"remote.api.role_escalation":    "派生设备的权限不得高于当前设备。",
	"remote.api.path_forbidden":     "远程访问不允许访问该路径。",
	"remote.api.apns_incomplete":    "APNs 凭据不完整：需要 team_id、key_id、bundle_id 与私钥。",
	"remote.api.token_invalid":      "设备令牌无效，请重新配对。",
	"remote.api.token_revoked":      "这台设备已被吊销，请重新配对。",
	"remote.api.device_not_found":   "设备不存在。",
	"remote.api.disabled_hint":      "远程访问当前未开启。",
	"remote.api.poll_limit":         "limit 必须在 1-500 之间。",
	"remote.api.poll_timeout":       "timeout_ms 必须在 0-55000 之间。",
	"remote.api.push_not_wired":     "APNs 推送通道尚未接入，填写的凭据暂不会生效。",

	"remote.word.enabled":  "开启",
	"remote.word.disabled": "关闭",

	// --- APNs 凭据 ---
	"remote.err.apns_incomplete": "remote: APNs 凭据不完整（需要 team_id、key_id、bundle_id 与私钥）",
	"remote.err.apns_key_pem":    "remote: APNs 私钥不是 PEM 格式（应当是一份 .p8 文件）",
	"remote.err.apns_key_type":   "remote: APNs 私钥不是 P-256 的 EC 私钥",
	"remote.err.apns_key_parse":  "remote: APNs 私钥无法解析",
	"remote.err.apns_jwt":        "remote: 签发 APNs 鉴权 token 失败: %w",
	"remote.err.apns_no_cipher":  "remote: 没有可用的加密器，拒绝以明文保存 APNs 私钥",
	"remote.err.apns_read":       "remote: 读取 APNs 凭据失败: %w",
	"remote.err.apns_decrypt":    "remote: 解密 APNs 凭据失败（主密钥换过？）: %w",
	"remote.err.apns_decode":     "remote: APNs 凭据格式不对: %w",
	"remote.err.apns_encode":     "remote: 序列化 APNs 凭据失败: %w",
	"remote.err.apns_encrypt":    "remote: 加密 APNs 凭据失败: %w",
	"remote.err.apns_write":      "remote: 写入 APNs 凭据失败: %w",

	// --- 推送 ---
	"remote.msg.push_no_token":       "这台设备没有登记推送令牌",
	"remote.msg.push_not_configured": "内核没有配置 APNs 凭据",

	"remote.push.app_failed_title":    "站点 %s 启动失败",
	"remote.push.app_unhealthy_title": "站点 %s 不健康",
	"remote.push.cert_failed_title":   "证书 %s 签发失败",
	"remote.push.ddns_failed_title":   "解析任务 %s 更新失败",
	"remote.push.ip_changed_title":    "公网地址已变化",
	"remote.push.test_title":          "ISC Mizar 测试推送",
	"remote.push.test_body":           "如果你看到这条通知，说明整条推送链路是通的。",

	// --- 公网访问（M9）---
	"remote.public.err.addr_ambiguous":       "remote: 本机有多条全局 IPv6 地址，但系统没有告诉我们哪一条是稳定的（临时地址会轮换）。请手动指定一条。",
	"remote.public.err.no_ipv6":              "remote: 本机没有可用的全局 IPv6 地址。公网访问需要一条能从外网路由进来的地址。",
	"remote.public.err.unsupported_platform": "remote: 此平台无法判断 IPv6 地址是稳定的还是临时的",
	"remote.public.err.rand":                 "remote: 生成子域名失败: %w",
	"remote.public.err.save":                 "remote: 保存公网访问台账失败: %w",
	"remote.public.err.no_zone":              "remote: 还没有选择子域名所在的区域",
	"remote.public.err.no_writer":            "remote: 公网访问需要 DNS 凭据，但内核没有可用的 DNS 服务",
	"remote.public.err.no_address":           "remote: 本机没有可用的公网地址，无法建立子域名",
	"remote.public.err.write":                "remote: 写入 DNS 记录失败（%s）: %w",
	"remote.public.err.probe":                "remote: 探测公网 IPv4 失败: %w",
	"remote.public.err.probe_body":           "remote: 公网 IP 回显服务的响应无法解析: %q",
	"remote.public.err.probe_not_v4":         "remote: 回显服务返回的不是 IPv4 地址: %s",
	"remote.public.err.zones":                "remote: 读取 DNS 区域列表失败: %w",
	"remote.public.err.zone_missing":         "remote: 选中的区域已经不在这个账号下了（%s）",
	"remote.public.err.disabled":             "remote: 公网访问没有开启",
	"remote.public.note.no_target":           "还没有可探测的公网地址 —— 请先在 Phecda 上开启公网访问并同步一次。",
	"remote.public.err.no_domain":            "remote: 还没有选择子域名挂在哪个域名下",
	"remote.public.err.no_resolver":          "remote: 内核拿不到 DNS 凭据列表，无法按域名自动匹配",
	"remote.err.no_cert":                     "remote: 没有可用的证书",
}
