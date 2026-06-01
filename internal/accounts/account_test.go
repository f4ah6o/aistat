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

func TestNewAccount(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"happy path", func(t *testing.T) {
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
		}},
		{"empty raw", func(t *testing.T) {
			_, err := NewAccount(json.RawMessage{}, "uuid-1", "user@example.com", "", "", time.Now())
			if err == nil {
				t.Fatal("expected error for empty raw blob")
			}
		}},
		{"invalid json", func(t *testing.T) {
			_, err := NewAccount(json.RawMessage(`{not json`), "uuid-1", "user@example.com", "", "", time.Now())
			if err == nil {
				t.Fatal("expected error for invalid JSON")
			}
		}},
		{"empty uuid", func(t *testing.T) {
			raw := rawBlob("at-abc", "rt-xyz", 0)
			_, err := NewAccount(raw, "", "user@example.com", "", "", time.Now())
			if err == nil {
				t.Fatal("expected error for empty uuid")
			}
		}},
		// NewAccount no longer requires claudeAiOauth.accessToken — token
		// validation is provider-specific.
		{"no access token required", func(t *testing.T) {
			raw := json.RawMessage(`{"claudeAiOauth":{"accessToken":""}}`)
			_, err := NewAccount(raw, "uuid-1", "user@example.com", "", "", time.Now())
			if err != nil {
				t.Fatalf("NewAccount should succeed without accessToken; got %v", err)
			}
		}},
		// Any valid JSON is accepted regardless of shape — provider-neutrality
		// means we don't inspect the fields.
		{"any valid json accepted", func(t *testing.T) {
			raw := json.RawMessage(`{"otherField":"value"}`)
			_, err := NewAccount(raw, "uuid-1", "user@example.com", "", "", time.Now())
			if err != nil {
				t.Fatalf("NewAccount should accept any valid JSON; got %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
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

	if restored.UUID != orig.UUID {
		t.Errorf("UUID after round-trip: got %q, want %q", restored.UUID, orig.UUID)
	}
	if restored.Email != orig.Email {
		t.Errorf("Email after round-trip: got %q, want %q", restored.Email, orig.Email)
	}
	if string(restored.RawBlob) != string(orig.RawBlob) {
		t.Errorf("RawBlob after round-trip: got %q, want %q", restored.RawBlob, orig.RawBlob)
	}
}
