package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

type stubProvider struct {
	id      string
	calls   atomic.Int32
	results []stubResult // consumed in order; last entry repeats if exhausted
	// fetch, if non-nil, takes precedence over results — used by deterministic
	// tests that need to coordinate with the orchestrator (barriers, channel
	// signals, etc.) rather than play back canned results.
	fetch func(context.Context) (providers.ProviderOutput, error)
}

type stubResult struct {
	out providers.ProviderOutput
	err error
}

func (s *stubProvider) ID() string { return s.id }

func (s *stubProvider) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	s.calls.Add(1)
	if s.fetch != nil {
		return s.fetch(ctx)
	}
	idx := int(s.calls.Load()) - 1
	if idx >= len(s.results) {
		idx = len(s.results) - 1
	}
	r := s.results[idx]
	return r.out, r.err
}

func mkOutput(used float64) providers.ProviderOutput {
	return providers.ProviderOutput{Limits: map[string]providers.Limit{
		"five_hour": {UsedPercent: used, RemainingPercent: 100 - used, ResetsAt: time.Now(), ResetAfterSeconds: 1},
	}}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"all succeed", func(t *testing.T) {
			c := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
			cx := &stubProvider{id: "codex", results: []stubResult{{out: mkOutput(5)}}}
			cp := &stubProvider{id: "copilot", results: []stubResult{{out: mkOutput(8)}}}
			r, status := Run(context.Background(), []string{"claude", "codex", "copilot"}, []providers.Provider{c, cx, cp}, Options{})
			if status != StatusOK {
				t.Errorf("status = %d, want OK", status)
			}
			if len(r.Providers) != 3 {
				t.Errorf("expected 3 providers in report, got %d", len(r.Providers))
			}
			for _, id := range []string{"claude", "codex", "copilot"} {
				if r.Providers[id].Error != "" {
					t.Errorf("%s should have no error, got %q", id, r.Providers[id].Error)
				}
			}
		}},
		{"one fails", func(t *testing.T) {
			c := &stubProvider{id: "claude", results: []stubResult{{err: fmt.Errorf("%w: missing", providers.ErrAuthMissing)}}}
			cx := &stubProvider{id: "codex", results: []stubResult{{out: mkOutput(5)}}}
			r, status := Run(context.Background(), []string{"claude", "codex"}, []providers.Provider{c, cx}, Options{})
			if status != StatusAnyFailed {
				t.Errorf("status = %d, want AnyFailed", status)
			}
			if r.Providers["claude"].Error == "" {
				t.Errorf("claude should have error set")
			}
			if r.Providers["codex"].Error != "" {
				t.Errorf("codex should not have error: %s", r.Providers["codex"].Error)
			}
			if len(r.Providers["codex"].Limits) == 0 {
				t.Errorf("codex limits missing")
			}
		}},
		// TestRun_FetchInvokedOncePerProvider asserts that the orchestrator calls each
		// provider's Fetch exactly once per Run, even when Fetch returns ErrTransient.
		// HTTP-layer retries happen inside httpx.Doer; the orchestrator does not retry.
		{"fetch invoked once per provider", func(t *testing.T) {
			transient := &stubProvider{id: "claude", results: []stubResult{
				{err: fmt.Errorf("%w: HTTP 503", providers.ErrTransient)},
			}}
			ok := &stubProvider{id: "codex", results: []stubResult{{out: mkOutput(5)}}}

			_, status := Run(context.Background(), []string{"claude", "codex"}, []providers.Provider{transient, ok}, Options{})

			if transient.calls.Load() != 1 {
				t.Errorf("transient provider: expected 1 call, got %d", transient.calls.Load())
			}
			if ok.calls.Load() != 1 {
				t.Errorf("ok provider: expected 1 call, got %d", ok.calls.Load())
			}
			if status != StatusAnyFailed {
				t.Errorf("status = %d, want StatusAnyFailed (transient provider failed)", status)
			}
		}},
		{"auth error is not retried", func(t *testing.T) {
			p := &stubProvider{id: "claude", results: []stubResult{
				{err: fmt.Errorf("%w: missing", providers.ErrAuthMissing)},
			}}
			_, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
			if status != StatusAnyFailed {
				t.Errorf("status = %d, want AnyFailed", status)
			}
			if p.calls.Load() != 1 {
				t.Errorf("auth error should produce exactly one call; calls = %d", p.calls.Load())
			}
		}},
		{"debug writes lines", func(t *testing.T) {
			// Providers run concurrently; wrap the bytes.Buffer so per-provider
			// debug lines don't interleave mid-write. (Real CLI does the same via
			// realProviders wiring.)
			var buf bytes.Buffer
			safe := httpx.NewConcurrencySafeWriter(&buf)
			p1 := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
			p2 := &stubProvider{id: "codex", results: []stubResult{{out: mkOutput(5)}}}
			Run(context.Background(), []string{"claude", "codex"}, []providers.Provider{p1, p2}, Options{Debug: safe})
			out := buf.String()
			if !strings.Contains(out, "[debug] claude:") {
				t.Errorf("debug missing claude line: %s", out)
			}
			if !strings.Contains(out, "[debug] codex:") {
				t.Errorf("debug missing codex line: %s", out)
			}
		}},
		{"concurrent execution", func(t *testing.T) {
			// Deterministic concurrency proof: all three stubs hit a barrier (WaitGroup
			// countdown) before any can proceed. arrived.Wait() returns only when every
			// goroutine has reached the barrier, which can only happen under concurrent
			// execution.
			var arrived sync.WaitGroup
			arrived.Add(3)
			release := make(chan struct{})
			barrier := func(_ context.Context) (providers.ProviderOutput, error) {
				arrived.Done()
				<-release
				return mkOutput(1), nil
			}
			p1 := &stubProvider{id: "claude", fetch: barrier}
			p2 := &stubProvider{id: "codex", fetch: barrier}
			p3 := &stubProvider{id: "copilot", fetch: barrier}

			runDone := make(chan struct{})
			go func() {
				defer close(runDone)
				_, _ = Run(context.Background(), []string{"claude", "codex", "copilot"}, []providers.Provider{p1, p2, p3}, Options{})
			}()
			arrived.Wait() // proves all three reached Fetch concurrently
			close(release)
			<-runDone
		}},
		// Verify the orchestrator does not pre-validate provider existence — main does.
		{"unknown requested is skipped", func(t *testing.T) {
			p := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
			r, status := Run(context.Background(), []string{"claude", "nonexistent"}, []providers.Provider{p}, Options{})
			if status != StatusOK {
				t.Errorf("unknown requested should be silently skipped, status = %d", status)
			}
			if _, ok := r.Providers["nonexistent"]; ok {
				t.Errorf("nonexistent should not appear in report")
			}
		}},
		{"duplicate requested dedupes", func(t *testing.T) {
			p := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
			r, _ := Run(context.Background(),
				[]string{"claude", "claude", "claude"},
				[]providers.Provider{p}, Options{})
			if p.calls.Load() != 1 {
				t.Errorf("expected exactly one call, got %d", p.calls.Load())
			}
			if len(r.Providers) != 1 {
				t.Errorf("expected one result, got %d", len(r.Providers))
			}
		}},
		// TestRun_LimitsJSONShape_SuccessEmptyVsFailure asserts the contract scripted
		// callers rely on to distinguish "asked, got nothing" from "failed": a
		// success-with-empty-windows provider serializes as `"limits": {}` (with no
		// error key); a failed provider serializes as `"limits": null` with an error
		// key. The test runs the real orchestrator code path so any future provider
		// regression that re-introduces a `len==0 → nil` block (which would defeat
		// the {} side of the distinction) is caught here.
		{"limits json shape success empty vs failure", func(t *testing.T) {
			emptySuccess := &stubProvider{id: "claude", results: []stubResult{{out: providers.ProviderOutput{Limits: map[string]providers.Limit{}}}}}
			failure := &stubProvider{id: "codex", results: []stubResult{{err: fmt.Errorf("%w: missing", providers.ErrAuthMissing)}}}
			report, _ := Run(context.Background(), []string{"claude", "codex"}, []providers.Provider{emptySuccess, failure}, Options{})

			b, err := json.Marshal(report.Providers["claude"])
			if err != nil {
				t.Fatal(err)
			}
			if got := string(b); got != `{"limits":{}}` {
				t.Errorf("success-with-empty shape wrong, got %s", got)
			}
			b, err = json.Marshal(report.Providers["codex"])
			if err != nil {
				t.Fatal(err)
			}
			got := string(b)
			if !strings.Contains(got, `"limits":null`) {
				t.Errorf("failure should serialize limits as null, got %s", got)
			}
			if !strings.Contains(got, `"error":`) {
				t.Errorf("failure should include error key, got %s", got)
			}
		}},
		// TestRun_AccountMixedResult_ExitZero: one ok account + one error account with
		// nil Fetch error → StatusOK. Per-account errors don't flip the exit code.
		{"account mixed result exit zero", func(t *testing.T) {
			okAcc := providers.AccountResult{
				Email: "a@example.com", UUID: "uuid-a", Active: true,
				Limits: map[string]providers.Limit{"five_hour": {UsedPercent: 10, RemainingPercent: 90}},
			}
			errAcc := providers.AccountResult{Email: "b@example.com", UUID: "uuid-b", Error: "HTTP 503"}
			p := &stubProvider{id: "claude", results: []stubResult{{
				out: providers.ProviderOutput{Accounts: []providers.AccountResult{okAcc, errAcc}},
			}}}
			r, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
			if status != StatusOK {
				t.Errorf("status = %d, want OK (per-account errors do not flip exit code)", status)
			}
			result := r.Providers["claude"]
			if len(result.Accounts) != 2 {
				t.Fatalf("expected 2 accounts, got %d", len(result.Accounts))
			}
			if result.Accounts[0].Error != "" {
				t.Errorf("accounts[0] (ok) should have no error, got %q", result.Accounts[0].Error)
			}
			if result.Accounts[1].Error == "" {
				t.Errorf("accounts[1] should carry per-account error string")
			}
			if result.Error != "" {
				t.Errorf("provider-level Error should be empty, got %q", result.Error)
			}
		}},
		// TestRun_AccountAllError_NonTransient_PreservesAccounts: all-error accounts +
		// non-transient provider error → StatusAnyFailed (no retry); account rows preserved.
		{"account all error non transient preserves accounts", func(t *testing.T) {
			errAccounts := []providers.AccountResult{
				{Email: "a@example.com", UUID: "uuid-a", Error: "auth denied"},
				{Email: "b@example.com", UUID: "uuid-b", Error: "auth denied"},
			}
			p := &stubProvider{id: "claude", results: []stubResult{{
				out: providers.ProviderOutput{Accounts: errAccounts},
				err: fmt.Errorf("%w: all accounts rejected", providers.ErrAuthDenied),
			}}}
			r, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
			if status != StatusAnyFailed {
				t.Errorf("status = %d, want AnyFailed", status)
			}
			if p.calls.Load() != 1 {
				t.Errorf("non-transient error must not trigger retry, calls = %d", p.calls.Load())
			}
			result := r.Providers["claude"]
			if len(result.Accounts) != 2 {
				t.Fatalf("expected 2 preserved account rows, got %d", len(result.Accounts))
			}
			if result.Error == "" {
				t.Errorf("provider-level Error should be set")
			}
		}},
		// TestRun_AccountAllError_BareError_PreservesAccounts: all-error accounts + bare
		// (non-classified) Fetch error → StatusAnyFailed (no retry); account rows preserved.
		{"account all error bare error preserves accounts", func(t *testing.T) {
			errAccounts := []providers.AccountResult{
				{Email: "a@example.com", UUID: "uuid-a", Error: "timeout"},
				{Email: "b@example.com", UUID: "uuid-b", Error: "timeout"},
			}
			p := &stubProvider{id: "claude", results: []stubResult{{
				out: providers.ProviderOutput{Accounts: errAccounts},
				err: fmt.Errorf("network timeout"),
			}}}
			r, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
			if status != StatusAnyFailed {
				t.Errorf("status = %d, want AnyFailed", status)
			}
			if p.calls.Load() != 1 {
				t.Errorf("bare error must not trigger retry, calls = %d", p.calls.Load())
			}
			result := r.Providers["claude"]
			if len(result.Accounts) != 2 {
				t.Fatalf("expected 2 preserved account rows, got %d", len(result.Accounts))
			}
			if result.Error == "" {
				t.Errorf("provider-level Error should be set")
			}
		}},
		{"now injection", func(t *testing.T) {
			frozen := time.Date(2026, 5, 26, 20, 0, 0, 999_999_999, time.UTC)
			p := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
			r, _ := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{
				Now: func() time.Time { return frozen },
			})
			// CheckedAt should be truncated-to-second.
			want := time.Date(2026, 5, 26, 20, 0, 0, 0, time.UTC)
			if !r.CheckedAt.Equal(want) {
				t.Errorf("CheckedAt = %v, want %v", r.CheckedAt, want)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetchOnce_DebugLineSingleLineOnMultilineError(t *testing.T) {
	// Upstream-error bodies (e.g. an HTML 500 page) can include embedded
	// newlines. The orchestrator's [debug] line must stay single-line so
	// grep '\[debug\]' counts requests, not response paragraphs.
	var buf bytes.Buffer
	safe := httpx.NewConcurrencySafeWriter(&buf)
	p := &stubProvider{id: "claude", results: []stubResult{
		{err: fmt.Errorf("HTTP 500: <html>\nline two\nline three</html>")},
	}}
	Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{Debug: safe})
	out := buf.String()
	if got := strings.Count(out, "\n"); got != 1 {
		t.Errorf("expected exactly one newline in debug output, got %d:\n%s", got, out)
	}
	if !strings.Contains(out, `\n`) {
		t.Errorf("expected escaped \\n in sanitized outcome, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "[debug] claude:") {
		t.Errorf("expected line to start with [debug] claude:, got:\n%s", out)
	}
}
