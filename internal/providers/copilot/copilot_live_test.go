//go:build live

package copilot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drogers0/aistat/internal/providers"
)

func TestLive_RealAuthAndEndpoint(t *testing.T) {
	c := New(nil, "aistat-live-test/0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := c.Fetch(ctx)
	if err != nil {
		if errors.Is(err, providers.ErrAuthMissing) {
			t.Skipf("no GitHub token or missing user scope; skipping live test: %v", err)
		}
		t.Fatalf("live Fetch failed: %v", err)
	}
	m, ok := out.Limits["month"]
	if !ok {
		t.Fatal("live Fetch returned no month limit")
	}
	if m.UsedPercent < 0 || m.UsedPercent > 100 {
		t.Errorf("used_percent out of [0,100]: %v — possible silent SKU-filter drift", m.UsedPercent)
	}
	if !m.ResetsAt.After(time.Now().Add(-time.Minute)) {
		t.Errorf("resets_at in the past: %v", m.ResetsAt)
	}
	t.Logf("live limits: %+v", out.Limits)
}
