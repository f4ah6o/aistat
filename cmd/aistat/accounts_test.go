package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/testutil"
)

// noopResolver is a stub resolveActiveUUID that always returns "" (no active account).
func noopResolver(_ context.Context, _ []accounts.Account) (string, error) {
	return "", nil
}

// stubResolver returns a resolver that always reports the given UUID as active.
func stubResolver(activeUUID string) func(context.Context, []accounts.Account) (string, error) {
	return func(_ context.Context, _ []accounts.Account) (string, error) {
		return activeUUID, nil
	}
}

// runAccountsTest calls runAccounts with a single-provider (Claude) providerStore
// built from the given store and resolver. g controls rendering (Human: true for text).
func runAccountsTest(store *accounts.MemoryStore, resolver func(context.Context, []accounts.Account) (string, error), g globals, args ...string) runResult {
	var stdout, stderr bytes.Buffer
	ps := []providerStore{{
		id:             "claude",
		store:          store,
		activeResolver: resolver,
		logoutHint:     "use 'claude /logout' first",
	}}
	code := runAccounts(args, &stdout, &stderr, g, ps)
	return runResult{stdout.String(), stderr.String(), code}
}

// seedAccount inserts an account into ms with the given fields.
func seedAccount(t *testing.T, ms *accounts.MemoryStore, uuid, email, plan string, lastSeen time.Time) {
	t.Helper()
	rawBlob, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "tok-" + uuid, "refreshToken": "rt-" + uuid},
	})
	a, err := accounts.NewAccount(rawBlob, uuid, email, email, plan, lastSeen)
	if err != nil {
		t.Fatalf("seedAccount: %v", err)
	}
	if err := ms.Upsert(context.Background(), a); err != nil {
		t.Fatalf("seedAccount Upsert: %v", err)
	}
}

// --- Unknown subcommand errors ---

