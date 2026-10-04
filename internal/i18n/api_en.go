package i18n

// apiMessagesEn 是 HTTP 接口层的消息（错误说明与操作名）。
//
// 必须与 apiMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var apiMessagesEn = map[string]string{
	// --- generic ---
	"api.empty_body":       "The request body is empty",
	"api.encode_failed":    "Failed to serialise the response",
	"api.internal_failed":  "Failed to %s",
	"api.provider_missing": "The platform backend is unavailable",
	"api.platform_missing": "platform is not wired up",

	// --- auth and middleware ---
	"api.token_empty":    "The access token is empty, so every request is refused (this is a configuration error)",
	"api.handler_panic":  "panic while handling the request",
	"api.http_request":   "http request",
	"api.console_index":  "Failed to read the console index",
	"api.console_host":   "Refused a console bootstrap request whose Host is not loopback",
	"api.console_rebind": "Refused a request whose Host is not local (a possible DNS rebinding attempt)",

	// --- certificates ---
	"api.cert.list_failed":    "Failed to read certificate status",
	"api.cert.issue_failed":   "Certificate issuance failed",
	"api.cert.none_needed":    "No certificate needs renewal",
	"api.cert.issued":         "Issued %d certificate(s)",
	"api.cert.issued_partial": "Issuance failed: ",

	// --- change orchestration ---
	"api.change.plan_failed":        "Failed to record the change plan",
	"api.change.apply_failed":       "Failed to apply the change",
	"api.change.result_failed":      "Failed to read the change result",
	"api.change.rollback_failed":    "Failed to read the rollback result",
	"api.change.query_failed":       "Failed to query the change log",
	"api.change.read_failed":        "Failed to read the change record",
	"api.change.interrupted_failed": "Failed to check for interrupted changes",

	// --- config import/export ---
	"api.config.export_failed": "Failed to export the configuration",
	"api.config.redacted":      "redacted",
	"api.config.plaintext":     "contains plaintext credentials",
	"api.config.import_long": "dry_run defaults to true: it first returns what would happen, and you " +
		"confirm by submitting with dry_run=false. Credentials are matched by (provider, label): " +
		"an existing one is updated, otherwise a new one is created.",
	"api.config.too_large":     "The import exceeds the 1 MB limit",
	"api.config.import_failed": "Failed to import the configuration",
	"api.config.preview":       "preview",
	"api.config.applied":       "applied",
	"api.config.cred_added":    "; credentials +",
	"api.config.skipped":       " skipped ",

	// --- credentials ---
	"api.credential.list_failed": "Failed to list credentials",
	"api.credential.in_use":      "This credential is still used by %d task(s)",
	"api.credential.op_failed":   "Credential operation failed",

	// --- credential verification ---
	"api.verify.no_service":  "The record management service is unavailable",
	"api.verify.no_dns":      "The DNS service is not wired up",
	"api.verify.save_failed": "Failed to record the verification result",
	"api.verify.ok":          "Connection is fine",
	"api.verify.unsupported": "This provider does not support credential verification (it has no " +
		"read-only check endpoint). That does not mean the credential is bad — you can confirm by " +
		"listing the zones once in the DNS records panel",
	"api.verify.hint_auth": "\n\nThis may not be a wrong credential but **insufficient permission**: " +
		"the credential needs DNS edit rights on the target zone. For Cloudflare, a token needs at " +
		"least Zone:DNS:Edit; note that the zone's resource scope must also cover the domain.",
	"api.verify.hint_network": "\n\nThis looks like a **network problem** rather than a credential " +
		"problem — confirm this machine can reach the provider's API endpoint (some providers' " +
		"endpoints are unreliable from mainland China networks).",
	"api.verify.mark_auth":        "signature",
	"api.verify.mark_auth2":       "authentication",
	"api.verify.mark_perm":        "permission",
	"api.verify.mark_timeout":     "timeout",
	"api.verify.mark_unreachable": "unreachable",

	// --- jobs ---
	"api.job.list_failed":   "Failed to list jobs",
	"api.job.get_failed":    "Failed to look up the job",
	"api.job.cancel_failed": "Failed to cancel the job",
	"api.job.noop_failed":   "Failed to submit the noop job",
	"api.job.noop_required": "fail_at_step asks for a failure at step N",

	// --- DNS zones and records ---
	"api.dns.list_zones":   "list zones",
	"api.dns.list_records": "list records",
	"api.dns.get_record":   "read record",
	"api.dns.create":       "create record",
	"api.dns.update":       "update record",
	"api.dns.delete":       "delete record",
	"api.dns.op_failed":    "DNS operation failed",

	// --- dynamic DNS ---
	"api.ddns.snapshot_failed": "Failed to read the interface snapshot",
	"api.ddns.list_failed":     "Failed to list tasks",
	"api.ddns.op_failed":       "Task operation failed",

	// --- proxy and reachability ---
	"api.proxy.read_failed":  "Failed to read proxy routes",
	"api.reach.probe_failed": "Reachability probe failed",

	// --- notifications ---
	"api.notify.no_center":  "The notification centre is not initialised",
	"api.notify.test_title": "ISC test notification",
	"api.notify.test_body":  "If you can read this, the channel is configured correctly.",

	// --- event stream ---
	"api.events.handshake": "Event stream handshake failed",
	"api.events.gap":       "Failed to push events.gap",
	"api.events.heartbeat": "Event stream heartbeat failed; closing the connection",
	"api.events.write":     "Event stream write failed; closing the connection",
	"api.events.closed":    "The event stream subscription was dropped",

	// --- settings and audit ---
	"api.settings.proxy_failed": "Failed to start the reverse proxy from the settings",
	"api.audit.query_failed":    "Failed to query the audit log",
	"api.verify_stop_failed":    "Failed to stop the verification session",

	// --- system service ---
	"api.service.installed":     "Service installed",
	"api.service.uninstalled":   "Service uninstalled",
	"api.service.started":       "Service started",
	"api.service.stopped":       "Service stopped",
	"api.service.no_self_path":  "Cannot determine the path of the running executable: ",
	"api.service.needs_admin":   "administrator rights required",
	"api.service.needs_root":    "root rights required",
	"api.service.access_denied": "access denied",

	// --- remote access (ISC Mizar) ---
	"api.remote_unwired":           "remote: the remote access subsystem is not wired up",
	"api.remote_settings_changed":  "remote access %s; listening on port %d",
	"api.remote_pairing_started":   "pairing session started (role %s)",
	"api.remote_pairing_canceled":  "pairing session cancelled",
	"api.remote_pairing_failed":    "pairing failed",
	"api.remote_paired":            "paired device %s (role %s)",
	"api.remote_derived":           "derived device %s (role %s) from %s",
	"api.remote_revoked":           "revoked %d device(s)",
	"api.remote_devices_failed":    "device operation failed",
	"api.remote_push_registered":   "push token registered (%s)",
	"api.remote_push_unregistered": "push token unregistered",

	"api.events_bus_missing":            "the event bus is not wired up",
	"api.events_subscribe_failed":       "failed to subscribe to the event stream",
	"api.remote_apns_set":               "APNs credentials saved",
	"api.remote_apns_cleared":           "APNs credentials deleted",
	"api.remote_push_sent":              "Sent. If nothing appears on the phone, check notification permission in system settings.",
	"api.remote_push_failed":            "Send failed (HTTP %d)",
	"api.remote_push_test":              "Test push: %s",
	"api.remote_public_synced":          "Synced the public subdomain DNS records",
	"api.remote_public_removed":         "Deleted the public subdomain",
	"api.remote_public_teardown_failed": "Failed to delete the public subdomain (a record may remain in your DNS zone)",
	"api.remote_public_bad_family":      "the address family must be ipv4 or ipv6",
	"api.remote_public_same_lan":        "the phone and the kernel are on the same LAN, so this probe does not count",
	"api.remote_public_reachable":       "an external client connected",
	"api.remote_public_unreachable":     "an external client could not connect",
	"api.remote_public_check_failed":    "failed to record the reachability check",
}
