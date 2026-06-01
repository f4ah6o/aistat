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

func TestStoredAccessToken_Present(t *testing.T) {
	a := accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}
	if got := StoredAccessToken(a); got != "test-access-token-abc" {
		t.Errorf("StoredAccessToken = %q, want test-access-token-abc", got)
	}
}

func TestStoredAccessToken_Empty(t *testing.T) {
	a := accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":""}}`)}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("StoredAccessToken = %q, want empty", got)
	}
}

func TestStoredAccessToken_MalformedJSON(t *testing.T) {
	a := accounts.Account{RawBlob: []byte(`{ not json }`)}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("StoredAccessToken = %q, want empty for malformed JSON", got)
	}
}

func TestStoredAccessToken_EmptyRawBlob(t *testing.T) {
	a := accounts.Account{RawBlob: nil}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("StoredAccessToken = %q, want empty for nil RawBlob", got)
	}
}

func TestStoredRefreshToken_Present(t *testing.T) {
	a := accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}
	if got := StoredRefreshToken(a); got != "test-refresh-token-xyz" {
		t.Errorf("StoredRefreshToken = %q, want test-refresh-token-xyz", got)
	}
}

func TestStoredRefreshToken_Absent(t *testing.T) {
	a := accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok"}}`)}
	if got := StoredRefreshToken(a); got != "" {
		t.Errorf("StoredRefreshToken = %q, want empty when absent", got)
	}
}

func TestStoredExpiresAt_Present(t *testing.T) {
	// The fixture's id_token has exp=9999999999; ExpiresAt = exp*1000.
	a := accounts.Account{RawBlob: loadFixtureBytes(t, "auth.json")}
	want := int64(9999999999000)
	if got := StoredExpiresAt(a); got != want {
		t.Errorf("StoredExpiresAt = %d, want %d", got, want)
	}
}

func TestStoredExpiresAt_NoIDToken(t *testing.T) {
	a := accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok","refresh_token":"ref"}}`)}
	if got := StoredExpiresAt(a); got != 0 {
		t.Errorf("StoredExpiresAt = %d, want 0 when id_token absent", got)
	}
}

func TestStoredExpiresAt_MalformedIDToken(t *testing.T) {
	a := accounts.Account{RawBlob: []byte(`{"tokens":{"access_token":"tok","id_token":"bad"}}`)}
	if got := StoredExpiresAt(a); got != 0 {
		t.Errorf("StoredExpiresAt = %d, want 0 for malformed id_token", got)
	}
}
