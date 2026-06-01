package codex

import (
	"os"
	"testing"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

func loadFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("loading fixture %s: %v", name, err)
	}
	return data
}

func TestStoredAccessToken(t *testing.T) {
	tests := []struct {
		name    string
		account accounts.Account
		want    string
	}{
		{"present", accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}, "test-access-token-abc"},
		{"empty", accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":""}}`)}, ""},
		{"malformed json", accounts.Account{RawBlob: []byte(`{ not json }`)}, ""},
		{"nil raw blob", accounts.Account{RawBlob: nil}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredAccessToken(tt.account); got != tt.want {
				t.Errorf("StoredAccessToken = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoredRefreshToken(t *testing.T) {
	tests := []struct {
		name    string
		account accounts.Account
		want    string
	}{
		{"present", accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}, "test-refresh-token-xyz"},
		{"absent", accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok"}}`)}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredRefreshToken(tt.account); got != tt.want {
				t.Errorf("StoredRefreshToken = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoredExpiresAt(t *testing.T) {
	tests := []struct {
		name    string
		account accounts.Account
		want    int64
	}{
		// The fixture's id_token has exp=9999999999; ExpiresAt = exp*1000.
		{"present", accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}, 9999999999000},
		{"no id_token", accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok","refresh_token":"ref"}}`)}, 0},
		{"malformed id_token", accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok","id_token":"bad"}}`)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredExpiresAt(tt.account); got != tt.want {
				t.Errorf("StoredExpiresAt = %d, want %d", got, tt.want)
			}
		})
	}
}
