// Package ynab provides the narrow, stateless YNAB MCP domain adapter.
package ynab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	localmcp "personal-mcp-gateway/internal/mcp"
)

const (
	ToolList               = "list"
	ToolGet                = "get"
	ToolCreate             = "create"
	ToolUpdate             = "update"
	ToolDelete             = "delete"
	ToolImport             = "import_transactions"
	maxItems               = 10
	maxProviderBytes int64 = 32 << 20
	maxExportBytes   int64 = 64 << 20
)

// Options contains only private host configuration. Tokens never occur in MCP arguments.
type Options struct {
	Token      string
	ExportRoot string
}

type Tools struct {
	token, exportRoot, baseURL string
	client                     *http.Client
	providerSlots              chan struct{}
	exportSlots                chan struct{}
	quota                      *quotaStore
	quotaPath                  string
	quotaErr                   error
	quotaOnce                  sync.Once
	now                        func() time.Time
	sleep                      func(context.Context, time.Duration) error
	jitter                     func(time.Duration) time.Duration
}

func New(options Options) (*Tools, error) {
	if strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("YNAB token is required")
	}
	if !filepath.IsAbs(options.ExportRoot) {
		return nil, errors.New("YNAB export root must be absolute")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, errors.New("YNAB provider state directory unavailable")
	}
	t := baseTools(options, "https://api.ynab.com/v1", &http.Client{Transport: newProviderTransport(), Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})
	t.quotaPath = filepath.Join(dir, "personal-mcp-gateway", "ynab", "provider-state", "state.sqlite")
	return t, nil
}

// newTools is an unexported test seam; production always uses the fixed API URL.
func newTools(options Options, baseURL string, client *http.Client) *Tools {
	t := baseTools(options, baseURL, client)
	t.quota, t.quotaErr = openQuotaStore("")
	return t
}
func baseTools(options Options, baseURL string, client *http.Client) *Tools {
	return &Tools{token: options.Token, exportRoot: options.ExportRoot, baseURL: strings.TrimRight(baseURL, "/"), client: client, providerSlots: make(chan struct{}, 2), exportSlots: make(chan struct{}, 1), now: time.Now, sleep: sleepContext, jitter: fullJitter}
}
func (t *Tools) Close() error {
	if t != nil {
		t.quotaOnce.Do(func() {})
	}
	if t != nil && t.client != nil {
		t.client.CloseIdleConnections()
	}
	if t != nil && t.quota != nil {
		return t.quota.db.Close()
	}
	return nil
}

