package main

import (
	"io"
	"testing"

	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
)

// TestRealProvidersCoversKnownIDs is a tripwire: if a new provider is added to
// providers.KnownProviderIDs without a corresponding realProviders entry, CLI
// validation would silently flag it as "provider not available". This test
// fails at build time instead.
func TestRealProvidersCoversKnownIDs(t *testing.T) {
	list := realProviders(&httpx.ConcurrencySafeWriter{W: io.Discard}, false)
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
