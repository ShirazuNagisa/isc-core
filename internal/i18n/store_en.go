package i18n

// storeMessagesEn 是**持久化层**的消息。
//
// 必须与 storeMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var storeMessagesEn = map[string]string{
	// --- change records ---
	"store.err.marshal_steps":   "store: failed to serialise the change steps: %w",
	"store.err.marshal_warns":   "store: failed to serialise the change warnings: %w",
	"store.err.marshal_notes":   "store: failed to serialise the change notes: %w",
	"store.err.write_change":    "store: failed to write the change record: %w",
	"store.err.query_change":    "store: failed to query the change record: %w",
	"store.err.query_interrupt": "store: failed to query interrupted changes: %w",
	"store.err.iter_changes":    "store: failed to iterate the change records: %w",
	"store.steps_unparsable":    "(the step details cannot be parsed, but the change itself can still be undone)",

	// --- credentials ---
	"store.err.list_creds":      "store: failed to list credentials: %w",
	"store.err.scan_cred":       "store: failed to scan the credential: %w",
	"store.err.iter_creds":      "store: failed to iterate the credentials: %w",
	"store.err.get_cred":        "store: failed to query the credential: %w",
	"store.err.cred_by_label":   "store: failed to query the credential by label: %w",
	"store.err.insert_cred":     "store: failed to insert the credential: %w",
	"store.err.update_cred":     "store: failed to update the credential: %w",
	"store.err.delete_cred":     "store: failed to delete the credential: %w",
	"store.err.delete_result":   "store: failed to read the delete result: %w",
	"store.err.count_cred_refs": "store: failed to count credential references: %w",

	// --- dynamic DNS tasks ---
	"store.err.list_tasks":  "store: failed to list tasks: %w",
	"store.err.iter_tasks":  "store: failed to iterate the tasks: %w",
	"store.err.get_task":    "store: failed to query the task: %w",
	"store.err.insert_task": "store: failed to insert the task: %w",
	"store.err.update_task": "store: failed to update the task: %w",
	"store.err.delete_task": "store: failed to delete the task: %w",

	// --- job engine ---
	"store.err.save_job":   "store: failed to save the job: %w",
	"store.err.get_job":    "store: failed to query the job: %w",
	"store.err.list_jobs":  "store: failed to list jobs: %w",
	"store.err.scan_job":   "store: failed to scan the job: %w",
	"store.err.iter_jobs":  "store: failed to iterate the jobs: %w",
	"store.err.prune_jobs": "store: failed to prune old jobs: %w",

	// --- the rest of the migration machinery ---
	"store.err.mk_mig_table": "store: failed to create the migrations table: %w",
	"store.err.begin_mig_tx": "store: failed to begin the migration transaction: %w",
	"store.err.apply_mig":    "store: failed to apply migration %04d_%s: %w",
	"store.err.record_mig":   "store: failed to record migration %04d_%s: %w",
	"store.err.commit_mig":   "store: failed to commit migration %04d_%s: %w",
	"store.err.read_mig_dir": "store: failed to read the migrations directory: %w",
	"store.err.dup_mig":      "store: migration version %d is duplicated (%s and %s)",
	"store.err.read_mig":     "store: failed to read migration %s: %w",
	"store.err.mig_bad_name": "store: the migration filename %q is not in NNNN_description.sql form",
	"store.err.mig_bad_ver":  "store: the version number in migration filename %q is not a positive integer",
	"store.err.mig_no_desc":  "store: the migration filename %q has no description part",

	// --- notification channels ---
	"store.err.query_channels":  "store: failed to query notification channels: %w",
	"store.err.scan_channel":    "store: failed to scan the notification channel: %w",
	"store.err.iter_channels":   "store: failed to iterate the notification channels: %w",
	"store.err.begin_chan_tx":   "store: failed to begin the transaction: %w",
	"store.err.clear_channels":  "store: failed to clear the notification channels: %w",
	"store.err.marshal_headers": "store: failed to serialise the request headers: %w",
	"store.err.write_channel":   "store: failed to write the notification channel: %w",
	"store.err.commit_channels": "store: failed to commit the notification channels: %w",

	// --- proxy routes ---
	"store.err.query_routes":  "store: failed to query proxy routes: %w",
	"store.err.scan_route":    "store: failed to scan the proxy route: %w",
	"store.err.iter_routes":   "store: failed to iterate the proxy routes: %w",
	"store.err.clear_routes":  "store: failed to clear the proxy routes: %w",
	"store.err.write_route":   "store: failed to write the proxy route: %w",
	"store.err.commit_routes": "store: failed to commit the proxy routes: %w",

	// --- settings and audit ---
	"store.err.read_settings":   "store: failed to read the settings: %w",
	"store.err.scan_setting":    "store: failed to scan the setting: %w",
	"store.err.iter_settings":   "store: failed to iterate the settings: %w",
	"store.err.begin_set_tx":    "store: failed to begin the settings transaction: %w",
	"store.err.write_setting":   "store: failed to write the setting %q: %w",
	"store.err.commit_settings": "store: failed to commit the settings: %w",
	"store.err.audit_write":     "store: failed to write the audit record: %w",
	"store.err.audit_query":     "store: failed to query the audit records: %w",
	"store.err.audit_scan":      "store: failed to scan the audit record: %w",
	"store.err.audit_iter":      "store: failed to iterate the audit records: %w",
	"store.err.closed":          "store: the database is closed",

	// --- opening ---
	"store.err.open": "store: failed to open the database: %w",
	"store.err.ping": "store: failed to connect to the database: %w",
}