func TestAccounts(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"empty subcommand", func(t *testing.T) {
			ms := testutil.MemStore(t)
			r := runAccountsTest(ms, noopResolver, globals{}) // no args → empty sub
			wantExit(t, r, 2)
			wantErrOut(t, r, "unknown subcommand \"\" — want \"list\" or \"remove\"")
		}},
		{"unknown subcommand", func(t *testing.T) {
			ms := testutil.MemStore(t)
			r := runAccountsTest(ms, noopResolver, globals{}, "foo")
			wantExit(t, r, 2)
			wantErrOut(t, r, "unknown subcommand \"foo\" — want \"list\" or \"remove\"")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// --- Store-open failure (tested via runAccountsSubcommand) ---

func TestAccountsSubcmd_StoreOpenFailure(t *testing.T) {
	old := openAccountStore
	injectErr := errors.New("permission denied")
	openAccountStore = func(_ io.Writer) (accounts.Store, error) {
		return nil, injectErr
	}
	t.Cleanup(func() { openAccountStore = old })

	var stdout, stderr bytes.Buffer
	code := runAccountsSubcommand([]string{"list"}, &stdout, &stderr, globals{})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	want := "aistat: claude: could not open account store: permission denied"
	if !strings.Contains(stderr.String(), want) {
		t.Fatalf("missing error %q; stderr: %s", want, stderr.String())
	}
}

// --- accounts list (single-provider tests) ---

func TestAccountsList(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		// ---- single-provider (text mode) ----
		{"empty store", func(t *testing.T) {
			ms := testutil.MemStore(t)
			r := runAccountsTest(ms, noopResolver, globals{Human: true}, "list")
			wantExit(t, r, 0)
			if r.stdout != "" {
				t.Fatalf("expected empty stdout, got %q", r.stdout)
			}
		}},
		{"not stale at 30 days", func(t *testing.T) {
			ms := testutil.MemStore(t)
			// 30 days minus 1 minute: unambiguously NOT stale regardless of test execution time.
			lastSeen := time.Now().Add(-30*24*time.Hour + time.Minute)
			seedAccount(t, ms, "aaaa-1111", "user@example.com", "default_claude_max_5x", lastSeen)

			r := runAccountsTest(ms, noopResolver, globals{Human: true}, "list")
			wantExit(t, r, 0)
			if strings.Contains(r.stdout, "(stale)") {
				t.Fatalf("account at <30 days should NOT be stale; stdout: %s", r.stdout)
			}
			wantOut(t, r, "user@example.com")
		}},
		{"stale after 30 days plus 1 minute", func(t *testing.T) {
			ms := testutil.MemStore(t)
			// 30 days + 1 minute: unambiguously stale regardless of test execution time.
			lastSeen := time.Now().Add(-30*24*time.Hour - time.Minute)
			seedAccount(t, ms, "bbbb-2222", "old@example.com", "default_claude_pro", lastSeen)

			r := runAccountsTest(ms, noopResolver, globals{Human: true}, "list")
			wantExit(t, r, 0)
			wantOut(t, r, "(stale)")
		}},
		{"sorted by email", func(t *testing.T) {
			ms := testutil.MemStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-z", "z@example.com", "plan", now)
			seedAccount(t, ms, "uuid-a", "a@example.com", "plan", now)
			seedAccount(t, ms, "uuid-m", "m@example.com", "plan", now)

			r := runAccountsTest(ms, noopResolver, globals{Human: true}, "list")
			wantExit(t, r, 0)
			idxA := strings.Index(r.stdout, "a@example.com")
			idxM := strings.Index(r.stdout, "m@example.com")
			idxZ := strings.Index(r.stdout, "z@example.com")
			if !(idxA < idxM && idxM < idxZ) {
				t.Fatalf("accounts not sorted by email; stdout:\n%s", r.stdout)
			}
		}},
		// ---- multi-provider JSON tests ----
		{"bulk json two providers", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			now := time.Now()
			seedAccount(t, claudeMS, "c-uuid-1", "alice@example.com", "plan-a", now)
			seedAccount(t, claudeMS, "c-uuid-2", "bob@example.com", "plan-b", now)

			codexMS := testutil.MemStore(t)
			seedCodexAccount(t, codexMS, "d-uuid-1", "user@chatgpt.com", "codex-plan", now)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list")
			wantExit(t, r, 0)
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
		}},
		{"json shape pins top-level keys and per-account fields", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			now := time.Now()
			seedAccount(t, claudeMS, "shape-uuid", "shape@example.com", "shape-plan", now)

			codexMS := testutil.MemStore(t)
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
		}},
		{"bulk text section headers", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			seedAccount(t, claudeMS, "c-uuid-h", "claude@example.com", "plan", time.Now())

			codexMS := testutil.MemStore(t)
			seedCodexAccount(t, codexMS, "d-uuid-h", "codex@chatgpt.com", "plan", time.Now())

			r := runAccountsTwoStores(claudeMS, codexMS, globals{Human: true}, "list")
			wantExit(t, r, 0)
			wantOut(t, r, "=== claude ===")
			wantOut(t, r, "=== codex ===")
		}},
		{"single provider claude json", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			seedAccount(t, claudeMS, "c-uuid-sp", "sp@example.com", "plan", time.Now())

			codexMS := testutil.MemStore(t)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list", "claude")
			wantExit(t, r, 0)
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
		}},
		{"single provider claude text no section header", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			seedAccount(t, claudeMS, "c-uuid-txt", "txt@example.com", "plan", time.Now())

			codexMS := testutil.MemStore(t)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{Human: true}, "list", "claude")
			wantExit(t, r, 0)
			// Single-provider text mode: no header (backward compat).
			if strings.Contains(r.stdout, "===") {
				t.Errorf("single-provider text should have no section header; stdout: %q", r.stdout)
			}
			wantOut(t, r, "txt@example.com")
		}},
		{"unknown provider errors", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			codexMS := testutil.MemStore(t)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "list", "bogus")
			wantExit(t, r, 2)
			wantErrOut(t, r, "unknown provider")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// --- accounts remove ---

