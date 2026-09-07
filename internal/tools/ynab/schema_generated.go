package ynab

// This file contains the model-visible projection of the accepted YNAB v1.86
// contract. It deliberately does not mirror provider response envelopes: doing
// so made the Codex renderer collapse the schemas to unknown in the validation
// spike. Handler validation remains the authority for cross-field provider
// rules such as date ranges and target combinations.

import (
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

func inputSchemaForVerb(verb string) (*jsonschema.Schema, error) {
	variants, err := inputAlternatives(verb)
	if err != nil {
		return nil, err
	}
	min, max := 1, maxItems
	return closedObject(map[string]*jsonschema.Schema{
		"items": {
			Type:        "array",
			Description: "One to ten operations, in execution order. Mixed types are allowed only where alternatives below allow them.",
			Items:       &jsonschema.Schema{OneOf: variants},
			MinItems:    &min,
			MaxItems:    &max,
		},
	}, "items"), nil
}

func inputAlternatives(verb string) ([]*jsonschema.Schema, error) {
	resource := func(kind string, properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
		properties["type"] = literal(kind)
		return closedObject(properties, append([]string{"type"}, required...)...)
	}
	withPlan := func(kind string, properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
		// Each branch must own its schema tree: the SDK rejects shared nodes.
		properties["plan_id"] = id("plan_id", "Plan ID; `last-used` and `default` have YNAB's documented alias semantics and are not stable storage identities.")
		return resource(kind, properties, append([]string{"plan_id"}, required...)...)
	}

	transactionScope := func(scope string, extra map[string]*jsonschema.Schema, extraRequired ...string) *jsonschema.Schema {
		fields := map[string]*jsonschema.Schema{
			"since_date":               date("Inclusive start date; required for every transaction list. No implicit history range."),
			"until_date":               date("Inclusive end date; must not precede since_date."),
			"last_knowledge_of_server": integer("Optional YNAB change token; use only as returned by YNAB."),
			"filter":                   enum("uncategorized", "unapproved"),
		}
		for k, v := range extra {
			fields[k] = v
		}
		return withPlan("transaction", fields, append([]string{"since_date", "until_date"}, extraRequired...)...)
	}

	switch verb {
	case ToolList:
		changed := func() map[string]*jsonschema.Schema {
			return map[string]*jsonschema.Schema{"last_knowledge_of_server": integer("Optional provider change token.")}
		}
		return []*jsonschema.Schema{
			resource("plan", map[string]*jsonschema.Schema{"include_accounts": boolean("Include accounts in returned plan metadata.")}),
			withPlan("account", changed()),
			withPlan("category", changed()),
			withPlan("payee", changed()),
			withPlan("payee_location", map[string]*jsonschema.Schema{}),
			withPlan("payee_location", map[string]*jsonschema.Schema{"payee_id": id("payee_id", "Restrict locations to this payee.")}, "payee_id"),
			withPlan("month", changed()),
			withPlan("money_movement", map[string]*jsonschema.Schema{}),
			withPlan("money_movement", map[string]*jsonschema.Schema{"month": month("Restrict to this budget month.")}, "month"),
			withPlan("money_movement_group", map[string]*jsonschema.Schema{}),
			withPlan("money_movement_group", map[string]*jsonschema.Schema{"month": month("Restrict to this budget month.")}, "month"),
			transactionScope("plan", nil),
			transactionScope("account", map[string]*jsonschema.Schema{"account_id": id("account_id", "Account scope.")}, "account_id"),
			transactionScope("category", map[string]*jsonschema.Schema{"category_id": id("category_id", "Category scope.")}, "category_id"),
			transactionScope("payee", map[string]*jsonschema.Schema{"payee_id": id("payee_id", "Payee scope.")}, "payee_id"),
			transactionScope("month", map[string]*jsonschema.Schema{"month": month("Budget month scope; both dates must be inside this month.")}, "month"),
			withPlan("scheduled_transaction", changed()),
		}, nil
	case ToolGet:
		return []*jsonschema.Schema{
			resource("user", map[string]*jsonschema.Schema{}),
			withPlan("plan", map[string]*jsonschema.Schema{}),
			withPlan("settings", map[string]*jsonschema.Schema{}),
			withPlan("account", map[string]*jsonschema.Schema{"account_id": id("account_id", "Account ID.")}, "account_id"),
			withPlan("category", map[string]*jsonschema.Schema{"category_id": id("category_id", "Category ID.")}, "category_id"),
			withPlan("category", map[string]*jsonschema.Schema{"category_id": id("category_id", "Category ID."), "month": month("Budget month.")}, "category_id", "month"),
			withPlan("payee", map[string]*jsonschema.Schema{"payee_id": id("payee_id", "Payee ID.")}, "payee_id"),
			withPlan("payee_location", map[string]*jsonschema.Schema{"payee_location_id": id("payee_location_id", "Payee location ID.")}, "payee_location_id"),
			withPlan("month", map[string]*jsonschema.Schema{"month": month("Budget month.")}, "month"),
			withPlan("transaction", map[string]*jsonschema.Schema{"transaction_id": id("transaction_id", "Transaction ID.")}, "transaction_id"),
			withPlan("scheduled_transaction", map[string]*jsonschema.Schema{"scheduled_transaction_id": id("scheduled_transaction_id", "Scheduled transaction ID.")}, "scheduled_transaction_id"),
		}, nil
	case ToolCreate:
		return []*jsonschema.Schema{
			withPlan("account", map[string]*jsonschema.Schema{"data": accountCreate()}, "data"),
			withPlan("category", map[string]*jsonschema.Schema{"data": categoryCreate()}, "data"),
			withPlan("category_group", map[string]*jsonschema.Schema{"data": categoryGroupCreate()}, "data"),
			withPlan("payee", map[string]*jsonschema.Schema{"data": payeeCreate()}, "data"),
			withPlan("transaction", map[string]*jsonschema.Schema{"data": transactionCreate()}, "data"),
			withPlan("scheduled_transaction", map[string]*jsonschema.Schema{"data": scheduledCreate()}, "data"),
			exportPlan(), exportTransactions(),
		}, nil
	case ToolUpdate:
		return []*jsonschema.Schema{
			withPlan("category", map[string]*jsonschema.Schema{"category_id": id("category_id", "Category ID."), "data": categoryUpdate()}, "category_id", "data"),
			withPlan("category", map[string]*jsonschema.Schema{"category_id": id("category_id", "Month-category ID."), "month": month("Budget month."), "data": monthCategoryUpdate()}, "category_id", "month", "data"),
			withPlan("category_group", map[string]*jsonschema.Schema{"category_group_id": id("category_group_id", "Category group ID."), "data": categoryGroupUpdate()}, "category_group_id", "data"),
			withPlan("payee", map[string]*jsonschema.Schema{"payee_id": id("payee_id", "Payee ID."), "data": payeeUpdate()}, "payee_id", "data"),
			withPlan("transaction", map[string]*jsonschema.Schema{"transaction_id": id("transaction_id", "Transaction ID; omit only for native import-ID update."), "data": transactionUpdate()}, "data"),
			withPlan("scheduled_transaction", map[string]*jsonschema.Schema{"scheduled_transaction_id": id("scheduled_transaction_id", "Scheduled transaction ID."), "data": scheduledUpdate()}, "scheduled_transaction_id", "data"),
		}, nil
	case ToolDelete:
		return []*jsonschema.Schema{
			withPlan("transaction", map[string]*jsonschema.Schema{"transaction_id": id("transaction_id", "Transaction ID.")}, "transaction_id"),
			withPlan("scheduled_transaction", map[string]*jsonschema.Schema{"scheduled_transaction_id": id("scheduled_transaction_id", "Scheduled transaction ID.")}, "scheduled_transaction_id"),
		}, nil
	case ToolImport:
		return []*jsonschema.Schema{withPlan("bank_import", map[string]*jsonschema.Schema{})}, nil
	default:
		return nil, fmt.Errorf("unsupported YNAB verb %q", verb)
	}
}

func accountCreate() *jsonschema.Schema       { return inputRecord("SaveAccount") }
func categoryCreate() *jsonschema.Schema      { return inputRecord("NewCategory") }
func categoryGroupCreate() *jsonschema.Schema { return inputRecord("SaveCategoryGroup") }
func payeeCreate() *jsonschema.Schema         { return inputRecord("PostPayee") }
func categoryGroupUpdate() *jsonschema.Schema { return inputRecord("SaveCategoryGroup") }
func payeeUpdate() *jsonschema.Schema         { return inputRecord("SavePayee") }
func categoryUpdate() *jsonschema.Schema      { return inputRecord("SaveCategory") }
func monthCategoryUpdate() *jsonschema.Schema { return inputRecord("SaveMonthCategory") }
func transactionCreate() *jsonschema.Schema {
	s := inputRecord("NewTransaction")
	s.Required = []string{"account_id", "date", "amount"}
	return s
}
func transactionUpdate() *jsonschema.Schema { return inputRecord("SaveTransactionWithIdOrImportId") }
func scheduledCreate() *jsonschema.Schema   { return inputRecord("SaveScheduledTransaction") }
func scheduledUpdate() *jsonschema.Schema   { return inputRecord("SaveScheduledTransaction") }

func inputRecord(name string) *jsonschema.Schema {
	s := providerSchema("input", name)
	closeInputTree(s)
	return s
}
func closeInputTree(s *jsonschema.Schema) {
	if len(s.Properties) > 0 {
		s.AdditionalProperties = &jsonschema.Schema{Not: &jsonschema.Schema{}}
		for name, field := range s.Properties {
			switch name {
			case "amount", "balance", "budgeted":
				field.Description = "Signed integer milliunits (1000 = one currency unit)."
			case "goal_target":
				field.Description = "Integer milliunits; null removes the target."
			case "memo":
				field.Description = "Null clears; omit to leave unchanged."
			case "goal_frequency":
				field.Description = "Requires goal_target; cannot combine with goal_target_date or use for credit-card payment categories."
			}
			closeInputTree(field)
		}
	}
	if s.Items != nil {
		closeInputTree(s.Items)
	}
}

func exportPlan() *jsonschema.Schema {
	return closedObject(map[string]*jsonschema.Schema{"type": literal("export"), "plan_id": id("plan_id", "Plan ID."), "filename": stringField("Relative unused filename beneath the configured private export root."), "source": literal("plan"), "format": literal("json")}, "type", "plan_id", "filename", "source", "format")
}
func exportTransactions() *jsonschema.Schema {
	return closedObject(map[string]*jsonschema.Schema{"type": literal("export"), "plan_id": id("plan_id", "Plan ID."), "filename": stringField("Relative unused filename beneath the configured private export root."), "source": literal("transactions"), "format": enum("json", "csv"), "since_date": date("Inclusive export start date."), "until_date": date("Inclusive export end date."), "account_id": id("account_id", "Optional account scope; choose only one scope."), "category_id": id("category_id", "Optional category scope."), "payee_id": id("payee_id", "Optional payee scope."), "month": month("Optional month scope."), "filter": enum("uncategorized", "unapproved"), "last_knowledge_of_server": integer("Optional provider change token.")}, "type", "plan_id", "filename", "source", "format", "since_date", "until_date")
}

// outputSchema keeps result collections resource-specific without recursively
// repeating provider envelopes. The handler returns only collections appropriate
// to the operation; each record remains a typed provider projection.
func outputSchemaForVerb(verb string) (*jsonschema.Schema, error) {
	collections := map[string]*jsonschema.Schema{}
	var kinds []string
	switch verb {
	case ToolList:
		kinds = []string{"plans", "accounts", "category_groups", "categories", "payees", "payee_locations", "months", "money_movements", "money_movement_groups", "transactions", "subtransactions", "scheduled_transactions", "scheduled_subtransactions"}
	case ToolGet:
		kinds = []string{"plans", "users", "settings", "accounts", "categories", "payees", "payee_locations", "months", "transactions", "subtransactions", "scheduled_transactions", "scheduled_subtransactions"}
	}
	for _, kind := range kinds {
		collections[kind] = &jsonschema.Schema{Type: "array", Items: outputRecord(kind)}
	}
	collections["index"] = integer("Input item position.")
	collections["status"] = enum("success", "error", "not_attempted", "uncertain")
	collections["error"] = stringField("Sanitized provider or validation error.")
	collections["error_code"] = enum("invalid_request", "authentication", "permission", "not_found", "conflict", "rate_limited", "upstream_unavailable", "canceled", "response_too_large", "invalid_response", "local_state", "uncertain_write")
	collections["http_status"] = integer("Upstream HTTP status when received; omitted for local failures.")
	collections["recovery"] = enum("fix_request", "fix_credentials", "wait", "retry_read", "inspect_before_retry", "contact_operator")
	collections["retry_at"] = stringField("Earliest gateway retry time in UTC RFC3339, not a guarantee of provider quota availability.")
	collections["server_knowledge"] = integer("YNAB server knowledge when the provider supplies it.")
	collections["ids"] = &jsonschema.Schema{Type: "array", Items: stringField("Provider record ID.")}
	collections["duplicate_import_ids"] = &jsonschema.Schema{Type: "array", Items: stringField("Import ID the provider identified as duplicate.")}
	collections["retry_after"] = stringField("Provider retry guidance, when supplied.")
	collections["default_plan_id"] = stringField("Provider-selected default plan, when supplied.")
	collections["parent"] = stringField("Parent resource identity for a flattened nested collection.")
	collections["artifact"] = closedObject(map[string]*jsonschema.Schema{
		"filename": stringField("Export filename."), "format": enum("json", "csv"), "mime_type": stringField("Artifact MIME type."), "bytes": integer("Artifact byte size."),
		"location": stringField("Gateway-host local artifact location, not a ChatGPT download URL."), "source": enum("plan", "transactions"), "plan_id": id("plan_id", "Source plan ID."),
		"since_date": date("Transaction export start when applicable."), "until_date": date("Transaction export end when applicable."), "account_id": id("account_id", "Transaction account scope when applicable."), "category_id": id("category_id", "Transaction category scope when applicable."), "payee_id": id("payee_id", "Transaction payee scope when applicable."), "month": month("Transaction month scope when applicable."),
		"record_count": integer("Known exported record count."), "server_knowledge": integer("Known source server knowledge."),
	}, "filename", "format", "mime_type", "bytes", "location", "source", "plan_id")
	// Only create can export. Advertising this nested shape on reads wastes
	// the client's schema-rendering budget and suggests an impossible result.
	if verb != ToolCreate {
		delete(collections, "artifact")
	}
	return closedObject(map[string]*jsonschema.Schema{"results": {Type: "array", Items: closedObject(collections, "index", "status")}}, "results"), nil
}

func outputRecord(kind string) *jsonschema.Schema {
	name := map[string]string{"plans": "plan", "users": "user", "settings": "settings", "accounts": "account", "category_groups": "category_group", "categories": "category", "payees": "payee", "payee_locations": "payee_location", "months": "month", "money_movements": "money_movement", "money_movement_groups": "money_movement_group", "transactions": "transaction", "subtransactions": "subtransaction", "scheduled_transactions": "scheduled_transaction", "scheduled_subtransactions": "scheduled_subtransaction"}[kind]
	return providerSchema("output", name)
}

func closedObject(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
}
func updateObject(properties map[string]*jsonschema.Schema, fields ...string) *jsonschema.Schema {
	any := make([]*jsonschema.Schema, 0, len(fields))
	for _, field := range fields {
		any = append(any, &jsonschema.Schema{Required: []string{field}})
	}
	return &jsonschema.Schema{Type: "object", Properties: properties, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, AnyOf: any}
}
func literal(value string) *jsonschema.Schema {
	v := any(value)
	return &jsonschema.Schema{Type: "string", Const: &v}
}
func stringField(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}
func boolean(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "boolean", Description: description}
}
func integer(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "integer", Description: description}
}
func date(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Format: "date", Description: description}
}
func month(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Pattern: "^([0-9]{4}-[0-9]{2}-01|current)$", Description: description}
}
func id(name, description string) *jsonschema.Schema { return stringField(description) }
func nullableString(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}, Description: description}
}
func nullableID(name, description string) *jsonschema.Schema { return nullableString(description) }
func nullableInteger(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"integer", "null"}, Description: description}
}
func nullableBoolean(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"boolean", "null"}, Description: description}
}
func nullableDate(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Types: []string{"string", "null"}, Format: "date", Description: description}
}
func enum(values ...string) *jsonschema.Schema {
	choices := make([]any, len(values))
	for i := range values {
		choices[i] = values[i]
	}
	return &jsonschema.Schema{Type: "string", Enum: choices}
}
func nullableEnum(description string, values ...string) *jsonschema.Schema {
	choices := make([]any, 0, len(values)+1)
	for _, value := range values {
		choices = append(choices, value)
	}
	choices = append(choices, nil)
	return &jsonschema.Schema{Types: []string{"string", "null"}, Enum: choices, Description: description}
}
