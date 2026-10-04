package i18n

// remoteMessagesEn 是远程管理面的英文文案。
//
// 与中文逐条对应 —— 完整性由 keys_test.go 与分层合并测试守着。
var remoteMessagesEn = map[string]string{
	// --- certificate and key ---
	"remote.err.cert_dir":     "remote: cannot create the certificate directory: %w",
	"remote.err.key_gen":      "remote: cannot generate the private key: %w",
	"remote.err.key_marshal":  "remote: cannot encode the private key: %w",
	"remote.err.key_write":    "remote: cannot write the private key: %w",
	"remote.err.serial":       "remote: cannot generate a certificate serial: %w",
	"remote.err.cert_sign":    "remote: cannot sign the certificate: %w",
	"remote.err.cert_write":   "remote: cannot write the certificate: %w",
	"remote.err.cert_parse":   "remote: the certificate cannot be parsed; a new one will be issued",
	"remote.err.random":       "remote: the random source is unavailable",
	"remote.err.qr_payload":   "remote: cannot build the QR payload: %w",
	"remote.err.port_range":   "remote: port %d is outside 1-65535",
	"remote.err.no_handler":   "remote: the route handler has not been injected yet",
	"remote.err.listen":       "remote: cannot listen on the port: %v",
	"remote.err.serve":        "remote: the listener stopped unexpectedly: %v",
	"remote.err.no_store":     "remote: the device store is not wired up",
	"remote.err.role_invalid": "remote: unknown role",
	// This one must never happen. If it does, the derivation logic itself is
	// broken, so the wording reads like an internal fault rather than advice.
	"remote.err.role_escalation": "remote: derived device would outrank its parent; refused",

	// --- runtime log ---
	"remote.msg.listening":       "remote access is listening on :%d (public key fingerprint %s)",
	"remote.msg.shutdown":        "remote: shutdown timed out; connections will be closed abruptly",
	"remote.msg.failed":          "remote: remote access is unavailable",
	"remote.msg.touch_failed":    "remote: cannot record the device's last-seen time",
	"remote.msg.push_log_failed": "remote: cannot record the push delivery",

	// --- devices ---
	"remote.device.unnamed": "Unnamed device",

	// --- API-layer detail text (titles reuse the generic error.* keys) ---
	"remote.api.disabled":           "Remote access is off. Turn it on in Phecda's Remote Access page first.",
	"remote.api.pairing_conflict":   "A pairing session is already active. Cancel it in Phecda or wait for it to expire.",
	"remote.api.pairing_locked":     "Pairing is temporarily locked after repeated failures. Try again later.",
	"remote.api.pairing_mismatch":   "The pairing code is wrong, or the session has expired.",
	"remote.api.pairing_none":       "There is no active pairing session.",
	"remote.api.rate_limited":       "Too many requests. Try again shortly.",
	"remote.api.credential_missing": "A pairing secret (from the QR code or pairing link) is required.",
	"remote.api.device_missing":     "Device information is missing.",
	"remote.api.role_invalid":       "The role is not valid.",
	"remote.api.role_escalation":    "A derived device may not outrank the current device.",
	"remote.api.path_forbidden":     "Remote access does not allow this path.",
	"remote.api.apns_incomplete":    "APNs credentials are incomplete: team_id, key_id, bundle_id and the private key are all required.",
	"remote.api.token_invalid":      "The device token is not valid. Pair again.",
	"remote.api.token_revoked":      "This device has been revoked. Pair again.",
	"remote.api.device_not_found":   "No such device.",
	"remote.api.disabled_hint":      "Remote access is currently off.",
	"remote.api.poll_limit":         "limit must be between 1 and 500.",
	"remote.api.poll_timeout":       "timeout_ms must be between 0 and 55000.",
	"remote.api.push_not_wired":     "The APNs push channel is not wired up yet; stored credentials do not take effect.",

	"remote.word.enabled":  "enabled",
	"remote.word.disabled": "disabled",

	// --- APNs credentials ---
	"remote.err.apns_incomplete": "remote: the APNs credentials are incomplete (team_id, key_id, bundle_id and the private key are required)",
	"remote.err.apns_key_pem":    "remote: the APNs private key is not PEM (it should be a .p8 file)",
	"remote.err.apns_key_type":   "remote: the APNs private key is not a P-256 EC key",
	"remote.err.apns_key_parse":  "remote: the APNs private key cannot be parsed",
	"remote.err.apns_jwt":        "remote: failed to sign the APNs authorisation token: %w",
	"remote.err.apns_no_cipher":  "remote: no cipher is available; refusing to store the APNs private key in the clear",
	"remote.err.apns_read":       "remote: failed to read the APNs credentials: %w",
	"remote.err.apns_decrypt":    "remote: failed to decrypt the APNs credentials (was the master key replaced?): %w",
	"remote.err.apns_decode":     "remote: the APNs credentials are malformed: %w",
	"remote.err.apns_encode":     "remote: failed to serialise the APNs credentials: %w",
	"remote.err.apns_encrypt":    "remote: failed to encrypt the APNs credentials: %w",
	"remote.err.apns_write":      "remote: failed to write the APNs credentials: %w",

	// --- push ---
	"remote.msg.push_no_token":       "this device has no push token registered",
	"remote.msg.push_not_configured": "the kernel has no APNs credentials",

	"remote.push.app_failed_title":    "Site %s failed to start",
	"remote.push.app_unhealthy_title": "Site %s is unhealthy",
	"remote.push.cert_failed_title":   "Certificate for %s failed",
	"remote.push.ddns_failed_title":   "DNS task %s failed to update",
	"remote.push.ip_changed_title":    "Public address changed",
	"remote.push.test_title":          "ISC Mizar test notification",
	"remote.push.test_body":           "If you can see this, the whole push path works.",

	// --- public access (M9) ---
	"remote.public.err.addr_ambiguous":       "remote: this machine has several global IPv6 addresses but the system did not say which are stable (temporary ones rotate). Please pick one manually.",
	"remote.public.err.no_ipv6":              "remote: this machine has no usable global IPv6 address. Public access needs an address routable from the internet.",
	"remote.public.err.unsupported_platform": "remote: this platform cannot tell whether an IPv6 address is stable or temporary",
	"remote.public.err.rand":                 "remote: failed to generate a subdomain: %w",
	"remote.public.err.save":                 "remote: failed to persist the public access record: %w",
	"remote.public.err.no_zone":              "remote: no zone has been chosen for the subdomain yet",
	"remote.public.err.no_writer":            "remote: public access needs DNS credentials but the kernel has no DNS service available",
	"remote.public.err.no_address":           "remote: this machine has no usable public address, so no subdomain can be created",
	"remote.public.err.write":                "remote: failed to write the DNS record (%s): %w",
	"remote.public.err.probe":                "remote: failed to discover the public IPv4 address: %w",
	"remote.public.err.probe_body":           "remote: the public IP echo service returned something unparseable: %q",
	"remote.public.err.probe_not_v4":         "remote: the echo service did not return an IPv4 address: %s",
	"remote.public.err.zones":                "remote: failed to list DNS zones: %w",
	"remote.public.err.zone_missing":         "remote: the chosen zone is no longer in this account (%s)",
	"remote.public.err.disabled":             "remote: public access is not enabled",
	"remote.public.note.no_target":           "There is no public address to probe yet — enable public access in Phecda and sync once.",
	"remote.public.err.no_domain":            "remote: no domain has been chosen for the subdomain yet",
	"remote.public.err.no_resolver":          "remote: the kernel has no credential list, so it cannot match by domain",
	"remote.err.no_cert":                     "remote: no certificate is available",
}
