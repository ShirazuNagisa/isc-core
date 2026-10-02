package i18n

// opsMessagesEn 是**变更编排**与**守护进程通知**的消息。
//
// 必须与 opsMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var opsMessagesEn = map[string]string{
	// --- change: errors ---
	"change.err.autorollback": "the change failed and automatic rollback did not finish; the system may be in an intermediate state: %w",
	"change.err.no_journal": "change: the change journal could not be written, so execution was abandoned " +
		"(no unrecorded changes): %w",
	"change.err.cancelled":       "the change was cancelled: %w",
	"change.err.step_failed":     "step %q failed: %w",
	"change.err.no_context":      "change: no execution context for plan %s, so it cannot be rolled back automatically",
	"change.err.no_undo":         "change: no undo action found for step %s",
	"change.err.undo_failed":     "undoing step %q failed: %w",
	"change.err.read_record":     "change: failed to read the change record: %w",
	"change.err.no_reverter":     "change: no reverter is registered for type %q, so it cannot be undone",
	"change.err.check_interrupt": "change: failed to check for interrupted changes: %w",
	"change.err.not_found":       "change: the change record does not exist",
	"change.err.plan_expired":    "change: the plan does not exist or has expired; generate it again",
	"change.err.still_running": "change: the change %s is still marked as running; " +
		"if you are sure the kernel exited abnormally last time, run the recovery check first",

	// --- change: user-visible status ---
	"change.msg.interrupted": "the previous run did not finish (the kernel may have exited abnormally)",
	"change.msg.interrupted_detail": "the previous run stopped after step %d; " +
		"%d step(s) are already in effect",

	// --- plan validation ---
	"change.plan.no_id":        "change: the plan has no ID",
	"change.plan.no_kind":      "change: the plan has no change kind",
	"change.plan.no_title":     "change: the plan has no title",
	"change.plan.step_no_id":   "change: step %d has no ID",
	"change.plan.dup_step_id":  "change: duplicate step ID: %s",
	"change.plan.step_no_body": "change: step %s has no action",

	// --- daemon ---
	"daemon.err.no_channel": "no channel is available",
	"daemon.err.token":      "daemon: failed to generate the access token: %w",

	// --- notifications: dynamic DNS ---
	"notify.ddns.failed":   "Dynamic DNS failed: %s",
	"notify.ddns.updated":  "Dynamic DNS updated: %s",
	"notify.ddns.new_addr": "New address %s",

	// --- notifications: prefix ---
	"notify.prefix.changed": "The IPv6 prefix changed (%s)",
	"notify.prefix.new":     "New prefix %s",

	// --- notifications: certificates ---
	"notify.cert.issued": "Certificate issued: %s",
	"notify.cert.failed": "Certificate issuance failed: %s",

	// --- notifications: system changes ---
	"notify.change.rollback_failed": "The change failed and automatic rollback did not finish — " +
		"the system may be in an intermediate state.\n%s\n%s",
	"notify.change.failed":   "System change failed: %s",
	"notify.change.reverted": "System change undone: %s",
}
