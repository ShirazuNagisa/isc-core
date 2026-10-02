package i18n

// storeMessagesZh 是**持久化层**的消息。
//
// # 关于它和 messages_zh.go 里那几条 store.* 的分工
//
// `store.migrate_*` / `store.migration_changed` 那一组讲的是**迁移机制本身**
// （校验和不匹配、补列失败），它们和"哪个查询失败了"不是一类东西，
// 因此留在基础表里。
//
// 这里放的是**仓储层的数据库错误**。它们高度格式化：
// `store: <做了什么>失败: %w`。看起来千篇一律，但它们出现在用户配置出错、
// 磁盘满、数据库被别的进程锁住这些时刻，而"哪一步失败了"正是排查的起点 ——
// 所以逐条保留，没有做成拼接（英文语序会把拼接弄乱）。
var storeMessagesZh = map[string]string{
	// --- 变更记录 ---
	"store.err.marshal_steps":   "store: 序列化变更步骤失败: %w",
	"store.err.marshal_warns":   "store: 序列化变更警告失败: %w",
	"store.err.marshal_notes":   "store: 序列化变更说明失败: %w",
	"store.err.write_change":    "store: 写入变更记录失败: %w",
	"store.err.query_change":    "store: 查询变更记录失败: %w",
	"store.err.query_interrupt": "store: 查询中断的变更失败: %w",
	"store.err.iter_changes":    "store: 遍历变更记录失败: %w",
	// 这一条是**给用户看的占位**：步骤详情存坏了，但撤销动作还在，
	// 因此变更仍然可以撤销。它出现在界面的步骤列表里。
	"store.steps_unparsable": "（步骤详情无法解析，但变更本身仍可撤销）",

	// --- 凭据 ---
	"store.err.list_creds":      "store: 查询凭据列表失败: %w",
	"store.err.scan_cred":       "store: 扫描凭据失败: %w",
	"store.err.iter_creds":      "store: 遍历凭据失败: %w",
	"store.err.get_cred":        "store: 查询凭据失败: %w",
	"store.err.cred_by_label":   "store: 按标签查询凭据失败: %w",
	"store.err.insert_cred":     "store: 插入凭据失败: %w",
	"store.err.update_cred":     "store: 更新凭据失败: %w",
	"store.err.delete_cred":     "store: 删除凭据失败: %w",
	"store.err.delete_result":   "store: 读取删除结果失败: %w",
	"store.err.count_cred_refs": "store: 统计凭据引用失败: %w",

	// --- 动态解析任务 ---
	"store.err.list_tasks":  "store: 查询任务列表失败: %w",
	"store.err.iter_tasks":  "store: 遍历任务失败: %w",
	"store.err.get_task":    "store: 查询任务失败: %w",
	"store.err.insert_task": "store: 插入任务失败: %w",
	"store.err.update_task": "store: 更新任务失败: %w",
	"store.err.delete_task": "store: 删除任务失败: %w",

	// --- 任务引擎 ---
	"store.err.save_job":   "store: 保存任务失败: %w",
	"store.err.get_job":    "store: 查询任务失败: %w",
	"store.err.list_jobs":  "store: 查询任务列表失败: %w",
	"store.err.scan_job":   "store: 扫描任务失败: %w",
	"store.err.iter_jobs":  "store: 遍历任务失败: %w",
	"store.err.prune_jobs": "store: 清理历史任务失败: %w",

	// --- 迁移机制的其余部分 ---
	"store.err.mk_mig_table": "store: 创建迁移记录表失败: %w",
	"store.err.begin_mig_tx": "store: 开启迁移事务失败: %w",
	"store.err.apply_mig":    "store: 应用迁移 %04d_%s 失败: %w",
	"store.err.record_mig":   "store: 记录迁移 %04d_%s 失败: %w",
	"store.err.commit_mig":   "store: 提交迁移 %04d_%s 失败: %w",
	"store.err.read_mig_dir": "store: 读取迁移目录失败: %w",
	"store.err.dup_mig":      "store: 迁移版本号 %d 重复（%s 与 %s）",
	"store.err.read_mig":     "store: 读取迁移 %s 失败: %w",
	"store.err.mig_bad_name": "store: 迁移文件名 %q 不符合 NNNN_描述.sql 格式",
	"store.err.mig_bad_ver":  "store: 迁移文件名 %q 的版本号不是正整数",
	"store.err.mig_no_desc":  "store: 迁移文件名 %q 缺少描述部分",

	// --- 通知通道 ---
	"store.err.query_channels":  "store: 查询通知通道失败: %w",
	"store.err.scan_channel":    "store: 扫描通知通道失败: %w",
	"store.err.iter_channels":   "store: 遍历通知通道失败: %w",
	"store.err.begin_chan_tx":   "store: 开始事务失败: %w",
	"store.err.clear_channels":  "store: 清空通知通道失败: %w",
	"store.err.marshal_headers": "store: 序列化请求头失败: %w",
	"store.err.write_channel":   "store: 写入通知通道失败: %w",
	"store.err.commit_channels": "store: 提交通知通道失败: %w",

	// --- 代理路由 ---
	"store.err.query_routes":  "store: 查询代理路由失败: %w",
	"store.err.scan_route":    "store: 扫描代理路由失败: %w",
	"store.err.iter_routes":   "store: 遍历代理路由失败: %w",
	"store.err.clear_routes":  "store: 清空代理路由失败: %w",
	"store.err.write_route":   "store: 写入代理路由失败: %w",
	"store.err.commit_routes": "store: 提交代理路由失败: %w",

	// --- 设置与审计 ---
	"store.err.read_settings":   "store: 读取设置失败: %w",
	"store.err.scan_setting":    "store: 扫描设置失败: %w",
	"store.err.iter_settings":   "store: 遍历设置失败: %w",
	"store.err.begin_set_tx":    "store: 开启设置事务失败: %w",
	"store.err.write_setting":   "store: 写入设置 %q 失败: %w",
	"store.err.commit_settings": "store: 提交设置失败: %w",
	"store.err.audit_write":     "store: 写入审计失败: %w",
	"store.err.audit_query":     "store: 查询审计失败: %w",
	"store.err.audit_scan":      "store: 扫描审计记录失败: %w",
	"store.err.audit_iter":      "store: 遍历审计失败: %w",
	"store.err.closed":          "store: 数据库已关闭",

	// --- 打开 ---
	"store.err.open": "store: 打开数据库失败: %w",
	"store.err.ping": "store: 连接数据库失败: %w",
}
