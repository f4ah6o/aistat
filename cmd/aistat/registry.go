package main

import (
	"context"
	"errors"
	"io"

	"github.com/f4ah6o/aistat/v2/internal/accounts"
	"github.com/f4ah6o/aistat/v2/internal/httpx"
	"github.com/f4ah6o/aistat/v2/internal/providers"
	"github.com/f4ah6o/aistat/v2/internal/providers/claude"
	"github.com/f4ah6o/aistat/v2/internal/providers/codex"
)

// realProviders constructs the live read-only providers.
//
// This fork intentionally does not open the platform account store. Claude and
// Codex receive per-process memory stores, and Copilot is intentionally omitted.
func realProviders(serialStderr *httpx.ConcurrencySafeWriter, includeDebug bool, cacheBypass bool) []providers.Provider {
	var debugSink io.Writer
	if includeDebug {
		debugSink = serialStderr
	}

	v := resolvedVersion()
	return []providers.Provider{
		singleAccountProvider{Provider: claude.New(debugSink, claude.DefaultUserAgent(v), claude.WithStore(accounts.NewMemoryStore()), claude.WithCacheBypass(cacheBypass))},
		singleAccountProvider{Provider: codex.New(debugSink, codex.DefaultUserAgent(v), codex.WithStore(accounts.NewMemoryStore()), codex.WithCacheBypass(cacheBypass))},
	}
}

// singleAccountProvider collapses the upstream multi-account provider shape into
// a single active-account limits block.
type singleAccountProvider struct {
	providers.Provider
}

func (p singleAccountProvider) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	out, err := p.Provider.Fetch(ctx)
	if len(out.Accounts) == 0 {
		return out, err
	}

	selected := &out.Accounts[0]
	for i := range out.Accounts {
		if out.Accounts[i].Active {
			selected = &out.Accounts[i]
			break
		}
	}

	collapsed := providers.ProviderOutput{Limits: selected.Limits}
	if selected.Error != "" && err == nil {
		err = errors.New(selected.Error)
	}
	return collapsed, err
}

// buildProviders resolves the provider set (fake-mode-first) and picks the
// orchestrator debug writer. Extracted from runUsage() to keep that function
// scannable.
func buildProviders(
	serialStderr *httpx.ConcurrencySafeWriter,
	includeDebug bool,
	cacheBypass bool,
	fakeFn func() []providers.Provider,
) (chosen []providers.Provider, orchDebug io.Writer) {
	if fakeFn != nil {
		chosen = fakeFn()
	}
	if chosen == nil {
		chosen = realProviders(serialStderr, includeDebug, cacheBypass)
	}
	if includeDebug {
		orchDebug = serialStderr
	}
	return chosen, orchDebug
}
