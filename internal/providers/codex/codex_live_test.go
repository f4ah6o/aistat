//go:build live

package codex

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
			t.Skipf("no Codex token at ~/.codex/auth.json; skipping live test: %v", err)
		}
		t.Fatalf("live Fetch failed: %v", err)
	}
	if len(out.Limits) == 0 {
		t.Fatal("live Fetch returned no limits — possible API breakage or empty account")
	}
	t.Logf("live limits: %+v", out.Limits)
}
