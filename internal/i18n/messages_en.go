package i18n

// messagesEn 是英文消息目录。
//
// 必须与 messagesZh 的 key 集合完全一致 —— 由 i18n 完整性测试强制保证。
var messagesEn = map[string]string{
	// --- generic errors ---
	"error.unauthorized":       "Unauthorized: missing or invalid access token",
	"error.forbidden":          "Forbidden",
	"error.not_found":          "The requested resource does not exist",
	"error.method_not_allowed": "Method not allowed",
	"error.invalid_request":    "Invalid request parameters",
	"error.internal":           "Internal error",
	"error.not_implemented":    "This feature is not implemented on the current platform; falling back to guided mode",
	"error.timeout":            "Operation timed out",
	"error.conflict":           "Operation not allowed in the current state",

	// --- jobs ---
	"error.job_not_found":      "Job not found",
	"error.job_not_cancelable": "Job has already finished and cannot be canceled",
	"job.noop.running":         "Noop job running (step %d/%d)",
	"job.noop.done":            "Noop job finished",
	"job.noop.failed":          "Noop job failed at step %d as requested",
	"job.canceled":             "Job canceled",

	// --- event stream ---
	"error.websocket_upgrade": "Cannot upgrade to a WebSocket connection: %s",
	"events.gap":              "Requested event id %d is outside the retention window (earliest is %d); please refetch full state",

	// --- daemon ---
	"daemon.starting":             "ISC core is starting",
	"daemon.started":              "ISC core started",
	"daemon.stopping":             "ISC core is shutting down",
	"daemon.stopped":              "ISC core stopped",
	"daemon.already_running":      "ISC core already appears to be running (PID %d); refusing to start a second instance",
	"daemon.stale_runtime_file":   "Found a stale runtime file (PID %d no longer exists); cleaned up",
	"daemon.runtime_write_failed": "Failed to write runtime file: %s",
	"daemon.transport_failed":     "Failed to establish the local management channel: %s",

	// --- transport ---
	"transport.named_pipe": "named pipe",
	"transport.unix_sock":  "Unix domain socket",
	"transport.loopback":   "loopback TCP",
	"transport.desc":       "Local management channel: %s (%s)",

	// --- data directories ---
	"paths.windows":       "Windows",
	"paths.linux":         "Linux",
	"paths.darwin":        "macOS",
	"paths.not_supported": "Unsupported platform (%s); cannot determine the data directory",

	// --- platform capabilities ---
	"platform.unsupported":      "This backend is not implemented on the current platform; falling back to guided mode",
	"platform.not_implemented":  "Platform backend not implemented: %s",
	"platform.low_port_denied":  "Missing CAP_NET_BIND_SERVICE; cannot bind ports below 1024",
	"platform.low_port_granted": "Low ports can be bound",

	// --- CLI ---
	"cli.daemon_not_running": "ISC core is not running. Start it with 'isc daemon run' or install it as a system service.",
	"cli.connecting":         "Connecting to ISC core",
	"cli.connected":          "Connected to ISC core",
	"cli.status_header":      "ISC core status",
	"cli.version_header":     "ISC version information",
	"cli.unknown_command":    "Unknown command: %s",

	// --- credential fields (provider registry) ---
	"provider.field.access_key_id":     "Access Key ID",
	"provider.field.access_key_secret": "Access Key Secret",
	"provider.field.secret_id":         "SecretId",
	"provider.field.secret_key":        "SecretKey",
	"provider.field.api_token":         "API token",
	"provider.field.api_key":           "API key",
	"provider.field.api_secret":        "API secret",
	"provider.field.dnspod_id":         "DNSPod ID",
	"provider.field.dnspod_token":      "DNSPod token",
	"provider.field.id":                "ID",
	"provider.field.secret":            "Secret",
	"provider.field.ext_param":         "Extra parameters",

	"provider.help.access_key_id": "Create it in the cloud provider's access control page; grant only DNS-related permissions",
	"provider.help.api_token": "Create it under My Profile → API Tokens in the Cloudflare dashboard; " +
		"prefer the \"Edit zone DNS\" template scoped to specific zones",
	"provider.help.dnspod_id":    "Find it under User Center → Security Settings → API keys in the DNSPod console",
	"provider.help.dnspod_token": "Issued together with the DNSPod ID; it is shown only once, so save it now",
	"provider.help.tier2_id":     "Currently used only for config import; the exact meaning will be settled when the implementation lands",
	"provider.help.tier2_secret": "Currently used only for config import; the exact meaning will be settled when the implementation lands",
	"provider.help.tier2_ext_param": "Extra parameters required by a few providers (for example Vercel's teamId); " +
		"leave empty for most providers",

	// --- credentials ---
	"credential.created":            "Credential created",
	"credential.updated":            "Credential updated",
	"credential.deleted":            "Credential deleted",
	"credential.not_found":          "Credential not found",
	"credential.duplicate":          "A credential with the same label already exists for this provider",
	"credential.in_use":             "This credential is still in use and cannot be deleted",
	"credential.verify.running":     "Verifying credential",
	"credential.verify.ok":          "Credential is valid",
	"credential.verify.failed":      "Credential verification failed",
	"credential.verify.unsupported": "This provider is not implemented yet; the credential cannot be verified",

	// --- settings ---
	"settings.updated": "Settings updated",

	// --- dynamic DNS tasks ---
	"ddns.task_not_found":    "Task not found",
	"ddns.task_created":      "Task created",
	"ddns.task_updated":      "Task updated",
	"ddns.task_deleted":      "Task deleted",
	"ddns.credential_in_use": "This credential is still used by %d task(s) and cannot be deleted",
	"ddns.triggered":         "Run requested",
	"ddns.no_address":        "Could not obtain an %s address",
	"ddns.updated_count":     "Updated %d record(s)",
	"ddns.unchanged":         "Records already match; nothing to change",
	"ddns.skipped":           "Address unchanged; skipped the provider comparison",
	"ddns.detected_change":   "Address change detected; running dynamic DNS",

	// --- import/export ---
	"config.export.empty":               "There is nothing to export",
	"config.import.invalid":             "Cannot parse the imported content",
	"config.import.dry_run":             "Dry run: nothing was written",
	"config.import.applied":             "Import completed",
	"config.import.ddnsgo.bad":          "This does not look like a ddns-go configuration",
	"config.import.ddnsgo.none":         "No ddns-go entries were found in the configuration",
	"config.import.skipped":             "Skipped entry %d: %s",
	"config.import.webhook_unsupported": "ddns-go webhook settings are not migrated yet (the notification centre lands in M4)",

	// --- DNS record management ---
	"dns.unsupported":       "This provider does not support the operation",
	"dns.record_not_found":  "DNS record not found",
	"dns.upstream_error":    "The provider rejected the request",
	"dns.zone_not_found":    "DNS zone not found",
	"dns.invalid_record":    "The record content is invalid",
	"dns.too_many_requests": "The provider is rate limiting; try again later",

	// --- verification console ---
	"console.title":            "ISC Verification Console",
	"console.host_not_allowed": "The request Host is not a loopback address; rejected",
	"console.token_missing":    "Could not obtain an access token; open the console via 127.0.0.1",

	// --- reachability and system changes ---
	"reach.provider_not_found": "No such reachability method",
	"change.not_found":         "Change record not found",
	"change.interrupted_found": "Found %d unfinished system change(s); run 'isc doctor' to inspect",

	// --- external verification ---
	"verify.start_failed":      "Could not start external verification",
	"verify.session_not_found": "Verification session not found",

	// --- system changes ---
	"change.plan_expired":    "The plan does not exist or has expired; generate it again",
	"change.rollback_failed": "Rollback failed",
	"reach.plan_failed":      "Could not build the change plan",

	// --- reverse proxy ---
	"proxy.invalid_routes": "The forwarding rules are invalid",

	// --- certificates ---
	"cert.no_tls_routes": "No HTTPS routes are configured yet, so no certificate is needed",

	// --- notifications ---
	"notify.cert_hint": "Run 'isc cert list' for details; DNS-01 failures usually mean a credential permission or domain ownership problem.",

	// --- notifications ---
	"notify.invalid_channels": "The notification channel configuration is invalid",

	// --- system service ---
	"service.install_failed": "Failed to install the system service",
	"service.action_failed":  "The system service operation failed",

	// --- store ---
	"store.open_failed":    "Failed to open the database: %s",
	"store.migrate_failed": "Database migration failed: %s",
	"store.closed":         "The database is closed",
	// --- credential management (CLI) ---
	"cli.credential.short":      "Manage DNS provider credentials",
	"cli.credential.list_short": "List saved credentials",
	"cli.credential.add_short":  "Add a credential",
	"cli.credential.rm_short":   "Delete a credential",
	"cli.credential.long": "Manage DNS provider credentials.\n\n" +
		"Credentials are stored encrypted under a master key held in the system\n" +
		"key store (Windows DPAPI / macOS Keychain / Linux Secret Service).\n" +
		"The API only ever returns **masked** values for secret fields; the\n" +
		"plaintext is never sent back.\n\n" +
		"Use 'isc credential fields <provider>' to see which fields a provider needs.",
	"cli.credential.list_empty":       "No credentials yet.",
	"cli.credential.list_empty_hint":  "Add one with isc credential add <provider>; the fields subcommand lists supported providers.",
	"cli.credential.list_title":       "Credentials (%d)",
	"cli.credential.not_implemented":  "this provider is not implemented yet",
	"cli.credential.verify_failed_at": "last check failed (%s)",

	"cli.credential.add_long": "Add a DNS provider credential.\n\n" +
		"Fields are passed with --field, repeatable:\n\n" +
		"  isc credential add cloudflare --label my-cf --field token=<API-TOKEN>\n" +
		"  isc credential add dnspod --label primary \\\n" +
		"      --field id=<ID> --field secret=<TOKEN>\n\n" +
		"Use 'isc credential fields <provider>' to see the field names.\n\n" +
		"**Least privilege**: DNS record editing is all that is needed.\n" +
		"For Cloudflare, a token with Zone:DNS:Edit is enough — the kernel\n" +
		"does not touch any other setting.",
	"cli.credential.provider_empty": "the provider name cannot be empty",
	"cli.credential.label_required": "--label is required to name the credential — " +
		"one provider can have several credentials, and the label is how they are told apart",
	"cli.credential.added":       "✅ Credential added (%s)",
	"cli.credential.added_hint":  "Next: create a dynamic DNS task with this ID, or manage DNS records in the console.",
	"cli.credential.fields_hint": "run 'isc credential fields %s' to see which fields it needs",

	"cli.credential.fields_short":   "Show which credential fields a provider needs",
	"cli.credential.fields_long":    "Show which credential fields a provider needs.\n\nWith no argument, lists every provider.",
	"cli.credential.fields_unknown": "No provider named %q. Run without an argument to see them all",
	"cli.credential.no_fields":      "(no credential fields needed)",
	"cli.credential.field_required": "(required)",
	"cli.credential.field_secret":   " [secret]",
	"cli.credential.field_example":  "  e.g. %s",
	"cli.credential.capabilities":   "    Capabilities: %s",
	"cli.credential.cap_dynamic":    "dynamic DNS",
	"cli.credential.cap_zones":      "list zones",
	"cli.credential.cap_create":     "create records",
	"cli.credential.cap_update":     "update records",
	"cli.credential.cap_delete":     "delete records",
	"cli.credential.cap_dns01":      "DNS-01 certificates",
	"cli.credential.tier":           "  [Tier-%d]",
	"cli.credential.unavailable":    "  ⚠ not implemented yet",

	"cli.credential.rm_confirm": "Deleting credential %s invalidates every dynamic DNS task that references it.\nAdd --yes to confirm.",
	"cli.credential.removed":    "✅ Credential %s deleted",

	"cli.credential.verify_short": "Check whether a credential works",
	"cli.credential.verify_long": "Check whether a credential works (\"test connection\").\n\n" +
		"**Not every provider supports this**: Alibaba Cloud / Tencent Cloud /\n" +
		"Huawei Cloud / GoDaddy have no read-only check endpoint, and faking one\n" +
		"by listing domains would demand extra permissions — misjudging a\n" +
		"least-privilege account as invalid.\n\n" +
		"When unsupported, this command says so instead of reporting a fake failure.",
	"cli.credential.verify_ok":  "✅ Credential works",
	"cli.credential.verify_bad": "❌ Credential does not work",

	"cli.credential.field_format": "--field takes name=value, got %q",
	"cli.credential.field_noname": "--field is missing a field name: %q",
	"cli.credential.field_none":   "at least one --field is required. Use 'isc credential fields <provider>' to see which",
	"cli.credential.yes_flag":     "skip confirmation",
	"cli.credential.label_flag":   "human-readable name (required) — one provider can have several credentials",
	"cli.credential.field_flag":   "a field as name=value, repeatable",
}
