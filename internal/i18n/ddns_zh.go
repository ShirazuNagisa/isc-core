package i18n

// ddnsMessagesZh 是**动态解析**（把本机地址同步到 DNS 记录）的消息。
//
// 这一层的文案会出现在两个地方，因此措辞要同时照顾两者：
//
//   - **任务执行结果**：`已更新 2 条记录` / `地址未变化，本次未与服务商比对`
//     —— 它们会进审计记录与通知，是用户判断"它到底干活了没有"的依据；
//   - **任务配置校验**：哪一项没填、哪一项填错了。
//
// "地址未变化，本次未与服务商比对"这句是刻意写全的：只说"未变化"会让用户
// 以为内核根本没检查，而实际上它检查了本机地址、只是没去打扰服务商。
var ddnsMessagesZh = map[string]string{
	// --- 执行 ---
	"ddns.err.cred_failed":   "ddns: 取凭据失败: %w",
	"ddns.err.no_dynamic":    "ddns: 服务商 %s 尚不支持动态解析",
	"ddns.err.no_addr":       "未能获取 %s 地址",
	"ddns.err.domain_failed": "域名 %s 更新失败",
	"ddns.err.update_failed": "更新失败",
	"ddns.msg.unchanged":     "地址未变化，本次未与服务商比对",
	"ddns.msg.updated":       "已更新 %d 条记录",
	"ddns.msg.no_change":     "记录已是目标值，无需改动",

	// --- 服务层 ---
	"ddns.err.gen_id": "ddns: 生成任务 ID 失败: %w",

	// --- 任务配置校验 ---
	"ddns.err.no_name":        "ddns: 任务名称不能为空",
	"ddns.err.no_cred":        "ddns: 必须指定凭据",
	"ddns.err.no_family":      "ddns: 至少要启用 IPv4 或 IPv6 之一",
	"ddns.err.no_domain":      "ddns: 启用的地址来源必须至少填写一个域名",
	"ddns.err.bad_getter":     "ddns: 不支持的获取方式",
	"ddns.err.empty_getter":   "ddns: 获取方式的取值不能为空",
	"ddns.err.bad_domain":     "ddns: 域名格式不合法",
	"ddns.err.task_not_found": "ddns: 任务不存在",
}
