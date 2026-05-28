package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"syscall"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/orchestrate"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/render"
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
	return fmt.Sprintf("aistat/%s (+%s)", resolvedVersion(), providers.ProjectURL)
}

var helpText = buildHelpText()

func buildHelpText() string {
	var sb strings.Builder
	sb.WriteString("aistat — report Claude, Codex, and Copilot usage\n\nUsage:\n  aistat [provider] [flags]\n\nProviders (optional, must be the first argument):\n")
	for _, id := range providers.KnownProviderIDs {
		fmt.Fprintf(&sb, "  %-9s Only query %s\n", id, providers.Title(id))
	}
	sb.WriteString("  (none)    Query all providers\n\nFlags:\n  -h, --human   Render human-readable text instead of JSON (default JSON)\n      --debug   Write per-request and per-provider lines to stderr\n      --version Print version and exit\n      --help    Print this help and exit\n\nExit codes:\n  0  All requested providers succeeded.\n  1  One or more requested providers failed at runtime.\n  2  Usage error (unknown provider, malformed flags).\n  3  Stdout write error (broken pipe, disk full).\n")
	return sb.String()
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var service string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if !slices.Contains(providers.KnownProviderIDs, args[0]) {
			fmt.Fprintf(stderr, "unexpected argument: %s (provider must be one of %s)\n",
				args[0], strings.Join(providers.KnownProviderIDs, ", "))
			return int(orchestrate.StatusUsageError)
		}
		service = args[0]
		args = args[1:]
	}

	fs := flag.NewFlagSet("aistat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {} // silence default Usage; we print our own help on --help.

	human := fs.Bool("human", false, "")
	fs.BoolVar(human, "h", false, "")
	debugFlag := fs.Bool("debug", false, "")
	help := fs.Bool("help", false, "")
	versionFlag := fs.Bool("version", false, "")
	fakeFn := registerFakeMode(fs) // nil unless built with -tags=fake

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return int(orchestrate.StatusUsageError)
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
		return int(orchestrate.StatusUsageError)
	}

	requested := selectedProviders(service)

	serialStderr := httpx.NewConcurrencySafeWriter(stderr)
	chosen, orchDebug := buildProviders(serialStderr, *debugFlag, fakeFn)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
		return int(orchestrate.StatusRenderError)
	}
	return int(status)
}

func selectedProviders(service string) []string {
	if service == "" {
		return append([]string(nil), providers.KnownProviderIDs...)
	}
	return []string{service}
}
