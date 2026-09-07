package ynab

import (
	"encoding/json"
	"errors"
)

// Data is the bounded raw provider response used only by file exports. Model
// results use named collections so provider wrappers do not explode the schema.
type Result struct {
	Index              int             `json:"index"`
	Status             string          `json:"status"`
	Error              string          `json:"error,omitempty"`
	ErrorCode          string          `json:"error_code,omitempty"`
	HTTPStatus         int             `json:"http_status,omitempty"`
	Recovery           string          `json:"recovery,omitempty"`
	RetryAt            string          `json:"retry_at,omitempty"`
	Artifact           *Artifact       `json:"artifact,omitempty"`
	ServerKnowledge    *int64          `json:"server_knowledge,omitempty"`
	Parent             string          `json:"parent,omitempty"`
	IDs                []string        `json:"ids,omitempty"`
	DuplicateImportIDs []string        `json:"duplicate_import_ids,omitempty"`
	RetryAfter         string          `json:"retry_after,omitempty"`
	DefaultPlanID      string          `json:"default_plan_id,omitempty"`
	Data               json.RawMessage `json:"-"`
	collections        map[string][]json.RawMessage
}

func normalizeResult(r Result) Result {
	if r.Status == "uncertain" {
		r.ErrorCode = "uncertain_write"
		r.Recovery = "inspect_before_retry"
	}
	return r
}

func (r Result) MarshalJSON() ([]byte, error) {
	type plain Result
	b, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err = json.Unmarshal(b, &values); err != nil {
		return nil, err
	}
	if r.IDs != nil {
		values["ids"], err = json.Marshal(r.IDs)
		if err != nil {
			return nil, err
		}
	}
	for key, rows := range r.collections {
		if rows == nil {
			rows = []json.RawMessage{}
		}
		b, err = json.Marshal(rows)
		if err != nil {
			return nil, err
		}
		values[key] = b
	}
	return json.Marshal(values)
}

var providerCollections = map[string]string{
	"plan": "plans", "user": "users", "settings": "settings", "account": "accounts",
	"category": "categories", "category_group": "category_groups", "payee": "payees",
	"payee_location": "payee_locations", "month": "months", "transaction": "transactions",
	"scheduled_transaction": "scheduled_transactions", "money_movement": "money_movements",
	"money_movement_group": "money_movement_groups", "subtransaction": "subtransactions",
	"scheduled_subtransaction": "scheduled_subtransactions",
}

