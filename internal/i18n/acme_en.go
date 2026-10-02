package i18n

// acmeMessagesEn 是 ACME（Let's Encrypt 等）证书签发的消息。
//
// 必须与 acmeMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var acmeMessagesEn = map[string]string{
	// --- shared ---
	"acme.need_domain": "acme: at least one domain must be given",
	"acme.need_cred":   "acme: no credential was given for DNS-01 validation",
	"acme.cred_failed": "acme: failed to load the credential: %w",

	// --- client: files and keys ---
	"acme.client.mkdir_failed":        "acme: failed to create the certificate directory %s: %w",
	"acme.client.cert_parse":          "acme: the certificate file %s cannot be parsed: %w",
	"acme.client.write_failed":        "acme: failed to write %s: %w",
	"acme.client.not_pem":             "acme: the certificate is not valid PEM",
	"acme.client.gen_account_key":     "acme: failed to generate the account key: %w",
	"acme.client.marshal_account_key": "acme: failed to serialise the account key: %w",
	"acme.client.mkdir_account_key":   "acme: failed to create the account key directory: %w",
	"acme.client.gen_cert_key":        "acme: failed to generate the certificate key: %w",
	"acme.client.gen_csr":             "acme: failed to generate the CSR: %w",
	"acme.client.bad_key_type":        "acme: unsupported key type",
	"acme.client.marshal_cert_key":    "acme: failed to serialise the certificate key: %w",
	"acme.client.account_key_parse": "acme: the account key file %s cannot be parsed. " +
		"Deleting it permanently invalidates this ACME account " +
		"(certificates already issued could no longer be renewed), so back it up and be sure first",

	// --- client: the protocol flow ---
	"acme.client.register_failed":  "acme: failed to register the account: %w",
	"acme.client.order_failed":     "acme: failed to create the order: %w",
	"acme.client.csr_failed":       "acme: failed to submit the CSR: %w",
	"acme.client.authz_failed":     "acme: failed to read the authorisation: %w",
	"acme.client.no_dns01":         "acme: the authorisation for domain %s offers no DNS-01 challenge (available: %v)",
	"acme.client.challenge_failed": "acme: failed to compute the challenge value: %w",
	"acme.client.cleanup_failed": "acme: failed to clean up the challenge record for domain %s " +
		"(you can delete the _acme-challenge record by hand): %v\n",
	"acme.client.notify_failed": "acme: failed to tell the CA the challenge is ready: %w",

	// The most important message in this package: it has to let the user
	// locate the problem themselves.
	"acme.client.dns01_rejected": "acme: DNS-01 validation for domain %s did not pass: %w\n" +
		"Common causes:\n" +
		"  · the domain's authoritative DNS is not the selected provider (check the NS records)\n" +
		"  · the record has not finished propagating at the provider (retry later)\n" +
		"  · the credential lacks permission to edit this domain\n" +
		"  · the domain itself does not exist or has expired",

	// --- DNS-01 record writing ---
	"acme.dns01.no_list_zones":     "acme: provider %s cannot list zones, so the DNS zone for %s cannot be located",
	"acme.dns01.no_create":         "acme: provider %s cannot create records",
	"acme.dns01.list_zones_failed": "acme: failed to list zones: %w",
	"acme.dns01.zone_not_found": "acme: among the %d zones linked to credential \"%s\", none contains %s. " +
		"Check that this domain is on that credential's account",
	"acme.dns01.write_failed":     "acme: failed to write the challenge record %s: %w",
	"acme.dns01.impl_unavailable": "acme: the implementation for provider %s is unavailable",
	"acme.dns01.no_delete":        "acme: provider %s cannot delete records",
	"acme.dns01.impl_for_dns01":   "acme: the implementation for provider %s is unavailable, so DNS-01 validation cannot complete",

	// --- manager ---
	"acme.manager.empty_domain": "acme: the domain is empty",
	"acme.manager.bad_chars":    "acme: the domain contains illegal characters: %q",
	"acme.manager.single_label": "acme: %q does not look like a full domain; " +
		"public CAs do not issue certificates for single-label domains",
	"acme.manager.wildcard_pos":    "acme: a wildcard may only appear first (*.example.com): %q",
	"acme.manager.not_covering":    "the existing certificate does not cover %s",
	"acme.manager.no_expiry":       "the certificate's validity period cannot be read, so it is reissued to be safe",
	"acme.manager.expired":         "the certificate expired on %s",
	"acme.manager.below_threshold": "the remaining validity is %d day(s), below the renewal threshold of %d day(s)",
	"acme.manager.issuing":         "acme: the certificate for %s is already being issued; please wait",
	"acme.manager.read_failed":     "the certificate file cannot be read:",

	// --- TLS provider (finds the certificate by SNI during the handshake) ---
	"acme.provider.no_sni":      "acme: the TLS handshake did not provide a domain name",
	"acme.provider.no_routes":   "acme: no HTTPS route has been configured yet",
	"acme.provider.read_failed": "acme: failed to read the certificate for domain %s (certificate name %s): %w",
	"acme.provider.no_cert": "acme: no certificate is configured for domain %s. " +
		"Add an HTTPS-enabled route for it",
	"acme.provider.mismatch": "acme: the certificate and private key for domain %s do not match (certificate name %s): %w. " +
		"Reissue this certificate",
}
