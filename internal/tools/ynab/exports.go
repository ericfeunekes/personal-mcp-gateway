package ynab

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const exportWriteChunk = 32 << 10

// export fetches one complete provider response and materializes it without
// returning that financial content to the model-visible result.
func (t *Tools) export(ctx context.Context, index int, x Item) Result {
	select {
	case t.exportSlots <- struct{}{}:
		defer func() { <-t.exportSlots }()
	case <-ctx.Done():
		return Result{Index: index, Status: "not_attempted", Error: "call canceled before export admission", ErrorCode: "canceled", Recovery: "fix_request"}
	}
	if err := ctx.Err(); err != nil {
		return Result{Index: index, Status: "not_attempted", Error: "call canceled before export dispatch", ErrorCode: "canceled", Recovery: "fix_request"}
	}

	source, verb := x, ToolGet
	if x.Source == "plan" {
		source.Type = "plan"
	} else {
		source.Type, verb = "transaction", ToolList
	}
	r := t.requestProvider(ctx, index, verb, source, false)
	if r.Status != "success" {
		return r
	}
	if err := validYNABEnvelope(r.Data, x.Source); err != nil {
		return Result{Index: index, Status: "error", Error: "provider returned an invalid export response", ErrorCode: "invalid_response", Recovery: "contact_operator"}
	}

	payload := r.Data
	var records int
	var knowledge *int64
	var err error
	if x.Source == "transactions" {
		records, knowledge, err = transactionExportMetadata(r.Data)
		if err != nil {
			return Result{Index: index, Status: "error", Error: "provider transactions cannot be read for export", ErrorCode: "invalid_response", Recovery: "contact_operator"}
		}
		if x.Format == "csv" {
			payload, err = transactionCSV(r.Data)
			if err != nil {
				return Result{Index: index, Status: "error", Error: "provider transactions cannot be rendered as CSV", ErrorCode: "invalid_response", Recovery: "contact_operator"}
			}
		}
	}
	if !withinExportLimit(int64(len(payload))) {
		return Result{Index: index, Status: "error", Error: "export exceeds 64 MiB", ErrorCode: "response_too_large", Recovery: "fix_request"}
	}
	if err := ctx.Err(); err != nil {
		return Result{Index: index, Status: "error", Error: "export canceled", ErrorCode: "canceled", Recovery: "fix_request"}
	}
	if err := validateExportFilename(x.Filename); err != nil {
		return Result{Index: index, Status: "error", Error: "invalid export destination", ErrorCode: "invalid_request", Recovery: "fix_request"}
	}

	root, err := openExportRoot(t.exportRoot)
	if err != nil {
		return Result{Index: index, Status: "error", Error: "export root unavailable", ErrorCode: "local_state", Recovery: "contact_operator"}
	}
	defer root.Close()
	if _, err := root.Lstat(x.Filename); err == nil {
		return Result{Index: index, Status: "error", Error: "export filename already exists", ErrorCode: "conflict", Recovery: "fix_request"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Result{Index: index, Status: "error", Error: "export destination unavailable", ErrorCode: "local_state", Recovery: "contact_operator"}
	}
	tmp, file, err := newExportTemp(root)
	if err != nil {
		return Result{Index: index, Status: "error", Error: "unable to create export", ErrorCode: "local_state", Recovery: "contact_operator"}
	}
	defer root.Remove(tmp)
	if err := writeExportPayload(ctx, file, payload); err != nil {
		file.Close()
		code, recovery := "local_state", "contact_operator"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			code, recovery = "canceled", "fix_request"
		}
		return Result{Index: index, Status: "error", Error: exportWriteError(err), ErrorCode: code, Recovery: recovery}
	}
	if err := file.Close(); err != nil {
		return Result{Index: index, Status: "error", Error: "unable to write export", ErrorCode: "local_state", Recovery: "contact_operator"}
	}
	if err := ctx.Err(); err != nil {
		return Result{Index: index, Status: "error", Error: "export canceled", ErrorCode: "canceled", Recovery: "fix_request"}
	}
	// Link has absent-destination semantics, so an interloper cannot be overwritten
	// between the check above and publication. The temporary name is removed after
	// successful publication by the deferred private-stage removal.
	if err := root.Link(tmp, x.Filename); err != nil {
		return Result{Index: index, Status: "error", Error: "unable to publish export", ErrorCode: "local_state", Recovery: "contact_operator"}
	}
	mime := "application/json"
	if x.Format == "csv" {
		mime = "text/csv"
	}
	var count *int
	if x.Source == "transactions" {
		count = &records
	}
	return Result{Index: index, Status: "success", Artifact: &Artifact{
		Filename: x.Filename, Format: x.Format, MIMEType: mime, Bytes: int64(len(payload)), Location: filepath.Join(t.exportRoot, x.Filename),
		Source: x.Source, PlanID: x.PlanID, SinceDate: x.SinceDate, UntilDate: x.UntilDate, AccountID: x.AccountID, CategoryID: x.CategoryID, PayeeID: x.PayeeID, Month: x.Month,
		RecordCount: count, ServerKnowledge: knowledge,
	}}
}

