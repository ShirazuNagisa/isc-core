package i18n

// acmeMessagesZh 是 ACME（Let's Encrypt 等）证书签发的消息。
//
// 这一层的文案有一个明确的用途：**证书签不下来时，用户要能自己找到原因**。
// DNS-01 校验失败是最常见的一种，而它的成因有四种完全不同的可能 ——
// NS 指向别家、记录还没传播、凭据没有编辑权限、域名本身不存在。
// 因此那条错误不是一句"校验失败"，而是把这四种可能**逐条列出来**。
var acmeMessagesZh = map[string]string{
	// --- 共享 ---
	"acme.need_domain": "acme: 至少要指定一个域名",
	"acme.need_cred":   "acme: 未指定用于 DNS-01 校验的凭据",
	"acme.cred_failed": "acme: 取凭据失败: %w",

	// --- 客户端：文件与密钥 ---
	"acme.client.mkdir_failed":        "acme: 无法创建证书目录 %s: %w",
	"acme.client.cert_parse":          "acme: 证书文件 %s 无法解析: %w",
	"acme.client.write_failed":        "acme: 写入 %s 失败: %w",
	"acme.client.not_pem":             "acme: 证书不是合法的 PEM",
	"acme.client.gen_account_key":     "acme: 生成账户密钥失败: %w",
	"acme.client.marshal_account_key": "acme: 序列化账户密钥失败: %w",
	"acme.client.mkdir_account_key":   "acme: 创建账户密钥目录失败: %w",
	"acme.client.gen_cert_key":        "acme: 生成证书密钥失败: %w",
	"acme.client.gen_csr":             "acme: 生成 CSR 失败: %w",
	"acme.client.bad_key_type":        "acme: 不支持的密钥类型",
	"acme.client.marshal_cert_key":    "acme: 序列化证书密钥失败: %w",
	"acme.client.account_key_parse": "acme: 账户密钥文件 %s 无法解析。" +
		"删除它会让这个 ACME 账户永久失效（已签发的证书将无法续期），" +
		"请先备份并确认",

	// --- 客户端：协议流程 ---
	"acme.client.register_failed":  "acme: 注册账户失败: %w",
	"acme.client.order_failed":     "acme: 创建订单失败: %w",
	"acme.client.csr_failed":       "acme: 提交 CSR 失败: %w",
	"acme.client.authz_failed":     "acme: 读取授权失败: %w",
	"acme.client.no_dns01":         "acme: 域名 %s 的授权里没有 DNS-01 校验方式（可用的有 %v）",
	"acme.client.challenge_failed": "acme: 计算挑战值失败: %w",
	"acme.client.cleanup_failed": "acme: 清理域名 %s 的挑战记录失败" +
		"（可手动删除 _acme-challenge 记录）: %v\n",
	"acme.client.notify_failed": "acme: 通知挑战就绪失败: %w",

	// 这是整个包最重要的一条文案：它必须让用户能自己定位问题。
	"acme.client.dns01_rejected": "acme: 域名 %s 的 DNS-01 校验未通过: %w\n" +
		"常见原因：\n" +
		"  · 该域名的权威 DNS 不是所选服务商（检查 NS 记录）\n" +
		"  · 服务商那边的记录传播还没完成（稍后重试）\n" +
		"  · 凭据没有该域名的编辑权限\n" +
		"  · 域名本身不存在或已过期",

	// --- DNS-01 记录写入 ---
	"acme.dns01.no_list_zones":     "acme: 服务商 %s 不支持列出区域，无法定位 %s 的 DNS 区域",
	"acme.dns01.no_create":         "acme: 服务商 %s 不支持新增记录",
	"acme.dns01.list_zones_failed": "acme: 列出区域失败: %w",
	"acme.dns01.zone_not_found": "acme: 在与凭据「%s」关联的 %d 个区域里找不到 %s 所属的区域。" +
		"请确认该凭据的账号下有这个域名",
	"acme.dns01.write_failed":     "acme: 写入挑战记录 %s 失败: %w",
	"acme.dns01.impl_unavailable": "acme: 服务商 %s 的实现不可用",
	"acme.dns01.no_delete":        "acme: 服务商 %s 不支持删除记录",
	"acme.dns01.impl_for_dns01":   "acme: 服务商 %s 的实现不可用，无法完成 DNS-01 校验",

	// --- 管理器 ---
	"acme.manager.empty_domain": "acme: 域名为空",
	"acme.manager.bad_chars":    "acme: 域名含有非法字符: %q",
	"acme.manager.single_label": "acme: %q 看起来不是完整域名；" +
		"公网 CA 不为单标签域名签发证书",
	"acme.manager.wildcard_pos":    "acme: 通配符只能出现在最前面（*.example.com）: %q",
	"acme.manager.not_covering":    "现有证书不覆盖 %s",
	"acme.manager.no_expiry":       "无法读出证书的有效期，为安全起见重新签发",
	"acme.manager.expired":         "证书已于 %s 过期",
	"acme.manager.below_threshold": "剩余有效期 %d 天，低于续期阈值 %d 天",
	"acme.manager.issuing":         "acme: %s 的证书正在签发中，请稍候",
	"acme.manager.read_failed":     "无法读取证书文件：",

	// --- TLS 提供方（握手时按 SNI 找证书）---
	"acme.provider.no_sni":      "acme: TLS 握手没有提供域名",
	"acme.provider.no_routes":   "acme: 尚未配置任何 HTTPS 路由",
	"acme.provider.read_failed": "acme: 读取域名 %s 的证书失败（证书名 %s）: %w",
	"acme.provider.no_cert": "acme: 没有为域名 %s 配置证书。" +
		"请为它添加一条启用 HTTPS 的路由",
	"acme.provider.mismatch": "acme: 域名 %s 的证书或私钥无法配对（证书名 %s）：%w。" +
		"请重新签发这张证书",
}
