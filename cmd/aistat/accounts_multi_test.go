package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

// writerBuf is a simple io.Writer backed by strings.Builder.
type writerBuf struct{ b strings.Builder }

func (w *writerBuf) Write(p []byte) (int, error) { return w.b.Write(p) }
func (w *writerBuf) String() string               { return w.b.String() }

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

// ---- accounts list JSON tests ----

func TestAccountsList_BulkJSON(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	now := time.Now()
	seedAccount(t, claudeMS, "c-uuid-1", "alice@example.com", "plan-a", now)
	seedAccount(t, claudeMS, "c-uuid-2", "bob@example.com", "plan-b", now)

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "d-uuid-1", "user@chatgpt.com", "codex-plan", now)

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	var result map[string][]accountSummary
	if err := json.Unmarshal([]byte(r.stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, r.stdout)
	}
	if len(result["claude"]) != 2 {
		t.Errorf("expected 2 Claude accounts, got %d", len(result["claude"]))
	}
	if len(result["codex"]) != 1 {
		t.Errorf("expected 1 Codex account, got %d", len(result["codex"]))
	}
	// Verify field shape on first entry.
	if result["claude"][0].Email == "" {
		t.Error("claude[0].email is empty")
	}
	if result["claude"][0].UUID == "" {
		t.Error("claude[0].uuid is empty")
	}
}

// TestAccountsList_JSONShape pins the exact top-level keys and per-account field set.
func TestAccountsList_JSONShape(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	now := time.Now()
	seedAccount(t, claudeMS, "shape-uuid", "shape@example.com", "shape-plan", now)

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "shape-duuid", "shape@chatgpt.com", "codex-shape", now)

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(r.stdout), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{"claude", "codex"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}

	// Verify per-account field set for Claude.
	var claudeAccts []map[string]json.RawMessage
	if err := json.Unmarshal(raw["claude"], &claudeAccts); err != nil {
		t.Fatalf("claude array: %v", err)
	}
	if len(claudeAccts) != 1 {
		t.Fatalf("expected 1 claude account, got %d", len(claudeAccts))
	}
	for _, field := range []string{"email", "uuid", "plan", "stale"} {
		if _, ok := claudeAccts[0][field]; !ok {
			t.Errorf("claude account missing field %q", field)
		}
	}
}

func TestAccountsList_BulkText_SectionHeaders(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	seedAccount(t, claudeMS, "c-uuid-h", "claude@example.com", "plan", time.Now())

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "d-uuid-h", "codex@chatgpt.com", "plan", time.Now())

	r := runAccountsTwoStores(claudeMS, codexMS, globals{Human: true}, "list")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "=== claude ===") {
		t.Errorf("missing claude section header; stdout: %q", r.stdout)
	}
	if !strings.Contains(r.stdout, "=== codex ===") {
		t.Errorf("missing codex section header; stdout: %q", r.stdout)
	}
}

func TestAccountsList_SingleProviderClaude_JSON(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	seedAccount(t, claudeMS, "c-uuid-sp", "sp@example.com", "plan", time.Now())

	codexMS := accounts.NewMemoryStore()

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list", "claude")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	var result map[string][]accountSummary
	if err := json.Unmarshal([]byte(r.stdout), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := result["claude"]; !ok {
		t.Error("missing claude key")
	}
	if _, ok := result["codex"]; ok {
		t.Error("codex key should be absent for single-provider list")
	}
}

func TestAccountsList_SingleProviderClaude_Text(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	seedAccount(t, claudeMS, "c-uuid-txt", "txt@example.com", "plan", time.Now())

	codexMS := accounts.NewMemoryStore()

	r := runAccountsTwoStores(claudeMS, codexMS, globals{Human: true}, "list", "claude")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	// Single-provider text mode: no header (backward compat).
	if strings.Contains(r.stdout, "===") {
		t.Errorf("single-provider text should have no section header; stdout: %q", r.stdout)
	}
	if !strings.Contains(r.stdout, "txt@example.com") {
		t.Errorf("missing account in output; stdout: %q", r.stdout)
	}
}

func TestAccountsList_UnknownProvider(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	codexMS := accounts.NewMemoryStore()

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list", "bogus")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "unknown provider") {
		t.Errorf("missing unknown provider error; stderr: %q", r.stderr)
	}
}

