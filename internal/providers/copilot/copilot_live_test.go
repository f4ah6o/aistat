//go:build live

package copilot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func TestLive_RealAuthAndEndpoint(t *testing.T) {
	c := New(nil, "aistat-live-test/0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := c.Fetch(ctx)
	if err != nil {
		if errors.Is(err, providers.ErrAuthMissing) || errors.Is(err, providers.ErrAuthDenied) {
			t.Skipf("no GitHub token or no Copilot access; skipping live test: %v", err)
		}
		t.Fatalf("live Fetch failed: %v", err)
	}
	// An unlimited grant or no metered pool yields an empty (non-nil) limits
	// map — a valid N/A result, not a failure.
	if len(out.Limits) == 0 {
		t.Logf("live account has no metered Copilot credit window (unlimited or no allotment)")
		return
	}
	m, ok := out.Limits["month"]
	if !ok {
		t.Fatal("live Fetch returned non-empty limits without a month window")
	}
	if m.UsedPercent < 0 || m.UsedPercent > 100 {
		t.Errorf("used_percent out of [0,100]: %v", m.UsedPercent)
	}
	if !m.ResetsAt.After(time.Now().Add(-time.Minute)) {
		t.Errorf("resets_at in the past: %v", m.ResetsAt)
	}
	t.Logf("live limits: %+v", out.Limits)
}
