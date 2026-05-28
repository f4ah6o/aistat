//go:build fake

package main

import (
	"flag"
	"strings"

	"github.com/drogers0/aistat/internal/providers"
)

func registerFakeMode(fs *flag.FlagSet) func() []providers.Provider {
	enabled := fs.Bool("fake", false, "")
	failList := fs.String("fake-fail", "", "")
	return func() []providers.Provider {
		if !*enabled {
			return nil
		}
		return fakeProviders(*failList)
	}
}

func fakeProviders(failList string) []providers.Provider {
	fail := map[string]bool{}
	for _, id := range strings.Split(failList, ",") {
		if id = strings.TrimSpace(id); id != "" {
			fail[id] = true
		}
	}
	return []providers.Provider{
		newFakeProvider("claude", fail["claude"]),
		newFakeProvider("codex", fail["codex"]),
		newFakeProvider("copilot", fail["copilot"]),
	}
}
