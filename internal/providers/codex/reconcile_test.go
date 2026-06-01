package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/cred"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// syntheticIDToken builds a signature-free JWT whose payload is
// {"sub":<sub>,"email":<email>,"exp":<expSec>}. The third segment is a
// placeholder so ParseCodexIDToken's 3-segment check passes.
func syntheticIDToken(sub, email string, expSec int64) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{
		"sub":   sub,
		"email": email,
		"exp":   expSec,
	})
	payloadEnc := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + payloadEnc + ".testsig"
}

// rawCodexBlob builds a minimal valid Codex auth.json blob. expiresAtSec==0
// omits the id_token field entirely.
func rawCodexBlob(accessToken, refreshToken string, expiresAtSec int64) json.RawMessage {
	// Derive a deterministic sub from the access token for test simplicity.
	sub := "sub-" + accessToken
	tokens := map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
	}
	if expiresAtSec != 0 {
		tokens["id_token"] = syntheticIDToken(sub, sub+"@example.com", expiresAtSec)
	}
	b, _ := json.Marshal(map[string]any{
		"tokens": tokens,
	})
	return json.RawMessage(b)
}

// makeCodexCred wraps rawCodexBlob in a cred.Credential.
func makeCodexCred(accessToken, refreshToken string, expiresAtSec int64) *cred.Credential {
	raw := rawCodexBlob(accessToken, refreshToken, expiresAtSec)
	var r struct {
		Tokens struct {
			IDToken string `json:"id_token"`
		} `json:"tokens"`
	}
	_ = json.Unmarshal(raw, &r)
	var expiresAt int64
	if r.Tokens.IDToken != "" {
		_, _, expSec, _ := cred.ParseCodexIDToken(r.Tokens.IDToken)
		expiresAt = expSec * 1000
	}
	return &cred.Credential{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		Raw:          []byte(raw),
	}
}

// makeCodexAccount builds a stored Account with embedded Codex tokens.
func makeCodexAccount(uuid, email, accessToken, refreshToken string, expiresAtSec int64) accounts.Account {
	raw := rawCodexBlob(accessToken, refreshToken, expiresAtSec)
	return accounts.Account{
		UUID:       uuid,
		Email:      email,
		LastSeenAt: time.Time{},
		RawBlob:    raw,
	}
}

// ── LookupID stubs ────────────────────────────────────────────────────────────

func noLookupCall(t *testing.T) func(string) (string, string, error) {
	t.Helper()
	return func(string) (string, string, error) {
		t.Fatal("LookupID must not be called on the byte-match path")
		return "", "", nil
	}
}

func fixedLookup(sub, email string) func(string) (string, string, error) {
	return func(string) (string, string, error) { return sub, email, nil }
}

func errLookup(err error) func(string) (string, string, error) {
	return func(string) (string, string, error) { return "", "", err }
}

// ── test clock ────────────────────────────────────────────────────────────────

var testNow = time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)

// ── Reconcile tests ───────────────────────────────────────────────────────────

// TestReconcile_ByteMatch_SetsActive: live AT byte-matches slot[0] → active,
// Upserted, no LookupID call.
func TestReconcile_ByteMatch_SetsActive(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
		makeCodexAccount("uuid-1", "user1@example.com", "tok-b", "ref-b", 2000),
	}
	live := makeCodexCred("tok-a", "ref-a", 1000)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-0" {
		t.Errorf("ActiveUUID = %q, want %q", out.ActiveUUID, "uuid-0")
	}
	if !out.Upserted {
		t.Error("Upserted = false, want true")
	}
	if out.Inserted {
		t.Error("Inserted = true, want false")
	}
	if out.LiveUnstored != nil {
		t.Errorf("LiveUnstored should be nil on byte-match, got %v", out.LiveUnstored)
	}
	if out.CaptureWarn != "" {
		t.Errorf("CaptureWarn should be empty on byte-match, got %q", out.CaptureWarn)
	}
	if len(out.Accounts) != 2 {
		t.Errorf("Accounts len = %d, want 2", len(out.Accounts))
	}
}

