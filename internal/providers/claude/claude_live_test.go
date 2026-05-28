//go:build live

package claude

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

// TestLive_RealKeychainAndEndpoint hits the user's real macOS Keychain and
// api.anthropic.com. Opt-in: `go test -tags live ./internal/providers/claude`.
// Confirms the live response still parses to >0 limits.
func TestLive_RealKeychainAndEndpoint(t *testing.T) {
	// Isolate cache writes from the developer's real cache directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
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
	if len(out.Limits) == 0 && len(out.Accounts) == 0 {
		t.Fatal("live Fetch returned no limits and no accounts — possible API breakage or empty account")
	}
	t.Logf("live limits: %+v", out.Limits)
	t.Logf("live accounts: %d", len(out.Accounts))
}

// TestLive_MultiAccount is a smoke test for multi-account reporting. It requires
// AISTAT_LIVE=1 and at least two stored accounts; it is skipped otherwise.
// This test does not assert specific usage values — it only verifies that Fetch
// returns ≥2 account rows without a provider-level error.
//
// Run with: AISTAT_LIVE=1 go test -tags live ./internal/providers/claude/...
func TestLive_MultiAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live multi-account smoke test in short mode")
	}

	// Isolate cache writes from the developer's real cache directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	c := New(nil, "aistat-live-test/0")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := c.Fetch(ctx)
	if err != nil {
		if errors.Is(err, providers.ErrAuthMissing) {
			t.Skipf("no Claude token in Keychain; skipping live test: %v", err)
		}
		t.Fatalf("live Fetch failed: %v", err)
	}

	if len(out.Accounts) < 2 {
		t.Skipf("need ≥2 stored accounts for multi-account smoke test, got %d", len(out.Accounts))
	}

	t.Logf("multi-account Fetch returned %d accounts", len(out.Accounts))
	for i, ar := range out.Accounts {
		t.Logf("  [%d] email=%s uuid=%s active=%v err=%q limits=%d",
			i, ar.Email, ar.UUID, ar.Active, ar.Error, len(ar.Limits))
	}

	// Exactly one account must be active.
	var activeCount int
	for _, ar := range out.Accounts {
		if ar.Active {
			activeCount++
		}
	}
	if activeCount != 1 {
		t.Errorf("expected exactly 1 active account, got %d", activeCount)
	}

	// Active account must appear first.
	if !out.Accounts[0].Active {
		t.Errorf("first account should be active, got %q", out.Accounts[0].Email)
	}
}
