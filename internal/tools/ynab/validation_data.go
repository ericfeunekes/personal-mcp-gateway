package ynab

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
)

func validatePayload(verb string, x Item) error {
	scopes := 0
	for _, v := range []string{x.AccountID, x.CategoryID, x.PayeeID, x.Month} {
		if v != "" {
			scopes++
		}
	}
	if (x.Type == "transaction" && verb == ToolList || x.Type == "export" && x.Source == "transactions") && scopes > 1 {
		return errors.New("choose one transaction scope")
	}
	if verb != ToolCreate && verb != ToolUpdate || x.Type == "export" {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(x.Data, &fields) != nil || len(fields) == 0 {
		return errors.New("data must contain resource fields")
	}
	for _, name := range []string{"amount", "balance", "budgeted", "goal_target"} {
		if value, ok := fields[name]; ok && string(value) != "null" {
			var number int64
			if json.Unmarshal(value, &number) != nil {
				return errors.New("amount fields must be integer milliunits")
			}
		}
	}
	if _, ok := fields["goal_frequency"]; ok {
		target, present := fields["goal_target"]
		if !present || string(target) == "null" {
			return errors.New("goal_frequency requires goal_target")
		}
		if _, present = fields["goal_target_date"]; present {
			return errors.New("goal_frequency cannot combine with goal_target_date")
		}
	}
	if x.Type != "transaction" && x.Type != "scheduled_transaction" {
		return nil
	}
	if x.Type == "transaction" && verb == ToolUpdate {
		identities := 0
		if itemID(x) != "" {
			identities++
		}
		for _, name := range []string{"id", "import_id"} {
			if value, ok := fields[name]; ok && string(value) != "null" {
				identities++
			}
		}
		if identities != 1 {
			return errors.New("transaction update requires one ID or import ID")
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if value, ok := fields["date"]; ok {
		var date string
		if json.Unmarshal(value, &date) != nil {
			return errors.New("date must be YYYY-MM-DD")
		}
		parsed, err := time.Parse("2006-01-02", date)
		if err != nil {
			return errors.New("date must be YYYY-MM-DD")
		}
		if x.Type == "transaction" && parsed.After(today) {
			return errors.New("future transactions require scheduled_transaction")
		}
		if x.Type == "scheduled_transaction" && (!parsed.After(today) || parsed.After(today.AddDate(5, 0, 0))) {
			return errors.New("scheduled date must be future and within five years")
		}
	}
	if x.Type == "scheduled_transaction" {
		if len(fields["date"]) == 0 || len(fields["account_id"]) == 0 {
			return errors.New("scheduled transactions require date and account_id")
		}
	}
	if subs, ok := fields["subtransactions"]; ok {
		var children []struct {
			Amount int64 `json:"amount"`
		}
		if json.Unmarshal(subs, &children) != nil || len(children) == 0 {
			return errors.New("split requires nonempty subtransactions with integer amounts")
		}
		if string(fields["category_id"]) != "null" {
			return errors.New("split category_id must be explicit null")
		}
		if verb == ToolCreate {
			var amount int64
			if json.Unmarshal(fields["amount"], &amount) != nil {
				return errors.New("split requires parent amount")
			}
			var sum int64
			for _, child := range children {
				n := child.Amount
				if n > 0 && sum > (1<<63-1)-n || n < 0 && sum < (-1<<63)-n {
					return errors.New("split amount overflow")
				}
				sum += n
			}
			if sum != amount {
				return errors.New("split amounts must sum to the parent amount")
			}
		}
	}
	return nil
}

func retryAfter(value string) string {
	if n, err := strconv.ParseUint(value, 10, 32); err == nil {
		return strconv.FormatUint(n, 10)
	}
	if date, err := httpDate(value); err == nil {
		return date.UTC().Format(time.RFC1123)
	}
	return ""
}

func httpDate(value string) (time.Time, error) { return time.Parse(time.RFC1123, value) }