// ---- accounts remove multi-provider tests ----

func TestAccountsRemove_InferByIDUniqueCodex(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "uuid-dtarget", "target@codex.com", "plan", time.Now())

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "target@codex.com")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "removed target@codex.com") {
		t.Errorf("missing removal confirmation; stdout: %q", r.stdout)
	}
	listed, _ := codexMS.List(context.Background())
	if len(listed) != 0 {
		t.Fatalf("account should be removed from Codex store; got %d", len(listed))
	}
}

func TestAccountsRemove_InferByIDAmbigiuous(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	seedAccount(t, claudeMS, "uuid-cs", "shared@example.com", "plan", time.Now())

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "uuid-ds", "shared@chatgpt.com", "plan", time.Now())

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "shared")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "multiple providers") {
		t.Errorf("missing ambiguity error; stderr: %q", r.stderr)
	}
}

func TestAccountsRemove_ExplicitProviderCodex(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()

	codexMS := accounts.NewMemoryStore()
	// Use a hex-format UUID so UUID-prefix matching works.
	seedCodexAccount(t, codexMS, "abcdef01-1111-2222-3333-444444444444", "remove@chatgpt.com", "plan", time.Now())

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "abcdef01", "codex")
	if r.code != 0 {
		t.Fatalf("expected exit 0, got %d (stderr %q)", r.code, r.stderr)
	}
	listed, _ := codexMS.List(context.Background())
	if len(listed) != 0 {
		t.Fatalf("account should be removed; got %d accounts", len(listed))
	}
}

func TestAccountsRemove_TooManyArgs(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	codexMS := accounts.NewMemoryStore()

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "some-id", "claude", "extra")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "unexpected argument") {
		t.Errorf("missing unexpected-argument error; stderr: %q", r.stderr)
	}
}

func TestAccountsRemove_ExplicitProviderUnknown(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	codexMS := accounts.NewMemoryStore()

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "some-id", "bogus")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "unknown provider") {
		t.Errorf("missing unknown provider error; stderr: %q", r.stderr)
	}
}

// TestAccountsRemove_InferSameProviderMultiMatch verifies that when inference
// finds multiple matches all in the same provider, the single-provider
// disambiguation message is shown (not the cross-provider ambiguity message).
func TestAccountsRemove_InferSameProviderMultiMatch(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()
	seedAccount(t, claudeMS, "uuid-cm1", "shared@work.com", "plan", time.Now())
	seedAccount(t, claudeMS, "uuid-cm2", "shared@personal.com", "plan", time.Now())

	codexMS := accounts.NewMemoryStore() // empty → no Codex match

	r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "shared")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "multiple stored accounts match") {
		t.Errorf("missing single-provider disambiguation message; stderr: %q", r.stderr)
	}
	if strings.Contains(r.stderr, "multiple providers") {
		t.Errorf("should NOT show cross-provider ambiguity; stderr: %q", r.stderr)
	}
}

func TestAccountsRemove_ActiveProtectionCodex(t *testing.T) {
	claudeMS := accounts.NewMemoryStore()

	codexMS := accounts.NewMemoryStore()
	seedCodexAccount(t, codexMS, "uuid-codex-active", "active@chatgpt.com", "plan", time.Now())

	stores := []providerStore{
		{
			id:             "claude",
			store:          claudeMS,
			activeResolver: noopResolver,
			logoutHint:     "use 'claude /logout' first",
		},
		{
			id:             "codex",
			store:          codexMS,
			activeResolver: stubResolver("uuid-codex-active"),
			logoutHint:     "log out of the Codex app first",
		},
	}

	var bOut, bErr writerBuf
	code := runAccounts([]string{"remove", "active@chatgpt.com"}, &bOut, &bErr, globals{}, stores)
	r := runResult{bOut.String(), bErr.String(), code}

	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	want := "cannot remove currently active account \u2014 log out of the Codex app first"
	if !strings.Contains(r.stderr, want) {
		t.Errorf("missing active-protection message; stderr: %q", r.stderr)
	}
	listed, _ := codexMS.List(context.Background())
	if len(listed) != 1 {
		t.Fatalf("account should still be present; got %d", len(listed))
	}
}
