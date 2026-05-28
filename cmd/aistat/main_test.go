package main

import (
	"bytes"
	"strings"
	"testing"
)

type runResult struct {
	stdout string
	stderr string
	code   int
}

// runCLI invokes run() in-process so tests do not pay the per-test go-build
// cost a subprocess approach would. Tests that need --fake live in
// main_fake_test.go (build-tagged) so they only run under -tags=fake.
func runCLI(args ...string) runResult {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return runResult{stdout.String(), stderr.String(), code}
}

func TestCLI_BogusPositional(t *testing.T) {
	// Leading positional that isn't a known provider → naming-the-set error.
	r := runCLI("bogus")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if r.stdout != "" {
		t.Fatalf("stdout should be empty on error: %s", r.stdout)
	}
	if !strings.Contains(r.stderr, "unexpected argument: bogus") {
		t.Fatalf("missing actionable error: %s", r.stderr)
	}
	if !strings.Contains(r.stderr, "claude") || !strings.Contains(r.stderr, "codex") || !strings.Contains(r.stderr, "copilot") {
		t.Fatalf("error should name valid providers: %s", r.stderr)
	}
}

func TestCLI_UnknownFlag(t *testing.T) {
	// --unknown is not a registered flag (and not a known --fake gated one
	// under the default build either).
	r := runCLI("--unknown")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if r.stdout != "" {
		t.Fatalf("stdout should be empty: %s", r.stdout)
	}
	if !strings.Contains(r.stderr, "flag provided but not defined") {
		t.Fatalf("missing parse error: %s", r.stderr)
	}
}

func TestCLI_DroppedJSONFlag(t *testing.T) {
	r := runCLI("--json")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if r.stdout != "" {
		t.Fatalf("stdout must be empty (no help-block leak from Usage): %s", r.stdout)
	}
	if !strings.Contains(r.stderr, "flag provided but not defined: -json") &&
		!strings.Contains(r.stderr, "flag provided but not defined: --json") {
		t.Fatalf("missing parse error: %s", r.stderr)
	}
}

func TestCLI_Help(t *testing.T) {
	r := runCLI("--help")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d", r.code)
	}
	if !strings.Contains(r.stdout, "aistat") {
		t.Fatalf("help missing program name: %s", r.stdout)
	}
	if !strings.Contains(r.stdout, "-h, --human") {
		t.Fatalf("help missing -h, --human: %s", r.stdout)
	}
	if r.stderr != "" {
		t.Fatalf("stderr should be empty on --help: %s", r.stderr)
	}
}

func TestCLI_Version(t *testing.T) {
	r := runCLI("--version")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	// Tests build without ldflags. resolvedVersion() may return:
	//   - "dev" when debug.ReadBuildInfo gives no useful version
	//   - a SemVer like "v2.1.0" when go-installed at a tag
	//   - a pseudo-version when built from a working tree in a tagged module
	// All non-empty outputs are acceptable; blank output is a regression.
	if got := strings.TrimSpace(r.stdout); got == "" {
		t.Fatalf("expected non-empty version, got empty")
	}
}

func TestHelp_ListsAllKnownProviders(t *testing.T) {
	r := runCLI("--help")
	for _, id := range []string{"claude", "codex", "copilot"} {
		if !strings.Contains(r.stdout, id) {
			t.Errorf("help missing provider %q: %s", id, r.stdout)
		}
	}
}
