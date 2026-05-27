//go:build fake

package main

import (
	"flag"

	"github.com/drogers0/llm-usage/internal/providers"
)

func registerFakeMode(fs *flag.FlagSet) func() []providers.Provider {
	enabled := fs.Bool("fake", false, "")
	return func() []providers.Provider {
		if !*enabled {
			return nil
		}
		return fakeProviders()
	}
}

func fakeProviders() []providers.Provider {
	return []providers.Provider{
		newFakeProvider("claude"),
		newFakeProvider("codex"),
		newFakeProvider("copilot"),
	}
}