func projectProviderResult(index int, raw json.RawMessage, expected ...string) Result {
	r := Result{Index: index, Status: "success", Data: raw, collections: map[string][]json.RawMessage{}}
	fail := func() Result {
		return Result{Index: index, Status: "error", Error: "provider returned an invalid resource response", ErrorCode: "invalid_response", Recovery: "contact_operator"}
	}
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil {
		return fail()
	}
	d := envelope.Data
	if len(expected) > 0 {
		found := false
		for _, key := range expected {
			if value, ok := d[key]; ok && string(value) != "null" {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	if value, ok := d["default_plan"]; ok && string(value) != "null" {
		var plan struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(value, &plan) != nil {
			return fail()
		}
		r.DefaultPlanID = plan.ID
	}
	if token, ok := d["server_knowledge"]; ok {
		var n int64
		if json.Unmarshal(token, &n) != nil {
			return fail()
		}
		r.ServerKnowledge = &n
	}
	for _, key := range []string{"transaction_ids", "duplicate_import_ids"} {
		if value, ok := d[key]; ok {
			var ids []string
			if json.Unmarshal(value, &ids) != nil {
				return fail()
			}
			if key == "transaction_ids" {
				r.IDs = ids
			} else {
				r.DuplicateImportIDs = ids
			}
		}
	}
	for singular, plural := range providerCollections {
		// Settings has the same singular/plural spelling and is an object upstream.
		if value, ok := d[singular]; ok {
			if err := r.addRecord(plural, value); err != nil {
				return fail()
			}
		}
		if singular == plural {
			continue
		}
		if value, ok := d[plural]; ok {
			var rows []json.RawMessage
			if json.Unmarshal(value, &rows) != nil {
				return fail()
			}
			r.ensureCollection(plural)
			for _, row := range rows {
				if plural == "plans" {
					var plan map[string]json.RawMessage
					if json.Unmarshal(row, &plan) != nil {
						return fail()
					}
					if accounts, ok := plan["accounts"]; ok {
						var entries []map[string]json.RawMessage
						if json.Unmarshal(accounts, &entries) != nil {
							return fail()
						}
						r.ensureCollection("accounts")
						for _, account := range entries {
							account["plan_id"] = plan["id"]
							encoded, err := json.Marshal(account)
							if err != nil || r.addRecord("accounts", encoded) != nil {
								return fail()
							}
						}
					}
				}
				if err := r.addRecord(plural, row); err != nil {
					return fail()
				}
			}
		}
	}
	if len(r.collections) == 0 && r.IDs == nil && r.DuplicateImportIDs == nil {
		return fail()
	}
	return r
}

func (r *Result) ensureCollection(kind string) {
	if _, ok := r.collections[kind]; !ok {
		r.collections[kind] = []json.RawMessage{}
	}
}

func (r *Result) addRecord(kind string, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("not a record")
	}
	if kind == "plans" {
		// The plan get is metadata-only; complete related data remains in Data for
		// export, and is never projected into the model result.
		for _, key := range []string{"accounts", "category_groups", "categories", "payees", "payee_locations", "months", "transactions", "subtransactions", "scheduled_transactions", "scheduled_subtransactions"} {
			delete(fields, key)
		}
	} else {
		nested := ""
		switch kind {
		case "category_groups", "months":
			nested = "categories"
		case "transactions":
			nested = "subtransactions"
		case "scheduled_transactions":
			nested = "scheduled_subtransactions"
		}
		sourceKey := nested
		if kind == "scheduled_transactions" {
			sourceKey = "subtransactions"
		}
		if value, ok := fields[sourceKey]; nested != "" && ok {
			var rows []json.RawMessage
			if json.Unmarshal(value, &rows) != nil {
				return errors.New("invalid nested collection")
			}
			r.ensureCollection(nested)
			for _, row := range rows {
				var child map[string]json.RawMessage
				if json.Unmarshal(row, &child) != nil || child == nil {
					return errors.New("invalid child")
				}
				if kind == "transactions" && len(child["transaction_id"]) == 0 {
					child["transaction_id"] = fields["id"]
				}
				if kind == "scheduled_transactions" && len(child["scheduled_transaction_id"]) == 0 {
					child["scheduled_transaction_id"] = fields["id"]
				}
				if kind == "category_groups" && len(child["category_group_id"]) == 0 {
					child["category_group_id"] = fields["id"]
				}
				if kind == "months" {
					child["month"] = fields["month"]
				}
				b, err := json.Marshal(child)
				if err != nil {
					return err
				}
				if err = r.addRecord(nested, b); err != nil {
					return err
				}
			}
			delete(fields, sourceKey)
		}
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	r.ensureCollection(kind)
	r.collections[kind] = append(r.collections[kind], b)
	return nil
}

// acknowledge uses the provider's returned identity without re-reading writes.
func (r Result) acknowledge() Result {
	if len(r.IDs) == 0 {
		for _, kind := range []string{"accounts", "categories", "category_groups", "payees", "transactions", "scheduled_transactions"} {
			for _, raw := range r.collections[kind] {
				var v struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &v) == nil && v.ID != "" {
					r.IDs = append(r.IDs, v.ID)
				}
			}
		}
	}
	r.collections = nil
	if r.Status == "success" && r.Artifact == nil && r.IDs == nil && len(r.DuplicateImportIDs) == 0 {
		r.Status = "uncertain"
		r.Error = "provider did not return a mutation identity"
	}
	return r
}
