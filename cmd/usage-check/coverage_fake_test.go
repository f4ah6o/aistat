//go:build fake

package main

import (
	"testing"

	"github.com/drogers0/llm-usage/internal/providers"
)

// TestFakeProvidersCoversKnownIDs is a tripwire for the fake-build variant.
// Runs only under `go test -tags=fake ./cmd/usage-check`.
func TestFakeProvidersCoversKnownIDs(t *testing.T) {
	got := map[string]bool{}
	for _, p := range fakeProviders() {
		got[p.ID()] = true
	}
	for _, id := range providers.KnownProviderIDs {
		if !got[id] {
			t.Errorf("fakeProviders missing provider %q", id)
		}
	}
}
