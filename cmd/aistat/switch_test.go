package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/providers"
)

// stubSwitchClient implements switchable for tests.
type stubSwitchClient struct {
	fetchResults       []providers.AccountResult
	fetchErr           error
	reconcileCalled    bool
	postSwitchVerifyFn func(context.Context, accounts.Account) error
}

func (s *stubSwitchClient) FetchForSwitch(_ context.Context) ([]providers.AccountResult, error) {
	return s.fetchResults, s.fetchErr
}

func (s *stubSwitchClient) ReconcileAndPersist(_ context.Context) error {
	s.reconcileCalled = true
	return nil
}

func (s *stubSwitchClient) PostSwitchVerify(ctx context.Context, target accounts.Account) error {
	if s.postSwitchVerifyFn != nil {
		return s.postSwitchVerifyFn(ctx, target)
	}
	return nil
}

// withSwitchClient swaps newSwitchClient for the duration of the test.
func withSwitchClient(t *testing.T, stub *stubSwitchClient) {
	t.Helper()
	old := newSwitchClient
	newSwitchClient = func(_ io.Writer, _ string, _ accounts.Store) switchable {
		return stub
	}
	t.Cleanup(func() { newSwitchClient = old })
}

// withSwitchActiveUUID stubs switchLookupActiveUUID to return a fixed UUID.
func withSwitchActiveUUID(t *testing.T, uuid string) {
	t.Helper()
	old := switchLookupActiveUUID
	switchLookupActiveUUID = func(_ context.Context, _ []accounts.Account, _ io.Writer) (string, error) {
		return uuid, nil
	}
	t.Cleanup(func() { switchLookupActiveUUID = old })
}

// withFetchLiveUsageFn stubs fetchLiveUsage with a function that receives the token.
func withFetchLiveUsageFn(t *testing.T, fn func(token string) (map[string]providers.Limit, error)) {
	t.Helper()
	old := fetchLiveUsage
	fetchLiveUsage = func(_ context.Context, token, _, _ string, _ io.Writer) (map[string]providers.Limit, error) {
		return fn(token)
	}
	t.Cleanup(func() { fetchLiveUsage = old })
}

// withWriteBlob stubs writeClaudeLiveBlob, capturing the written bytes.
// Setting *writeErr before the call makes the stub return that error.
func withWriteBlob(t *testing.T) (written *[]byte, writeErr *error) {
	t.Helper()
	var blob []byte
	var werr error
	old := writeClaudeLiveBlob
	writeClaudeLiveBlob = func(_ context.Context, raw []byte) error {
		if werr != nil {
			return werr
		}
		blob = append([]byte{}, raw...)
		return nil
	}
	t.Cleanup(func() { writeClaudeLiveBlob = old })
	return &blob, &werr
}

// runSwitchTest calls runSwitch directly with an empty globals struct.
func runSwitchTest(args ...string) runResult {
	var stdout, stderr bytes.Buffer
	code := runSwitch(args, &stdout, &stderr, globals{})
	return runResult{stdout.String(), stderr.String(), code}
}

// makeLimits builds a Limit map with the given five_hour RemainingPercent.
func makeLimits(fiveHourRemaining float64) map[string]providers.Limit {
	return map[string]providers.Limit{
		"five_hour": {
			RemainingPercent: fiveHourRemaining,
			UsedPercent:      100 - fiveHourRemaining,
		},
	}
}

// getAccountFromStore returns the account for uuid from the store, failing the
// test if it is absent.
func getAccountFromStore(t *testing.T, ms *accounts.MemoryStore, uuid string) accounts.Account {
	t.Helper()
	all, err := ms.List(context.Background())
	if err != nil {
		t.Fatalf("getAccountFromStore: List: %v", err)
	}
	for _, a := range all {
		if a.UUID == uuid {
			return a
		}
	}
	t.Fatalf("getAccountFromStore: uuid %q not found", uuid)
	return accounts.Account{}
}

// ---- --to targeted switch tests ----

