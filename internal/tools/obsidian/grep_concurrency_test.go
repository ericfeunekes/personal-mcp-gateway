package obsidian

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"personal-mcp-gateway/internal/fsx"
)

func TestGrepConcurrentCeilingAndCanonicalReduction(t *testing.T) {
	root := t.TempDir()
	for i := range 12 {
		writeGrepFile(t, root, string(rune('a'+i))+".md", strings.Repeat("absent literal workload\n", 2*1024))
	}
	tools := grepTools(t, root)
	literal := false
	release := make(chan struct{})
	activeEight := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	maxActive := 0
	maxInFlight := 0
	hooks := &grepConcurrentHooks{
		gate: func(ctx context.Context, _ int) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		observe: func(snapshot grepConcurrentSnapshot) {
			mu.Lock()
			if snapshot.Active > maxActive {
				maxActive = snapshot.Active
			}
			if snapshot.InFlight > maxInFlight {
				maxInFlight = snapshot.InFlight
			}
			if snapshot.Active == grepWorkerCeiling {
				once.Do(func() { close(activeEight) })
			}
			mu.Unlock()
		},
	}
	type result struct {
		out GrepOutput
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, out, err := tools.grep(context.Background(), GrepInput{Pattern: "not-present", Regex: &literal, CaseSensitive: true}, hooks)
		done <- result{out, err}
	}()
	select {
	case <-activeEight:
	case <-time.After(time.Second):
		t.Fatal("workers did not reach the fixed concurrent ceiling")
	}
	close(release)
	got := <-done
	if got.err != nil || !got.out.OK || !got.out.Coverage.ScopeComplete || got.out.Coverage.FilesScanned != 12 {
		t.Fatalf("out=%#v err=%v", got.out, got.err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maxActive != grepWorkerCeiling {
		t.Fatalf("max active workers = %d, want %d", maxActive, grepWorkerCeiling)
	}
	if maxInFlight != grepWorkerCeiling {
		t.Fatalf("max in-flight reservations = %d, want %d", maxInFlight, grepWorkerCeiling)
	}
}

func TestGrepConcurrentDiscardedSuffixErrorCannotOverrideResultBoundary(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "hit\n")
	if err := os.WriteFile(filepath.Join(root, "b.md"), append([]byte("broken "), 0xff), 0o600); err != nil {
		t.Fatal(err)
	}
	tools := grepTools(t, root)
	literal := false
	release := make(chan struct{})
	completedSuffix := make(chan struct{})
	var once sync.Once
	hooks := &grepConcurrentHooks{
		gate: func(ctx context.Context, sequence int) error {
			if sequence != 0 {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		},
		terminalEvent: func(sequence int) {
			if sequence != 0 {
				once.Do(func() { close(completedSuffix) })
			}
		},
	}
	done := make(chan GrepOutput, 1)
	go func() {
		_, out, _ := tools.grep(context.Background(), GrepInput{Pattern: "hit", Regex: &literal, CaseSensitive: true, Limit: 1}, hooks)
		done <- out
	}()
	select {
	case <-completedSuffix:
	case <-time.After(time.Second):
		t.Fatal("suffix scan did not complete before the canonical boundary")
	}
	close(release)
	out := <-done
	if !out.OK || len(out.Matches) != 1 || out.Matches[0].Path != "a.md" || out.Error != nil ||
		out.Coverage.StoppedBy != string(CursorStopResultLimit) {
		t.Fatalf("suffix error became authoritative: %#v", out)
	}
	if out.Coverage.FilesScanned != 1 || out.Coverage.BytesScanned != uint64(len("hit\n")) {
		t.Fatalf("speculative suffix leaked into public coverage: %#v", out.Coverage)
	}
	if out.Coverage.NextCursor == "" {
		t.Fatalf("result boundary omitted cursor: %#v", out.Coverage)
	}
	_, continued, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "hit", Regex: &literal, CaseSensitive: true, Limit: 1, Cursor: out.Coverage.NextCursor})
	if err != nil || continued.OK || continued.Error == nil || continued.Coverage.Continuation != CoverageContinuationRestart {
		t.Fatalf("cursor after discarded suffix error=%#v err=%v", continued, err)
	}
}

func TestGrepConcurrentSuffixMatchWaitsForCanonicalPrefix(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "hit first\n")
	writeGrepFile(t, root, "b.md", "hit second\n")
	tools := grepTools(t, root)
	literal := false
	release := make(chan struct{})
	seenSuffix := make(chan struct{})
	var once sync.Once
	hooks := &grepConcurrentHooks{gate: func(ctx context.Context, sequence int) error {
		if sequence != 0 {
			once.Do(func() { close(seenSuffix) })
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
			return nil
		}
	}}
	done := make(chan GrepOutput, 1)
	go func() {
		_, out, _ := tools.grep(context.Background(), GrepInput{Pattern: "hit", Regex: &literal, CaseSensitive: true, Limit: 2}, hooks)
		done <- out
	}()
	select {
	case <-seenSuffix:
	case <-time.After(time.Second):
		t.Fatal("suffix match was not scanned speculatively")
	}
	close(release)
	out := <-done
	if !out.OK || len(out.Matches) != 2 || out.Matches[0].Path != "a.md" || out.Matches[1].Path != "b.md" {
		t.Fatalf("non-canonical output: %#v", out)
	}
}

func TestGrepConcurrentCancellationJoinsBlockedWorkers(t *testing.T) {
	root := t.TempDir()
	for i := range 9 {
		writeGrepFile(t, root, string(rune('a'+i))+".md", strings.Repeat("blocked work\n", 4096))
	}
	tools := grepTools(t, root)
	tools.grepActivity = &fsx.SchedulerActivity{}
	literal := false
	entered := make(chan struct{})
	var once sync.Once
	hooks := &grepConcurrentHooks{gate: func(ctx context.Context, _ int) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan GrepOutput, 1)
	go func() {
		_, out, _ := tools.grep(ctx, GrepInput{Pattern: "missing", Regex: &literal, CaseSensitive: true}, hooks)
		done <- out
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("worker did not enter gate")
	}
	cancel()
	out := <-done
	if out.OK || out.Error == nil || out.Error.Code != "canceled" {
		t.Fatalf("cancellation output = %#v", out)
	}
	if snapshot := tools.grepActivity.Snapshot(); snapshot.Active != 0 || snapshot.InFlight != 0 {
		t.Fatalf("canceled scheduler retained work: %#v", snapshot)
	}
	_, followup, err := tools.Grep(context.Background(), nil, GrepInput{Pattern: "missing", Regex: &literal, CaseSensitive: true})
	if err != nil || !followup.OK {
		t.Fatalf("follow-up after canceled workers = %#v err=%v", followup, err)
	}
}

func TestGrepConcurrentGateErrorsAreStructured(t *testing.T) {
	root := t.TempDir()
	writeGrepFile(t, root, "a.md", "content\n")
	tools := grepTools(t, root)
	literal := false
	hooks := &grepConcurrentHooks{gate: func(context.Context, int) error { return errors.New("test gate") }}
	_, out, err := tools.grep(context.Background(), GrepInput{Pattern: "missing", Regex: &literal, CaseSensitive: true}, hooks)
	if err != nil || out.OK || out.Error == nil {
		t.Fatalf("out=%#v err=%v", out, err)
	}
}
