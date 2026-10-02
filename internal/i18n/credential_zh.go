package i18n

// credentialMessagesZh 覆盖**凭据、主密钥与路径**三块。
//
// 它们放在一起，因为出错时它们是同一条链上的相邻环节：路径决定数据目录
// 在哪 → 主密钥存在那里 → 凭据用它加密。而用户看到的报错往往需要**跨过**
// 这三层才能定位（"解密失败"的根因可能是"刚迁移过数据目录"）。
//
// 因此这里的措辞刻意多写了一句"该怎么办"：
// 主密钥长度异常、解密失败这两条都点明了下一步动作。
var credentialMessagesZh = map[string]string{
	// --- 凭据校验 ---
	"cred.err.no_label":           "credential: 标签不能为空",
	"cred.err.no_provider":        "credential: 服务商不能为空",
	"cred.err.no_fields":          "credential: 缺少必填字段",
	"cred.err.extra_fields":       "credential: 存在未声明的字段",
	"cred.err.not_found":          "credential: 凭据不存在",
	"cred.err.dup_label":          "credential: 同一服务商下标签重复",
	"cred.err.in_use":             "credential: 凭据仍被 %d 个任务使用",
	"cred.err.immutable_provider": "%w: 服务商不可修改（当前 %s，请求 %s）",
	"cred.err.check_refs":         "credential: 检查引用失败: %w",
	"cred.err.marshal":            "credential: 序列化字段失败: %w",
	"cred.err.encrypt":            "credential: 加密字段失败: %w",
	"cred.err.decrypt":            "credential: 解密 %s 的字段失败: %w",
	"cred.err.parse_fields":       "credential: 解析 %s 的字段失败: %w",
	"cred.err.gen_id":             "credential: 生成 ID 失败: %w",
	"cred.err.unknown_provider":   "credential: 未知的服务商",
	"cred.err.provider_locked":    "credential: 服务商不可修改",

	// --- 主密钥与信封加密 ---
	"secret.err.nil_store":   "secret: 密钥存储为 nil",
	"secret.err.read_master": "secret: 读取主密钥失败: %w",
	"secret.err.bad_len": "secret: 主密钥长度异常（期望 %d 字节，实际 %d 字节）；" +
		"密钥存储可能已损坏，请删除主密钥后重新录入凭据",
	"secret.err.gen_master":  "secret: 生成主密钥失败: %w",
	"secret.err.save_master": "secret: 保存主密钥失败: %w",
	"secret.err.gen_nonce":   "secret: 生成 nonce 失败: %w",
	"secret.err.short":       "secret: 密文过短，不是合法的信封",
	"secret.err.bad_version": "secret: 不支持的密文格式版本 %d",
	"secret.err.decrypt": "secret: 解密失败（密文已损坏，或当前主密钥与加密时不一致；" +
		"若刚迁移过数据目录，请重新录入凭据）",
	"secret.err.bad_key_len": "secret: 主密钥长度异常（%d 字节）",
	"secret.err.new_aes":     "secret: 构造 AES 失败: %w",
	"secret.err.new_gcm":     "secret: 构造 GCM 失败: %w",

	// --- 路径 ---
	"paths.err.data_dir":   "paths: 解析数据目录 %q: %w",
	"paths.err.config_dir": "paths: 解析配置目录 %q: %w",
	"paths.err.mkdir":      "paths: 创建目录 %q: %w",
	"paths.err.no_home":    "paths: 无法确定用户主目录: %w",
	"paths.warn.chmod":     "无法将 %s 权限收紧至 0700：%v",
	"paths.warn.acl":       "收紧 %s 的访问权限失败，内核仍会运行但令牌可能被其他用户读取：%v",
	"paths.err.acl_build":  "构造访问控制列表失败: %w",
	"paths.err.acl_apply":  "设置目录安全信息失败: %w",
	"paths.err.sid_lookup": "解析内置账户 SID (类型 %d) 失败: %w",
}
