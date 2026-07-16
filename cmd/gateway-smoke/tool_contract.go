package main

import (
	"encoding/json"
	"reflect"
	"sort"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"personal-mcp-gateway/internal/tools/obsidian"
)

func exactCandidateToolGrammar(tools []*sdk.Tool) bool {
	if len(tools) != 5 {
		return false
	}
	byName := make(map[string]*sdk.Tool, len(tools))
	for _, tool := range tools {
		if tool == nil || byName[tool.Name] != nil || !exactReadOnlyAnnotations(tool.Annotations) {
			return false
		}
		byName[tool.Name] = tool
	}
	if byName[obsidian.ToolResolve] == nil || byName[obsidian.ToolResolve].Description != obsidian.ResolveDescription ||
		byName[obsidian.ToolLS] == nil || byName[obsidian.ToolLS].Description != obsidian.LSDescription ||
		byName[obsidian.ToolRead] == nil || byName[obsidian.ToolRead].Description != obsidian.ReadDescription ||
		byName[obsidian.ToolReadMany] == nil || byName[obsidian.ToolReadMany].Description != obsidian.ReadManyDescription ||
		byName[obsidian.ToolGrep] == nil || byName[obsidian.ToolGrep].Description != obsidian.GrepDescription {
		return false
	}

	resolve, ok := normalizedSchema(byName[obsidian.ToolResolve].InputSchema)
	if !ok || !closedObjectGrammar(resolve, []string{"base", "path"}, []string{"path"}) {
		return false
	}
	ls, ok := normalizedSchema(byName[obsidian.ToolLS].InputSchema)
	if !ok || !closedObjectGrammar(ls, []string{"base", "cursor", "limit", "path"}, []string{"path"}) ||
		!integerGrammar(property(ls, "limit"), 100, 1, 500) {
		return false
	}
	read, ok := normalizedSchema(byName[obsidian.ToolRead].InputSchema)
	if !ok || !closedObjectGrammar(read, []string{"base", "cursor", "max_bytes", "path", "selector"}, []string{"path"}) ||
		!integerGrammar(property(read, "max_bytes"), 65_536, 1, 262_144) ||
		!defaultObject(property(read, "selector"), map[string]any{"kind": "content", "start_line": float64(1)}) ||
		!selectorGrammar(property(read, "selector")) {
		return false
	}
	readMany, ok := normalizedSchema(byName[obsidian.ToolReadMany].InputSchema)
	if !ok || !closedObjectGrammar(readMany, []string{"cursor", "max_bytes", "requests"}, []string{"requests"}) ||
		!integerGrammar(property(readMany, "max_bytes"), 65_536, 1, 262_144) {
		return false
	}
	requests := property(readMany, "requests")
	if number(requests, "minItems") != 1 || number(requests, "maxItems") != 20 {
		return false
	}
	request, ok := requests["items"].(map[string]any)
	if !ok || !closedObjectGrammar(request, []string{"base", "max_bytes", "path", "selector"}, []string{"path"}) ||
		!integerGrammar(property(request, "max_bytes"), 65_536, 1, 262_144) ||
		!defaultObject(property(request, "selector"), map[string]any{"kind": "content", "start_line": float64(1)}) ||
		!selectorGrammar(property(request, "selector")) {
		return false
	}
	grep, ok := normalizedSchema(byName[obsidian.ToolGrep].InputSchema)
	return ok && closedObjectGrammar(grep,
		[]string{"base", "case_sensitive", "context_lines", "cursor", "limit", "max_bytes", "max_files", "path", "pattern", "regex"},
		[]string{"pattern"}) &&
		stringBounds(property(grep, "pattern"), 1, 4096) && property(grep, "path")["default"] == "." &&
		property(grep, "regex")["default"] == true && property(grep, "case_sensitive")["default"] == false &&
		integerGrammar(property(grep, "context_lines"), 1, 0, 3) &&
		integerGrammar(property(grep, "limit"), 50, 1, 200) &&
		integerGrammar(property(grep, "max_files"), 10_000, 1, 50_000) &&
		integerGrammar(property(grep, "max_bytes"), 268_435_456, 1, 1_073_741_824)
}

