//go:build fake

package main

import (
	"io"
	"testing"

	"github.com/drogers0/aistat/internal/providers"
)

// TestFakeProvidersCoversKnownIDs is a tripwire for the fake-build variant.
// Runs only under `go test -tags=fake ./cmd/aistat`.
func TestFakeProvidersCoversKnownIDs(t *testing.T) {
	got := map[string]bool{}
	for _, p := range fakeProviders("") {
		got[p.ID()] = true
	}
	for _, id := range providers.KnownProviderIDs {
		if !got[id] {
			t.Errorf("fakeProviders missing provider %q", id)
		}
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, io.ErrClosedPipe }

// TestRun_RenderErrorExits3 — when render fails (stdout write error), the CLI
// must return exit code 3, distinguishable from "providers failed" (exit 1).
func TestRun_RenderErrorExits3(t *testing.T) {
	code := run([]string{"--fake"}, failWriter{}, io.Discard)
	if code != 3 {
		t.Errorf("got exit code %d, want 3", code)
	}
}
