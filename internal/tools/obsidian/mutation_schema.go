package obsidian

func mutationPreconditionSchema(allowFingerprint bool) map[string]any {
	variants := []any{objectSchema(map[string]any{"kind": constStringSchema(MutationPreconditionAbsent)}, "kind")}
	if allowFingerprint {
		variants = append(variants, objectSchema(map[string]any{
			"kind":        constStringSchema(MutationPreconditionFingerprint),
			"fingerprint": fingerprintSchema(),
		}, "kind", "fingerprint"))
	}
	return map[string]any{"description": "one exact concurrency precondition; no fields from another variant are accepted", "oneOf": variants}
}

func fingerprintSchema() map[string]any {
	return map[string]any{"type": "string", "description": "opaque 43-character unpadded base64url source fingerprint returned by stat, read, write, edit, or move", "minLength": 43, "maxLength": 43}
}

func mutationEncodingSchema() map[string]any {
	return map[string]any{"type": "string", "description": "explicit value encoding; use utf8 for valid UTF-8 text or base64 for canonical standard base64 bytes", "enum": []string{MutationEncodingUTF8, MutationEncodingBase64}}
}

func mutationValueSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func statInputSchema() map[string]any {
	return objectSchema(map[string]any{"path": stringSchema("existing regular file or empty directory relative to base, or to the vault root when base is omitted"), "base": stringSchema("optional vault-relative working directory used to resolve path and express the returned path")}, "path")
}

func writeInputSchema() map[string]any {
	return objectSchema(map[string]any{
		"path": stringSchema("file to create or replace relative to base, or to the vault root when base is omitted"), "base": stringSchema("optional vault-relative working directory used to resolve path and express the returned path"),
		"encoding": mutationEncodingSchema(), "value": mutationValueSchema("complete decoded file value, at most 524288 bytes"),
		"precondition": mutationPreconditionSchema(true),
	}, "path", "encoding", "value", "precondition")
}

func editInputSchema() map[string]any {
	replacement := objectSchema(map[string]any{"old": nonEmptyStringSchema("non-empty exact original value"), "new": mutationValueSchema("replacement value")}, "old", "new")
	return objectSchema(map[string]any{
		"path": stringSchema("existing regular file to patch relative to base, or to the vault root when base is omitted"), "base": stringSchema("optional vault-relative working directory used to resolve path and express the returned path"),
		"fingerprint": fingerprintSchema(), "encoding": mutationEncodingSchema(),
		"replacements": map[string]any{"type": "array", "description": "one through 64 ordered exact replacements validated together against the original source", "items": replacement, "minItems": 1, "maxItems": MutationMaxReplacements},
	}, "path", "fingerprint", "encoding", "replacements")
}

func moveInputSchema() map[string]any {
	return objectSchema(map[string]any{
		"source": stringSchema("existing source file or empty directory relative to base, or to the vault root when base is omitted"), "destination": stringSchema("absent destination relative to the same base"), "base": stringSchema("optional vault-relative working directory used for both paths and the returned destination path"),
		"fingerprint": fingerprintSchema(), "destination_precondition": mutationPreconditionSchema(false),
	}, "source", "destination", "fingerprint", "destination_precondition")
}

func deleteInputSchema() map[string]any {
	return objectSchema(map[string]any{"path": stringSchema("existing regular file or empty directory to permanently remove relative to base, or to the vault root when base is omitted"), "base": stringSchema("optional vault-relative working directory used to resolve path and express the returned path"), "fingerprint": fingerprintSchema()}, "path", "fingerprint")
}
