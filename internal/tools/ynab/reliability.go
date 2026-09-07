package ynab

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const readAttempts = 2
const retryCap = time.Second

// Go may invisibly replay requests on reused connections or HTTP/2 streams.
// Fresh HTTP/1 connections ensure every replay passes our reservation gate.
func newProviderTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	// Clone retains the default transport's initialized ALPN list. Protocols
	// alone does not remove h2 from that TLS advertisement.
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	return transport
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func fullJitter(cap time.Duration) time.Duration { return time.Duration(rand.Int64N(int64(cap) + 1)) }
func transientStatus(status int) bool {
	switch status {
	case 408, 500, 502, 503, 504:
		return true
	}
	return false
}
func retryTime(value string, now time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return now.Add(time.Duration(seconds) * time.Second), true
	}
	if at, err := http.ParseTime(value); err == nil {
		if !time.Unix(0, at.UnixNano()).Equal(at) {
			return time.Time{}, false
		}
		if at.Before(now) {
			at = now
		}
		return at, true
	}
	return time.Time{}, false
}
func canceledResult(dispatched, write bool) Result {
	r := Result{Status: "not_attempted", Error: "call canceled before dispatch", ErrorCode: "canceled", Recovery: "retry_read"}
	if dispatched {
		r.Status = "error"
		r.Error = "provider call canceled"
	}
	if write {
		r.Recovery = "fix_request"
		if dispatched {
			r.Status = "uncertain"
			r.ErrorCode = "uncertain_write"
			r.Recovery = "inspect_before_retry"
		}
	}
	return r
}
func providerFailure(code, message string, status int, write bool, ambiguous bool) Result {
	r := Result{Status: "error", Error: message, ErrorCode: code, HTTPStatus: status, Recovery: "fix_request"}
	switch code {
	case "authentication", "permission":
		r.Recovery = "fix_credentials"
	case "upstream_unavailable":
		r.Recovery = "retry_read"
	case "invalid_response", "local_state":
		r.Recovery = "contact_operator"
	}
	if write && ambiguous {
		r.Status = "uncertain"
		r.ErrorCode = "uncertain_write"
		r.Recovery = "inspect_before_retry"
	}
	return r
}

func (t *Tools) providerQuota() *quotaStore {
	t.quotaOnce.Do(func() {
		if t.quota == nil && t.quotaPath != "" {
			t.quota, t.quotaErr = openQuotaStore(t.quotaPath)
		}
	})
	return t.quota
}

// dispatch is the only HTTP attempt boundary, including native batches and
// export reads. GET alone can repeat; no write is automatically replayed.
func (t *Tools) dispatch(ctx context.Context, req *http.Request, resource string) ([]byte, Result) {
	write := req.Method != http.MethodGet
	if ctx.Err() != nil {
		return nil, canceledResult(false, write)
	}
	q := t.providerQuota()
	if q == nil || t.quotaErr != nil {
		return nil, stateFailure()
	}
	for attempt := 0; attempt < readAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, canceledResult(attempt > 0, write)
		}
		select {
		case t.providerSlots <- struct{}{}:
		case <-ctx.Done():
			return nil, canceledResult(attempt > 0, write)
		}
		p, denial := q.reserve(ctx, tokenKey(t.token), req.Method+":"+resource, t.now())
		if denial.Status != "" {
			<-t.providerSlots
			if ctx.Err() != nil {
				return nil, canceledResult(attempt > 0, write)
			}
			if attempt > 0 {
				denial.Status = "error"
			}
			return nil, denial
		}
		if ctx.Err() != nil {
			<-t.providerSlots
			t.finishAttempt(q, p, attemptNeutral, time.Time{}, time.Time{})
			return nil, canceledResult(attempt > 0, write)
		}
		resp, err := t.client.Do(req.Clone(ctx))
		var raw []byte
		var result Result
		transient := false
		var hint time.Time
		var tokenUntil time.Time
		if err != nil {
			transient = ctx.Err() == nil
			result = providerFailure("upstream_unavailable", "provider request did not complete", 0, write, true)
		} else {
			hint, _ = retryTime(resp.Header.Get("Retry-After"), t.now())
			if resp.StatusCode == http.StatusTooManyRequests {
				if hint.IsZero() {
					hint = t.now().Add(quotaWindow)
				}
				if hint.Before(t.now().Add(time.Second)) {
					hint = t.now().Add(time.Second)
				}
				// Headers are authoritative even if the error body is malformed or
				// truncated. Persist with completion; never parse the error body.
				tokenUntil = hint
				result = waitResult("rate_limited", hint)
				result.Status = "error"
				result.HTTPStatus = 429
				result.RetryAfter = retryAfter(resp.Header.Get("Retry-After"))
			} else if resp.StatusCode < 200 || resp.StatusCode > 299 {
				code := "invalid_request"
				switch resp.StatusCode {
				case 401:
					code = "authentication"
				case 403:
					code = "permission"
				case 404:
					code = "not_found"
				case 409:
					code = "conflict"
				default:
					if resp.StatusCode >= 500 || resp.StatusCode == 408 {
						code = "upstream_unavailable"
					}
				}
				transient = transientStatus(resp.StatusCode)
				result = providerFailure(code, fmt.Sprintf("provider returned HTTP %d", resp.StatusCode), resp.StatusCode, write, resp.StatusCode >= 500 || resp.StatusCode == 408)
			} else {
				raw, err = io.ReadAll(io.LimitReader(resp.Body, maxProviderBytes+1))
				if err != nil {
					transient = ctx.Err() == nil
					result = providerFailure("upstream_unavailable", "provider response did not complete", resp.StatusCode, write, true)
				} else if int64(len(raw)) > maxProviderBytes {
					result = providerFailure("response_too_large", "provider response exceeds 32 MiB", resp.StatusCode, write, true)
				}
			}
			resp.Body.Close()
		}
		<-t.providerSlots
		var deferUntil time.Time
		if transient {
			deferUntil = hint
		}
		outcome := attemptHealthy
		if transient {
			outcome = attemptTransient
		}
		if ctx.Err() != nil {
			outcome = attemptNeutral
		}
		retryAt, recordErr := t.finishAttempt(q, p, outcome, deferUntil, tokenUntil)
		if recordErr != nil {
			return nil, providerFailure("local_state", "unable to record provider recovery state", 0, write, true)
		}
		if ctx.Err() != nil {
			return nil, canceledResult(true, write)
		}
		if result.Status == "" {
			return raw, Result{}
		}
		if !write && transient && !retryAt.IsZero() {
			result.Recovery = "wait"
			result.RetryAt = retryAt.UTC().Format(time.RFC3339)
			return nil, result
		}
		if write || !transient || attempt+1 == readAttempts {
			return nil, result
		}
		delay := t.jitter(retryCap)
		if !hint.IsZero() && hint.Sub(t.now()) > delay {
			delay = hint.Sub(t.now())
		}
		// Long server-directed waits belong in caller recovery, not an MCP call.
		if delay > retryCap {
			result.Recovery = "wait"
			result.RetryAt = hint.UTC().Format(time.RFC3339)
			return nil, result
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return nil, result
		}
		if err := t.sleep(ctx, delay); err != nil {
			return nil, canceledResult(true, false)
		}
	}
	panic("unreachable bounded provider attempt loop")
}
func (t *Tools) finishAttempt(q *quotaStore, p attemptPermit, outcome attemptOutcome, familyUntil, tokenUntil time.Time) (time.Time, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return q.record(ctx, p, outcome, t.now(), familyUntil, tokenUntil)
}
