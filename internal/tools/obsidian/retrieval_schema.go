package obsidian

// Retrieval input schemas are written explicitly because selector and batch
// request objects are closed tagged unions. Go's struct inference cannot
// express the mutually exclusive selector fields without weakening the public
// grammar that the SDK validates before a handler runs.

func readInputSchema() map[string]any {
	return objectSchema(map[string]any{
		"path":      stringSchema("canonical vault-relative Markdown path returned by resolve, ls, or grep"),
		"base":      stringSchema("optional vault-relative base used only to resolve path"),
		"selector":  withDefault(selectorSchema(), map[string]any{"kind": SelectorContent, "start_line": 1}),
		"max_bytes": integerSchema("selected source-byte work for this call; this never widens the complete SDK result cap", 1, MaxReadBytes, DefaultReadBytes),
		"cursor":    stringSchema("coverage.next_cursor from the prior read page; repeat path, base, selector, and max_bytes unchanged"),
	}, "path")
}

func readManyInputSchema() map[string]any {
	request := objectSchema(map[string]any{
		"path":      stringSchema("canonical vault-relative Markdown path returned by resolve, ls, or grep"),
		"base":      stringSchema("optional vault-relative base used only to resolve path"),
		"selector":  withDefault(selectorSchema(), map[string]any{"kind": SelectorContent, "start_line": 1}),
		"max_bytes": integerSchema("selected source-byte work for this item", 1, MaxReadBytes, DefaultReadBytes),
	}, "path")
	return objectSchema(map[string]any{
		"requests": map[string]any{
			"type":        "array",
			"description": "one to 20 first-call read request shapes processed strictly in order",
			"items":       request,
			"minItems":    1,
			"maxItems":    MaxReadManyRequests,
		},
		"max_bytes": integerSchema("aggregate selected source-byte work for this page", 1, MaxReadManyBytes, DefaultReadManyBytes),
		"cursor":    stringSchema("coverage.next_cursor from the prior read_many page; repeat the identical ordered requests and max_bytes"),
	}, "requests")
}

func grepInputSchema() map[string]any {
	return objectSchema(map[string]any{
		"pattern": map[string]any{
			"type":        "string",
			"description": "non-empty UTF-8 Go RE2 pattern or literal, at most 4096 bytes; never retained in telemetry",
			"minLength":   1,
			"maxLength":   MaxGrepPatternBytes,
		},
		"path":           withDefault(stringSchema("vault-relative directory or Markdown file scope"), "."),
		"base":           stringSchema("optional vault-relative base used only to resolve path"),
		"regex":          withDefault(map[string]any{"type": "boolean", "description": "true for Go RE2 syntax and false for a literal"}, true),
		"case_sensitive": withDefault(map[string]any{"type": "boolean", "description": "case-sensitive matching when true"}, false),
		"context_lines":  integerSchema("bounded source-line evidence before and after each match", 0, MaxGrepContextLines, DefaultGrepContextLines),
		"limit":          integerSchema("matching-line result limit", 1, MaxGrepLimit, DefaultGrepLimit),
		"max_files":      integerSchema("Markdown files opened for new content work", 1, MaxGrepMaxFiles, DefaultGrepMaxFiles),
		"max_bytes":      integerSchema("source bytes read for new content work", 1, MaxGrepMaxBytes, DefaultGrepMaxBytes),
		"cursor":         stringSchema("coverage.next_cursor from the prior grep page; repeat every other field unchanged"),
	}, "pattern")
}

func selectorSchema() map[string]any {
	return map[string]any{
		"description": "one closed Markdown source-unit selector; fields from other selector kinds are rejected",
		"oneOf": []any{
			objectSchema(map[string]any{
				"kind":       constStringSchema(SelectorContent),
				"start_line": integerSchema("one-based first physical source line", 1, MaxMarkdownSourceLines, 1),
			}, "kind"),
			objectSchema(map[string]any{
				"kind":       constStringSchema(SelectorHeading),
				"heading":    nonEmptyStringSchema("NFC-normalized trimmed case-sensitive heading text"),
				"occurrence": integerSchema("one-based occurrence when headings repeat", 1, MaxMarkdownSourceLines, 1),
			}, "kind", "heading"),
			objectSchema(map[string]any{
				"kind":     constStringSchema(SelectorBlock),
				"block_id": nonEmptyStringSchema("exact Obsidian block ID without the leading caret"),
			}, "kind", "block_id"),
			objectSchema(map[string]any{"kind": constStringSchema(SelectorFrontmatter)}, "kind"),
			objectSchema(map[string]any{"kind": constStringSchema(SelectorOutline)}, "kind"),
		},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func nonEmptyStringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description, "minLength": 1}
}

func constStringSchema(value string) map[string]any {
	return map[string]any{"type": "string", "const": value}
}

func integerSchema(description string, minimum, maximum, defaultValue any) map[string]any {
	return map[string]any{
		"type":        "integer",
		"description": description,
		"minimum":     minimum,
		"maximum":     maximum,
		"default":     defaultValue,
	}
}

func withDefault(schema map[string]any, value any) map[string]any {
	schema["default"] = value
	return schema
}
