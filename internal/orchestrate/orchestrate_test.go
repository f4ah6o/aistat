package orchestrate

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
)

type stubProvider struct {
	id      string
	calls   atomic.Int32
	results []stubResult // consumed in order; last entry repeats if exhausted
	delay   time.Duration
}

type stubResult struct {
	out providers.ProviderOutput
	err error
}

func (s *stubProvider) ID() string { return s.id }

func (s *stubProvider) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	idx := int(s.calls.Add(1)) - 1
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return providers.ProviderOutput{}, ctx.Err()
		}
	}
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
	safe := &httpx.ConcurrencySafeWriter{W: &buf}
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

func TestRun_ContextCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &stubProvider{id: "claude", results: []stubResult{
		{err: fmt.Errorf("%w: HTTP 503", providers.ErrTransient)},
		{out: mkOutput(2)}, // shouldn't be reached
	}}
	// cancel right after the first call returns
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	r, status := Run(ctx, []string{"claude"}, []providers.Provider{p}, Options{})
	if status != StatusAnyFailed {
		t.Errorf("status should be AnyFailed on cancellation, got %d", status)
	}
	if errMsg := r.Providers["claude"].Error; !strings.Contains(errMsg, "context canceled") {
		t.Errorf("expected context canceled error, got %q", errMsg)
	}
	if p.calls.Load() != 1 {
		t.Errorf("provider should be called once (cancel during backoff prevents retry); got %d", p.calls.Load())
	}
}

func TestRun_ConcurrentExecution(t *testing.T) {
	delay := 100 * time.Millisecond
	p1 := &stubProvider{id: "claude", delay: delay, results: []stubResult{{out: mkOutput(1)}}}
	p2 := &stubProvider{id: "codex", delay: delay, results: []stubResult{{out: mkOutput(2)}}}
	p3 := &stubProvider{id: "copilot", delay: delay, results: []stubResult{{out: mkOutput(3)}}}
	start := time.Now()
	_, _ = Run(context.Background(), []string{"claude", "codex", "copilot"}, []providers.Provider{p1, p2, p3}, Options{})
	elapsed := time.Since(start)
	// Concurrent execution should be ~100ms, well under 300ms (sequential).
	if elapsed > 250*time.Millisecond {
		t.Errorf("expected concurrent execution under 250ms, got %v (sequential would be ~300ms)", elapsed)
	}
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
