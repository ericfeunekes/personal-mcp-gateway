package ynab

import (
	_ "embed"
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// Provider field definitions projected from the official YNAB OpenAPI 1.86.0
// specification. Descriptions and repeated response envelopes are removed;
// provider properties, enums, nullability and field limits remain intact.
// Source: https://github.com/ynab/ynab-sdk-python/blob/main/open_api_spec.yaml
//
//go:embed provider_schema.json
var providerSchemaJSON []byte

var providerSchemaDefinitions = func() map[string]map[string]json.RawMessage {
	var out map[string]map[string]json.RawMessage
	if err := json.Unmarshal(providerSchemaJSON, &out); err != nil {
		panic(err)
	}
	return out
}()

func providerSchema(section, name string) *jsonschema.Schema {
	var out jsonschema.Schema
	if err := json.Unmarshal(providerSchemaDefinitions[section][name], &out); err != nil {
		panic(err)
	}
	return &out
}
