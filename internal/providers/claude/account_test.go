package claude

import (
	"encoding/json"
	"testing"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

// makeRawBlob builds a minimal Claude credential blob for account helper tests.
func makeRawBlob(accessToken, refreshToken string, expiresAt int64) json.RawMessage {
	// rawBlob is already defined in reconcile_test.go; use the same JSON shape.
	b, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  accessToken,
			"refreshToken": refreshToken,
			"expiresAt":    expiresAt,
		},
	})
	return json.RawMessage(b)
}

func TestStoredAccessToken(t *testing.T) {
	tests := []struct {
		name string
		acct accounts.Account
		want string
	}{
		{"happy path", accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 1000)}, "at-abc"},
		{"missing claudeAiOauth", accounts.Account{RawBlob: json.RawMessage(`{"otherField":"value"}`)}, ""},
		{"empty access token", accounts.Account{RawBlob: json.RawMessage(`{"claudeAiOauth":{"accessToken":""}}`)}, ""},
		{"zero value account", accounts.Account{}, ""},
		{"malformed raw blob", accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredAccessToken(tt.acct); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoredRefreshToken(t *testing.T) {
	tests := []struct {
		name string
		acct accounts.Account
		want string
	}{
		{"happy path", accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 1000)}, "rt-xyz"},
		{"zero value account", accounts.Account{}, ""},
		{"malformed raw blob", accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredRefreshToken(tt.acct); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoredExpiresAt(t *testing.T) {
	tests := []struct {
		name string
		acct accounts.Account
		want int64
	}{
		{"happy path", accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 9876543210000)}, 9876543210000},
		{"zero value account", accounts.Account{}, 0},
		{"malformed raw blob", accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredExpiresAt(tt.acct); got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
