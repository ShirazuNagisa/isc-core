package i18n

// tier1MessagesZh 是 Tier-1 六家服务商（全量 CRUD）的消息。
//
// 这一层的文案有一个特点：**它是用户排查凭据问题时看到的第一手信息**。
// 凭据填错、权限不足、套餐限制 —— 用户最先看到的就是这里的句子。
// 因此它们的措辞比别处更值得花心思：说清楚"哪里不对"以及"该怎么办"。
//
// 跨服务商重复的句子（记录类型不能为空、HTTP 各阶段的失败）放在共享段里，
// 各家的差异（GoDaddy 没有原生记录 ID、DNSPod 的 TTL 套餐限制）各自成段。
var tier1MessagesZh = map[string]string{
	// --- 操作名（用于 "%s失败：%s" 这类错误前缀）---
	"tier1.op.list_zones":    "列出区域",
	"tier1.op.list_records":  "列出记录",
	"tier1.op.get_record":    "读取记录",
	"tier1.op.create_record": "新增记录",
	"tier1.op.update_record": "修改记录",
	"tier1.op.delete_record": "删除记录",
	"tier1.op.verify":        "校验凭据",
	"tier1.op.list_domains":  "列出域名",
	"tier1.op.default":       "默认",

	// --- HTTP 各阶段 ---
	"tier1.http.serialize":    "%s: 序列化请求体失败: %w",
	"tier1.http.build":        "%s: 构造请求失败: %w",
	"tier1.http.sign":         "%s: 计算请求签名失败: %w",
	"tier1.http.request":      "%s: 请求失败: %w",
	"tier1.http.read":         "%s: 读取响应失败: %w",
	"tier1.http.parse":        "%s: 解析响应失败: %w",
	"tier1.http.op_failed":    "tier1: %s失败：%s",
	"tier1.no_error_detail":   "未提供错误详情",
	"tier1.vendor_no_message": "服务商未提供错误说明",
	"tier1.dynamic_unwired":   "tier1: 动态解析实现未接入（装配遗漏）",

	// --- 共享校验 ---
	"tier1.need_type":          "tier1: 记录类型不能为空",
	"tier1.need_name":          "tier1: 记录名不能为空",
	"tier1.need_content":       "tier1: 记录值不能为空",
	"tier1.need_record_id":     "tier1: 更新记录需要记录 ID",
	"tier1.need_id_delete":     "tier1: 删除记录需要记录 ID",
	"tier1.ali.need_id_update": "tier1: 修改记录需要记录 ID",

	// --- Cloudflare ---
	"tier1.cf.need_token":          "tier1: Cloudflare 需要 API 令牌",
	"tier1.cf.rejected":            "tier1: Cloudflare 拒绝了该凭据：%s",
	"tier1.cf.global_key":          "tier1: Cloudflare 判定这段凭据的请求头不合法。最常见的原因是填了 Global API Key —— 它是 37 位十六进制，不能当作 API Token 使用。请到 Cloudflare 控制台「我的个人资料 → API 令牌」创建一个 Token（建议用「编辑区域 DNS」模板）。",
	"tier1.cf.token_not_active":    "tier1: Cloudflare 令牌状态为 %q，不是 active",
	"tier1.cf.list_zones_failed":   "tier1: 列出区域失败：%s",
	"tier1.cf.zones_bad_shape":     "tier1: 区域列表的响应结构不符合预期",
	"tier1.cf.list_records_failed": "tier1: 列出记录失败：%s",
	"tier1.cf.records_bad_shape":   "tier1: 记录列表的响应结构不符合预期",
	"tier1.cf.create_failed":       "tier1: 新增记录失败：%s",
	"tier1.cf.update_failed":       "tier1: 修改记录失败：%s",
	"tier1.cf.delete_failed":       "tier1: 删除记录失败：%s",
	"tier1.cf.record_bad_shape":    "tier1: 记录响应结构不符合预期",
	"tier1.cf.record_parse":        "tier1: 解析记录响应失败: %w",

	// --- 阿里云 ---
	"tier1.ali.display_name":       "阿里云 DNS",
	"tier1.ali.need_credentials":   "tier1: 阿里云需要 AccessKey ID 与 AccessKey Secret",
	"tier1.ali.need_zone_name":     "tier1: 列出记录需要区域名（阿里云按域名定位记录）",
	"tier1.ali.no_record_id":       "tier1: 阿里云未返回新记录的 ID，无法确认写入结果",
	"tier1.ali.need_zone_for_host": "tier1: 需要区域名才能把记录名拆成主机记录",
	"tier1.ali.name_outside_zone": "tier1: 记录名 %q 不属于区域 %q" +
		"（记录名要写完整域名，例如 www.%s）",

	// --- DNSPod ---
	"tier1.dnspod.default_line": "默认",
	"tier1.dnspod.need_credentials": "tier1: DNSPod 需要 API ID 与 API Token 两个凭据字段" +
		"（登录令牌由二者拼接而成）",
	"tier1.dnspod.create_bad_shape": "tier1: DNSPod 的记录新增响应结构不符合预期（缺少 record 字段）",
	"tier1.dnspod.no_record_id":     "tier1: DNSPod 未返回新记录的 ID，无法定位刚创建的记录",
	"tier1.dnspod.need_current": "tier1: 修改记录前需要先读取记录 %s 的当前内容" +
		"（DNSPod 要求带上记录线路，且未提供字段的语义没有文档化）：%w",
	"tier1.dnspod.record_bad_shape": "tier1: DNSPod 的记录信息响应结构不符合预期（缺少 record 字段）",
	"tier1.dnspod.need_zone": "tier1: %s需要区域 ID 或区域名" +
		"（DNSPod 的接口以 domain_id 或 domain 定位域名）",
	"tier1.dnspod.need_zone_for_host": "tier1: 需要区域名才能把记录名 %q 翻译成 DNSPod 的主机记录" +
		"（缺少区域名会被 DNSPod 当成根域名 @，因此不能猜）",
	"tier1.dnspod.name_outside_zone": "tier1: 记录名 %q 不在区域 %q 之下，无法算出 DNSPod 的主机记录",
	"tier1.dnspod.ttl_out_of_range": "tier1: TTL %d 超出 DNSPod 允许的范围（%d-%d 秒）；" +
		"另外不同套餐的最低值不同：免费版 600 秒、专业版 60 秒、企业版 1 秒",

	// --- GoDaddy ---
	"tier1.gd.need_id_update": "tier1: 更新记录需要记录 ID（GoDaddy 没有原生 ID，" +
		"请使用 ListRecords 返回的 ID）",
	"tier1.gd.need_id_delete": "tier1: 删除记录需要记录 ID（GoDaddy 没有原生 ID，" +
		"请使用 ListRecords 返回的 ID）",
	"tier1.gd.id_mismatch": "tier1: 记录 ID 与要修改的记录不一致：ID 指向 %s/%s，提交的是 %s/%s。" +
		"GoDaddy 没有原生记录 ID，ID 由 type|name|data 合成，" +
		"记录值被改动后原 ID 即失效，请重新列出记录后重试",
	"tier1.gd.not_found": "tier1: 该域名或记录在 GoDaddy 侧不存在，未执行任何删除" +
		"（也可能记录已被删除或改名，请重新列出记录）: %w",
	"tier1.gd.bad_id": "tier1: 无法解析记录 ID %q：GoDaddy 没有原生记录 ID，" +
		"ID 由 type|name|data 合成，请使用 ListRecords 返回的 ID",
	"tier1.gd.id_parse":    "tier1: 无法解析记录 ID %q: %w",
	"tier1.gd.id_missing":  "tier1: 记录 ID %q 缺少类型或名字段",
	"tier1.gd.need_domain": "tier1: 需要域名（GoDaddy 的区域就是域名本身）",
	"tier1.gd.name_outside_domain": "tier1: 记录名 %q 不在域名 %q 之下 —— " +
		"GoDaddy 的记录名是相对域名的（根记录写 @），请检查输入的域名是否写全",
	"tier1.gd.record_no_type":   "tier1: 记录缺少类型",
	"tier1.gd.record_no_name":   "tier1: 记录缺少名字",
	"tier1.gd.need_credentials": "tier1: GoDaddy 需要 API Key 与 API Secret",

	// --- 华为云 ---
	"tier1.hw.display_name":     "华为云 DNS",
	"tier1.hw.need_credentials": "tier1: 华为云需要 access_key_id 与 access_key_secret 两个凭据字段",
	"tier1.hw.need_zone_list":   "tier1: 列出记录需要区域 ID",
	"tier1.hw.need_zone_create": "tier1: 新增记录需要区域 ID",
	"tier1.hw.need_zone_update": "tier1: 修改记录需要区域 ID",
	"tier1.hw.need_zone_delete": "tier1: 删除记录需要区域 ID",
	"tier1.hw.need_id_update":   "tier1: 修改记录需要记录 ID（华为云为记录集 ID）",
	"tier1.hw.need_id_delete":   "tier1: 删除记录需要记录 ID（华为云为记录集 ID）",

	// --- 腾讯云 ---
	"tier1.tc.default_line": "默认",
	"tier1.tc.display_name": "腾讯云 DNS",
	"tier1.tc.no_record_id": "tier1: 新增记录成功但服务商未返回记录编号",
	"tier1.tc.id_mismatch": "tier1: 修改记录返回的编号 %d 与请求的 %d 不一致，" +
		"请到控制台确认记录状态",
	"tier1.tc.need_credentials": "tier1: 腾讯云需要 SecretId 与 SecretKey" +
		"（凭据字段 secret_id / secret_key）",
	"tier1.tc.need_zone_name": "tier1: 腾讯云需要区域名（域名），只给区域 ID 无法定位记录",
	"tier1.tc.need_record_id": "tier1: 腾讯云需要记录 ID",
	"tier1.tc.bad_record_id":  "tier1: 记录 ID %q 不是腾讯云的记录编号（应为十进制数字）",
}
