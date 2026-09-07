// ynab-schema-gen regenerates the compact YNAB provider schema from a frozen
// public OpenAPI fixture. It intentionally has no network dependency.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"sort"
)

const (
	specPath = "internal/tools/ynab/testdata/openapi-v1.86.0.json"
	outPath  = "internal/tools/ynab/provider_schema.json"
)

func main() {
	write := flag.Bool("write", false, "write the regenerated provider schema")
	flag.Parse()
	if flag.NArg() != 0 {
		fail("usage: ynab-schema-gen [-write]")
	}

	generated, err := generate(specPath)
	if err != nil {
		fail(err.Error())
	}
	if *write {
		if err := os.WriteFile(outPath, generated, 0o644); err != nil {
			fail(fmt.Sprintf("write %s: %v", outPath, err))
		}
		return
	}
	existing, err := os.ReadFile(outPath)
	if err != nil {
		fail(fmt.Sprintf("read %s: %v", outPath, err))
	}
	if !sameJSON(existing, generated) {
		fail(fmt.Sprintf("%s is out of date; run go run ./cmd/ynab-schema-gen -write", outPath))
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "ynab-schema-gen:", message)
	os.Exit(1)
}

func generate(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var spec map[string]any
	if err := decodeJSON(raw, &spec); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	components, ok := object(spec["components"])
	if !ok {
		return nil, fmt.Errorf("%s has no components object", path)
	}
	schemas, ok := object(components["schemas"])
	if !ok {
		return nil, fmt.Errorf("%s has no component schemas", path)
	}

	var resolve func(any) any
	resolve = func(value any) any {
		schema, ok := object(value)
		if !ok {
			if list, ok := value.([]any); ok {
				out := make([]any, len(list))
				for i := range list {
					out[i] = resolve(list[i])
				}
				return out
			}
			return value
		}
		if ref, ok := schema["$ref"].(string); ok {
			const prefix = "#/components/schemas/"
			if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
				fail("unsupported reference " + ref)
			}
			target, ok := object(schemas[ref[len(prefix):]])
			if !ok {
				fail("missing referenced schema " + ref)
			}
			return resolve(target)
		}
		out := make(map[string]any, len(schema))
		for key, item := range schema {
			if key != "description" {
				out[key] = resolve(item)
			}
		}
		members, hasAllOf := out["allOf"].([]any)
		if !hasAllOf {
			return out
		}
		canFlatten := true
		for _, member := range members {
			m, ok := object(member)
			if !ok || (m["type"] != "object" && m["required"] == nil && m["properties"] == nil) {
				canFlatten = false
				break
			}
		}
		if !canFlatten {
			return out
		}
		delete(out, "allOf")
		properties := map[string]any{}
		required := map[string]bool{}
		for _, member := range members {
			m, _ := object(member)
			if p, ok := object(m["properties"]); ok {
				for name, property := range p {
					properties[name] = property
				}
			}
			if names, ok := m["required"].([]any); ok {
				for _, name := range names {
					if s, ok := name.(string); ok {
						required[s] = true
					}
				}
			}
		}
		out["type"] = "object"
		out["properties"] = properties
		out["required"] = sortedStrings(required)
		return out
	}

	record := func(name string, drop ...string) map[string]any {
		source, ok := object(schemas[name])
		if !ok {
			fail("missing schema " + name)
		}
		expanded, ok := object(resolve(source))
		if !ok {
			fail("schema " + name + " is not an object")
		}
		properties, _ := object(expanded["properties"])
		for _, field := range drop {
			delete(properties, field)
		}
		if required, ok := expanded["required"].([]any); ok {
			filtered := make([]any, 0, len(required))
			for _, field := range required {
				name, _ := field.(string)
				if !contains(drop, name) {
					filtered = append(filtered, field)
				}
			}
			expanded["required"] = filtered
		}
		return expanded
	}

	inputNames := []string{"SaveAccount", "NewCategory", "SaveCategory", "SaveCategoryGroup", "PostPayee", "SavePayee", "NewTransaction", "SaveTransactionWithIdOrImportId", "SaveScheduledTransaction", "SaveMonthCategory"}
	input := map[string]any{}
	for _, name := range inputNames {
		input[name] = record(name)
	}
	output := map[string]any{
		"plan":                     record("PlanDetail", "accounts", "payees", "payee_locations", "category_groups", "categories", "months", "transactions", "subtransactions", "scheduled_transactions", "scheduled_subtransactions"),
		"user":                     record("User"),
		"settings":                 record("PlanSettings"),
		"account":                  record("Account"),
		"category_group":           record("CategoryGroup"),
		"category":                 record("Category"),
		"payee":                    record("Payee"),
		"payee_location":           record("PayeeLocation"),
		"month":                    record("MonthDetail", "categories"),
		"transaction":              record("TransactionDetail", "subtransactions"),
		"subtransaction":           record("SubTransaction"),
		"scheduled_transaction":    record("ScheduledTransactionDetail", "subtransactions"),
		"scheduled_subtransaction": record("ScheduledSubTransaction"),
		"money_movement":           record("MoneyMovement"),
		"money_movement_group":     record("MoneyMovementGroup"),
	}

	account := output["account"].(map[string]any)
	accountProperties, _ := object(account["properties"])
	accountProperties["direct_import_linked"] = map[string]any{"type": []any{"boolean", "null"}}
	accountProperties["direct_import_in_error"] = map[string]any{"type": []any{"boolean", "null"}}
	accountProperties["plan_id"] = map[string]any{"type": "string"}
	category := output["category"].(map[string]any)
	categoryProperties, _ := object(category["properties"])
	categoryProperties["month"] = map[string]any{"type": "string"}
	transaction := output["transaction"].(map[string]any)
	transactionProperties, _ := object(transaction["properties"])
	hybrid := record("HybridTransaction")
	hybridProperties, _ := object(hybrid["properties"])
	transactionProperties["type"] = hybridProperties["type"]
	transactionProperties["parent_transaction_id"] = hybridProperties["parent_transaction_id"]
	transaction["required"] = []any{"id"}

	return json.MarshalIndent(map[string]any{"input": input, "output": output}, "", "  ")
}

func sameJSON(left, right []byte) bool {
	var a, b any
	return decodeJSON(left, &a) == nil && decodeJSON(right, &b) == nil && reflect.DeepEqual(a, b)
}

func decodeJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func object(value any) (map[string]any, bool) {
	out, ok := value.(map[string]any)
	return out, ok
}

func sortedStrings(values map[string]bool) []any {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]any, len(keys))
	for i := range keys {
		out[i] = keys[i]
	}
	return out
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
