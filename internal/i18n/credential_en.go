package i18n

// credentialMessagesEn 覆盖**凭据、主密钥与路径**三块。
//
// 必须与 credentialMessagesZh 的 key 集合完全一致 —— 由目录完整性测试强制保证。
var credentialMessagesEn = map[string]string{
	// --- credential validation ---
	"cred.err.no_label":           "credential: the label cannot be empty",
	"cred.err.no_provider":        "credential: the provider cannot be empty",
	"cred.err.no_fields":          "credential: a required field is missing",
	"cred.err.extra_fields":       "credential: there are undeclared fields",
	"cred.err.not_found":          "credential: the credential does not exist",
	"cred.err.dup_label":          "credential: the label is already used under this provider",
	"cred.err.in_use":             "credential: the credential is still used by %d task(s)",
	"cred.err.immutable_provider": "%w: the provider cannot be changed (currently %s, requested %s)",
	"cred.err.check_refs":         "credential: failed to check references: %w",
	"cred.err.marshal":            "credential: failed to serialise the fields: %w",
	"cred.err.encrypt":            "credential: failed to encrypt the fields: %w",
	"cred.err.decrypt":            "credential: failed to decrypt the fields of %s: %w",
	"cred.err.parse_fields":       "credential: failed to parse the fields of %s: %w",
	"cred.err.gen_id":             "credential: failed to generate the ID: %w",
	"cred.err.unknown_provider":   "credential: unknown provider",
	"cred.err.provider_locked":    "credential: the provider cannot be changed",

	// --- master key and envelope encryption ---
	"secret.err.nil_store":   "secret: the key store is nil",
	"secret.err.read_master": "secret: failed to read the master key: %w",
	"secret.err.bad_len": "secret: the master key has an unexpected length (expected %d bytes, got %d); " +
		"the key store may be corrupted — delete the master key and enter the credentials again",
	"secret.err.gen_master":  "secret: failed to generate the master key: %w",
	"secret.err.save_master": "secret: failed to save the master key: %w",
	"secret.err.gen_nonce":   "secret: failed to generate the nonce: %w",
	"secret.err.short":       "secret: the ciphertext is too short to be a valid envelope",
	"secret.err.bad_version": "secret: unsupported ciphertext format version %d",
	"secret.err.decrypt": "secret: decryption failed (the ciphertext is corrupted, or the current master key " +
		"differs from the one used to encrypt it; if you just moved the data directory, enter the credentials again)",
	"secret.err.bad_key_len": "secret: the master key has an unexpected length (%d bytes)",
	"secret.err.new_aes":     "secret: failed to construct AES: %w",
	"secret.err.new_gcm":     "secret: failed to construct GCM: %w",

	// --- paths ---
	"paths.err.data_dir":   "paths: failed to resolve the data directory %q: %w",
	"paths.err.config_dir": "paths: failed to resolve the config directory %q: %w",
	"paths.err.mkdir":      "paths: failed to create the directory %q: %w",
	"paths.err.no_home":    "paths: cannot determine the user's home directory: %w",
	"paths.warn.chmod":     "could not tighten the permissions of %s to 0700: %v",
	"paths.warn.acl": "failed to tighten the access permissions of %s; the kernel still runs, " +
		"but the token may be readable by other users: %v",
	"paths.err.acl_build":  "failed to build the access control list: %w",
	"paths.err.acl_apply":  "failed to set the directory security information: %w",
	"paths.err.sid_lookup": "failed to resolve the built-in account SID (type %d): %w",
}
