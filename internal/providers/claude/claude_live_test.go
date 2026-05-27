//go:build live

package claude

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drogers0/aistat/internal/providers"
)

// TestLive_RealKeychainAndEndpoint hits the user's real macOS Keychain and
// api.anthropic.com. Opt-in: `go test -tags live ./internal/providers/claude`.
// Confirms the live response still parses to >0 limits.
func TestLive_RealKeychainAndEndpoint(t *testing.T) {
	c := New(nil, "aistat-live-test/0")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := c.Fetch(ctx)
	if err != nil {
		if errors.Is(err, providers.ErrAuthMissing) {
			t.Skipf("no Claude token in Keychain; skipping live test: %v", err)
		}
		t.Fatalf("live Fetch failed: %v", err)
	}
	if len(out.Limits) == 0 {
		t.Fatal("live Fetch returned no limits — possible API breakage or empty account")
	}
	t.Logf("live limits: %+v", out.Limits)
}
