package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/orchestrate"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/render"
)

// runUsage runs the `usage` subcommand: fetch and render provider limits.
// args contains everything after the "usage" subcommand token (or all original
// args when invoked with no subcommand). An optional first positional argument
// names a single provider to query; all other arguments are flags.
func runUsage(args []string, stdout, stderr io.Writer, g globals) int {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	registerGlobalFlags(fs, &g)
	fakeFn := registerFakeMode(fs)
	var refresh bool
	fs.BoolVar(&refresh, "refresh", false, "")

	// First pass: parse any leading flags before the optional provider positional.
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return int(orchestrate.StatusUsageError)
	}

	// After --help/--version check (they may appear after "usage" token).
	if handled, code := handleGlobals(g, stdout); handled {
		return code
	}

	// Extract the optional provider positional (first non-flag arg from first pass).
	var service string
	tail := fs.Args()
	if len(tail) > 0 {
		service = tail[0]
		tail = tail[1:]
	}

	// Second pass: parse any trailing flags after the optional provider.
	// Always call Parse (even with empty tail) so fs.Args() is reset and
	// fs.NArg() correctly reflects only unconsumed positionals from this pass.
	if err := fs.Parse(tail); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return int(orchestrate.StatusUsageError)
	}

	// After --help/--version that may appear after the provider.
	if handled, code := handleGlobals(g, stdout); handled {
		return code
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected positional argument: %s\n", fs.Arg(0))
		return int(orchestrate.StatusUsageError)
	}

	if service != "" && !slices.Contains(providers.KnownProviderIDs, service) {
		fmt.Fprintf(stderr, "usage %s: provider must be one of %s\n",
			service, strings.Join(providers.KnownProviderIDs, ", "))
		return int(orchestrate.StatusUsageError)
	}

	requested := selectedProviders(service)

	serialStderr := httpx.NewConcurrencySafeWriter(stderr)
	chosen, orchDebug := buildProviders(serialStderr, g.Debug, refresh, fakeFn)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	report, status := orchestrate.Run(ctx, requested, chosen, orchestrate.Options{Debug: orchDebug})

	var renderErr error
	if g.Human {
		renderErr = render.Text(stdout, report, requested)
	} else {
		renderErr = render.JSON(stdout, report)
	}
	if renderErr != nil {
		fmt.Fprintln(stderr, renderErr.Error())
		return int(orchestrate.StatusRenderError)
	}
	return int(status)
}

// selectedProviders returns the provider ID list to query. When service is
// empty, all known providers are requested.
func selectedProviders(service string) []string {
	if service == "" {
		return append([]string(nil), providers.KnownProviderIDs...)
	}
	return []string{service}
}
