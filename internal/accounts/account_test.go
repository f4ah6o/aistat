package accounts

import (
	"encoding/json"
	"testing"
	"time"
)

// rawBlob builds a minimal credential JSON blob for tests.
func rawBlob(accessToken, refreshToken string, expiresAt int64) json.RawMessage {
	type oauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	}
	type payload struct {
		ClaudeAiOauth oauth `json:"claudeAiOauth"`
	}
	data, err := json.Marshal(payload{ClaudeAiOauth: oauth{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}})
	if err != nil {
		panic(err)
	}
	return json.RawMessage(data)
}

func TestNewAccount_HappyPath(t *testing.T) {
	raw := rawBlob("at-abc", "rt-xyz", 1234567890000)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	a, err := NewAccount(raw, "uuid-1", "user@example.com", "User", "default_claude_max_5x", now)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	if a.UUID != "uuid-1" {
		t.Errorf("UUID: got %q, want %q", a.UUID, "uuid-1")
	}
	if a.Email != "user@example.com" {
		t.Errorf("Email: got %q", a.Email)
	}
	if a.DisplayName != "User" {
		t.Errorf("DisplayName: got %q", a.DisplayName)
	}
	if a.RateLimitTier != "default_claude_max_5x" {
		t.Errorf("RateLimitTier: got %q", a.RateLimitTier)
	}
	if !a.LastSeenAt.Equal(now) {
		t.Errorf("LastSeenAt: got %v, want %v", a.LastSeenAt, now)
	}
	if a.AccessToken() != "at-abc" {
		t.Errorf("AccessToken: got %q, want %q", a.AccessToken(), "at-abc")
	}
}

func TestNewAccount_EmptyRaw(t *testing.T) {
	_, err := NewAccount(json.RawMessage{}, "uuid-1", "user@example.com", "", "", time.Now())
	if err == nil {
		t.Fatal("expected error for empty raw blob")
	}
}

func TestNewAccount_InvalidJSON(t *testing.T) {
	_, err := NewAccount(json.RawMessage(`{not json`), "uuid-1", "user@example.com", "", "", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNewAccount_MissingAccessToken(t *testing.T) {
	raw := json.RawMessage(`{"claudeAiOauth":{"accessToken":""}}`)
	_, err := NewAccount(raw, "uuid-1", "user@example.com", "", "", time.Now())
	if err == nil {
		t.Fatal("expected error for empty accessToken")
	}
}

func TestNewAccount_EmptyUUID(t *testing.T) {
	raw := rawBlob("at-abc", "rt-xyz", 0)
	_, err := NewAccount(raw, "", "user@example.com", "", "", time.Now())
	if err == nil {
		t.Fatal("expected error for empty uuid")
	}
}

func TestNewAccount_MissingClaudeAiOauthField(t *testing.T) {
	raw := json.RawMessage(`{"otherField":"value"}`)
	_, err := NewAccount(raw, "uuid-1", "user@example.com", "", "", time.Now())
	if err == nil {
		t.Fatal("expected error when claudeAiOauth is absent")
	}
}

func TestAccount_ZeroValueSafe(t *testing.T) {
	var a Account
	if got := a.AccessToken(); got != "" {
		t.Errorf("zero-value AccessToken: got %q, want empty", got)
	}
	if got := a.RefreshToken(); got != "" {
		t.Errorf("zero-value RefreshToken: got %q, want empty", got)
	}
	if got := a.ExpiresAt(); got != 0 {
		t.Errorf("zero-value ExpiresAt: got %d, want 0", got)
	}
}

func TestAccount_TokenMethods(t *testing.T) {
	raw := rawBlob("at-abc", "rt-xyz", 9876543210000)
	now := time.Now()
	a, err := NewAccount(raw, "uuid-1", "u@x.com", "", "", now)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	if got := a.AccessToken(); got != "at-abc" {
		t.Errorf("AccessToken: got %q", got)
	}
	if got := a.RefreshToken(); got != "rt-xyz" {
		t.Errorf("RefreshToken: got %q", got)
	}
	if got := a.ExpiresAt(); got != 9876543210000 {
		t.Errorf("ExpiresAt: got %d", got)
	}
}

func TestAccount_RoundTrip(t *testing.T) {
	raw := rawBlob("at-round", "rt-round", 111222333)
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	orig, err := NewAccount(raw, "uuid-rt", "rt@example.com", "RT User", "default_claude_pro", now)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	marshaled, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var restored Account
	if err := json.Unmarshal(marshaled, &restored); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if restored.AccessToken() != orig.AccessToken() {
		t.Errorf("AccessToken after round-trip: got %q, want %q", restored.AccessToken(), orig.AccessToken())
	}
	if restored.RefreshToken() != orig.RefreshToken() {
		t.Errorf("RefreshToken after round-trip: got %q, want %q", restored.RefreshToken(), orig.RefreshToken())
	}
	if restored.ExpiresAt() != orig.ExpiresAt() {
		t.Errorf("ExpiresAt after round-trip: got %d, want %d", restored.ExpiresAt(), orig.ExpiresAt())
	}
	if restored.UUID != orig.UUID {
		t.Errorf("UUID after round-trip: got %q, want %q", restored.UUID, orig.UUID)
	}
}

func TestAccount_MalformedRawBlob(t *testing.T) {
	// Accounts with a malformed RawBlob (e.g. loaded from a corrupt store entry)
	// should return safe zero values from token methods, not panic.
	a := Account{
		UUID:    "uuid-bad",
		RawBlob: json.RawMessage(`{not valid json`),
	}
	if got := a.AccessToken(); got != "" {
		t.Errorf("AccessToken on malformed blob: got %q, want empty", got)
	}
	if got := a.RefreshToken(); got != "" {
		t.Errorf("RefreshToken on malformed blob: got %q, want empty", got)
	}
	if got := a.ExpiresAt(); got != 0 {
		t.Errorf("ExpiresAt on malformed blob: got %d, want 0", got)
	}
}