func TestAccountsRemove(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		// ---- argument errors (single-provider) ----
		{"no arg", func(t *testing.T) {
			ms := testutil.MemStore(t)
			r := runAccountsTest(ms, noopResolver, globals{}, "remove")
			wantExit(t, r, 2)
			wantErrOut(t, r, "accounts remove requires an email or uuid argument")
		}},
		{"no match", func(t *testing.T) {
			ms := testutil.MemStore(t)
			seedAccount(t, ms, "cccc-3333", "real@example.com", "plan", time.Now())

			r := runAccountsTest(ms, noopResolver, globals{}, "remove", "nobody@example.com")
			wantExit(t, r, 2)
			wantErrOut(t, r, `no stored account matches "nobody@example.com"`)
		}},
		{"multiple matches", func(t *testing.T) {
			ms := testutil.MemStore(t)
			seedAccount(t, ms, "dddd-4444", "work@company.com", "plan", time.Now())
			seedAccount(t, ms, "eeee-5555", "work@other.com", "plan", time.Now())

			// "work" matches both emails via substring.
			r := runAccountsTest(ms, noopResolver, globals{}, "remove", "work")
			wantExit(t, r, 2)
			wantErrOut(t, r, `multiple stored accounts match "work", disambiguate by uuid`)
		}},
		// ---- active-protection (D11) ----
		{"active protection blocks remove", func(t *testing.T) {
			ms := testutil.MemStore(t)
			activeUUID := "ffff-6666"
			seedAccount(t, ms, activeUUID, "active@example.com", "plan", time.Now())

			// Resolver reports this account as active.
			r := runAccountsTest(ms, stubResolver(activeUUID), globals{}, "remove", "active@example.com")
			wantExit(t, r, 2)
			wantErrOut(t, r, "cannot remove currently active account — use 'claude /logout' first")

			// Account must still be present in the store.
			listed, _ := ms.List(context.Background())
			if len(listed) != 1 {
				t.Fatalf("store should still have 1 account after blocked remove; got %d", len(listed))
			}
		}},
		{"resolver error fails closed", func(t *testing.T) {
			ms := testutil.MemStore(t)
			uuid := "ffff-7777"
			seedAccount(t, ms, uuid, "target@example.com", "plan", time.Now())

			errResolver := func(_ context.Context, _ []accounts.Account) (string, error) {
				return "", errors.New("profile lookup timed out")
			}
			r := runAccountsTest(ms, errResolver, globals{}, "remove", "target@example.com")
			wantExit(t, r, 2)
			wantErrOut(t, r, "could not verify active account")
			wantErrOut(t, r, "profile lookup timed out")
			// Account must still be present.
			listed, _ := ms.List(context.Background())
			if len(listed) != 1 {
				t.Fatalf("store should still have 1 account after blocked remove; got %d", len(listed))
			}
		}},
		// ---- happy path ----
		{"happy path removes account", func(t *testing.T) {
			ms := testutil.MemStore(t)
			uuid := "gggg-7777"
			seedAccount(t, ms, uuid, "gone@example.com", "plan", time.Now())

			// A different UUID is active → remove is allowed.
			r := runAccountsTest(ms, stubResolver("other-uuid"), globals{}, "remove", "gone@example.com")
			wantExit(t, r, 0)
			wantOut(t, r, "removed gone@example.com")
			wantOut(t, r, fmt.Sprintf("uuid %s", uuid))

			// Account must be gone.
			listed, _ := ms.List(context.Background())
			if len(listed) != 0 {
				t.Fatalf("store should be empty after remove; got %d", len(listed))
			}
		}},
		// ---- UUID-prefix matching ----
		{"uuid prefix matching", func(t *testing.T) {
			ms := testutil.MemStore(t)
			uuid := "abcdef01-1234-5678-9abc-def012345678"
			seedAccount(t, ms, uuid, "user@example.com", "plan", time.Now())

			// 8+ hex chars prefix matches.
			r := runAccountsTest(ms, noopResolver, globals{}, "remove", "abcdef01")
			wantExit(t, r, 0)
			listed, _ := ms.List(context.Background())
			if len(listed) != 0 {
				t.Fatalf("account should be removed; got %d accounts", len(listed))
			}
		}},
		{"email substring matching", func(t *testing.T) {
			ms := testutil.MemStore(t)
			seedAccount(t, ms, "hhhh-8888", "david@personal.com", "plan", time.Now())

			// "personal" matches as email substring.
			r := runAccountsTest(ms, noopResolver, globals{}, "remove", "personal")
			wantExit(t, r, 0)
			listed, _ := ms.List(context.Background())
			if len(listed) != 0 {
				t.Fatalf("account should be removed; got %d accounts", len(listed))
			}
		}},
		// ---- multi-provider remove tests ----
		{"infer by id unique codex", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)

			codexMS := testutil.MemStore(t)
			seedCodexAccount(t, codexMS, "uuid-dtarget", "target@codex.com", "plan", time.Now())

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "target@codex.com")
			wantExit(t, r, 0)
			wantOut(t, r, "removed target@codex.com")
			listed, _ := codexMS.List(context.Background())
			if len(listed) != 0 {
				t.Fatalf("account should be removed from Codex store; got %d", len(listed))
			}
		}},
		{"infer by id ambiguous across providers", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			seedAccount(t, claudeMS, "uuid-cs", "shared@example.com", "plan", time.Now())

			codexMS := testutil.MemStore(t)
			seedCodexAccount(t, codexMS, "uuid-ds", "shared@chatgpt.com", "plan", time.Now())

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "shared")
			wantExit(t, r, 2)
			wantErrOut(t, r, "multiple providers")
		}},
		{"explicit provider codex removes by uuid prefix", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)

			codexMS := testutil.MemStore(t)
			// Use a hex-format UUID so UUID-prefix matching works.
			seedCodexAccount(t, codexMS, "abcdef01-1111-2222-3333-444444444444", "remove@chatgpt.com", "plan", time.Now())

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "abcdef01", "codex")
			wantExit(t, r, 0)
			listed, _ := codexMS.List(context.Background())
			if len(listed) != 0 {
				t.Fatalf("account should be removed; got %d accounts", len(listed))
			}
		}},
		{"too many args errors", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			codexMS := testutil.MemStore(t)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "some-id", "claude", "extra")
			wantExit(t, r, 2)
			wantErrOut(t, r, "unexpected argument")
		}},
		{"explicit provider unknown errors", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			codexMS := testutil.MemStore(t)

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "some-id", "bogus")
			wantExit(t, r, 2)
			wantErrOut(t, r, "unknown provider")
		}},
		{"infer same provider multi match shows single-provider message", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)
			seedAccount(t, claudeMS, "uuid-cm1", "shared@work.com", "plan", time.Now())
			seedAccount(t, claudeMS, "uuid-cm2", "shared@personal.com", "plan", time.Now())

			codexMS := testutil.MemStore(t) // empty → no Codex match

			r := runAccountsTwoStores(claudeMS, codexMS, globals{}, "remove", "shared")
			wantExit(t, r, 2)
			wantErrOut(t, r, "multiple stored accounts match")
			if strings.Contains(r.stderr, "multiple providers") {
				t.Errorf("should NOT show cross-provider ambiguity; stderr: %q", r.stderr)
			}
		}},
		{"active protection codex uses provider logout hint", func(t *testing.T) {
			claudeMS := testutil.MemStore(t)

			codexMS := testutil.MemStore(t)
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

			wantExit(t, r, 2)
			wantErrOut(t, r, "cannot remove currently active account — log out of the Codex app first")
			listed, _ := codexMS.List(context.Background())
			if len(listed) != 1 {
				t.Fatalf("account should still be present; got %d", len(listed))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
