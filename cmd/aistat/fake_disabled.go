//go:build !fake

package main

import (
	"flag"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func registerFakeMode(_ *flag.FlagSet) func() []providers.Provider { return nil }
