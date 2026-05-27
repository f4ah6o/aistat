//go:build !fake

package main

import (
	"flag"

	"github.com/drogers0/llm-usage/internal/providers"
)

func registerFakeMode(_ *flag.FlagSet) func() []providers.Provider { return nil }