func TestSwitchTo(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"email happy path", func(t *testing.T) {
			ms := withMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-work", "work@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))

			withSwitchActiveUUID(t, "uuid-work") // work is currently active
			stub := &stubSwitchClient{}
			withSwitchClient(t, stub)
			written, _ := withWriteBlob(t)

			r := runSwitchTest("--to", "personal") // email-substring match
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "switched to personal@example.com (uuid uuid-personal)") {
				t.Errorf("unexpected stdout: %q", r.stdout)
			}
			if !strings.Contains(r.stdout, "was work@example.com") {
				t.Errorf("missing 'was' part: %q", r.stdout)
			}

			// Written blob must match personal account's RawBlob.
			personal := getAccountFromStore(t, ms, "uuid-personal")
			if !bytes.Equal(*written, []byte(personal.RawBlob)) {
				t.Errorf("written blob mismatch: got %q, want %q", *written, personal.RawBlob)
			}

			// ReconcileAndPersist must be called post-write.
			if !stub.reconcileCalled {
				t.Error("ReconcileAndPersist was not called after successful write")
			}
		}},
		{"uuid prefix happy path", func(t *testing.T) {
			ms := withMemoryStore(t)
			now := time.Now()
			// Use hex UUIDs so the prefix triggers the uuidish branch.
			seedAccount(t, ms, "bbbb6666-7777-8888-9999-000000000000", "work@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))
			seedAccount(t, ms, "aaaa1111-2222-3333-4444-555555555555", "personal@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))

			withSwitchActiveUUID(t, "bbbb6666-7777-8888-9999-000000000000")
			stub := &stubSwitchClient{}
			withSwitchClient(t, stub)
			written, _ := withWriteBlob(t)

			r := runSwitchTest("--to", "aaaa1111") // UUID-prefix (8 hex chars)
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "aaaa1111-2222-3333-4444-555555555555") {
				t.Errorf("UUID not in output: %q", r.stdout)
			}

			personal := getAccountFromStore(t, ms, "aaaa1111-2222-3333-4444-555555555555")
			if !bytes.Equal(*written, []byte(personal.RawBlob)) {
				t.Errorf("written blob mismatch")
			}
			if !stub.reconcileCalled {
				t.Error("ReconcileAndPersist was not called")
			}
		}},
		{"already active no write", func(t *testing.T) {
			ms := withMemoryStore(t)
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", time.Now())
			withSwitchActiveUUID(t, "uuid-personal") // personal is active

			written, _ := withWriteBlob(t)

			r := runSwitchTest("--to", "personal")
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d", r.code)
			}
			if !strings.Contains(r.stdout, "already on personal@example.com") {
				t.Errorf("wrong stdout: %q", r.stdout)
			}
			if *written != nil {
				t.Error("writeClaudeLiveBlob should not have been called")
			}
		}},
		{"unknown target errors", func(t *testing.T) {
			ms := withMemoryStore(t)
			seedAccount(t, ms, "uuid-work", "work@example.com", "default_claude_max_20x", time.Now())
			withSwitchActiveUUID(t, "uuid-work")

			r := runSwitchTest("--to", "nobody@example.com")
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, `no stored account matches "nobody@example.com"`) {
				t.Errorf("wrong error: %q", r.stderr)
			}
		}},
		{"multiple matches disambiguate error", func(t *testing.T) {
			ms := withMemoryStore(t)
			now := time.Now()
			// Both emails contain "example" → multiple matches.
			seedAccount(t, ms, "uuid-a", "a@example.com", "default_claude_max_5x", now)
			seedAccount(t, ms, "uuid-b", "b@example.com", "default_claude_max_20x", now)
			withSwitchActiveUUID(t, "uuid-a")

			r := runSwitchTest("--to", "example")
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, `multiple stored accounts match "example", disambiguate by uuid`) {
				t.Errorf("wrong error: %q", r.stderr)
			}
		}},
		{"write error no reconcile", func(t *testing.T) {
			ms := withMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-work", "work@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))

			withSwitchActiveUUID(t, "uuid-work")
			stub := &stubSwitchClient{}
			withSwitchClient(t, stub)
			_, writeErrPtr := withWriteBlob(t)
			*writeErrPtr = errors.New("keychain locked")

			r := runSwitchTest("--to", "personal")
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, "aistat: claude: write to live credential failed: keychain locked") {
				t.Errorf("wrong error: %q", r.stderr)
			}
			// ReconcileAndPersist must NOT have been called (store must be unchanged).
			if stub.reconcileCalled {
				t.Error("ReconcileAndPersist must not be called when write fails")
			}
		}},
		{"store open failure errors", func(t *testing.T) {
			old := openAccountStore
			openAccountStore = func(_ io.Writer) (accounts.Store, error) {
				return nil, errors.New("disk unavailable")
			}
			t.Cleanup(func() { openAccountStore = old })

			r := runSwitchTest("--to", "anyone")
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, "aistat: claude: could not open account store: disk unavailable") {
				t.Errorf("wrong error: %q", r.stderr)
			}
		}},
		{"store list failure errors", func(t *testing.T) {
			old := openAccountStore
			openAccountStore = func(_ io.Writer) (accounts.Store, error) {
				return &failListStore{listErr: errors.New("disk I/O error")}, nil
			}
			t.Cleanup(func() { openAccountStore = old })
			withCodexMemoryStore(t) // empty Codex store for determinism

			r := runSwitchTest("--to", "anyone")
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, "aistat: claude: could not list accounts: disk I/O error") {
				t.Errorf("wrong error: %q", r.stderr)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// ---- Auto-pick switch tests ----

func TestSwitchAutoPick(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"zero stored both empty", func(t *testing.T) {
			withMemoryStore(t)      // empty Claude store
			withCodexMemoryStore(t) // empty Codex store
			withSwitchActiveUUID(t, "")

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, "no providers have multiple stored accounts") {
				t.Errorf("missing expected message: %q", r.stderr)
			}
		}},
		{"higher headroom wins", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t) // isolate from any real Codex store on dev machines
			now := time.Now()
			seedAccount(t, ms, "uuid-work", "work@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))

			withSwitchActiveUUID(t, "uuid-work")
			stub := &stubSwitchClient{
				fetchResults: []providers.AccountResult{
					{Email: "personal@example.com", UUID: "uuid-personal", Limits: makeLimits(80)},
				},
			}
			withSwitchClient(t, stub)
			// Active (work) has 20% remaining → personal wins.
			withFetchLiveUsageFn(t, func(_ string) (map[string]providers.Limit, error) {
				return makeLimits(20), nil
			})
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "switched to personal@example.com") {
				t.Errorf("unexpected stdout: %q", r.stdout)
			}
			if !strings.Contains(r.stdout, "was work@example.com") {
				t.Errorf("missing 'was' part: %q", r.stdout)
			}
			personal := getAccountFromStore(t, ms, "uuid-personal")
			if !bytes.Equal(*written, []byte(personal.RawBlob)) {
				t.Errorf("written blob mismatch")
			}
		}},
		{"active already best no write", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))
			seedAccount(t, ms, "uuid-work", "work@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))

			withSwitchActiveUUID(t, "uuid-personal")
			stub := &stubSwitchClient{
				fetchResults: []providers.AccountResult{
					// work is non-active, only 20% remaining
					{Email: "work@example.com", UUID: "uuid-work", Limits: makeLimits(20)},
				},
			}
			withSwitchClient(t, stub)
			// Active (personal) has 80% remaining → already best.
			withFetchLiveUsageFn(t, func(_ string) (map[string]providers.Limit, error) {
				return makeLimits(80), nil
			})
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "already on best account (personal@example.com)") {
				t.Errorf("wrong stdout: %q", r.stdout)
			}
			if *written != nil {
				t.Error("writeClaudeLiveBlob should not have been called when active is already best")
			}
		}},
		{"tiebreaker most recent wins", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t)
			now := time.Now()
			// accountA: 82% remaining, last seen 2 hours ago (floor(82/5)=16)
			// accountB: 80% remaining, last seen 1 hour ago  (floor(80/5)=16)
			// Same bucket → tiebreak by LastSeenAt → accountB wins (more recent).
			seedAccount(t, ms, "uuid-active", "active@example.com", "default_claude_max_5x", now.Add(-30*time.Minute))
			seedAccount(t, ms, "uuid-a", "accounta@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))
			seedAccount(t, ms, "uuid-b", "accountb@example.com", "default_claude_max_20x", now.Add(-1*time.Hour))

			withSwitchActiveUUID(t, "uuid-active")
			stub := &stubSwitchClient{
				fetchResults: []providers.AccountResult{
					{Email: "accounta@example.com", UUID: "uuid-a", Limits: makeLimits(82)},
					{Email: "accountb@example.com", UUID: "uuid-b", Limits: makeLimits(80)},
				},
			}
			withSwitchClient(t, stub)
			// Active has 50% remaining (bucket 10), below both candidates (bucket 16).
			withFetchLiveUsageFn(t, func(_ string) (map[string]providers.Limit, error) {
				return makeLimits(50), nil
			})
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			// accountB should win (same bucket as A, but more recent LastSeenAt).
			if !strings.Contains(r.stdout, "accountb@example.com") {
				t.Errorf("expected accountB to win, got stdout: %q", r.stdout)
			}
			accountB := getAccountFromStore(t, ms, "uuid-b")
			if !bytes.Equal(*written, []byte(accountB.RawBlob)) {
				t.Errorf("written blob does not match accountB's RawBlob")
			}
		}},
		{"one failing excluded other wins", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-active", "active@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))
			seedAccount(t, ms, "uuid-good", "good@example.com", "default_claude_max_20x", now.Add(-30*time.Minute))
			// "bad" is excluded — FetchForSwitch already warned; stub does not return it.

			withSwitchActiveUUID(t, "uuid-active")
			stub := &stubSwitchClient{
				fetchResults: []providers.AccountResult{
					// Only good@example.com survived; bad@example.com was excluded.
					{Email: "good@example.com", UUID: "uuid-good", Limits: makeLimits(70)},
				},
			}
			withSwitchClient(t, stub)
			withFetchLiveUsageFn(t, func(_ string) (map[string]providers.Limit, error) {
				return makeLimits(20), nil // active at 20% → good at 70% wins
			})
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "good@example.com") {
				t.Errorf("expected good to win, got stdout: %q", r.stdout)
			}
			good := getAccountFromStore(t, ms, "uuid-good")
			if !bytes.Equal(*written, []byte(good.RawBlob)) {
				t.Errorf("written blob mismatch")
			}
		}},
		{"all failing exits 2", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-active", "active@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))
			seedAccount(t, ms, "uuid-other", "other@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))

			withSwitchActiveUUID(t, "uuid-active")
			stub := &stubSwitchClient{
				fetchResults: nil, // all excluded
			}
			withSwitchClient(t, stub)
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, "auto-pick failed: no accounts produced usable usage data") {
				t.Errorf("wrong error: %q", r.stderr)
			}
			if *written != nil {
				t.Error("writeClaudeLiveBlob should not have been called")
			}
		}},
		{"fetch error exits 2", func(t *testing.T) {
			ms := withMemoryStore(t)
			withCodexMemoryStore(t)
			now := time.Now()
			seedAccount(t, ms, "uuid-active", "active@example.com", "default_claude_max_5x", now.Add(-1*time.Hour))
			seedAccount(t, ms, "uuid-other", "other@example.com", "default_claude_max_20x", now.Add(-2*time.Hour))

			withSwitchActiveUUID(t, "uuid-active")
			stub := &stubSwitchClient{
				fetchErr: errors.New("network timeout"),
			}
			withSwitchClient(t, stub)
			written, _ := withWriteBlob(t)

			r := runSwitchTest()
			if r.code != 2 {
				t.Fatalf("expected exit 2, got %d", r.code)
			}
			if !strings.Contains(r.stderr, "aistat: claude: auto-pick usage fetch failed: network timeout") {
				t.Errorf("wrong error: %q", r.stderr)
			}
			if *written != nil {
				t.Error("writeClaudeLiveBlob should not have been called")
			}
		}},
		{"only one account no eligible providers", func(t *testing.T) {
			ms := withMemoryStore(t)
			seedAccount(t, ms, "uuid-personal", "personal@example.com", "default_claude_max_5x", time.Now())
			withCodexMemoryStore(t) // empty Codex store
			withSwitchActiveUUID(t, "uuid-personal")

			r := runSwitchTest()
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stderr, "no providers have multiple stored accounts") {
				t.Errorf("missing expected message: %q", r.stderr)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// failListStore is a Store whose List always returns an error; all other ops succeed.
type failListStore struct {
	listErr error
}

func (f *failListStore) List(_ context.Context) ([]accounts.Account, error) {
	return nil, f.listErr
}
func (f *failListStore) Upsert(_ context.Context, _ accounts.Account) error { return nil }
func (f *failListStore) Delete(_ context.Context, _ string) error           { return nil }

// ---- PostSwitchVerify tests (all use Codex scaffold per B4#2) ----

// scaffoldCodexSwitch sets up the Codex scaffold for PostSwitchVerify tests:
// two accounts ("alice" active-other, "bob" active), active = bob, --to alice.
// Returns the stub so callers can set postSwitchVerifyFn before calling runSwitchTest.
func scaffoldCodexSwitch(t *testing.T) *stubCodexSwitchClient {
	t.Helper()
	now := time.Now()
	ms := withCodexMemoryStore(t)
	seedCodexAccount(t, ms, "uuid-alice", "alice@example.com", "plan", now.Add(-1*time.Hour))
	seedCodexAccount(t, ms, "uuid-bob", "bob@example.com", "plan", now.Add(-2*time.Hour))
	withCodexActiveUUID(t, "uuid-bob")
	withMemoryStore(t) // empty Claude store — must not be touched
	stub := &stubCodexSwitchClient{}
	withCodexSwitchClient(t, stub)
	withCodexWriteBlob(t)
	return stub
}

func TestSwitchPostSwitchVerify(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"auth denied warns exit 0", func(t *testing.T) {
			stub := scaffoldCodexSwitch(t)
			stub.postSwitchVerifyFn = func(_ context.Context, _ accounts.Account) error {
				return fmt.Errorf("alice@example.com: tokens revoked by upstream...: %w", providers.ErrAuthDenied)
			}

			r := runSwitchTest("codex", "--to", "alice")
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d (stderr: %q)", r.code, r.stderr)
			}
			if !strings.Contains(r.stdout, "switched to") {
				t.Errorf("expected 'switched to' in stdout: %q", r.stdout)
			}
			wantSubstr := "aistat: codex: switched-to account's tokens are not usable: alice@example.com:"
			if !strings.Contains(r.stderr, wantSubstr) {
				t.Errorf("expected %q in stderr: %q", wantSubstr, r.stderr)
			}
			if !strings.Contains(r.stderr, "tokens revoked") {
				t.Errorf("expected 'tokens revoked' in stderr: %q", r.stderr)
			}
			if strings.Count(r.stderr, "aistat: codex:") != 1 {
				t.Errorf("expected exactly one 'aistat: codex:' in stderr, got %d: %q",
					strings.Count(r.stderr, "aistat: codex:"), r.stderr)
			}
		}},
		{"transient error silenced", func(t *testing.T) {
			stub := scaffoldCodexSwitch(t)
			stub.postSwitchVerifyFn = func(_ context.Context, _ accounts.Account) error {
				return providers.ErrTransient
			}

			r := runSwitchTest("codex", "--to", "alice")
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d", r.code)
			}
			if strings.Contains(r.stderr, "tokens are not usable") {
				t.Errorf("transient error should be silenced; stderr: %q", r.stderr)
			}
		}},
		{"nil error no warning", func(t *testing.T) {
			scaffoldCodexSwitch(t) // postSwitchVerifyFn unset → returns nil

			r := runSwitchTest("codex", "--to", "alice")
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d", r.code)
			}
			if strings.Contains(r.stderr, "tokens are not usable") {
				t.Errorf("nil verify should produce no warning; stderr: %q", r.stderr)
			}
		}},
		{"non-wrapping error silenced", func(t *testing.T) {
			stub := scaffoldCodexSwitch(t)
			stub.postSwitchVerifyFn = func(_ context.Context, _ accounts.Account) error {
				return errors.New("plain error without ErrAuthDenied wrap")
			}

			r := runSwitchTest("codex", "--to", "alice")
			if r.code != 0 {
				t.Fatalf("expected exit 0, got %d", r.code)
			}
			if strings.Contains(r.stderr, "tokens are not usable") {
				t.Errorf("non-wrapping error should be silenced; stderr: %q", r.stderr)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
