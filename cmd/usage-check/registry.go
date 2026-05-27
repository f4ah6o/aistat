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

// realProviders constructs the live providers and returns them along with the
// stderr writer (already wrapped in *ConcurrencySafeWriter) that main passes
// into orchestrate.Run.Options.Debug when --debug is on. The same writer backs
// the always-on Copilot warn channel, so Doer per-request debug, orchestrator
// per-provider summary lines, and warn lines all serialize through one mutex.
func realProviders(debug, stderr io.Writer) ([]providers.Provider, io.Writer) {
	safeStderr := &httpx.ConcurrencySafeWriter{W: stderr}
	var safeDebug io.Writer
	if debug != nil {
		safeDebug = safeStderr // same instance, shared mutex
	}
	ua := userAgent()
	warn := func(s string) { fmt.Fprintln(safeStderr, "usage-check: "+s) }
	return []providers.Provider{
		claude.New(safeDebug, ua),
		codex.New(safeDebug, ua),
		copilot.New(safeDebug, ua, copilot.WithWarn(warn)),
	}, safeStderr
}
