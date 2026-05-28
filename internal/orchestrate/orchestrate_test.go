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

	"github.com/drogers0/aistat/internal/httpx"
	"github.com/drogers0/aistat/internal/providers"
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

func TestRun_AllSucceed(t *testing.T) {
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
}

func TestRun_OneFails(t *testing.T) {
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
}

func TestRun_TransientIsRetriedOnce(t *testing.T) {
	p := &stubProvider{id: "claude", results: []stubResult{
		{err: fmt.Errorf("%w: HTTP 503", providers.ErrTransient)},
		{out: mkOutput(2)},
	}}
	r, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
	if status != StatusOK {
		t.Errorf("status = %d, want OK after retry success", status)
	}
	if p.calls.Load() != 2 {
		t.Errorf("provider should be called twice, got %d", p.calls.Load())
	}
	if r.Providers["claude"].Error != "" {
		t.Errorf("claude should not have error after retry: %s", r.Providers["claude"].Error)
	}
}

func TestRun_TransientFailsTwice(t *testing.T) {
	p := &stubProvider{id: "claude", results: []stubResult{
		{err: fmt.Errorf("%w: HTTP 503", providers.ErrTransient)},
	}}
	_, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
	if status != StatusAnyFailed {
		t.Errorf("status = %d, want AnyFailed after two transient failures", status)
	}
	if p.calls.Load() != 2 {
		t.Errorf("provider should be called twice (initial + retry), got %d", p.calls.Load())
	}
}

func TestRun_AuthErrorIsNotRetried(t *testing.T) {
	p := &stubProvider{id: "claude", results: []stubResult{
		{err: fmt.Errorf("%w: missing", providers.ErrAuthMissing)},
	}}
	_, status := Run(context.Background(), []string{"claude"}, []providers.Provider{p}, Options{})
	if status != StatusAnyFailed {
		t.Errorf("status = %d, want AnyFailed", status)
	}
	if p.calls.Load() != 1 {
		t.Errorf("auth error should not be retried; calls = %d", p.calls.Load())
	}
}

func TestRun_DebugWritesPerAttempt(t *testing.T) {
	// Providers run concurrently; wrap the bytes.Buffer so per-provider
	// debug lines don't interleave mid-write. (Real CLI does the same via
	// realProviders wiring.)
	var buf bytes.Buffer
	safe := httpx.NewConcurrencySafeWriter(&buf)
	p1 := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
	p2 := &stubProvider{id: "codex", results: []stubResult{
		{err: fmt.Errorf("%w: HTTP 503", providers.ErrTransient)},
		{out: mkOutput(5)},
	}}
	Run(context.Background(), []string{"claude", "codex"}, []providers.Provider{p1, p2}, Options{Debug: safe})
	out := buf.String()
	if !strings.Contains(out, "[debug] claude:") {
		t.Errorf("debug missing claude line: %s", out)
	}
	if !strings.Contains(out, "[debug] codex:") {
		t.Errorf("debug missing codex line: %s", out)
	}
	if !strings.Contains(out, "[retry]") {
		t.Errorf("retry line should be marked: %s", out)
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

func TestRun_ContextCancellationDuringBackoff(t *testing.T) {
	// Deterministic: stub signals on a channel as soon as the first call begins;
	// the test reads the signal, cancels the parent ctx, then a 1s backoff makes
	// the orchestrator's <-time.After(backoff) lose to <-ctx.Done() with no race.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstCallDone := make(chan struct{})
	p := &stubProvider{id: "claude", fetch: func(_ context.Context) (providers.ProviderOutput, error) {
		close(firstCallDone)
		return providers.ProviderOutput{}, fmt.Errorf("%w: HTTP 503", providers.ErrTransient)
	}}

	runDone := make(chan struct{})
	var report providers.Report
	var status ExitStatus
	go func() {
		defer close(runDone)
		report, status = Run(ctx, []string{"claude"}, []providers.Provider{p}, Options{RetryBackoff: 1 * time.Second})
	}()

	<-firstCallDone
	cancel()
	<-runDone

	if status != StatusAnyFailed {
		t.Errorf("status should be AnyFailed on cancellation, got %d", status)
	}
	if errMsg := report.Providers["claude"].Error; !strings.Contains(errMsg, "context canceled") {
		t.Errorf("expected context canceled error, got %q", errMsg)
	}
	if p.calls.Load() != 1 {
		t.Errorf("provider should be called once (cancel during backoff prevents retry); got %d", p.calls.Load())
	}
}

func TestRun_ConcurrentExecution(t *testing.T) {
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
}

// Verify the orchestrator does not pre-validate provider existence — main does.
func TestRun_UnknownRequestedIsSkipped(t *testing.T) {
	p := &stubProvider{id: "claude", results: []stubResult{{out: mkOutput(2)}}}
	r, status := Run(context.Background(), []string{"claude", "nonexistent"}, []providers.Provider{p}, Options{})
	if status != StatusOK {
		t.Errorf("unknown requested should be silently skipped, status = %d", status)
	}
	if _, ok := r.Providers["nonexistent"]; ok {
		t.Errorf("nonexistent should not appear in report")
	}
}

func TestRun_DuplicateRequestedDedupes(t *testing.T) {
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
}

// TestRun_LimitsJSONShape_SuccessEmptyVsFailure asserts the contract scripted
// callers rely on to distinguish "asked, got nothing" from "failed": a
// success-with-empty-windows provider serializes as `"limits": {}` (with no
// error key); a failed provider serializes as `"limits": null` with an error
// key. The test runs the real orchestrator code path so any future provider
// regression that re-introduces a `len==0 → nil` block (which would defeat
// the {} side of the distinction) is caught here.
func TestRun_LimitsJSONShape_SuccessEmptyVsFailure(t *testing.T) {
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
}

func TestRun_NowInjection(t *testing.T) {
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
}
