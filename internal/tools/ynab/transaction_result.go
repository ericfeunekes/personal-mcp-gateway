package ynab

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
)

// YNAB documents that some edits to existing splits are silently ignored.
// Inspect the response already returned by the write; no second request or
// rollback is performed, since other requested fields may have been applied.
func confirmTransactionFields(item Item, r Result) Result {
	var requested map[string]json.RawMessage
	if json.Unmarshal(item.Data, &requested) != nil {
		return r
	}
	checked := []string{}
	for _, key := range []string{"date", "amount", "category_id", "subtransactions"} {
		if _, ok := requested[key]; ok {
			checked = append(checked, key)
		}
	}
	if len(checked) == 0 || r.Status != "success" {
		return r
	}
	var envelope struct {
		Data struct {
			Transaction  json.RawMessage   `json:"transaction"`
			Transactions []json.RawMessage `json:"transactions"`
		} `json:"data"`
	}
	if json.Unmarshal(r.Data, &envelope) != nil {
		return unconfirmedTransaction(r, checked)
	}
	candidates := envelope.Data.Transactions
	if len(envelope.Data.Transaction) > 0 {
		candidates = append(candidates, envelope.Data.Transaction)
	}
	id, importID := transactionIdentity(item)
	var actual map[string]json.RawMessage
	for _, row := range candidates {
		var record map[string]json.RawMessage
		if json.Unmarshal(row, &record) != nil {
			continue
		}
		var rowID, rowImport string
		_ = json.Unmarshal(record["id"], &rowID)
		_ = json.Unmarshal(record["import_id"], &rowImport)
		if id != "" && rowID == id || id == "" && importID != "" && rowImport == importID {
			actual = record
			break
		}
	}
	if actual == nil {
		return unconfirmedTransaction(r, checked)
	}
	missing := []string{}
	for _, key := range checked {
		if !requestedJSONMatches(requested[key], actual[key]) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return unconfirmedTransaction(r, missing)
	}
	return r
}

func unconfirmedTransaction(r Result, fields []string) Result {
	r.Status = "uncertain"
	r.Error = "provider did not apply or confirm requested fields: " + strings.Join(fields, ", ") + "; other fields may have changed"
	return r.acknowledge()
}

func requestedJSONMatches(want, got []byte) bool {
	decode := func(raw []byte) (any, bool) {
		var v any
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		err := d.Decode(&v)
		return v, err == nil
	}
	a, ok := decode(want)
	if !ok {
		return false
	}
	b, ok := decode(got)
	return ok && requestedValueMatches(a, b)
}
func requestedValueMatches(want, got any) bool {
	switch a := want.(type) {
	case map[string]any:
		b, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range a {
			other, exists := b[key]
			if !exists || !requestedValueMatches(value, other) {
				return false
			}
		}
		return true
	case []any:
		b, ok := got.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		used := make([]bool, len(b))
		for _, value := range a {
			found := false
			for j, other := range b {
				if !used[j] && requestedValueMatches(value, other) {
					used[j] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	case json.Number:
		b, ok := got.(json.Number)
		if !ok {
			return false
		}
		x, ok := new(big.Rat).SetString(string(a))
		if !ok {
			return false
		}
		y, ok := new(big.Rat).SetString(string(b))
		return ok && x.Cmp(y) == 0
	default:
		return reflect.DeepEqual(want, got)
	}
}
