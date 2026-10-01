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
}
