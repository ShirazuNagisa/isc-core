package i18n

// ddnsMessagesEn 是**动态解析**（把本机地址同步到 DNS 记录）的消息。
//
// 必须与 ddnsMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var ddnsMessagesEn = map[string]string{
	// --- execution ---
	"ddns.err.cred_failed":   "ddns: failed to load the credential: %w",
	"ddns.err.no_dynamic":    "ddns: provider %s does not support dynamic DNS yet",
	"ddns.err.no_addr":       "could not obtain a %s address",
	"ddns.err.domain_failed": "updating the domain %s failed",
	"ddns.err.update_failed": "the update failed",
	"ddns.msg.unchanged":     "the address has not changed, so the provider was not contacted this time",
	"ddns.msg.updated":       "updated %d record(s)",
	"ddns.msg.no_change":     "the records already hold the target value; nothing to change",

	// --- service layer ---
	"ddns.err.gen_id": "ddns: failed to generate the task ID: %w",

	// --- task configuration validation ---
	"ddns.err.no_name":        "ddns: the task name cannot be empty",
	"ddns.err.no_cred":        "ddns: a credential must be given",
	"ddns.err.no_family":      "ddns: at least one of IPv4 or IPv6 must be enabled",
	"ddns.err.no_domain":      "ddns: an enabled address source needs at least one domain",
	"ddns.err.bad_getter":     "ddns: unsupported retrieval method",
	"ddns.err.empty_getter":   "ddns: the retrieval method cannot be empty",
	"ddns.err.bad_domain":     "ddns: the domain is not in a valid format",
	"ddns.err.task_not_found": "ddns: the task does not exist",
}