type Input struct {
	Items []Item `json:"items"`
}
type Item struct {
	Type                   string          `json:"type"`
	PlanID                 string          `json:"plan_id,omitempty"`
	ID                     string          `json:"id,omitempty"`
	TransactionID          string          `json:"transaction_id,omitempty"`
	AccountID              string          `json:"account_id,omitempty"`
	CategoryID             string          `json:"category_id,omitempty"`
	CategoryGroupID        string          `json:"category_group_id,omitempty"`
	PayeeID                string          `json:"payee_id,omitempty"`
	PayeeLocationID        string          `json:"payee_location_id,omitempty"`
	ScheduledTransactionID string          `json:"scheduled_transaction_id,omitempty"`
	Month                  string          `json:"month,omitempty"`
	SinceDate              string          `json:"since_date,omitempty"`
	UntilDate              string          `json:"until_date,omitempty"`
	LastKnowledge          *int64          `json:"last_knowledge_of_server,omitempty"`
	Filter                 string          `json:"filter,omitempty"`
	IncludeAccounts        bool            `json:"include_accounts,omitempty"`
	Data                   json.RawMessage `json:"data,omitempty"`
	Source                 string          `json:"source,omitempty"`
	Format                 string          `json:"format,omitempty"`
	Filename               string          `json:"filename,omitempty"`
}
type Output struct {
	Results []Result `json:"results"`
}
type Artifact struct {
	Filename        string `json:"filename"`
	Format          string `json:"format"`
	MIMEType        string `json:"mime_type"`
	Bytes           int64  `json:"bytes"`
	Location        string `json:"location"`
	Source          string `json:"source"`
	PlanID          string `json:"plan_id"`
	SinceDate       string `json:"since_date,omitempty"`
	UntilDate       string `json:"until_date,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	CategoryID      string `json:"category_id,omitempty"`
	PayeeID         string `json:"payee_id,omitempty"`
	Month           string `json:"month,omitempty"`
	RecordCount     *int   `json:"record_count,omitempty"`
	ServerKnowledge *int64 `json:"server_knowledge,omitempty"`
}

func (t *Tools) Descriptors() ([]localmcp.ToolDescriptor, error) {
	if t == nil {
		return nil, errors.New("YNAB tools are required")
	}
	makeDescriptor := func(name, description string, readOnly bool, h sdk.ToolHandlerFor[Input, Output]) (localmcp.ToolDescriptor, error) {
		outputSchema, err := outputSchemaForVerb(name)
		if err != nil {
			return localmcp.ToolDescriptor{}, err
		}
		inputSchema, err := inputSchemaForVerb(name)
		if err != nil {
			return localmcp.ToolDescriptor{}, err
		}
		destructive, openWorld := !readOnly, true
		return localmcp.NewExactJSONToolDescriptor(sdk.Tool{Name: name, Description: description, InputSchema: inputSchema, OutputSchema: outputSchema, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld}}, h, summarizeArgs, summarizeResult)
	}
	list, err := makeDescriptor(ToolList, "List typed YNAB resources. Transaction collections require inclusive since_date and until_date.", true, t.list)
	if err != nil {
		return nil, err
	}
	get, err := makeDescriptor(ToolGet, "Get one typed YNAB resource. This never returns a complete plan export.", true, t.get)
	if err != nil {
		return nil, err
	}
	create, err := makeDescriptor(ToolCreate, "Create typed YNAB resources, or create a bounded host-local JSON/CSV export. Exports return metadata only.", false, t.create)
	if err != nil {
		return nil, err
	}
	update, err := makeDescriptor(ToolUpdate, "Update typed YNAB resources. Later items are not attempted after a provider failure.", false, t.update)
	if err != nil {
		return nil, err
	}
	deleteTool, err := makeDescriptor(ToolDelete, "Delete typed YNAB transaction or scheduled transaction resources.", false, t.delete)
	if err != nil {
		return nil, err
	}
	imp, err := makeDescriptor(ToolImport, "Request linked-bank transaction import for each typed plan item.", false, t.importTransactions)
	if err != nil {
		return nil, err
	}
	return []localmcp.ToolDescriptor{list, get, create, update, deleteTool, imp}, nil
}

func (t *Tools) list(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolList, in)
}
func (t *Tools) get(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolGet, in)
}
func (t *Tools) create(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolCreate, in)
}
func (t *Tools) update(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolUpdate, in)
}
func (t *Tools) delete(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolDelete, in)
}
func (t *Tools) importTransactions(ctx context.Context, _ *sdk.CallToolRequest, in Input) (*sdk.CallToolResult, Output, error) {
	return t.execute(ctx, ToolImport, in)
}

func (t *Tools) execute(ctx context.Context, verb string, in Input) (*sdk.CallToolResult, Output, error) {
	if len(in.Items) == 0 || len(in.Items) > maxItems {
		out := Output{Results: []Result{{Index: 0, Status: "error", Error: "items must contain 1 through 10 items", ErrorCode: "invalid_request", Recovery: "fix_request"}}}
		return callOutput(out), out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out := Output{Results: make([]Result, 0, len(in.Items))}
	mutation := verb == ToolCreate || verb == ToolUpdate || verb == ToolDelete || verb == ToolImport
	// All locally checkable constraints are a pre-dispatch boundary. A later
	// invalid item must never allow an earlier mutation to reach YNAB.
	validation := make([]Result, len(in.Items))
	invalid := false
	for i, item := range in.Items {
		validation[i] = Result{Index: i, Status: "not_attempted", Error: "not dispatched because batch validation failed", ErrorCode: "invalid_request", Recovery: "fix_request"}
		if err := validateItem(verb, item); err != nil {
			validation[i] = Result{Index: i, Status: "error", Error: err.Error(), ErrorCode: "invalid_request", Recovery: "fix_request"}
			invalid = true
		}
	}
	if invalid {
		out.Results = validation
		return errorCallOutput(out), out, nil
	}
	for i := 0; i < len(in.Items); i++ {
		item := in.Items[i]
		if mutation && isNativeTransaction(verb, item) {
			end := i + 1
			for end < len(in.Items) && isNativeTransaction(verb, in.Items[end]) && in.Items[end].PlanID == item.PlanID {
				end++
			}
			if end-i > 1 && nativeBatchIdentifiable(verb, in.Items[i:end]) {
				results := t.nativeTransactions(ctx, verb, i, in.Items[i:end])
				out.Results = append(out.Results, results...)
				failed := false
				for _, result := range results {
					failed = failed || result.Status != "success"
				}
				if failed {
					appendNotAttempted(&out, end, len(in.Items))
					break
				}
				i = end - 1
				continue
			}
		}
		var r Result
		if verb == ToolCreate && item.Type == "export" {
			r = t.export(ctx, i, item)
		} else {
			r = t.provider(ctx, i, verb, item)
		}
		if mutation && r.Status == "success" {
			if verb == ToolUpdate && item.Type == "transaction" {
				r = confirmTransactionFields(item, r)
			}
			r = r.acknowledge()
		}
		out.Results = append(out.Results, r)
		if (mutation || r.ErrorCode == "rate_limited" || r.ErrorCode == "authentication" || r.ErrorCode == "permission" || r.ErrorCode == "canceled" || r.ErrorCode == "local_state" || (r.ErrorCode == "upstream_unavailable" && r.Recovery == "wait")) && r.Status != "success" {
			appendNotAttempted(&out, i+1, len(in.Items))
			break
		}
	}
	return finishOutput(out, mutation)
}

func isNativeTransaction(verb string, item Item) bool {
	return item.Type == "transaction" && (verb == ToolCreate || verb == ToolUpdate)
}
func nativeBatchIdentifiable(verb string, items []Item) bool {
	seen := map[string]bool{}
	for _, item := range items {
		id, importID := transactionIdentity(item)
		if verb == ToolCreate {
			id = ""
		}
		if id == "" && importID == "" {
			return false
		}
		key := importID
		if key == "" {
			key = "id:" + id
		}
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}
func transactionIdentity(item Item) (string, string) {
	id := itemID(item)
	var body struct {
		ID       *string `json:"id"`
		ImportID *string `json:"import_id"`
	}
	if json.Unmarshal(item.Data, &body) == nil {
		if id == "" && body.ID != nil {
			id = *body.ID
		}
		if body.ImportID != nil {
			return id, *body.ImportID
		}
	}
	return id, ""
}

// nativeTransactions deliberately reports one result for each input but does
// not infer a per-item created ID from provider ordering. Updates retain their
// caller-supplied ID, and duplicate imports retain the matched import ID.
func (t *Tools) nativeTransactions(ctx context.Context, verb string, start int, items []Item) []Result {
	data := make([]json.RawMessage, len(items))
	for i, item := range items {
		data[i] = item.Data
		if verb == ToolUpdate {
			id, importID := transactionIdentity(item)
			if importID == "" {
				var fields map[string]json.RawMessage
				if json.Unmarshal(item.Data, &fields) != nil {
					return groupResults(start, items, Result{Status: "error", Error: "invalid transaction fields"})
				}
				fields["id"], _ = json.Marshal(id)
				data[i], _ = json.Marshal(fields)
			}
		}
	}
	body, err := json.Marshal(map[string][]json.RawMessage{"transactions": data})
	if err != nil {
		return groupResults(start, items, Result{Status: "error", Error: "invalid provider request body"})
	}
	method := http.MethodPost
	if verb == ToolUpdate {
		method = http.MethodPatch
	}
	target := t.baseURL + "/plans/" + url.PathEscape(items[0].PlanID) + "/transactions"
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(string(body)))
	if err != nil {
		return groupResults(start, items, Result{Status: "error", Error: "provider request construction failed"})
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	raw, failure := t.dispatch(ctx, req, "transaction")
	if failure.Status != "" {
		return groupResults(start, items, failure)
	}
	provider := projectProviderResult(start, raw, "transactions", "transaction_ids", "duplicate_import_ids")
	if provider.Status != "success" {
		provider.Status = "uncertain"
		return groupResults(start, items, provider)
	}
	results := correlateNativeResults(start, items, provider)
	if verb == ToolUpdate {
		for i, item := range items {
			results[i].Data = raw
			results[i] = confirmTransactionFields(item, results[i])
			results[i].Data = nil
		}
	}
	return results
}
func groupResults(start int, items []Item, base Result) []Result {
	out := make([]Result, len(items))
	for i := range items {
		out[i] = base
		out[i].Index = start + i
		out[i].Data = nil
		out[i].collections = nil
		out[i].Artifact = nil
	}
	return out
}
func correlateNativeResults(start int, items []Item, provider Result) []Result {
	duplicates := map[string]bool{}
	for _, id := range provider.DuplicateImportIDs {
		duplicates[id] = true
	}
	byImport := map[string]string{}
	savedIDs := map[string]bool{}
	for _, id := range provider.IDs {
		savedIDs[id] = true
	}
	for _, raw := range provider.collections["transactions"] {
		var row struct {
			ID       string `json:"id"`
			ImportID string `json:"import_id"`
		}
		_ = json.Unmarshal(raw, &row)
		if row.ID != "" {
			savedIDs[row.ID] = true
		}
		if row.ID != "" && row.ImportID != "" {
			byImport[row.ImportID] = row.ID
		}
	}
	out := make([]Result, len(items))
	for i, item := range items {
		id, importID := transactionIdentity(item)
		r := Result{Index: start + i, Status: "success"}
		if id != "" && savedIDs[id] {
			r.IDs = []string{id}
		} else if importID != "" && byImport[importID] != "" {
			r.IDs = []string{byImport[importID]}
		} else if importID != "" && duplicates[importID] {
			r.DuplicateImportIDs = []string{importID}
		} else {
			r.Status = "uncertain"
			r.Error = "provider did not return a correlatable transaction identity"
		}
		out[i] = r
	}
	return out
}
func appendNotAttempted(out *Output, start, n int) {
	for i := start; i < n; i++ {
		r := Result{Index: i, Status: "not_attempted", Error: "not dispatched after an earlier batch failure"}
		if len(out.Results) > 0 {
			previous := out.Results[len(out.Results)-1]
			r.ErrorCode = previous.ErrorCode
			r.Recovery = previous.Recovery
			r.RetryAt = previous.RetryAt
		}
		out.Results = append(out.Results, r)
	}
}
func callOutput(out Output) *sdk.CallToolResult {
	b, _ := json.Marshal(out)
	failed := false
	for _, r := range out.Results {
		failed = failed || r.Status != "success"
	}
	return &sdk.CallToolResult{IsError: failed, Content: []sdk.Content{&sdk.TextContent{Text: "YNAB batch results; inspect each item outcome and recovery guidance."}}, StructuredContent: json.RawMessage(b)}
}
func errorCallOutput(out Output) *sdk.CallToolResult {
	result := callOutput(out)
	result.IsError = true
	return result
}

type operation struct {
	method, path string
	requiresID   bool
}

func route(verb string, x Item) (operation, error) {
	p := func(s string) string { return strings.ReplaceAll(s, "{plan}", url.PathEscape(x.PlanID)) }
	if verb == ToolImport && x.Type == "bank_import" {
		return operation{"POST", p("/plans/{plan}/transactions/import"), false}, nil
	}
	if x.Type == "user" && verb == ToolGet {
		return operation{"GET", "/user", false}, nil
	}
	if x.Type == "plan" {
		if verb == ToolList {
			return operation{"GET", "/plans", false}, nil
		}
		if verb == ToolGet {
			return operation{"GET", p("/plans/{plan}"), false}, nil
		}
	}
	if x.Type == "settings" && verb == ToolGet {
		return operation{"GET", p("/plans/{plan}/settings"), false}, nil
	}
	base := map[string]string{"account": "accounts", "category": "categories", "category_group": "category_groups", "payee": "payees", "payee_location": "payee_locations", "month": "months", "money_movement": "money_movements", "money_movement_group": "money_movement_groups", "transaction": "transactions", "scheduled_transaction": "scheduled_transactions"}[x.Type]
	if base == "" {
		return operation{}, fmt.Errorf("unsupported %s type %q", verb, x.Type)
	}
	if x.Type == "transaction" {
		return transactionRoute(verb, x, p)
	}
	if x.Type == "money_movement" || x.Type == "money_movement_group" {
		if x.Month != "" && verb == ToolList {
			return operation{"GET", p("/plans/{plan}/months/") + url.PathEscape(x.Month) + "/" + base, false}, nil
		}
		if verb == ToolList {
			return operation{"GET", p("/plans/{plan}/") + base, false}, nil
		}
	}
	if x.Type == "payee_location" && verb == ToolList && x.PayeeID != "" {
		return operation{"GET", p("/plans/{plan}/payees/") + url.PathEscape(x.PayeeID) + "/payee_locations", false}, nil
	}
	if x.Type == "month" {
		if verb == ToolList {
			return operation{"GET", p("/plans/{plan}/months"), false}, nil
		}
		if verb == ToolGet {
			return operation{"GET", p("/plans/{plan}/months/") + url.PathEscape(x.Month), true}, nil
		}
	}
	if x.Type == "category" && x.Month != "" {
		q := p("/plans/{plan}/months/") + url.PathEscape(x.Month) + "/categories/" + url.PathEscape(x.CategoryID)
		if verb == ToolGet {
			return operation{"GET", q, true}, nil
		}
		if verb == ToolUpdate {
			return operation{"PATCH", q, true}, nil
		}
	}
	path := p("/plans/{plan}/") + base
	switch verb {
	case ToolList:
		return operation{"GET", path, false}, nil
	case ToolGet:
		return operation{"GET", path + "/" + url.PathEscape(itemID(x)), true}, nil
	case ToolCreate:
		return operation{"POST", path, false}, nil
	case ToolUpdate:
		method := "PATCH"
		if x.Type == "scheduled_transaction" {
			method = "PUT"
		}
		return operation{method, path + "/" + url.PathEscape(itemID(x)), true}, nil
	case ToolDelete:
		if x.Type == "scheduled_transaction" {
			return operation{"DELETE", path + "/" + url.PathEscape(itemID(x)), true}, nil
		}
	}
	return operation{}, fmt.Errorf("unsupported %s type %q", verb, x.Type)
}
func transactionRoute(verb string, x Item, p func(string) string) (operation, error) {
	base := p("/plans/{plan}/transactions")
	if verb == ToolImport {
		return operation{"POST", base + "/import", false}, nil
	}
	if verb == ToolList {
		if x.AccountID != "" {
			base = p("/plans/{plan}/accounts/") + url.PathEscape(x.AccountID) + "/transactions"
		}
		if x.CategoryID != "" {
			base = p("/plans/{plan}/categories/") + url.PathEscape(x.CategoryID) + "/transactions"
		}
		if x.PayeeID != "" {
			base = p("/plans/{plan}/payees/") + url.PathEscape(x.PayeeID) + "/transactions"
		}
		if x.Month != "" {
			base = p("/plans/{plan}/months/") + url.PathEscape(x.Month) + "/transactions"
		}
		return operation{"GET", base, false}, nil
	}
	if verb == ToolGet {
		return operation{"GET", base + "/" + url.PathEscape(itemID(x)), true}, nil
	}
	if verb == ToolCreate {
		return operation{"POST", base, false}, nil
	}
	if verb == ToolUpdate {
		if itemID(x) == "" {
			return operation{"PATCH", base, false}, nil
		}
		return operation{"PUT", base + "/" + url.PathEscape(itemID(x)), true}, nil
	}
	if verb == ToolDelete {
		return operation{"DELETE", base + "/" + url.PathEscape(itemID(x)), true}, nil
	}
	return operation{}, fmt.Errorf("unsupported %s type transaction", verb)
}
func itemID(x Item) string {
	if x.ID != "" {
		return x.ID
	}
	switch x.Type {
	case "transaction":
		return x.TransactionID
	case "account":
		return x.AccountID
	case "category":
		return x.CategoryID
	case "category_group":
		return x.CategoryGroupID
	case "payee":
		return x.PayeeID
	case "payee_location":
		return x.PayeeLocationID
	case "scheduled_transaction":
		return x.ScheduledTransactionID
	case "month":
		return x.Month
	}
	return ""
}

func validateItem(verb string, x Item) error {
	if x.Type == "" {
		return errors.New("type is required")
	}
	for _, value := range []string{x.PlanID, itemID(x), x.AccountID, x.CategoryID, x.PayeeID, x.CategoryGroupID} {
		if value == "." || value == ".." || strings.ContainsAny(value, "/\\?#\r\n") {
			return errors.New("invalid resource identity")
		}
	}
	if err := validatePayload(verb, x); err != nil {
		return err
	}
	if x.Type == "user" {
		if verb != ToolGet {
			return fmt.Errorf("unsupported %s type user", verb)
		}
		return nil
	}
	if x.Type == "plan" && verb == ToolList {
		return nil
	}
	if x.Type == "export" {
		if verb != ToolCreate {
			return errors.New("export is create-only")
		}
		return validateExport(x)
	}
	if x.PlanID == "" {
		return errors.New("plan_id is required")
	}
	if _, err := route(verb, x); err != nil {
		return err
	}
	if (verb == ToolGet || verb == ToolDelete || (verb == ToolUpdate && !(x.Type == "transaction" && transactionLookup(x)))) && itemID(x) == "" && x.Type != "plan" && x.Type != "settings" {
		return errors.New("resource id is required")
	}
	if x.Type == "transaction" && verb == ToolList {
		return validateDates(x)
	}
	if x.Type == "category" && x.Month != "" && verb == ToolList {
		return errors.New("month category requires get or update")
	}
	return nil
}
func transactionLookup(x Item) bool {
	if x.Type != "transaction" || len(x.Data) == 0 {
		return false
	}
	var body struct {
		ID       *string `json:"id"`
		ImportID *string `json:"import_id"`
	}
	if json.Unmarshal(x.Data, &body) != nil {
		return false
	}
	return (body.ID != nil && *body.ID != "") || (body.ImportID != nil && *body.ImportID != "")
}
func validateDates(x Item) error {
	if x.SinceDate == "" || x.UntilDate == "" {
		return errors.New("since_date and until_date are required for transaction lists")
	}
	a, e := time.Parse("2006-01-02", x.SinceDate)
	if e != nil {
		return errors.New("since_date must be YYYY-MM-DD")
	}
	b, e := time.Parse("2006-01-02", x.UntilDate)
	if e != nil || b.Before(a) {
		return errors.New("until_date must be YYYY-MM-DD and not precede since_date")
	}
	monthPrefix := x.Month
	if monthPrefix == "current" {
		monthPrefix = time.Now().UTC().Format("2006-01")
	}
	if len(monthPrefix) == len("2006-01-02") {
		monthPrefix = monthPrefix[:7]
	}
	if x.Month != "" && (!strings.HasPrefix(x.SinceDate, monthPrefix) || !strings.HasPrefix(x.UntilDate, monthPrefix)) {
		return errors.New("month transaction dates must remain within month")
	}
	return nil
}
func validateExport(x Item) error {
	if x.PlanID == "" || x.Filename == "" || x.Source == "" || x.Format == "" {
		return errors.New("export requires plan_id, filename, source, and format")
	}
	if x.Source == "plan" && x.Format != "json" {
		return errors.New("plan exports require json")
	}
	if x.Source != "plan" && x.Source != "transactions" {
		return errors.New("unsupported export source")
	}
	if x.Source == "transactions" {
		if e := validateDates(x); e != nil {
			return e
		}
		if x.Format != "json" && x.Format != "csv" {
			return errors.New("transaction export format must be json or csv")
		}
	}
	if filepath.Base(x.Filename) != x.Filename || strings.Contains(x.Filename, "..") || !strings.HasSuffix(x.Filename, "."+x.Format) {
		return errors.New("filename must be a confined basename with matching extension")
	}
	return nil
}

func (t *Tools) provider(ctx context.Context, index int, verb string, x Item) Result {
	return t.requestProvider(ctx, index, verb, x, true)
}

func (t *Tools) requestProvider(ctx context.Context, index int, verb string, x Item, project bool) Result {
	op, err := route(verb, x)
	if err != nil {
		return Result{Index: index, Status: "error", Error: err.Error()}
	}
	q := url.Values{}
	if x.SinceDate != "" {
		q.Set("since_date", x.SinceDate)
	}
	if x.UntilDate != "" {
		q.Set("until_date", x.UntilDate)
	}
	if x.LastKnowledge != nil {
		q.Set("last_knowledge_of_server", fmt.Sprint(*x.LastKnowledge))
	}
	if x.Filter != "" {
		q.Set("type", x.Filter)
	}
	if x.IncludeAccounts && verb == ToolList && x.Type == "plan" {
		q.Set("include_accounts", "true")
	}
	target := t.baseURL + op.path
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	var body io.Reader
	if (op.method == "POST" || op.method == "PATCH" || op.method == "PUT") && verb != ToolImport {
		encoded, err := providerBody(x)
		if err != nil {
			return Result{Index: index, Status: "error", Error: "invalid provider request body"}
		}
		body = strings.NewReader(string(encoded))
	}
	req, err := http.NewRequestWithContext(ctx, op.method, target, body)
	if err != nil {
		return Result{Index: index, Status: "error", Error: "provider request construction failed"}
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	data, failure := t.dispatch(ctx, req, x.Type)
	if failure.Status != "" {
		failure.Index = index
		return failure
	}
	keys := []string{x.Type}
	if verb == ToolList {
		keys = []string{providerCollections[x.Type]}
		if x.Type == "category" {
			keys = []string{"category_groups"}
		}
	}
	if verb == ToolImport {
		keys = []string{"transaction_ids"}
	}
	if x.Type == "transaction" && op.method == "PATCH" {
		keys = []string{"transactions", "transaction_ids", "duplicate_import_ids"}
	}
	if !project {
		return Result{Index: index, Status: "success", Data: data}
	}
	r := projectProviderResult(index, data, keys...)
	if r.Status == "error" && (verb == ToolCreate || verb == ToolUpdate || verb == ToolDelete || verb == ToolImport) {
		r.Status = "uncertain"
	}
	return r
}

func providerBody(x Item) ([]byte, error) {
	if len(x.Data) == 0 {
		return nil, errors.New("data is required")
	}
	key := x.Type
	if key == "transaction" && itemID(x) == "" && transactionLookup(x) {
		return json.Marshal(map[string][]json.RawMessage{"transactions": []json.RawMessage{x.Data}})
	}
	return json.Marshal(map[string]json.RawMessage{key: x.Data})
}