// TestReconcile_ByteMatch_StaleRefreshToken: same AT, different RT/id_token in
// live → RawBlob updated, LastSeenAt stamped.
func TestReconcile_ByteMatch_StaleRefreshToken(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a-old", 1000),
	}
	live := makeCodexCred("tok-a", "ref-a-new", 9999)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-0" {
		t.Errorf("ActiveUUID = %q, want %q", out.ActiveUUID, "uuid-0")
	}
	if !out.Upserted {
		t.Error("Upserted = false, want true")
	}
	slot := out.Accounts[0]
	if got := StoredRefreshToken(slot); got != "ref-a-new" {
		t.Errorf("StoredRefreshToken() = %q, want %q", got, "ref-a-new")
	}
	if slot.LastSeenAt != testNow {
		t.Errorf("slot.LastSeenAt = %v, want %v", slot.LastSeenAt, testNow)
	}
}

// TestReconcile_LookupID_MatchesStoredUUID: no byte-match, LookupID returns sub
// matching stored UUID → Upserted, email drift applied.
func TestReconcile_LookupID_MatchesStoredUUID(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "old@example.com", "tok-a", "ref-a", 1000),
	}
	live := makeCodexCred("tok-b", "ref-b", 2000)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: fixedLookup("uuid-0", "new@example.com"),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-0" {
		t.Errorf("ActiveUUID = %q, want %q", out.ActiveUUID, "uuid-0")
	}
	if !out.Upserted {
		t.Error("Upserted = false, want true")
	}
	if out.Inserted {
		t.Error("Inserted = true, want false")
	}
	if got := out.Accounts[0].Email; got != "new@example.com" {
		t.Errorf("Email = %q, want %q", got, "new@example.com")
	}
}

// TestReconcile_LookupID_NewSlot: no byte-match, LookupID sub doesn't match
// any stored UUID → Inserted.
func TestReconcile_LookupID_NewSlot(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
	}
	live := makeCodexCred("tok-new", "ref-new", 5000)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: fixedLookup("uuid-brand-new", "brand-new@example.com"),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-brand-new" {
		t.Errorf("ActiveUUID = %q, want %q", out.ActiveUUID, "uuid-brand-new")
	}
	if !out.Inserted {
		t.Error("Inserted = false, want true")
	}
	if out.Upserted {
		t.Error("Upserted = true, want false")
	}
	if len(out.Accounts) != 2 {
		t.Fatalf("Accounts len = %d, want 2", len(out.Accounts))
	}
	newSlot := out.Accounts[1]
	if newSlot.UUID != "uuid-brand-new" {
		t.Errorf("new slot UUID = %q, want %q", newSlot.UUID, "uuid-brand-new")
	}
	if newSlot.Email != "brand-new@example.com" {
		t.Errorf("new slot Email = %q, want %q", newSlot.Email, "brand-new@example.com")
	}
}

// TestReconcile_LookupID_EmptyStore: no stored accounts, live present → Inserted.
func TestReconcile_LookupID_EmptyStore(t *testing.T) {
	live := makeCodexCred("tok-new", "ref-new", 5000)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   nil,
		LookupID: fixedLookup("uuid-fresh", "fresh@example.com"),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-fresh" {
		t.Errorf("ActiveUUID = %q, want %q", out.ActiveUUID, "uuid-fresh")
	}
	if !out.Inserted {
		t.Error("Inserted = false, want true")
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("Accounts len = %d, want 1", len(out.Accounts))
	}
	if out.Accounts[0].UUID != "uuid-fresh" {
		t.Errorf("Accounts[0].UUID = %q, want %q", out.Accounts[0].UUID, "uuid-fresh")
	}
}

