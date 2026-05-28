package main

import (
	"io"
	"testing"

	"github.com/drogers0/aistat/internal/httpx"
	"github.com/drogers0/aistat/internal/providers"
)

// TestRealProvidersCoversKnownIDs is a tripwire: if a new provider is added to
// providers.KnownProviderIDs without a corresponding realProviders entry, CLI
// validation would silently flag it as "provider not available". This test
// fails at build time instead.
func TestRealProvidersCoversKnownIDs(t *testing.T) {
	list := realProviders(httpx.NewConcurrencySafeWriter(io.Discard), false)
	got := map[string]bool{}
	for _, p := range list {
		got[p.ID()] = true
	}
	for _, id := range providers.KnownProviderIDs {
		if !got[id] {
			t.Errorf("realProviders missing provider %q", id)
		}
	}
}
