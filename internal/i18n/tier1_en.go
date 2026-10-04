package i18n

// tier1MessagesEn 是 Tier-1 六家服务商（全量 CRUD）的消息。
//
// 必须与 tier1MessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var tier1MessagesEn = map[string]string{
	// --- operation names (used as the "%s failed: %s" prefix) ---
	"tier1.op.list_zones":    "list zones",
	"tier1.op.list_records":  "list records",
	"tier1.op.get_record":    "read record",
	"tier1.op.create_record": "create record",
	"tier1.op.update_record": "update record",
	"tier1.op.delete_record": "delete record",
	"tier1.op.verify":        "verify credential",
	"tier1.op.list_domains":  "list domains",
	"tier1.op.default":       "default",

	// --- HTTP stages ---
	"tier1.http.serialize":    "%s: failed to serialise the request body: %w",
	"tier1.http.build":        "%s: failed to build the request: %w",
	"tier1.http.sign":         "%s: failed to sign the request: %w",
	"tier1.http.request":      "%s: request failed: %w",
	"tier1.http.read":         "%s: failed to read the response: %w",
	"tier1.http.parse":        "%s: failed to parse the response: %w",
	"tier1.http.op_failed":    "tier1: %s failed: %s",
	"tier1.no_error_detail":   "no error detail was provided",
	"tier1.vendor_no_message": "the provider gave no error message",
	"tier1.dynamic_unwired":   "tier1: the dynamic DNS implementation is not wired up (assembly gap)",

	// --- shared validation ---
	"tier1.need_type":          "tier1: the record type cannot be empty",
	"tier1.need_name":          "tier1: the record name cannot be empty",
	"tier1.need_content":       "tier1: the record content cannot be empty",
	"tier1.need_record_id":     "tier1: updating a record needs a record ID",
	"tier1.need_id_delete":     "tier1: deleting a record needs a record ID",
	"tier1.ali.need_id_update": "tier1: updating a record needs a record ID",

	// --- Cloudflare ---
	"tier1.cf.need_token":          "tier1: Cloudflare needs an API token",
	"tier1.cf.rejected":            "tier1: Cloudflare rejected this credential: %s",
	"tier1.cf.global_key":          "tier1: Cloudflare rejected the request headers for this credential. The usual cause is a Global API Key (37 hex characters), which cannot be used as an API Token. Create one under My Profile -> API Tokens (the \"Edit zone DNS\" template works well).",
	"tier1.cf.token_not_active":    "tier1: the Cloudflare token status is %q, not active",
	"tier1.cf.list_zones_failed":   "tier1: failed to list zones: %s",
	"tier1.cf.zones_bad_shape":     "tier1: the zone list response has an unexpected shape",
	"tier1.cf.list_records_failed": "tier1: failed to list records: %s",
	"tier1.cf.records_bad_shape":   "tier1: the record list response has an unexpected shape",
	"tier1.cf.create_failed":       "tier1: failed to create the record: %s",
	"tier1.cf.update_failed":       "tier1: failed to update the record: %s",
	"tier1.cf.delete_failed":       "tier1: failed to delete the record: %s",
	"tier1.cf.record_bad_shape":    "tier1: the record response has an unexpected shape",
	"tier1.cf.record_parse":        "tier1: failed to parse the record response: %w",

	// --- Alibaba Cloud ---
	"tier1.ali.display_name":       "Alibaba Cloud DNS",
	"tier1.ali.need_credentials":   "tier1: Alibaba Cloud needs an AccessKey ID and AccessKey Secret",
	"tier1.ali.need_zone_name":     "tier1: listing records needs the zone name (Alibaba Cloud locates records by domain)",
	"tier1.ali.no_record_id":       "tier1: Alibaba Cloud returned no ID for the new record, so the write cannot be confirmed",
	"tier1.ali.need_zone_for_host": "tier1: the zone name is needed to split the record name into a host record",
	"tier1.ali.name_outside_zone": "tier1: the record name %q does not belong to the zone %q " +
		"(use the full domain, e.g. www.%s)",

	// --- DNSPod ---
	"tier1.dnspod.default_line": "default",
	"tier1.dnspod.need_credentials": "tier1: DNSPod needs both the API ID and the API Token fields " +
		"(the login token is the two concatenated)",
	"tier1.dnspod.create_bad_shape": "tier1: the DNSPod create-record response has an unexpected shape (no record field)",
	"tier1.dnspod.no_record_id":     "tier1: DNSPod returned no ID for the new record, so it cannot be located",
	"tier1.dnspod.need_current": "tier1: the current content of record %s must be read before updating it " +
		"(DNSPod requires the record line, and the semantics of omitted fields are undocumented): %w",
	"tier1.dnspod.record_bad_shape": "tier1: the DNSPod record response has an unexpected shape (no record field)",
	"tier1.dnspod.need_zone": "tier1: %s needs a zone ID or zone name " +
		"(the DNSPod API locates a domain by domain_id or domain)",
	"tier1.dnspod.need_zone_for_host": "tier1: the zone name is needed to translate the record name %q " +
		"into a DNSPod host record (without it DNSPod assumes the root @, so it cannot be guessed)",
	"tier1.dnspod.name_outside_zone": "tier1: the record name %q is not under the zone %q, " +
		"so the DNSPod host record cannot be computed",
	"tier1.dnspod.ttl_out_of_range": "tier1: the TTL %d is outside the range DNSPod allows (%d-%d seconds); " +
		"the minimum also differs by plan: 600s on Free, 60s on Pro, 1s on Enterprise",

	// --- GoDaddy ---
	"tier1.gd.need_id_update": "tier1: updating a record needs a record ID (GoDaddy has no native ID; " +
		"use the ID returned by ListRecords)",
	"tier1.gd.need_id_delete": "tier1: deleting a record needs a record ID (GoDaddy has no native ID; " +
		"use the ID returned by ListRecords)",
	"tier1.gd.id_mismatch": "tier1: the record ID does not match the record being updated: the ID points at " +
		"%s/%s but %s/%s was submitted. GoDaddy has no native record ID — the ID is composed from " +
		"type|name|data, so it stops being valid once the record value changes. List the records again and retry",
	"tier1.gd.not_found": "tier1: that domain or record does not exist on GoDaddy's side; nothing was deleted " +
		"(the record may also have been deleted or renamed — list the records again): %w",
	"tier1.gd.bad_id": "tier1: cannot parse the record ID %q: GoDaddy has no native record ID — " +
		"the ID is composed from type|name|data, so use the ID returned by ListRecords",
	"tier1.gd.id_parse":    "tier1: cannot parse the record ID %q: %w",
	"tier1.gd.id_missing":  "tier1: the record ID %q is missing its type or name part",
	"tier1.gd.need_domain": "tier1: a domain is required (on GoDaddy the zone is the domain itself)",
	"tier1.gd.name_outside_domain": "tier1: the record name %q is not under the domain %q — " +
		"GoDaddy record names are relative to the domain (the root record is @); check that the domain is complete",
	"tier1.gd.record_no_type":   "tier1: the record has no type",
	"tier1.gd.record_no_name":   "tier1: the record has no name",
	"tier1.gd.need_credentials": "tier1: GoDaddy needs an API Key and API Secret",

	// --- Huawei Cloud ---
	"tier1.hw.display_name":     "Huawei Cloud DNS",
	"tier1.hw.need_credentials": "tier1: Huawei Cloud needs both access_key_id and access_key_secret",
	"tier1.hw.need_zone_list":   "tier1: listing records needs a zone ID",
	"tier1.hw.need_zone_create": "tier1: creating a record needs a zone ID",
	"tier1.hw.need_zone_update": "tier1: updating a record needs a zone ID",
	"tier1.hw.need_zone_delete": "tier1: deleting a record needs a zone ID",
	"tier1.hw.need_id_update":   "tier1: updating a record needs a record ID (a record set ID on Huawei Cloud)",
	"tier1.hw.need_id_delete":   "tier1: deleting a record needs a record ID (a record set ID on Huawei Cloud)",

	// --- Tencent Cloud ---
	"tier1.tc.default_line": "default",
	"tier1.tc.display_name": "Tencent Cloud DNS",
	"tier1.tc.no_record_id": "tier1: the record was created but the provider returned no record number",
	"tier1.tc.id_mismatch": "tier1: the number returned by the update (%d) differs from the one requested (%d); " +
		"confirm the record's state in the console",
	"tier1.tc.need_credentials": "tier1: Tencent Cloud needs a SecretId and SecretKey " +
		"(the credential fields secret_id / secret_key)",
	"tier1.tc.need_zone_name": "tier1: Tencent Cloud needs the zone name (domain); a zone ID alone cannot locate records",
	"tier1.tc.need_record_id": "tier1: Tencent Cloud needs a record ID",
	"tier1.tc.bad_record_id":  "tier1: the record ID %q is not a Tencent Cloud record number (it must be decimal)",
}
