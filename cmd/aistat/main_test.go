package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
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

// withMemoryStore swaps openAccountStore for a MemoryStore during the test,
// restoring the original on cleanup.
func withMemoryStore(t *testing.T) *accounts.MemoryStore {
	t.Helper()
	ms := accounts.NewMemoryStore()
	old := openAccountStore
	openAccountStore = func(_ io.Writer) (accounts.Store, error) {
		return ms, nil
	}
	t.Cleanup(func() { openAccountStore = old })
	return ms
}

// --- Help / Version ---

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
	if !strings.Contains(r.stdout, "accounts") {
		t.Fatalf("help missing accounts subcommand: %s", r.stdout)
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

// --- Global flag placement ---

func TestCLI_DebugBeforeSubcommand(t *testing.T) {
	// --debug before "usage" sets g.Debug so providers emit debug lines.
	// In a no-credentials test env the run will fail at the provider level
	// (exit 1), but the flag must be accepted without a usage error (exit 2).
	r := runCLI("--debug", "usage")
	if r.code == 2 {
		t.Fatalf("--debug before usage should not produce exit 2 (flag parse error); got stderr: %s", r.stderr)
	}
}

func TestCLI_GlobalFlagEqualFormRejected(t *testing.T) {
	r := runCLI("--debug=true", "usage")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "--flag=value form not supported for global flags") {
		t.Fatalf("missing error message: %s", r.stderr)
	}
	if !strings.Contains(r.stderr, "--debug") {
		t.Fatalf("error should name the offending flag: %s", r.stderr)
	}
}

func TestCLI_HumanFlagEquivalence(t *testing.T) {
	// --human before subcommand and after subcommand are both accepted.
	// We can't compare output (depends on real credentials) but we can
	// ensure neither produces exit 2.
	r1 := runCLI("--human", "usage")
	r2 := runCLI("usage", "--human")
	for _, r := range []runResult{r1, r2} {
		if r.code == 2 {
			t.Fatalf("--human placement should not produce exit 2; stderr: %s", r.stderr)
		}
	}
}

// --- Unknown subcommand ---

func TestCLI_UnknownSubcommand(t *testing.T) {
	r := runCLI("unknown-subcmd")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if r.stdout != "" {
		t.Fatalf("stdout should be empty: %s", r.stdout)
	}
	if !strings.Contains(r.stderr, `unknown subcommand "unknown-subcmd"`) {
		t.Fatalf("missing error: %s", r.stderr)
	}
}

// --- Unknown flag ---

func TestCLI_UnknownFlag(t *testing.T) {
	// Unknown flag left in rest by scanGlobals → passed to runUsage's FlagSet.
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
		t.Fatalf("stdout must be empty: %s", r.stdout)
	}
	if !strings.Contains(r.stderr, "flag provided but not defined") {
		t.Fatalf("missing parse error: %s", r.stderr)
	}
}

// --- Usage subcommand ---

func TestCLI_NoArgEquivalentToUsage(t *testing.T) {
	withMemoryStore(t)
	// No-arg and explicit "usage" both dispatch to runUsage — neither should
	// produce a usage error (exit 2). Provider-level failures (exit 1) are
	// expected in environments without live credentials; that is not a routing
	// failure. Both must produce valid JSON output.
	for _, args := range [][]string{{}, {"usage"}} {
		r := runCLI(args...)
		if r.code == 2 {
			t.Errorf("args %v: should not produce exit 2 (usage/routing error); stderr: %s", args, r.stderr)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
			t.Errorf("args %v: invalid JSON: %v\noutput: %s", args, err, r.stdout)
			continue
		}
		if _, ok := m["checked_at"]; !ok {
			t.Errorf("args %v: missing checked_at in JSON output", args)
		}
	}
}

func TestCLI_UsageUnknownProvider(t *testing.T) {
	r := runCLI("usage", "unknown-provider")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "usage unknown-provider: provider must be one of claude, codex, copilot") {
		t.Fatalf("missing error: %s", r.stderr)
	}
}

// --- switch subcommand ---

func TestCLI_SwitchHelp(t *testing.T) {
	r := runCLI("switch", "--help")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "aistat") {
		t.Fatalf("help output missing 'aistat': %s", r.stdout)
	}
}

func TestCLI_SwitchNoStoredAccounts(t *testing.T) {
	// Bulk switch with both stores empty → no eligible providers → exit 0.
	withMemoryStore(t)
	withCodexMemoryStore(t)

	r := runCLI("switch")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "no providers have multiple stored accounts") {
		t.Fatalf("missing expected message: %s", r.stderr)
	}
}

// --- accounts subcommand routing ---

func TestCLI_AccountsNoSubcmd(t *testing.T) {
	withMemoryStore(t)
	r := runCLI("accounts")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	want := "unknown subcommand \"\" \u2014 want \"list\" or \"remove\""
	if !strings.Contains(r.stderr, want) {
		t.Fatalf("missing error %q: %s", want, r.stderr)
	}
}

func TestCLI_AccountsList(t *testing.T) {
	withMemoryStore(t)
	withCodexMemoryStore(t)
	r := runCLI("accounts", "list")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	// JSON output for empty stores: both provider keys present.
	var result map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", r.stdout, err)
	}
	if _, ok := result["claude"]; !ok {
		t.Fatalf("missing claude key in JSON: %s", r.stdout)
	}
}

func TestCLI_AccountsListHelp(t *testing.T) {
	withMemoryStore(t)
	r := runCLI("accounts", "list", "--help")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d", r.code)
	}
	if !strings.Contains(r.stdout, "aistat") {
		t.Fatalf("help output missing 'aistat': %s", r.stdout)
	}
}

func TestCLI_AccountsListVersion(t *testing.T) {
	withMemoryStore(t)
	r := runCLI("accounts", "list", "--version")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d", r.code)
	}
	if got := strings.TrimSpace(r.stdout); got == "" {
		t.Fatalf("expected non-empty version, got empty")
	}
}

// --- New T4 integration tests ---

func TestCLI_AccountsListClaudeProviderArg(t *testing.T) {
	withMemoryStore(t)
	withCodexMemoryStore(t)
	r := runCLI("accounts", "list", "claude")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &result); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", r.stdout, err)
	}
	if _, ok := result["claude"]; !ok {
		t.Fatalf("missing claude key in JSON: %s", r.stdout)
	}
	if _, ok := result["codex"]; ok {
		t.Fatalf("codex key should be absent for single-provider list: %s", r.stdout)
	}
}

func TestCLI_AccountsRemoveUnknownProvider(t *testing.T) {
	withMemoryStore(t)
	withCodexMemoryStore(t)
	r := runCLI("accounts", "remove", "some-id", "bogus")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "unknown provider") {
		t.Fatalf("missing unknown provider error: %s", r.stderr)
	}
}

// TestCLI_SwitchClaudeOneAccountHint verifies that the Claude-specific login hint
// is shown when `aistat switch claude` is invoked with only one stored account.
func TestCLI_SwitchClaudeOneAccountHint(t *testing.T) {
	ms := withMemoryStore(t)
	withCodexMemoryStore(t)
	seedAccount(t, ms, "uuid-only", "only@example.com", "plan", time.Now())
	withSwitchActiveUUID(t, "uuid-only")

	r := runCLI("switch", "claude")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "claude /login") {
		t.Fatalf("missing Claude login hint: %s", r.stderr)
	}
}