func exactReadOnlyAnnotations(annotations *sdk.ToolAnnotations) bool {
	return annotations != nil && annotations.ReadOnlyHint && annotations.DestructiveHint != nil && !*annotations.DestructiveHint &&
		annotations.OpenWorldHint != nil && !*annotations.OpenWorldHint
}

func normalizedSchema(value any) (map[string]any, bool) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var schema map[string]any
	err = json.Unmarshal(data, &schema)
	return schema, err == nil
}

func closedObjectGrammar(schema map[string]any, properties, required []string) bool {
	if schema == nil || schema["type"] != "object" || schema["additionalProperties"] != false {
		return false
	}
	rawProperties, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	gotProperties := make([]string, 0, len(rawProperties))
	for name := range rawProperties {
		gotProperties = append(gotProperties, name)
	}
	sort.Strings(gotProperties)
	properties = append([]string(nil), properties...)
	sort.Strings(properties)
	gotRequired, ok := stringSlice(schema["required"])
	if !ok {
		return false
	}
	sort.Strings(gotRequired)
	required = append([]string(nil), required...)
	sort.Strings(required)
	return reflect.DeepEqual(gotProperties, properties) && reflect.DeepEqual(gotRequired, required)
}

func selectorGrammar(schema map[string]any) bool {
	variants, ok := schema["oneOf"].([]any)
	if !ok || len(variants) != 5 {
		return false
	}
	byKind := make(map[string]map[string]any, len(variants))
	for _, raw := range variants {
		variant, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		kind, _ := property(variant, "kind")["const"].(string)
		if kind == "" || byKind[kind] != nil {
			return false
		}
		byKind[kind] = variant
	}
	content := byKind["content"]
	heading := byKind["heading"]
	block := byKind["block"]
	frontmatter := byKind["frontmatter"]
	outline := byKind["outline"]
	return closedObjectGrammar(content, []string{"kind", "start_line"}, []string{"kind"}) &&
		integerGrammar(property(content, "start_line"), 1, 1, 50_000) &&
		closedObjectGrammar(heading, []string{"heading", "kind", "occurrence"}, []string{"heading", "kind"}) &&
		stringBounds(property(heading, "heading"), 1, -1) && integerGrammar(property(heading, "occurrence"), 1, 1, 50_000) &&
		closedObjectGrammar(block, []string{"block_id", "kind"}, []string{"block_id", "kind"}) &&
		stringBounds(property(block, "block_id"), 1, -1) &&
		closedObjectGrammar(frontmatter, []string{"kind"}, []string{"kind"}) &&
		closedObjectGrammar(outline, []string{"kind"}, []string{"kind"})
}

func property(schema map[string]any, name string) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	value, _ := properties[name].(map[string]any)
	return value
}

func integerGrammar(schema map[string]any, defaultValue, minimum, maximum float64) bool {
	return schema != nil && schema["type"] == "integer" && number(schema, "default") == defaultValue &&
		number(schema, "minimum") == minimum && number(schema, "maximum") == maximum
}

func stringBounds(schema map[string]any, minimum, maximum float64) bool {
	if schema == nil || schema["type"] != "string" || number(schema, "minLength") != minimum {
		return false
	}
	return maximum < 0 || number(schema, "maxLength") == maximum
}

func number(schema map[string]any, key string) float64 {
	value, _ := schema[key].(float64)
	return value
}

func defaultObject(schema map[string]any, expected map[string]any) bool {
	value, ok := schema["default"].(map[string]any)
	return ok && reflect.DeepEqual(value, expected)
}

func stringSlice(value any) ([]string, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		out[index] = text
	}
	return out, true
}