// TestReconcile_LookupIDFails_Fallback: LookupID returns error → LiveUnstored
// set, CaptureWarn matches D3 template.
func TestReconcile_LookupIDFails_Fallback(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
	}
	live := makeCodexCred("tok-b", "ref-b", 2000)

	lookupErr := fmt.Errorf("jwt parse failed")
	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: errLookup(lookupErr),
		Now:      testNow,
	})

	if out.ActiveUUID != "" {
		t.Errorf("ActiveUUID = %q, want empty", out.ActiveUUID)
	}
	if out.LiveUnstored == nil {
		t.Fatal("LiveUnstored should be non-nil on fallback")
	}
	if out.LiveUnstored != live {
		t.Error("LiveUnstored should point to the live blob")
	}
	if !strings.Contains(out.CaptureWarn, "could not identify live account") {
		t.Errorf("CaptureWarn = %q, want 'could not identify live account'", out.CaptureWarn)
	}
	if !strings.Contains(out.CaptureWarn, "codex login") {
		t.Errorf("CaptureWarn = %q, want recovery hint containing 'codex login'", out.CaptureWarn)
	}
	if out.Inserted || out.Upserted {
		t.Error("Inserted/Upserted must be false on fallback")
	}
	if len(out.Accounts) != len(stored) {
		t.Errorf("Accounts len = %d, want %d", len(out.Accounts), len(stored))
	}
}

// TestReconcile_MissingIDToken_Fallback: live blob has no tokens.id_token →
// extractIDToken returns "" → guard triggers before LookupID is called →
// LiveUnstored with CaptureWarn mentioning "no id_token".
func TestReconcile_MissingIDToken_Fallback(t *testing.T) {
	// Build a blob without id_token (expiresAtSec==0 omits it).
	raw := rawCodexBlob("tok-b", "ref-b", 0)
	live := &cred.Credential{
		AccessToken:  "tok-b",
		RefreshToken: "ref-b",
		Raw:          []byte(raw),
	}
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
	}

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: noLookupCall(t), // must NOT be called
		Now:      testNow,
	})

	if out.LiveUnstored == nil {
		t.Fatal("LiveUnstored should be non-nil when id_token is absent")
	}
	if !strings.Contains(out.CaptureWarn, "no id_token") {
		t.Errorf("CaptureWarn = %q, want 'no id_token'", out.CaptureWarn)
	}
}