func validateExportFilename(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || strings.Contains(name, "..") {
		return errors.New("not a basename")
	}
	return nil
}

func openExportRoot(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) {
		return nil, errors.New("root is not absolute")
	}
	// Darwin presents its system temporary directories through fixed /tmp and
	// /var compatibility symlinks. Canonicalize only those OS aliases; every
	// caller-controlled component below them is still opened no-follow.
	clean := filepath.Clean(name)
	for _, alias := range []string{"/tmp", "/var"} {
		if clean == alias || strings.HasPrefix(clean, alias+"/") {
			if info, err := os.Lstat(alias); err == nil && info.Mode()&os.ModeSymlink != 0 {
				name = "/private" + clean
			}
			break
		}
	}
	root, err := os.OpenRoot(string(os.PathSeparator))
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(name), string(os.PathSeparator)), string(os.PathSeparator)) {
		if part == "" || part == "." || part == ".." {
			root.Close()
			return nil, errors.New("invalid root component")
		}
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				root.Close()
				return nil, err
			}
			info, err = root.Lstat(part)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			root.Close()
			return nil, errors.New("invalid root component")
		}
		next, err := root.OpenRoot(part)
		if err != nil {
			root.Close()
			return nil, err
		}
		openedInfo, statErr := next.Stat(".")
		root.Close()
		if statErr != nil || !os.SameFile(info, openedInfo) {
			next.Close()
			return nil, errors.New("root changed during open")
		}
		root = next
	}
	return root, nil
}

func validYNABEnvelope(raw []byte, source string) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 || !json.Valid(envelope.Data) {
		return errors.New("invalid provider JSON")
	}
	if len(envelope.Data) == 0 || envelope.Data[0] != '{' {
		return errors.New("missing provider data object")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(envelope.Data, &data) != nil {
		return errors.New("invalid provider data")
	}
	value := bytes.TrimSpace(data[source])
	if len(value) == 0 || source == "plan" && value[0] != '{' || source == "transactions" && value[0] != '[' {
		return errors.New("missing export source")
	}
	return nil
}

func newExportTemp(root *os.Root) (string, *os.File, error) {
	var token [16]byte
	for range 8 {
		if _, err := rand.Read(token[:]); err != nil {
			return "", nil, err
		}
		name := ".export-" + hex.EncodeToString(token[:]) + ".partial"
		f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return name, f, err
	}
	return "", nil, errors.New("temporary filename collision")
}

func writeExport(ctx context.Context, f *os.File, payload []byte) error {
	for len(payload) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := exportWriteChunk
		if n > len(payload) {
			n = len(payload)
		}
		written, err := f.Write(payload[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
		payload = payload[n:]
	}
	return ctx.Err()
}

// writeExportPayload is a package-private fault seam for private-stage removal
// tests. Production always uses writeExport.
var writeExportPayload = writeExport

func withinExportLimit(size int64) bool { return size <= maxExportBytes }

func exportWriteError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "export canceled"
	}
	return "unable to write export"
}

func transactionExportMetadata(raw []byte) (int, *int64, error) {
	var envelope struct {
		Data struct {
			Transactions    []json.RawMessage `json:"transactions"`
			ServerKnowledge json.RawMessage   `json:"server_knowledge"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return 0, nil, err
	}
	var knowledge *int64
	if len(envelope.Data.ServerKnowledge) != 0 && string(envelope.Data.ServerKnowledge) != "null" {
		var n json.Number
		if err := json.Unmarshal(envelope.Data.ServerKnowledge, &n); err == nil {
			if v, err := n.Int64(); err == nil {
				knowledge = &v
			}
		}
	}
	return len(envelope.Data.Transactions), knowledge, nil
}

func transactionCSV(raw []byte) ([]byte, error) {
	var envelope struct {
		Data struct {
			Transactions []map[string]any `json:"transactions"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&envelope); err != nil {
		return nil, err
	}
	var b strings.Builder
	w := csv.NewWriter(&b)
	if err := w.Write([]string{"id", "account_id", "date", "amount_milliunits", "category_id", "payee_name", "memo", "cleared", "approved", "flag_color", "deleted", "subtransactions_json"}); err != nil {
		return nil, err
	}
	for _, transaction := range envelope.Data.Transactions {
		sub := ""
		if v, ok := transaction["subtransactions"]; ok && v != nil {
			encoded, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			sub = string(encoded)
		}
		row := []string{csvValue(transaction["id"]), csvValue(transaction["account_id"]), csvValue(transaction["date"]), csvValue(transaction["amount"]), csvValue(transaction["category_id"]), csvValue(transaction["payee_name"]), csvValue(transaction["memo"]), csvValue(transaction["cleared"]), csvValue(transaction["approved"]), csvValue(transaction["flag_color"]), csvValue(transaction["deleted"]), csvValue(sub)}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return []byte(b.String()), w.Error()
}

func csvValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		if len(text) > 0 && strings.ContainsRune("=+-@", rune(text[0])) {
			return "'" + text
		}
		return text
	}
	return fmt.Sprint(value)
}
