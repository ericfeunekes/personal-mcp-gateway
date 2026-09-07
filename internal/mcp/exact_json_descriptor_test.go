package mcp

import (
	"github.com/google/jsonschema-go/jsonschema"
	"testing"
)

func TestExactJSONNumberValidation(t *testing.T) {
	s, err := (&jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"n": {Type: "integer"}}, Required: []string{"n"}}).Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateExactJSON([]byte(`{"n":9007199254740993}`), s); err != nil {
		t.Fatal(err)
	}
	if err = validateExactJSON([]byte(`{"n":1.5}`), s); err == nil {
		t.Fatal("fraction accepted")
	}
	if err = validateExactJSON([]byte(`{"n":1e1000000000}`), s); err == nil {
		t.Fatal("unbounded exponent accepted")
	}
}
