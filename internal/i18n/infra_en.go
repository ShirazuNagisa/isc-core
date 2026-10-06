package i18n

// infraMessagesEn 覆盖 **DNS 服务层、任务引擎、运行时文件、配置导入导出、
// 服务商注册表** 五块。
//
// 必须与 infraMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var infraMessagesEn = map[string]string{
	// --- DNS service layer ---
	"dns.err.unsupported":    "dns: provider %s does not support %s",
	"dns.err.not_found":      "dns: the record does not exist",
	"dns.op.record_mgmt":     "record management",
	"dns.op.list_zones":      "listing zones",
	"dns.op.list_records":    "listing records",
	"dns.op.create":          "creating records",
	"dns.op.update":          "updating records",
	"dns.op.delete":          "deleting records",
	"dns.err.no_zone_id":     "dns: the zone ID is missing",
	"dns.err.zone_not_found": "dns: no zone %q under this credential (tried both the zone name and ID)",
	"dns.err.no_type":        "dns: the record type cannot be empty",
	"dns.err.no_name":        "dns: the record name cannot be empty",
	"dns.err.no_verify":      "dns: this provider does not support credential verification",

	// --- job engine ---
	"job.err.closed":             "job: the engine is closed and refuses new jobs",
	"job.err.persist":            "job: failed to persist the new job: %w",
	"job.err.drain_timeout":      "job: timed out waiting for in-flight jobs to drain: %w",
	"job.err.not_found":          "job: the job does not exist",
	"job.err.finished":           "job: the job has finished and cannot be cancelled",
	"job.err.gen_id":             "job: failed to generate the job ID: %w",
	"job.err.unknown_kind":       "job: unknown job kind %q",
	"job.err.interrupted":        "The kernel stopped while this job was running",
	"job.err.interrupted_detail": "The job did not finish: the kernel shut down while it was executing. Run it again.",

	// --- app hosting ---
	"apps.error.docker_required": "This deployment method needs Docker Desktop, but none was found on this machine. Install and start Docker Desktop, or choose another method.",

	// --- runtime provisioning (D28) ---
	"runtime.msg.using_bundled": "Installing the bundled %s %s",
	"runtime.msg.downloading":   "Downloading %s %s%s",
	"runtime.msg.extracting":    "Extracting %s %s",
	"runtime.msg.ready":         "%s %s is ready",
	"runtime.msg.using_system":  "Using the system-installed %s %s",

	// --- runtime file ---
	"runtime.err.mkdir":    "runtimeinfo: failed to create the runtime directory: %w",
	"runtime.err.marshal":  "runtimeinfo: failed to serialise: %w",
	"runtime.err.tempfile": "runtimeinfo: failed to create the temporary file: %w",
	"runtime.err.chmod":    "runtimeinfo: failed to set the permissions: %w",
	"runtime.err.write":    "runtimeinfo: failed to write: %w",
	"runtime.err.sync":     "runtimeinfo: failed to flush to disk: %w",
	"runtime.err.close":    "runtimeinfo: failed to close the temporary file: %w",
	"runtime.err.rename":   "runtimeinfo: failed to replace %s: %w",
	"runtime.err.parse":    "runtimeinfo: failed to parse %s: %w",

	// --- configuration import/export ---
	"configio.err.marshal":         "configio: failed to serialise the export: %w",
	"configio.err.too_new":         "%w: the file format version is %d but this kernel supports at most %d (please upgrade the kernel)",
	"configio.err.no_provider":     "provider or label is missing",
	"configio.err.unknown_prov":    "unknown provider %q",
	"configio.err.unrecognised":    "configio: unrecognised configuration document",
	"configio.ddnsgo.no_provider":  "that entry does not specify a provider",
	"configio.ddnsgo.unknown_prov": "the kernel does not know the provider %q",
	"configio.ddnsgo.migrated": "%d enabled dynamic DNS entries were found; " +
		"migrating and scheduling them takes effect once M2 is wired up",
	"configio.ddnsgo.not_ddnsgo": "configio: this is not a ddns-go configuration (no dnsconf section)",

	// --- provider registry ---
	"provider.name.alidns":       "Alibaba Cloud DNS",
	"provider.name.tencentcloud": "Tencent Cloud DNS",
	"provider.name.huaweicloud":  "Huawei Cloud DNS",
	"provider.name.ali_esa":      "Alibaba Cloud ESA",
	"provider.name.baiducloud":   "Baidu Cloud DNS",
	"provider.name.callback":     "Callback (custom webhook)",
	"provider.name.edgeone":      "Tencent EdgeOne",
	"provider.name.rainyun":      "RainYun",
	"provider.name.volcengine":   "Volcengine TrafficRoute",
	"provider.err.dup":           "provider: provider %q is registered twice",
	"provider.err.no_impl":       "provider: no implementation is registered for %s",
	"provider.err.no_ip":         "provider: no IP was given",

	// --- verification (Cloudflare's standalone implementation) ---
	"provider.cf.need_token":     "provider: Cloudflare needs an API token",
	"provider.cf.build_failed":   "provider: failed to build the verification request: %w",
	"provider.cf.connect_failed": "provider: failed to connect to Cloudflare: %w",
	"provider.cf.read_failed":    "provider: failed to read the Cloudflare response: %w",
	"provider.cf.bad_body":       "provider: Cloudflare returned an unparseable response (HTTP %d)",
	"provider.cf.rejected":       "provider: Cloudflare rejected this credential: %s",
	"provider.cf.bad_status":     "provider: Cloudflare returned HTTP %d",
	"provider.cf.not_active":     "provider: the Cloudflare token status is %q, not active",
	"provider.cf.no_detail":      "no error detail was provided",
	"provider.cf.global_key":     "provider: Cloudflare rejected the request headers for this credential. The usual cause is a Global API Key (37 hex characters), which cannot be used as an API Token. Create one under My Profile -> API Tokens (the \"Edit zone DNS\" template works well).",
}
