package main

import (
	"io"
	"strings"
	"testing"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers/claude"
)

func TestUsageRefresh(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"flag accepted before and after provider", func(t *testing.T) {
			// TestUsage_RefreshFlagAcceptedBeforeAndAfterProvider pins the two-pass
			// FlagSet parsing: --refresh must be accepted both before and after the
			// optional provider positional without producing a usage error (exit 2).
			// Provider-level failures (exit 1) are expected in environments without
			// live credentials; what matters is that the flag is recognized.
			withMemoryStore(t)
			for _, args := range [][]string{
				{"usage", "--refresh", "claude"},
				{"usage", "claude", "--refresh"},
				{"usage", "--refresh"},
				{"--refresh"}, // bare invocation (no subcommand token) routes to runUsage
			} {
				r := runCLI(args...)
				if r.code == 2 {
					t.Errorf("args %v: --refresh produced exit 2 (flag parse error); stderr: %s", args, r.stderr)
				}
				if strings.Contains(r.stderr, "flag provided but not defined") {
					t.Errorf("args %v: --refresh not recognized: %s", args, r.stderr)
				}
			}
		}},
		{"passes through to claude option", func(t *testing.T) {
			// TestUsage_RefreshPassesThroughToClaudeOption verifies that --refresh wires
			// WithCacheBypass(true) all the way through buildProviders → realProviders →
			// claude.New. Uses the real provider-construction path (not fake mode) since
			// fake providers bypass claude.New.
			serialStderr := httpx.NewConcurrencySafeWriter(io.Discard)

			// cacheBypass=true: the Claude provider must have CacheBypassEnabled()=true.
			chosen, _ := buildProviders(serialStderr, false, true, nil)
			if len(chosen) == 0 {
				t.Fatal("buildProviders returned no providers")
			}
			claudeClient, ok := chosen[0].(*claude.Client)
			if !ok {
				t.Fatalf("expected first provider to be *claude.Client, got %T", chosen[0])
			}
			if !claudeClient.CacheBypassEnabled() {
				t.Fatal("WithCacheBypass(true) was not threaded through to the claude.Client")
			}

			// cacheBypass=false (default): CacheBypassEnabled() must be false.
			chosen2, _ := buildProviders(serialStderr, false, false, nil)
			if len(chosen2) == 0 {
				t.Fatal("buildProviders returned no providers")
			}
			claudeClient2, ok := chosen2[0].(*claude.Client)
			if !ok {
				t.Fatalf("expected first provider to be *claude.Client, got %T", chosen2[0])
			}
			if claudeClient2.CacheBypassEnabled() {
				t.Fatal("default buildProviders should have CacheBypassEnabled()=false")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
