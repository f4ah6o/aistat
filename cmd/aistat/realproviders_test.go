package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/drogers0/aistat/internal/httpx"
)

func TestWrapWarn_PrefixesLines(t *testing.T) {
	var buf bytes.Buffer
	wrapWarn(&buf)("copilot: something drifted")
	got := buf.String()
	if !strings.HasPrefix(got, "aistat: copilot: something drifted") {
		t.Fatalf("missing prefix; got %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("expected trailing newline; got %q", got)
	}
}

func TestRealProviders_ReturnsThreeInOrder(t *testing.T) {
	var buf bytes.Buffer
	safe := httpx.NewConcurrencySafeWriter(&buf)
	got := realProviders(safe, false)
	if len(got) != 3 {
		t.Fatalf("want 3 providers, got %d", len(got))
	}
	wantIDs := []string{"claude", "codex", "copilot"}
	for i, want := range wantIDs {
		if got[i].ID() != want {
			t.Errorf("position %d: id = %q, want %q", i, got[i].ID(), want)
		}
	}
}
