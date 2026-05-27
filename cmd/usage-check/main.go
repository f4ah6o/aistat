package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/orchestrate"
	"github.com/drogers0/llm-usage/internal/providers"
	"github.com/drogers0/llm-usage/internal/render"
)

// version is the goreleaser-injected build tag (via `-ldflags "-X main.version=..."`);
// empty for go-install or working-tree builds, in which case resolvedVersion()
// falls back to debug.ReadBuildInfo — real ("vX") for `go install …@vX`, "(devel)" → "dev"
// for working-tree builds.
var version = ""

func resolvedVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}

func userAgent() string {
	return fmt.Sprintf("usage-check/%s (+https://github.com/drogers0/llm-usage)", resolvedVersion())
}

var knownServices = func() map[string]bool {
	m := make(map[string]bool, len(providers.KnownProviderIDs))
	for _, id := range providers.KnownProviderIDs {
		m[id] = true
	}
	return m
}()

var helpText = buildHelpText()

func buildHelpText() string {
	var sb strings.Builder
	sb.WriteString("usage-check — report Claude, Codex, and Copilot usage\n\nUsage:\n  usage-check [provider] [flags]\n\nProviders (optional, must be the first argument):\n")
	for _, id := range providers.KnownProviderIDs {
		fmt.Fprintf(&sb, "  %-9s Only query %s\n", id, providers.Title(id))
	}
	sb.WriteString("  (none)    Query all providers\n\nFlags:\n  -h, --human   Render human-readable text instead of JSON (default JSON)\n      --debug   Write per-request and per-provider lines to stderr\n      --version Print version and exit\n      --help    Print this help and exit\n")
	return sb.String()
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var service string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if !knownServices[args[0]] {
			fmt.Fprintf(stderr, "unexpected argument: %s (provider must be one of %s)\n",
				args[0], strings.Join(providers.KnownProviderIDs, ", "))
			return 2
		}
		service = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("usage-check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {} // silence default Usage; we print our own help on --help.

	human := fs.Bool("human", false, "")
	fs.BoolVar(human, "h", false, "")
	debug := fs.Bool("debug", false, "")
	help := fs.Bool("help", false, "")
	versionFlag := fs.Bool("version", false, "")
	fakeFn := registerFakeMode(fs) // nil unless built with -tags=fake

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return 2
	}

	if *help {
		fmt.Fprint(stdout, helpText)
		return 0
	}
	if *versionFlag {
		fmt.Fprintln(stdout, resolvedVersion())
		return 0
	}

	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected positional argument: %s (provider must come first, before any flags)\n", fs.Arg(0))
		return 2
	}

	requested := selectedProviders(service)

	var debugWriter io.Writer
	if *debug {
		debugWriter = stderr
	}

	var chosen []providers.Provider
	var orchDebug io.Writer
	if fakeFn != nil {
		chosen = fakeFn()
	}
	if chosen == nil {
		var safeStderr io.Writer
		chosen, safeStderr = realProviders(debugWriter, stderr)
		if *debug {
			orchDebug = safeStderr
		}
	} else if *debug {
		// Fake mode: no copilot warn writer to share with, but multiple fake
		// providers still write debug lines concurrently. Wrap stderr so
		// per-provider lines don't interleave mid-write.
		orchDebug = &httpx.ConcurrencySafeWriter{W: debugWriter}
	}

	available := map[string]providers.Provider{}
	for _, p := range chosen {
		available[p.ID()] = p
	}
	var missing []string
	for _, id := range requested {
		if _, ok := available[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stderr, "provider not available: %s\n", strings.Join(missing, ", "))
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	report, status := orchestrate.Run(ctx, requested, chosen, orchestrate.Options{Debug: orchDebug})

	var renderErr error
	if *human {
		renderErr = render.Text(stdout, report, requested)
	} else {
		renderErr = render.JSON(stdout, report)
	}
	if renderErr != nil {
		fmt.Fprintln(stderr, renderErr.Error())
		return 2
	}
	return int(status)
}

func selectedProviders(service string) []string {
	if service == "" {
		return append([]string(nil), providers.KnownProviderIDs...)
	}
	return []string{service}
}
