package main

import (
	"fmt"
	"io"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/providers/claude"
	"github.com/drogers0/aistat/v2/internal/providers/codex"
	"github.com/drogers0/aistat/v2/internal/providers/copilot"
)

// realProviders constructs the live providers. serialStderr is the
// mutex-wrapped writer that backs three concurrent stderr consumers
// (per-Doer debug, orchestrator per-provider summary, Copilot warn), so
// all three serialize through one mutex. The mutex is always in the path
// because copilot.warn is unconditional. includeDebug toggles per-request
// debug logging — when false, Doers receive a nil Debug writer.
func realProviders(serialStderr *httpx.ConcurrencySafeWriter, includeDebug bool) []providers.Provider {
	var debugSink io.Writer
	if includeDebug {
		debugSink = serialStderr
	}
	ua := userAgent()
	return []providers.Provider{
		claude.New(debugSink, ua),
		codex.New(debugSink, ua),
		copilot.New(debugSink, ua, copilot.WithWarn(wrapWarn(serialStderr))),
	}
}

// wrapWarn returns the warn callback wired into copilot.New. Every line the
// provider emits is prefixed with "aistat: " so the source is obvious
// when --debug is OFF and only the SKU-drift line surfaces. Extracted from
// realProviders so the prefix contract is unit-testable without HTTP.
func wrapWarn(out io.Writer) func(string) {
	return func(s string) { fmt.Fprintln(out, "aistat: "+s) }
}

// buildProviders resolves the provider set (fake-mode-first) and picks the
// orchestrator debug writer. Extracted from run() to keep that function
// scannable and to provide a non-CLI seam for tests that exercise
// warn-wiring against the real provider construction. The mutex inside
// serialStderr is always in the path because copilot.warn is unconditional.
func buildProviders(
	serialStderr *httpx.ConcurrencySafeWriter,
	includeDebug bool,
	fakeFn func() []providers.Provider,
) (chosen []providers.Provider, orchDebug io.Writer) {
	if fakeFn != nil {
		chosen = fakeFn()
	}
	if chosen == nil {
		chosen = realProviders(serialStderr, includeDebug)
	}
	if includeDebug {
		orchDebug = serialStderr
	}
	return chosen, orchDebug
}
