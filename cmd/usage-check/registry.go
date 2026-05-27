package main

import (
	"fmt"
	"io"

	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
	"github.com/drogers0/llm-usage/internal/providers/claude"
	"github.com/drogers0/llm-usage/internal/providers/codex"
	"github.com/drogers0/llm-usage/internal/providers/copilot"
)

// realProviders constructs the live providers. safeStderr is the
// mutex-wrapped writer that backs three concurrent stderr consumers
// (per-Doer debug, orchestrator per-provider summary, Copilot warn), so
// all three serialize through one mutex. includeDebug toggles per-request
// debug logging — when false, Doers receive a nil Debug writer.
func realProviders(safeStderr *httpx.ConcurrencySafeWriter, includeDebug bool) []providers.Provider {
	var debugSink io.Writer
	if includeDebug {
		debugSink = safeStderr
	}
	ua := userAgent()
	warn := func(s string) { fmt.Fprintln(safeStderr, "usage-check: "+s) }
	return []providers.Provider{
		claude.New(debugSink, ua),
		codex.New(debugSink, ua),
		copilot.New(debugSink, ua, copilot.WithWarn(warn)),
	}
}

// buildProviders resolves the provider set (fake-mode-first), picks the
// orchestrator debug writer, and computes which requested IDs are not
// available. Extracted from run() to keep that function scannable and to
// provide a non-CLI seam for tests that exercise warn-wiring against the
// real provider construction.
func buildProviders(
	safeStderr *httpx.ConcurrencySafeWriter,
	includeDebug bool,
	fakeFn func() []providers.Provider,
	requested []string,
) (chosen []providers.Provider, orchDebug io.Writer, missing []string) {
	if fakeFn != nil {
		chosen = fakeFn()
	}
	if chosen == nil {
		chosen = realProviders(safeStderr, includeDebug)
	}
	if includeDebug {
		orchDebug = safeStderr
	}
	available := map[string]struct{}{}
	for _, p := range chosen {
		available[p.ID()] = struct{}{}
	}
	for _, id := range requested {
		if _, ok := available[id]; !ok {
			missing = append(missing, id)
		}
	}
	return chosen, orchDebug, missing
}
