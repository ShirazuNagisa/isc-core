package i18n

// reachMessagesEn 是可达性检查（`isc doctor` 的正文）的消息。
//
// 必须与 reachMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var reachMessagesEn = map[string]string{
	// --- check names ---
	"reach.check.global_v6":   "Global IPv6 address",
	"reach.check.prefix":      "IPv6 delegated prefix",
	"reach.check.firewall":    "Local firewall backend",
	"reach.check.upstream":    "Upstream reachability",
	"reach.check.port_range":  "Port range",
	"reach.check.listening":   "Service listening",
	"reach.port.confirm_hint": "Confirm for yourself that the service on that port has been started.",
	"reach.check.portmap":     "Port mapping",
	"reach.check.lowport":     "Low-port permission",
	"reach.check.unimpl":      "Not implemented",

	// --- the IPv6-native approach ---
	"reach.v6.name": "IPv6 direct",
	"reach.v6.desc": "Serve directly from this machine's public IPv6 address.\n" +
		"No public IPv4 needed, no relay server, and no third party sees the traffic.\n" +
		"It requires the ISP to delegate an IPv6 prefix and the router to allow inbound connections.",

	// --- global IPv6 address ---
	"reach.v6.found":     "Found %d IPv6 address(es) on %s usable for public access",
	"reach.v6.not_found": "No IPv6 address usable for public access was found",
	"reach.v6.not_found_hint": "(link-local fe80:: and private fd00:: do not count)\n" +
		"Confirm the ISP has IPv6 enabled (most home broadband has it by default — the helpdesk can confirm); " +
		"then check the router's IPv6 settings: it must be on and in \"Native\" mode rather than a tunnel",

	// --- delegated prefix ---
	"reach.prefix.found":   "Found %d delegated prefix(es) (/64 or coarser)",
	"reach.prefix.no_addr": "there is no global IPv6 address, so the prefix cannot be determined",
	"reach.prefix.no_prefix": "There is a global IPv6 address but no delegated prefix of /64 or coarser. " +
		"That usually means the ISP assigns addresses per device (/128), so the address changes on reboot or when the device changes",
	"reach.prefix.no_prefix_hint": "Dynamic DNS still works in that case, but addresses change more often. " +
		"Consider shortening the detection interval",

	// --- firewall backend ---
	"reach.fw.unimplemented": "The firewall backend for this platform (%s) is not implemented, so the kernel cannot open ports automatically",
	"reach.fw.unimpl_hint": "This is a gap in the kernel, not a mistake in your configuration. " +
		"Open the ports you need in the system firewall by hand, or wait for a later release",
	"reach.fw.ready": "Wired up to %s; opening a port builds a previewable plan and rolls back automatically on failure",
	"reach.fw.manual_allow": "reach: the firewall backend for this platform (%s) is not implemented, so the port cannot be opened automatically; " +
		"open %d/%s in the system firewall by hand",

	// --- conclusion ---
	"reach.summary.ready":   "This machine meets the prerequisites for IPv6 direct; whether the internet can reach it must be confirmed from a phone on mobile data",
	"reach.summary.pending": "One step remains on this machine:",

	// --- plan and rollback ---
	"reach.plan.title":        "Allow inbound %d/%s",
	"reach.plan.notes_a":      "The rule opens this one port only and leaves other rules alone.",
	"reach.plan.notes_b":      "Undoing it restores the state from before it was added.",
	"reach.plan.irreversible": "This backend reports the change as not undoable; it will need manual cleanup after it is applied.",
	"reach.plan.diff":         "Inbound %s %s (any source)",
	"reach.diff_failed":       "reach: failed to compute the firewall diff: %w",
	"reach.rollback.no_backend": "reach: cannot undo %s automatically: the firewall backend for this platform is unavailable. " +
		"Delete the rules starting with %q from the system firewall by hand",
	"reach.rollback.no_payload": "reach: cannot undo %s automatically: this change has no saved rollback data " +
		"(it may have been created by an older kernel). Delete the rules starting with %q from the system firewall by hand",
	"reach.rollback.failed": "reach: failed to undo firewall change %s: %w",

	// --- upstream reachability ---
	"reach.upstream.blocked": "This is not something you can fix on this machine. You can try: " +
		"a different port (ISPs often block only specific ports), another reachability method, or asking your ISP to confirm",
	"reach.upstream.detail": "Not testable from this machine: reaching your own public address from here " +
		"usually goes over loopback, so it reports success whether or not the upstream allows it",
	"reach.upstream.unknown": "Open the verification address on a phone with 4G/5G. " +
		"Do not skip this — it is the only way to tell \"not configured here\" from \"blocked by the ISP\"",

	// --- argument validation ---
	"reach.bad_port":       "port %d is out of range",
	"reach.bad_port_range": "port %d is not within 1-65535",
	"reach.bad_port_hint":  "Enter a valid port.",
	"reach.bad_proto":      "reach: unsupported protocol %q (only tcp / udp)",
	"reach.udp_no_probe":   "%s ports cannot be probed by connecting (UDP has no notion of a connection)",

	// --- port probing ---
	"reach.port.listening":     "something is listening on port %d",
	"reach.port.not_listening": "nothing is listening on port %d",
	"reach.port.interrupted":   "the probe was interrupted",
	"reach.port.no_listener":   "reach: nothing is listening on that port",
	"reach.port.no_listener_hint": "With the rule open but no local service, nothing will get through from outside either — " +
		"and at that point it is hard to suspect this machine. " +
		"If the service is not started yet, start it; if it is not deployed yet, opening the port now is fine.",

	// --- port mapping ---
	"reach.portmap.passthrough": "external %d → internal %d (passthrough)",
	"reach.portmap.mapped":      "external %d → internal %d",
	"reach.portmap.hint": "A non-standard entry point: the port must be given explicitly when visiting, " +
		"e.g. https://example.com:8443",

	// --- low ports ---
	"reach.lowport.not_privileged": "%d is not a privileged port, so no extra permission is needed",
	"reach.lowport.ok":             "%d can be bound (%s)",
	"reach.lowport.denied": "Binding %d needs extra permission. Three options:\n" +
		"  · use a port ≥1024 (simplest; the cost is having to include the port when visiting)\n" +
		"  · grant it to the executable: sudo setcap 'cap_net_bind_service=+ep' /path/to/isc\n" +
		"  · run the kernel as root",

	// --- unavailable ---
	"reach.unavailable": "reach: this method is not available",
}
