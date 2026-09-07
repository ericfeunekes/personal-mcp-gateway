package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublishedSchemaMatchesFrozenProvider(t *testing.T) {
	root := filepath.Join("..", "..")
	generated, err := generate(filepath.Join(root, specPath))
	if err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(filepath.Join(root, outPath))
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(published, generated) {
		t.Fatal("provider schema drift: run go run ./cmd/ynab-schema-gen -write")
	}
}

func TestSameJSONIgnoresFormattingAndObjectOrder(t *testing.T) {
	if !sameJSON([]byte(`{"input":{"a":1,"b":[true,null]}}`), []byte("{\n  \"input\": {\"b\": [true, null], \"a\": 1}\n}\n")) {
		t.Fatal("equivalent JSON was reported different")
	}
	if sameJSON([]byte(`{"input":{"a":1}}`), []byte(`{"input":{"a":2}}`)) {
		t.Fatal("different JSON was reported equivalent")
	}
}
