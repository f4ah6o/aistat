package main

import (
	"strings"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

// writerBuf is a simple io.Writer backed by strings.Builder.
type writerBuf struct{ b strings.Builder }

func (w *writerBuf) Write(p []byte) (int, error) { return w.b.Write(p) }
func (w *writerBuf) String() string              { return w.b.String() }

// buildTwoProviderStores returns a []providerStore with both Claude and Codex stores.
func buildTwoProviderStores(claudeMS, codexMS *accounts.MemoryStore) []providerStore {
	return []providerStore{
		{
			id:             "claude",
			store:          claudeMS,
			activeResolver: noopResolver,
			logoutHint:     "use 'claude /logout' first",
		},
		{
			id:             "codex",
			store:          codexMS,
			activeResolver: noopResolver,
			logoutHint:     "log out of the Codex app first",
		},
	}
}

// runAccountsTwoStores is a convenience wrapper for multi-provider accounts tests.
func runAccountsTwoStores(claudeMS, codexMS *accounts.MemoryStore, g globals, args ...string) runResult {
	stores := buildTwoProviderStores(claudeMS, codexMS)
	var bOut, bErr writerBuf
	code := runAccounts(args, &bOut, &bErr, g, stores)
	return runResult{bOut.String(), bErr.String(), code}
}
