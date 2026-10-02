package i18n

// opsMessagesZh 是**变更编排**与**守护进程通知**的消息。
//
// 这两块放在一层里，因为它们面向的是同一个时刻：一次系统变更从计划、
// 执行、失败、到回滚的整条路径上，用户会在 CLI 输出、审计记录与手机通知里
// 反复看到同一件事的三种说法。
//
// 措辞上有一条反复出现的原则：**回滚没走完时必须说得足够严重**。
// "系统可能处于中间状态"不是客套话 —— 它意味着防火墙规则可能只放行了一半，
// 而用户此时最需要知道的是"别以为它没事"。
var opsMessagesZh = map[string]string{
	// --- 变更：错误 ---
	"change.err.autorollback": "变更失败且自动回滚未完成，系统可能处于中间状态：%w",
	"change.err.no_journal": "change: 无法写入变更日志，已放弃执行" +
		"（不做无记录的变更）: %w",
	"change.err.cancelled":       "变更被取消: %w",
	"change.err.step_failed":     "步骤 %q 失败: %w",
	"change.err.no_context":      "change: 找不到计划 %s 的执行上下文，无法自动回滚",
	"change.err.no_undo":         "change: 找不到步骤 %s 的撤销动作",
	"change.err.undo_failed":     "撤销步骤 %q 失败: %w",
	"change.err.read_record":     "change: 读取变更记录失败: %w",
	"change.err.no_reverter":     "change: 没有登记 %q 类型的撤销器，无法撤销",
	"change.err.check_interrupt": "change: 检查中断的变更失败: %w",
	"change.err.not_found":       "change: 变更记录不存在",
	"change.err.plan_expired":    "change: 计划不存在或已过期，请重新生成",
	"change.err.still_running": "change: 变更 %s 仍处于执行中状态；" +
		"若确认内核上次是异常退出，请先执行恢复检查",

	// --- 变更：给用户看的状态 ---
	"change.msg.interrupted": "上次执行未走完（内核可能异常退出）",
	"change.msg.interrupted_detail": "上次执行在第 %d 步之后中断，" +
		"已有 %d 个步骤生效",

	// --- 计划校验 ---
	"change.plan.no_id":        "change: 计划缺少 ID",
	"change.plan.no_kind":      "change: 计划缺少变更类型",
	"change.plan.no_title":     "change: 计划缺少标题",
	"change.plan.step_no_id":   "change: 第 %d 个步骤缺少 ID",
	"change.plan.dup_step_id":  "change: 步骤 ID 重复: %s",
	"change.plan.step_no_body": "change: 步骤 %s 没有执行体",

	// --- 守护进程 ---
	"daemon.err.no_channel": "无可用通道",
	"daemon.err.token":      "daemon: 生成访问令牌失败: %w",

	// --- 通知：动态解析 ---
	"notify.ddns.failed":   "动态解析失败：%s",
	"notify.ddns.updated":  "动态解析已更新：%s",
	"notify.ddns.new_addr": "新地址 %s",

	// --- 通知：前缀 ---
	"notify.prefix.changed": "IPv6 前缀已变化（%s）",
	"notify.prefix.new":     "新前缀 %s",

	// --- 通知：证书 ---
	"notify.cert.issued": "证书已签发：%s",
	"notify.cert.failed": "证书签发失败：%s",

	// --- 通知：系统变更 ---
	//
	// 回滚失败那一条刻意用了最强的措辞，并带两个 %s：
	// 前者是原始错误，后者是回滚错误 —— 两条都必须给出来，
	// 因为用户要据此判断"现在到底处于什么状态"。
	"notify.change.rollback_failed": "变更失败，且自动回滚未能完成 —— " +
		"系统可能处于中间状态。\n%s\n%s",
	"notify.change.failed":   "系统变更失败：%s",
	"notify.change.reverted": "系统变更已撤销：%s",
}
