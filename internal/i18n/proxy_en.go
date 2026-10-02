package i18n

// proxyMessagesEn 是**内置反向代理**的消息。
//
// 必须与 proxyMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var proxyMessagesEn = map[string]string{
	// --- lifecycle ---
	"proxy.err.bad_port":    "proxy: the listening port %d is invalid",
	"proxy.err.listen":      "proxy: failed to listen on port %d: %w (the port may already be in use by another program)",
	"proxy.err.not_running": "proxy: the proxy is not running",
	"proxy.err.load_routes": "proxy: failed to load the routes: %w",

	// --- route validation ---
	"proxy.err.dup_domain": "proxy: domain %s is used by two routes at once (%s and %s) — " +
		"a domain can point at only one upstream, otherwise which one gets the request depends on an invisible order",
	"proxy.err.no_route_id": "proxy: the route has no ID",
	"proxy.err.no_domain":   "proxy: a route needs at least one domain",

	// --- domain format ---
	"proxy.err.empty_domain": "proxy: the domain cannot be empty",
	"proxy.err.bad_chars":    "proxy: the domain contains illegal characters: %q",
	"proxy.err.wildcard_pos": "proxy: a wildcard may only be written as *.example.com " +
		"(it must come first): %q",
	"proxy.err.bad_pattern": "proxy: invalid domain pattern: %q",

	// --- upstream address (validation and resolution) ---
	"proxy.err.upstream_empty":   "proxy: the upstream address cannot be empty",
	"proxy.err.upstream_private": "proxy: the upstream address must be a loopback or private address",
	"proxy.err.self_loop": "proxy: the upstream points at the proxy itself (port %d) — " +
		"that would loop forever; give the port of the service that actually serves it",
	"proxy.err.need_port":        "proxy: the upstream address must include a port (e.g. 127.0.0.1:8096): %s",
	"proxy.err.bad_upstream_fmt": "proxy: the upstream address is malformed: %w",
	"proxy.err.bad_scheme":       "proxy: unsupported scheme: %s",
	"proxy.err.bad_port_str":     "proxy: invalid port: %s",
	"proxy.err.zero_port":        "proxy: the port cannot be 0",
	"proxy.err.no_host":          "proxy: the upstream address has no host part",
	"proxy.err.resolve_host":     "proxy: failed to resolve the upstream host %q: %w",
	"proxy.err.no_addr":          "proxy: the upstream host %q resolved to no addresses",
	"proxy.err.bad_target_fmt":   "proxy: the target address is malformed: %w",
	"proxy.err.no_upstream_addr": "no upstream address is available",
	"proxy.err.resolve_failed":   "proxy: failed to resolve the upstream address: %w",
	"proxy.err.denied_detail":    "%w: %s resolves to %s",
	"proxy.err.upstream_denied":  "the upstream address is not allowed",

	// https upstreams. Traffic between local services never leaves the
	// machine, so TLS only adds configuration burden.
	"proxy.err.no_https": "proxy: https upstreams are not supported — traffic between local services " +
		"never leaves the machine, so TLS would only cost you a self-signed certificate for no real gain",

	// --- while forwarding ---
	"proxy.err.no_connect":    "CONNECT is not supported",
	"proxy.err.no_rule":       "no forwarding rule is configured for this domain",
	"proxy.err.bad_forward":   "the forwarding configuration is wrong",
	"proxy.err.connect_local": "could not connect to the local service (is it running? is the port right?)",

	// --- TLS ---
	"proxy.err.no_sni": "proxy: the TLS handshake provided no SNI host name, so no certificate can be chosen. " +
		"Visit it by domain name rather than by IP",
	"proxy.err.need_cert_source": "proxy: enabling HTTPS needs a certificate source",
}
