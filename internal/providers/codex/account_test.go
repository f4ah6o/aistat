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
	// blob builds a Codex auth.json with the given access_token (raw string).
	blob := func(accessToken string) []byte {
		return []byte(`{"tokens":{"access_token":"` + accessToken + `","refresh_token":"ref"}}`)
	}
	tests := []struct {
		name    string
		account accounts.Account
		want    int64
	}{
		// Expiry is decoded from the access_token JWT's exp claim, not the id_token.
		{"access token jwt far future", accounts.Account{RawBlob: blob(accessJWT("tok", 9999999999))}, 9999999999000},
		// Past exp still yields a positive value (>0) so the gate still fires for
		// genuine expiry — the guard against silently disabling refresh.
		{"access token jwt past exp", accounts.Account{RawBlob: blob(accessJWT("tok", 1000000000))}, 1000000000000},
		{"access token opaque non-jwt", accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}, 0},
		{"access token absent", accounts.Account{RawBlob: []byte(`{"tokens":{"refresh_token":"ref"}}`)}, 0},
		// id_token carries an exp but access_token is opaque → 0: proves the
		// id_token is no longer consulted (direct regression guard for the bug).
		{"id token has exp but access opaque", accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"opaque","id_token":"` + syntheticIDToken("s", "e@x.com", 9999999999) + `"}}`)}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StoredExpiresAt(tt.account); got != tt.want {
				t.Errorf("StoredExpiresAt = %d, want %d", got, tt.want)
			}
		})
	}
}