// TestReconcile_LiveAbsent_StoredN: no live credential → all accounts returned,
// none active.
func TestReconcile_LiveAbsent_StoredN(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
		makeCodexAccount("uuid-1", "user1@example.com", "tok-b", "ref-b", 2000),
	}

	out := Reconcile(ReconcileInput{
		LiveBlob: nil,
		Stored:   stored,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if out.ActiveUUID != "" {
		t.Errorf("ActiveUUID = %q, want empty", out.ActiveUUID)
	}
	if len(out.Accounts) != 2 {
		t.Errorf("Accounts len = %d, want 2", len(out.Accounts))
	}
	for i, got := range out.Accounts {
		if got.UUID != stored[i].UUID {
			t.Errorf("Accounts[%d].UUID = %q, want %q", i, got.UUID, stored[i].UUID)
		}
	}
	if out.Inserted || out.Upserted {
		t.Error("Inserted/Upserted must be false when live credential is absent")
	}
}

// TestReconcile_LiveAbsent_StoredEmpty: no live, no stored → empty Accounts.
func TestReconcile_LiveAbsent_StoredEmpty(t *testing.T) {
	out := Reconcile(ReconcileInput{
		LiveBlob: nil,
		Stored:   nil,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if out.ActiveUUID != "" {
		t.Errorf("ActiveUUID = %q, want empty", out.ActiveUUID)
	}
	if len(out.Accounts) != 0 {
		t.Errorf("Accounts len = %d, want 0", len(out.Accounts))
	}
}

// TestReconcile_DuplicateAccessToken: two stored slots share AT → first-match-wins.
func TestReconcile_DuplicateAccessToken(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-first", "first@example.com", "tok-dup", "ref-a", 1000),
		makeCodexAccount("uuid-second", "second@example.com", "tok-dup", "ref-b", 2000),
	}
	live := makeCodexCred("tok-dup", "ref-a", 1000)

	out := Reconcile(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if out.ActiveUUID != "uuid-first" {
		t.Errorf("ActiveUUID = %q, want %q (first-match-wins)", out.ActiveUUID, "uuid-first")
	}
	if !out.Upserted {
		t.Error("Upserted = false, want true")
	}
}

// ── ResolveActiveUUID tests ───────────────────────────────────────────────────

// TestResolveActiveUUID_ByteMatch: returns stored UUID, no LookupID call.
func TestResolveActiveUUID_ByteMatch(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
		makeCodexAccount("uuid-1", "user1@example.com", "tok-b", "ref-b", 2000),
	}
	live := makeCodexCred("tok-b", "ref-b", 2000) // matches slot[1]

	uuid, err := ResolveActiveUUID(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: noLookupCall(t),
		Now:      testNow,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uuid != "uuid-1" {
		t.Errorf("uuid = %q, want %q", uuid, "uuid-1")
	}
}

// TestResolveActiveUUID_LookupSuccess: returns sub from LookupID.
func TestResolveActiveUUID_LookupSuccess(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
	}
	live := makeCodexCred("tok-c", "ref-c", 3000) // no byte-match

	uuid, err := ResolveActiveUUID(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: fixedLookup("uuid-from-lookup", "x@example.com"),
		Now:      testNow,
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uuid != "uuid-from-lookup" {
		t.Errorf("uuid = %q, want %q", uuid, "uuid-from-lookup")
	}
}

// TestResolveActiveUUID_LookupFails_ReturnsEmpty: LookupID error → ("", nil).
// All LookupID failures are pure parse errors (D1); never surfaced as errors
// from ResolveActiveUUID.
func TestResolveActiveUUID_LookupFails_ReturnsEmpty(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
	}
	live := makeCodexCred("tok-c", "ref-c", 3000)

	uuid, err := ResolveActiveUUID(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: errLookup(fmt.Errorf("parse error")),
		Now:      testNow,
	})

	if err != nil {
		t.Errorf("expected nil error (D1: all LookupID failures are parse-only), got: %v", err)
	}
	if uuid != "" {
		t.Errorf("uuid = %q, want empty on lookup failure", uuid)
	}
}

// TestResolveActiveUUID_NoMutation: asserts in.Stored is not modified.
func TestResolveActiveUUID_NoMutation(t *testing.T) {
	stored := []accounts.Account{
		makeCodexAccount("uuid-0", "user0@example.com", "tok-a", "ref-a", 1000),
		makeCodexAccount("uuid-1", "user1@example.com", "tok-b", "ref-b", 2000),
	}
	snapshot := make([]accounts.Account, len(stored))
	copy(snapshot, stored)

	live := makeCodexCred("tok-c", "ref-c", 3000) // no byte-match

	_, _ = ResolveActiveUUID(ReconcileInput{
		LiveBlob: live,
		Stored:   stored,
		LookupID: fixedLookup("uuid-0", "changed@example.com"),
		Now:      testNow,
	})

	for i, orig := range snapshot {
		got := stored[i]
		if got.UUID != orig.UUID {
			t.Errorf("stored[%d].UUID mutated: %q → %q", i, orig.UUID, got.UUID)
		}
		if got.Email != orig.Email {
			t.Errorf("stored[%d].Email mutated: %q → %q", i, orig.Email, got.Email)
		}
		if string(got.RawBlob) != string(orig.RawBlob) {
			t.Errorf("stored[%d].RawBlob mutated", i)
		}
		if got.LastSeenAt != orig.LastSeenAt {
			t.Errorf("stored[%d].LastSeenAt mutated: %v → %v", i, orig.LastSeenAt, got.LastSeenAt)
		}
	}
}
