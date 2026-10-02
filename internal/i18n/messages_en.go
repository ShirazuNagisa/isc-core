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

	"cli.zones.short": "List the DNS zones a credential can manage",
	"cli.zones.long": "List the DNS zones a credential can manage.\n\n" +
		"Get the credential ID from isc credential list.\n\n" +
		"Not every provider supports this — Tier-2 (the 30 dynamic-DNS-only\n" +
		"providers) cannot list zones. This command says so plainly instead of\n" +
		"handing you an empty list.",
	"cli.zones.empty": "This credential can manage no zones.",
	"cli.zones.empty_hint": "Common causes: the credential's scope covers no " +
		"domain, or the provider cannot list zones (Tier-2).",
	"cli.zones.title": "Zones (%d)",
	"cli.zones.next":  "Next: isc records list %s <zone-id>",

	"cli.records.short": "Manage DNS records (Tier-1 providers only)",
	"cli.records.long": "Browse and edit DNS records.\n\n" +
		"**Tier-1 providers only**: Cloudflare / Alibaba Cloud / Tencent Cloud /\n" +
		"DNSPod / Huawei Cloud / GoDaddy. Tier-2 (the 30 dynamic-DNS-only\n" +
		"providers) has no record management.\n\n" +
		"Record models differ between providers: a Huawei Cloud record belongs to\n" +
		"a \"record set\", and GoDaddy records have no independent ID — deleting\n" +
		"one affects other values under the same name.\n" +
		"See docs/PROVIDER-MATRIX.md.",
	"cli.records.list_short":  "List records in a zone",
	"cli.records.list_empty":  "No matching records in this zone.",
	"cli.records.list_title":  "Records (%d)",
	"cli.records.col_type":    "TYPE",
	"cli.records.col_name":    "NAME",
	"cli.records.col_content": "CONTENT",
	"cli.records.ttl_default": "default",
	"cli.records.filter_type": "only this type (A / AAAA / CNAME / MX / TXT …)",
	"cli.records.filter_name": "only this name",
	"cli.records.add_short":   "Add a record",
	"cli.records.add_long": "Add a DNS record.\n\n" +
		"Use the **full name** (www.example.com), not a relative one (www) —\n" +
		"providers disagree on relative names, while the full name means the\n" +
		"same thing on all six.",
	"cli.records.need_type":    "--type is required to specify the record type",
	"cli.records.need_content": "--content is required to specify the record content",
	"cli.records.added":        "✅ Added %s %s → %s",
	"cli.records.flag_type":    "record type (required): A / AAAA / CNAME / MX / TXT …",
	"cli.records.flag_content": "record content (required)",
	"cli.records.flag_ttl":     "TTL in seconds (0 = provider default)",
	"cli.records.rm_short":     "Delete a record",
	"cli.records.rm_long": "Delete a DNS record.\n\n" +
		"**Note the semantic differences**: GoDaddy records have no independent\n" +
		"ID, so deleting one affects **every** value of the same type under that\n" +
		"name. A Huawei Cloud record belongs to a \"record set\", so the deletion\n" +
		"granularity differs. See docs/PROVIDER-MATRIX.md.",
	"cli.records.rm_confirm": "This will delete record %s. Add --yes to confirm.",
	"cli.records.removed":    "✅ Record %s deleted",
	"cli.records.yes_flag":   "skip confirmation",

	"cli.settings.short":     "View and change kernel settings",
	"cli.settings.long":      "View and change kernel settings.\n\nWith no subcommand, prints every current setting.",
	"cli.settings.set_short": "Change settings",
	"cli.settings.set_long": "Change settings. **Only the fields you give explicitly are " +
		"submitted**; everything else stays as it is.\n\n" +
		"Examples:\n" +
		"  isc settings set --acme-email you@example.com --acme-dns-credential-id <id>\n" +
		"  isc settings set --proxy-enabled --proxy-port 443 --proxy-tls\n\n" +
		"ACME is a prerequisite for HTTPS: a DNS-01 credential must be set before\n" +
		"enabling proxy-tls, otherwise no certificate can be issued and the\n" +
		"symptom is \"the browser reports a certificate error\".",
	"cli.settings.conflict_proxy":  "--proxy-enabled and --proxy-disabled cannot be given together",
	"cli.settings.conflict_tls":    "--proxy-tls and --no-proxy-tls cannot be given together",
	"cli.settings.nothing":         "No field to change was given. Use isc settings to see the current values.",
	"cli.settings.updated":         "✅ Settings updated",
	"cli.settings.flag_lang":       "UI language: zh-CN or en",
	"cli.settings.flag_log_level":  "log level: debug / info / warn / error",
	"cli.settings.flag_proxy_on":   "enable the reverse proxy",
	"cli.settings.flag_proxy_off":  "disable the reverse proxy",
	"cli.settings.flag_proxy_port": "reverse proxy listen port",
	"cli.settings.flag_proxy_tls":  "serve the reverse proxy over HTTPS",
	"cli.settings.flag_no_tls":     "revert the reverse proxy to plain HTTP",
	"cli.settings.flag_acme_email": "ACME account email (the CA uses it to warn you if renewal fails)",
	"cli.settings.flag_acme_dir": "ACME directory URL; leave empty for production. " +
		"Certificates from the staging environment are not trusted by browsers",
	"cli.settings.flag_acme_cred": "credential ID used for the DNS-01 challenge",

	"cli.settings.render_title":   "Kernel settings",
	"cli.settings.render_lang":    "  Language      %s",
	"cli.settings.render_level":   "  Log level     %s",
	"cli.settings.render_buffer":  "  Event buffer  %d",
	"cli.settings.render_off":     "disabled",
	"cli.settings.render_on":      "enabled",
	"cli.settings.render_proxy":   "  Reverse proxy %s",
	"cli.settings.render_port":    " (port %d",
	"cli.settings.render_https":   ", HTTPS",
	"cli.settings.render_http":    ", plain HTTP",
	"cli.settings.render_close":   ")",
	"cli.settings.render_acme":    "\n  ACME (prerequisite for HTTPS)",
	"cli.settings.render_unset":   "(not set)",
	"cli.settings.render_email":   "    Email         %s",
	"cli.settings.render_prod":    "production",
	"cli.settings.render_staging": "  ⚠ staging certificates are not trusted by browsers",
	"cli.settings.render_dir":     "    Directory     %s",
	"cli.settings.render_cred":    "    DNS-01 cred.  %s",
	"cli.settings.render_no_cred": "    ⚠ Without a credential no certificate can be issued, and therefore no HTTPS",

	"cli.root.short": "ISC — put a machine with no public IPv4 on the internet",
	"cli.root.long": "ISC (the ingress orchestrator) makes an ordinary machine that only has a\n" +
		"dynamic IPv6 address reachable from the public internet.\n\n" +
		"It tracks IPv6 prefix changes, updates dynamic DNS, orchestrates the\n" +
		"firewall, issues certificates, and publishes local services on a usable\n" +
		"public port through a reverse proxy.\n\n" +
		"This one command is both the CLI client and the kernel daemon entry point:\n" +
		"  running isc status in a terminal talks to a running kernel;\n" +
		"  running isc daemon run turns the current process into the kernel itself.",

	"cli.error.with_detail": "%s (HTTP %d): %s",
	"cli.error.title_only":  "%s (HTTP %d)",

	"cli.ip.short": "Show current interface addresses and the IPv6 prefix",
	"cli.ip.long": "Show the addresses currently usable for DNS.\n\n" +
		"The IPv6 prefix is the core concept here: when the ISP re-dials, the whole\n" +
		"/64 prefix changes, so every AAAA record under it must be rewritten —\n" +
		"not just one address.",
	"cli.ip.no_interface": "No interface usable for DNS was found.",

	"cli.ddns.short": "Manage dynamic DNS tasks",
	"cli.ddns.long": "Manage dynamic DNS tasks.\n\n" +
		"A task = one credential + a set of domains + a set of address sources.\n" +
		"The scheduler runs it as soon as an address changes, and retries on a\n" +
		"fixed period as a fallback.",
	"cli.ddns.list_short":    "List every dynamic DNS task",
	"cli.ddns.list_empty":    "No dynamic DNS task configured yet.",
	"cli.ddns.field_status":  "    Status    %s\n",
	"cli.ddns.field_message": "    Detail    %s\n",
	"cli.ddns.run_use":       "run <task-id>",
	"cli.ddns.run_short":     "Run a task once now (ignoring debounce)",
	"cli.ddns.accepted":      "Accepted (task %s). Check the result with 'isc ddns list'.\n",
	"cli.ddns.state_on":      "[enabled]",
	"cli.ddns.state_off":     "[disabled]",
	"cli.ddns.never_run":     "never run",

	"cli.cert.short": "View and manage TLS certificates",
	"cli.cert.long": "View TLS certificate status, or trigger a renewal by hand.\n\n" +
		"The kernel issues and renews certificates automatically: the renewal\n" +
		"window opens at 1/3 of the remaining lifetime (30 days early for a\n" +
		"90-day certificate). Manual intervention is normally unnecessary.\n\n" +
		"ACME must be configured first: isc settings set --acme-email … --acme-dns-credential-id …",
	"cli.cert.list_short":  "List certificates and renewal status",
	"cli.cert.renew_short": "Check and issue (or renew) certificates for every HTTPS route now",
	"cli.cert.renew_long": "Trigger one certificate check and issuance now.\n\n" +
		"It is **idempotent**: an already-valid certificate is not re-issued —\n" +
		"that would needlessly burn ACME's failure quota (5 per hour in production).",
	"cli.cert.renew_triggered": "A certificate check has been triggered.",
	"cli.cert.list_empty":      "No certificates yet.",
	"cli.cert.list_empty_hint": "Once a route has HTTPS enabled (isc proxy add ... --tls), the kernel requests a certificate automatically.",
	"cli.cert.list_title":      "Certificates (%d)\n",
	"cli.cert.covers":          "      Covers:      %s\n",
	"cli.cert.valid_until":     "      Valid until: %s (%d days left)\n",
	"cli.cert.staging_warn":    "      ⚠ This certificate comes from the ACME **staging** environment; browsers will not trust it.",
	"cli.cert.staging_hint":    "        To get a real one, clear acme_directory and renew again.",
	"cli.cert.needs_renewal":   "      Needs renewal: %s\n",
	"cli.cert.last_failure":    "      Last failure:  %s\n",

	"cli.error.with_detail_short": "%s (%s)",

	"cli.ddns.add_short": "Create a dynamic DNS task",
	"cli.ddns.add_long": "Create a dynamic DNS task.\n\n" +
		"A task = one credential + a set of domains + a set of address sources.\n\n" +
		"Example (IPv6, read from an interface):\n\n" +
		"  isc ddns add --label home-ipv6 --credential <id> \\\n" +
		"      --domain home.example.com --type AAAA --source ipv6\n\n" +
		"Example (IPv4, queried from an external endpoint):\n\n" +
		"  isc ddns add --label home-ipv4 --credential <id> \\\n" +
		"      --domain home.example.com --type A --source ipv4 \\\n" +
		"      --get-type url --value https://api.ipify.org\n\n" +
		"With --source ipv6, --selector picks one of several addresses:\n\n" +
		"  --selector \"@2\"        the 2nd one (1-based)\n" +
		"  --selector \"^240e:.*\"  the first one matching a regex",
	"cli.ddns.need_label":      "--label is required to name the task",
	"cli.ddns.need_credential": "--credential is required to give the credential ID (see isc credential list)",
	"cli.ddns.need_domain":     "at least one --domain is required",
	"cli.ddns.bad_type":        "--type must be A or AAAA, got %q",
	"cli.ddns.bad_get_type":    "--get-type must be netInterface / url / cmd, got %q",
	"cli.ddns.need_value":      "--get-type %s requires --value to give the %s",
	"cli.ddns.value_url":       "endpoint URL",
	"cli.ddns.value_cmd":       "command to run",
	"cli.ddns.created":         "✅ Task created (%s)\n",
	"cli.ddns.created_hint":    "\nRun it once now: isc ddns run %s",
	"cli.ddns.rm_use":          "rm <task-id>",
	"cli.ddns.rm_short":        "Delete a dynamic DNS task",
	"cli.ddns.rm_long": "Delete a dynamic DNS task.\n\n" +
		"It deletes the **task** only; existing DNS records are left alone and\n" +
		"keep the last value that was resolved.",
	"cli.ddns.rm_confirm": "This will delete task %s. Add --yes to confirm.\n" +
		"(DNS records keep the last resolved value; they are not deleted.)\n",
	"cli.ddns.removed":  "✅ Task %s deleted\n",
	"cli.ddns.yes_flag": "skip confirmation",

	"cli.ddns.flag_label":      "task name (required) — it shows up in notifications and logs",
	"cli.ddns.flag_credential": "credential ID (required)",
	"cli.ddns.flag_domain": "domain to update, repeatable; " +
		"www:example.com names the root domain explicitly",
	"cli.ddns.flag_type":     "record type: A or AAAA",
	"cli.ddns.flag_source":   "address source: ipv6 or ipv4 (used for the default get-type)",
	"cli.ddns.flag_get_type": "how to obtain the value: netInterface (recommended) / url / cmd",
	"cli.ddns.flag_selector": "IPv6 only: address selector, e.g. @2 or ^240e:.*",
	"cli.ddns.flag_ttl":      "record TTL in seconds; empty uses the provider default",
	"cli.ddns.flag_disabled": "create it disabled",

	"cli.notify.short": "View and test notification channels",
	"cli.notify.long": "View notification channels and recent delivery results, or send a test.\n\n" +
		"Channels are configured through the API (PUT /v1/notify/channels) —\n" +
		"they carry many fields (URL, headers, body template) that a command\n" +
		"line is a poor fit for. The web console has a full form.",
	"cli.notify.list_short":       "List notification channels",
	"cli.notify.list_empty":       "No notification channel configured yet.",
	"cli.notify.log_always":       "(The log channel is always available; notifications appear in the isc daemon log.)",
	"cli.notify.enabled":          "enabled",
	"cli.notify.disabled":         "disabled",
	"cli.notify.min_severity":     "    only send at %s and above\n",
	"cli.notify.custom_body":      "    (custom request body template)",
	"cli.notify.deliveries_short": "List recent notification deliveries",
	"cli.notify.test_short":       "Send a test notification to every channel",
	"cli.notify.test_long": "Send a test message to every channel right away.\n\n" +
		"It **bypasses deduplication and the queue**: you should see the result\n" +
		"immediately rather than waiting for the next delivery cycle.",
	"cli.notify.sent":          "Test notification sent:",
	"cli.notify.no_deliveries": "No delivery record yet.",

	"cli.console.short": "Show (or open) the verification console address",
	"cli.console.long": "Show the local address of the verification console.\n\n" +
		"The console listens on loopback TCP with a port the kernel picks at\n" +
		"startup, so it differs on every run. Use this command to get the\n" +
		"current address, or add --open to launch a browser.\n\n" +
		"Note: the browser must use 127.0.0.1 (or localhost). The kernel rejects\n" +
		"requests whose Host header is not a local address — that defends against\n" +
		"DNS rebinding, so a LAN IP or a custom host name gets a 403.",
	"cli.console.not_running": "%w (hint: start the kernel first with 'isc daemon run')",
	"cli.console.no_tcp":      "the kernel offers no loopback TCP channel, so a browser cannot reach the console",
	"cli.console.not_tcp":     "the fallback channel is not TCP (%s), so a browser cannot reach the console",
	"cli.console.open_hint": "\nHint: add --open to launch a browser directly.\n" +
		"      The console must be reached via 127.0.0.1 — any other host name\n" +
		"      is rejected by the kernel with 403 (DNS rebinding defence).\n",
	"cli.console.open_flag":   "open the console in the default browser",
	"cli.console.open_failed": "cannot open a browser: %w",

	"cli.service.short": "Register the kernel as a system service (start on boot, restart on crash)",
	"cli.service.long": "Register the kernel as a system service. Once registered it will:\n\n" +
		"  · start automatically on boot (Windows uses delayed start, waiting for\n" +
		"    the network to come up)\n" +
		"  · restart after a crash (5s / 30s / 60s backoff)\n\n" +
		"**Installing and uninstalling need administrator rights**:\n" +
		"  Windows  right-click the terminal → Run as administrator\n" +
		"  Linux    use sudo\n" +
		"  macOS    use sudo\n\n" +
		"Without those rights it does not fail vaguely — it says exactly what is missing.\n\n" +
		"The service mechanism differs per platform:\n" +
		"  Windows  Service Control Manager (SCM), visible in services.msc\n" +
		"  Linux    systemd (/etc/systemd/system/isc-core.service)\n" +
		"  macOS    launchd (/Library/LaunchDaemons/com.isc.core.plist)",
	"cli.service.install_short": "Install the system service",
	"cli.service.install_long": "Install and register the system service.\n\n" +
		"If it is already installed the configuration is **updated** rather than\n" +
		"rejected — re-running the install command is normal (the path changed, or\n" +
		"the autostart setting did), and \"service already exists\" would only send\n" +
		"you off to uninstall it by hand.",
	"cli.service.installed":       "✅ Service installed (%s)\n",
	"cli.service.exe_path":        "   Executable: %s\n",
	"cli.service.data_dir":        "   Data dir:   %s\n",
	"cli.service.autostart_yes":   "   Autostart:  yes (delayed start, waits for the network)",
	"cli.service.autostart_no":    "   Autostart:  no (manual start)",
	"cli.service.restart_yes":     "   Restart:    yes (5s / 30s / 60s backoff)",
	"cli.service.next_steps":      "\nStart it with isc service start, or check it with isc service status.",
	"cli.service.flag_autostart":  "start automatically on boot (Windows uses delayed start, waiting for the network)",
	"cli.service.flag_no_restart": "do not restart after a crash",
	"cli.service.flag_exe":        "path of the executable to register (defaults to the running one)",
	"cli.service.uninstall_short": "Stop and delete the system service",
	"cli.service.uninstall_long": "Stop and delete the system service.\n\n" +
		"It is **idempotent**: a service that does not exist yields success.\n" +
		"Erroring would break \"uninstall then install\" deployment scripts, which\n" +
		"are the most common way this is written.\n\n" +
		"The data directory is **not** deleted — it holds your credentials and config.",
	"cli.service.uninstall_busy": "Removing the registration while leaving the process running is not supported yet; run isc service stop first",
	"cli.service.uninstalled":    "✅ Service uninstalled",
	"cli.service.data_kept": "   The data directory is kept: %s\n" +
		"   Delete it by hand if you want it gone.",
	"cli.service.keep_running": "remove only the registration, leaving the running process alone (not supported on every platform)",
	"cli.service.status_short": "Show system service status",
	"cli.service.status_long": "Show system service status. It reports two separate things:\n\n" +
		"\tthe OS service   installed? running? (**needs administrator rights**)\n" +
		"\tthe kernel       can it actually be reached right now? (needs no rights)\n\n" +
		"They are reported separately for a reason. On real hardware, *querying*\n" +
		"service status on Windows also needs administrator rights (opening the\n" +
		"service control manager needs full access), so an ordinary user simply\n" +
		"cannot run it. But what they usually want to know is \"is the kernel\n" +
		"running?\" — and one look at the runtime file answers that.",
	"cli.service.kernel_running": "▶  Kernel: running (the local API is reachable)",
	"cli.service.kernel_stopped": "⏹  Kernel: not running",
	"cli.service.query_failed":   "   Service: cannot query (%s)\n",
	"cli.service.state_stopped":  "not running",
	"cli.service.state_running":  "running",
	"cli.service.state_line":     "%s service: %s (%s)\n",
	"cli.service.start_short":    "Start the system service",
	"cli.service.start_long": "Start the system service.\n\n" +
		"It succeeds when the service is **already running** — erroring would\n" +
		"break \"make sure it is up\" scripts, which is the most common use.",
	"cli.service.started":      "✅ Service started",
	"cli.service.stop_short":   "Stop the system service",
	"cli.service.stopped":      "✅ Service stopped",
	"cli.service.no_self_path": "cannot determine the path of the running executable: %w. Pass it explicitly with --exe",
	"cli.service.not_abs":      "cannot resolve %q to an absolute path: %w",
	"cli.service.not_found":    "the executable does not exist or cannot be read: %s: %w",
	"cli.service.is_dir":       "the executable path points at a directory: %s",

	"cli.daemon.short": "Manage the kernel daemon",
	"cli.daemon.long": "Manage the kernel daemon.\n\n" +
		"The kernel is a GUI-less resident process; the CLI, the verification\n" +
		"console and any downstream GUI all talk to it over the local API. It has\n" +
		"to stay resident for the dynamic DNS timers to run reliably.",
	"cli.daemon.run_short": "Run the kernel in the foreground",
	"cli.daemon.run_long": "Run the kernel in the foreground until interrupted.\n\n" +
		"In production, install it as a system service with isc service install so\n" +
		"the kernel can run with nobody logged in, start on boot, and hold enough\n" +
		"privilege to change the firewall and bind low ports.",
	"cli.daemon.flag_listen":   "loopback listen address (default 127.0.0.1:0, i.e. pick a port)",
	"cli.daemon.flag_no_tcp":   "disable the loopback listener (note: the browser console will not be reachable)",
	"cli.daemon.flag_origins":  "allowed WebSocket Origin patterns (for local console debugging only)",
	"cli.daemon.stub_prefix":   "will be implemented in %s (see docs/PLAN.md)",
	"cli.daemon.install_short": "Install as a system service (needs administrator rights)",
	"cli.daemon.install_long": "Install the kernel as a system service.\n\n" +
		"Why a system service (see docs/DECISIONS.md D20): it can run with nobody\n" +
		"logged in, start on boot, and hold enough privilege to change the firewall\n" +
		"and bind low ports — a user-level process would raise a UAC prompt on\n" +
		"Windows every time it touched the firewall.",
	"cli.daemon.backend_install":   "Service install",
	"cli.daemon.uninstall_short":   "Uninstall the system service (needs administrator rights)",
	"cli.daemon.backend_uninstall": "Service uninstall",

	"cli.verify.short": "External check: confirm public reachability from a phone",
	"cli.verify.long": "Start an external verification of whether the service is reachable\n" +
		"from the public internet.\n\nFlow:\n" +
		"  1. the kernel listens on a temporary local port (random by default);\n" +
		"  2. the command prints an address with a random path;\n" +
		"  3. open that address on a phone with **Wi-Fi off, on 4G/5G**;\n" +
		"  4. the kernel judges the attempt by its **source address**.\n\n" +
		"There are three outcomes, and the second is the most misunderstood:\n\n" +
		"  public address  → the path really is open\n" +
		"  local address   → **proves nothing** (NAT hairpin, or Wi-Fi was left on)\n" +
		"  no request      → something upstream blocked it, which cannot be\n" +
		"                    tested from this machine",
	"cli.verify.flag_port":       "port to listen on (0 = let the kernel pick a free one)",
	"cli.verify.flag_wait":       "how long to wait for an external request (0 = just print the address)",
	"cli.verify.no_address_hint": "  Run isc doctor first to check this machine's IPv6 state.",
	"cli.verify.started":         "External verification started",
	"cli.verify.no_address": "⚠ No usable public address found, so no verification link could be built.\n" +
		"  Run isc doctor first to check this machine's IPv6 state.",
	"cli.verify.open_hint":        "\nOpen this on a phone (Wi-Fi off, on 4G/5G):",
	"cli.verify.listen_port":      "Listening port: %d\n",
	"cli.verify.allow_first":      "\nAllow this port in the kernel first:",
	"cli.verify.timeout":          "\nTimed out waiting.",
	"cli.verify.reachable":        "✅ External access succeeded — the path is open.",
	"cli.verify.next_step":        "   You can now open the port your real service uses.",
	"cli.verify.hairpin":          "⚠️  This access **does not count as proof**.",
	"cli.verify.hairpin_hint":     "\n   Make sure the phone's Wi-Fi is off and it is on mobile data.",
	"cli.verify.unreachable":      "❌ No external access arrived within the window.",
	"cli.verify.unreachable_hint": "\n   If every local check in isc doctor passes, the problem is not here:",
	"cli.verify.cause_router":     "     · the router firewall did not let the port through (check its IPv6 firewall)",
	"cli.verify.cause_isp":        "     · the ISP blocks inbound connections (true in some provinces)",
	"cli.verify.hits":             "\nRequests received:",

	"cli.doctor.short": "Self-check: which link in the chain is broken",
	"cli.doctor.long": "Check the whole \"can the service be reached from the internet\" chain,\n" +
		"layer by layer, bottom-up:\n\n" +
		"  1. does this machine have a global IPv6 address\n" +
		"  2. is there a delegated IPv6 prefix\n" +
		"  3. is the local firewall backend usable\n" +
		"  4. upstream reachability (not testable locally — use a phone on mobile data)\n\n" +
		"Failing items come with \"what to do\". Work through them in order — one\n" +
		"fix at a time beats changing five settings at once.\n\n" +
		"Note: this command **makes no system change**; it only reads.",
	"cli.doctor.none":          "No reachability provider is available.",
	"cli.doctor.unknown":       "no reachability provider named %q",
	"cli.doctor.read_failed":   "(note: could not read unfinished system changes: %v)\n",
	"cli.doctor.flag_provider": "check only this reachability provider",
	"cli.doctor.title":         "ISC self-check",
	"cli.doctor.pending":       "\n⚠ %d unfinished system change(s) found from a previous run:\n",
	"cli.doctor.pending_state": "      state: %s\n",
	"cli.doctor.pending_hint": "    These changes may have been applied only in part. Confirm they match\n" +
		"    what you intended, and if not, inspect them with 'isc changes' and undo.\n",
	"cli.doctor.needs_server":     "\n  (needs an external server)\n",
	"cli.doctor.local_section":    "\n  Local checks\n",
	"cli.doctor.upstream_section": "\n  External verification (not testable locally)\n",
	"cli.doctor.conclusion_blocked": "Conclusion: the local configuration is fine, but **something upstream blocks it**.\n" +
		"      This is not something you can fix on this machine — use another\n" +
		"      route, or ask your ISP whether inbound connections are blocked.\n",
	"cli.doctor.conclusion_ok": "Conclusion: this machine is ready. But **passing a local check does not\n" +
		"      prove the internet can reach it** — confirm with a phone on 4G/5G.\n",
	"cli.doctor.conclusion":            "Conclusion: %s\n",
	"cli.doctor.conclusion_next":       "      Deal with this one first: %s\n",
	"cli.doctor.state_running":         "in progress (the kernel may have exited abnormally)",
	"cli.doctor.state_applied":         "applied",
	"cli.doctor.state_failed":          "failed (rolled back automatically)",
	"cli.doctor.state_rolledback":      "undone",
	"cli.doctor.state_rollback_failed": "rollback failed (the system is in an intermediate state)",

	"cli.proxy.short": "Manage the reverse proxy",
	"cli.proxy.long": "Manage reverse proxy routes. A route maps a domain to a local service:\n\n" +
		"    home.example.com   →  127.0.0.1:8096\n" +
		"    *.lab.example.com  →  127.0.0.1:3000\n\n" +
		"Upstreams **must be loopback or private addresses**. The proxy listens on\n" +
		"the public internet, so allowing any upstream would let anyone use it as a\n" +
		"stepping stone — with all the traffic billed to you.\n\n" +
		"Note wildcard routes: *.example.com also matches example.com itself.",
	"cli.proxy.status_short": "Show reverse proxy status",
	"cli.proxy.running":      "Running: listening on port %d, %d route(s)\n",
	"cli.proxy.stopped":      "Not running.",
	"cli.proxy.stopped_hint": "Turn on \"reverse proxy\" in settings and give it a listen port to start it.",
	"cli.proxy.last_error":   "\nLast failure: %s\n",
	"cli.proxy.routes_short": "List every route",
	"cli.proxy.add_use":      "add <domain> [domain...] --to <upstream>",
	"cli.proxy.add_short":    "Add a route",
	"cli.proxy.add_long": "Point a set of domains at one local service.\n\n" +
		"The upstream needs a full host:port, e.g. 127.0.0.1:8096. A missing port\n" +
		"is rejected — guessing one forwards to an unintended service, and that\n" +
		"kind of problem is very hard to notice.",
	"cli.proxy.updated":         "Updated: %s → %s\n",
	"cli.proxy.added":           "Added: %s → %s\n",
	"cli.proxy.flag_to":         "upstream address, e.g. 127.0.0.1:8096 (required)",
	"cli.proxy.flag_label":      "human-readable route name",
	"cli.proxy.flag_tls":        "serve this domain over HTTPS",
	"cli.proxy.rm_use":          "rm <domain-or-route-id>",
	"cli.proxy.rm_short":        "Delete a route",
	"cli.proxy.rm_notfound":     "no route matches %q",
	"cli.proxy.removed":         "Deleted %d route(s).\n",
	"cli.proxy.list_empty":      "No route configured yet.",
	"cli.proxy.list_empty_hint": "Add one with 'isc proxy add home.example.com --to 127.0.0.1:8096'.",
	"cli.proxy.list_title":      "Routes (%d)\n",

	"cli.doctor.pending_hint_a":       "    These changes may have been applied only in part. Confirm they match\n",
	"cli.doctor.pending_hint_b":       "    what you intended, and if not, inspect them with 'isc changes' and undo.\n",
	"cli.doctor.conclusion_blocked_a": "Conclusion: the local configuration is fine, but **something upstream blocks it**.\n",
	"cli.doctor.conclusion_blocked_b": "      This is not something you can fix on this machine — use another\n",
	"cli.doctor.conclusion_blocked_c": "      route, or ask your ISP whether inbound connections are blocked.\n",
	"cli.doctor.conclusion_ok_a":      "Conclusion: this machine is ready. But **passing a local check does not\n",
	"cli.doctor.conclusion_ok_b":      "      prove the internet can reach it** — confirm with a phone on 4G/5G.\n",

	"cli.expose.short": "Open a port in the firewall (plan first, preview, undo)",
	"cli.expose.long": "Build a \"open a port\" change plan, then apply it once confirmed.\n\n" +
		"The command prints what it is about to do before waiting for your\n" +
		"confirmation. Opening a port changes system state, so you will see:\n\n" +
		"  · the ISC rules that already exist (they are left alone)\n" +
		"  · the rules about to be added\n" +
		"  · the risk level and anything worth knowing\n\n" +
		"Afterwards, 'isc changes' shows the history and 'isc rollback <plan-id>'\n" +
		"undoes it.\n\n" +
		"Note:\n" +
		"  · administrator rights are required (on Windows a non-admin simply\n" +
		"    cannot create firewall rules);\n" +
		"  · this opens the port on **this machine** — the router is separate.",
	"cli.expose.no_change":         "\nNothing to do — the rule already exists.",
	"cli.expose.confirm":           "Apply this change?",
	"cli.expose.cancelled":         "Cancelled.",
	"cli.expose.flag_port":         "port to open (required)",
	"cli.expose.flag_protocol":     "protocol: tcp or udp",
	"cli.expose.flag_label":        "service name, shown in the system firewall UI",
	"cli.expose.flag_provider":     "reachability provider",
	"cli.expose.flag_yes":          "apply without confirmation",
	"cli.expose.plan_title":        "Change plan",
	"cli.expose.plan_risk":         "Risk: %s\n",
	"cli.expose.plan_id":           "Plan ID: %s\n",
	"cli.expose.plan_diff":         "\nChanges about to happen:",
	"cli.expose.plan_irreversible": "  ⚠ this step cannot be undone\n",
	"cli.expose.plan_notes":        "\nNote:",
	"cli.expose.plan_details":      "\nDetails:",
	"cli.expose.applied":           "✅ Change applied.",
	"cli.expose.applied_undo":      "   Undo: isc rollback %s\n",
	"cli.expose.failed":            "❌ The change failed and was rolled back to the previous state.",
	"cli.expose.rollback_failed":   "‼️  The change failed **and the automatic rollback did not finish**.",
	"cli.expose.rollback_hint_a":   "   The system may be in an intermediate state; check with 'isc doctor',\n",
	"cli.expose.rollback_hint_b":   "   and if needed remove the isc- prefixed rules from the system firewall by hand.",
	"cli.expose.state":             "State: %s\n",
	"cli.expose.failed_step":       "   Failed step: %s\n",
	"cli.expose.failed_reason":     "     Reason: %s\n",

	"cli.changes.short":        "Show system change history and pending plans",
	"cli.changes.flag_limit":   "how many history entries",
	"cli.changes.pending":      "Pending plans (%d)\n",
	"cli.changes.pending_line": "      risk %s, expires %s\n",
	"cli.changes.pending_hint": "\n  These plans have not taken effect. Apply with: isc expose --port ... or through the API.",
	"cli.changes.empty":        "No system change recorded yet.",
	"cli.changes.title":        "Change history",
	"cli.changes.plan_id_line": "      Plan ID %s",
	"cli.changes.undo":         "   Undo: isc rollback %s",

	"cli.rollback.use":   "rollback <plan-id>",
	"cli.rollback.short": "Undo an applied system change",
	"cli.rollback.long": "Undo a change that was applied earlier.\n\n" +
		"It survives a kernel restart — everything needed to undo is persisted\n" +
		"alongside the change. 'isc changes' shows the plan IDs.\n\n" +
		"It is idempotent: undoing something already undone succeeds.",
	"cli.rollback.done":     "✅ Undone: %s\n",
	"cli.rollback.state":    "State: %s\n",
	"cli.rollback.no_stdin": "\n(cannot read input, cancelled; add --yes for automation)",
	"cli.rollback.yes":      "yes",
	"cli.risk.high":         "high — may cut off your own access to this machine",
	"cli.risk.medium":       "medium — opens one port",
	"cli.risk.low":          "low",

	"cli.init.short": "First-run guide: probe the environment and print the next commands",
	"cli.init.long": "First-run guide.\n\n" +
		"It checks whether this machine can serve traffic (public IPv6, delegated\n" +
		"prefix, firewall rights, low-port binding) and then prints a next-steps\n" +
		"list with **real values filled in**.\n\n" +
		"It changes no system state — it only tells you what to do. The real\n" +
		"changes happen in the commands that follow, and all of them can be\n" +
		"undone (see isc changes / isc rollback).",
	"cli.init.flag_port":   "port you intend to expose (checked for bindability and for a listener)",
	"cli.init.flag_domain": "domain you intend to use (used to build copy-pasteable commands)",
	"cli.init.title":       "ISC first-run guide",
	"cli.init.env_section": "[Environment]",
	"cli.init.ipv6_ok":     "  ✅ Public IPv6: %s\n",
	"cli.init.iface":       "     Interface: %s\n",
	"cli.init.ipv6_missing": "  ❌ No public IPv6 address found\n" +
		"     This is the product's core prerequisite. Check first:\n" +
		"       · IPv6 is on in the router, **with prefix delegation (DHCPv6-PD)**\n" +
		"       · the ONT is in bridge mode (router mode often yields no delegated prefix)\n" +
		"       · IPv6 is not disabled in the OS\n" +
		"     isc ip shows the details for every interface.",
	"cli.init.prefix":      "  ℹ️  Delegated prefix: %s\n",
	"cli.init.prefix_hint": "     This prefix changes when the ISP re-dials; ISC follows it and updates DNS.",
	"cli.init.ipv4_public": "  ℹ️  Public IPv4 detected (this product focuses on IPv6, but A records work too)",
	"cli.init.ipv4_cgnat":  "  ℹ️  No public IPv4 — the norm on home broadband here, and no obstacle",
	"cli.init.fw_ok":       "  ✅ Firewall backend available (opening ports needs administrator rights)",
	"cli.init.fw_bad": "  ⚠️  Firewall backend unavailable: %s\n" +
		"     You can still use it, but ports must be opened in the system firewall by hand.",
	"cli.init.lowport_ok": "  ✅ Low ports (443 etc.) can be bound — %s\n",
	"cli.init.lowport_bad": "  ⚠️  Low ports (443 etc.) cannot be bound: %s\n" +
		"     Using 443 needs root / setcap, or pick a port ≥1024.",
	"cli.init.kernel_section":  "[Kernel]",
	"cli.init.kernel_running":  "  ✅ Running",
	"cli.init.kernel_stopped":  "  ⏹  Not running",
	"cli.init.start_now":       "     Start it now: isc daemon run",
	"cli.init.install_service": "     Install as a system service (recommended, needs admin): isc service install && isc service start",
	"cli.init.steps_section":   "[Next steps]",
	"cli.init.footer_console":  "     Open the console (a graphical UI) with isc console.",

	"cli.init.step_run_desc":  "Start the kernel",
	"cli.init.step_run_note":  "(runs in the foreground, Ctrl-C stops it. For long-term use, isc service install)",
	"cli.init.step_cred_desc": "Add a DNS provider credential",
	"cli.init.step_cred_cmd":  "isc credential add cloudflare --label my-cf --field token=<API-TOKEN>",
	"cli.init.step_cred_note": "The token only needs Zone:DNS:Edit — the kernel only changes DNS records. " +
		"isc credential fields cloudflare lists the fields it needs.",
	"cli.init.step_ddns_desc": "Create a dynamic DNS task",
	"cli.init.step_ddns_cmd": "isc ddns add --label my-domain --credential <id> " +
		"--domain %s --type %s --source %s",
	"cli.init.step_ddns_note":   "Take the credential ID from the previous step's output (or isc credential list).",
	"cli.init.step_cred_note2":  "(replace cloudflare with the provider you actually use)",
	"cli.init.step_ddns_note2":  "Re-run isc init with --domain to get a version with your domain filled in.",
	"cli.init.step_expose_desc": "Open the port in the firewall (a plan is generated for you to confirm)",
	"cli.init.step_expose_cmd":  "isc expose --port %d --label my-service",
	"cli.init.step_expose_note": "This needs administrator rights; undo it afterwards with isc rollback.",
	"cli.init.step_verify_desc": "Confirm public reachability from a phone",
	"cli.init.step_verify_note": "**Do not skip this.** Reaching your own domain from this computer\n" +
		"proves nothing — the router may be doing NAT hairpin;\n" +
		"only a request from the public internet counts.",

	"cli.init.ipv6_missing_a": "  ❌ No public IPv6 address found",
	"cli.init.ipv6_missing_b": "     This is the product's core prerequisite. Check first:",
	"cli.init.ipv6_missing_c": "       · IPv6 is on in the router, **with prefix delegation (DHCPv6-PD)**",
	"cli.init.ipv6_missing_d": "       · the ONT is in bridge mode (router mode often yields no delegated prefix)",
	"cli.init.ipv6_missing_e": "       · IPv6 is not disabled in the OS",
	"cli.init.fw_bad_a":       "  ⚠️  Firewall backend unavailable: %s",
	"cli.init.fw_bad_b":       "     You can still use it, but ports must be opened in the system firewall by hand.",
	"cli.init.lowport_bad_a":  "  ⚠️  Low ports (443 etc.) cannot be bound: %s",
	"cli.init.lowport_bad_b":  "     Using 443 needs root / setcap, or pick a port ≥1024.",
	"cli.init.footer_undo":    "Note: every system change can be undone. Use isc changes for the history and isc rollback <ID> to undo.",

	"cli.init.step_verify_note_a": "**Do not skip this.** Reaching your own domain from this computer\n",
	"cli.init.step_verify_note_b": "proves nothing — the router may be doing NAT hairpin;\n",
	"cli.init.step_verify_note_c": "only a request from the public internet counts.",
	"cli.init.step_ddns_cmd2":     "isc ddns add --label my-domain --credential <id> --domain home.example.com --type AAAA --source ipv6",

	"cli.init.ipv6_missing_f":   "     isc ip shows the details for every interface.",
	"cli.init.step_cred_note_a": "The token only needs Zone:DNS:Edit — the kernel only changes DNS records.",
	"cli.init.step_cred_note_b": "isc credential fields cloudflare lists the fields it needs.",

	"cli.err.not_running":  "cli: the kernel is not running",
	"cli.err.unreachable":  "cli: the kernel is unreachable",
	"cli.err.runtime_read": "cli: cannot read the runtime file: %w",
	"cli.err.no_endpoint":  "the runtime file lists no usable channel",
	"cli.err.http_status":  "cli: the kernel returned HTTP %d",
	"cli.flag.json":        "output JSON for scripts to consume",
	"cli.flag.lang":        "output language: zh-CN or en",
	"cli.flag.verbose":     "output debug logs",
	"cli.flag.data_dir":    "override the data directory (defaults to the platform location, or the %s environment variable)",
	"cli.status.short":     "Show the running kernel's status",
	"cli.status.long": "Connect to the running kernel and print its status.\n\n" +
		"If the kernel is not running the command exits non-zero and says how to\n" +
		"start it — which makes it usable directly as a health check in scripts.",
	"cli.status.backends": "\nPlatform backend availability:",
	"cli.version.short":   "Print version information",

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
