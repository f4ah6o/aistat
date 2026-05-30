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

func TestStoredAccessToken_HappyPath(t *testing.T) {
	a := accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 1000)}
	if got := StoredAccessToken(a); got != "at-abc" {
		t.Errorf("got %q, want %q", got, "at-abc")
	}
}

func TestStoredRefreshToken_HappyPath(t *testing.T) {
	a := accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 1000)}
	if got := StoredRefreshToken(a); got != "rt-xyz" {
		t.Errorf("got %q, want %q", got, "rt-xyz")
	}
}

func TestStoredExpiresAt_HappyPath(t *testing.T) {
	a := accounts.Account{RawBlob: makeRawBlob("at-abc", "rt-xyz", 9876543210000)}
	if got := StoredExpiresAt(a); got != 9876543210000 {
		t.Errorf("got %d, want 9876543210000", got)
	}
}

func TestStoredAccessToken_MissingClaudeAiOauth(t *testing.T) {
	a := accounts.Account{RawBlob: json.RawMessage(`{"otherField":"value"}`)}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("missing claudeAiOauth: got %q, want empty", got)
	}
}

func TestStoredAccessToken_EmptyAccessToken(t *testing.T) {
	a := accounts.Account{RawBlob: json.RawMessage(`{"claudeAiOauth":{"accessToken":""}}`)}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("empty accessToken: got %q, want empty", got)
	}
}

func TestStoredAccessToken_ZeroValueAccount(t *testing.T) {
	var a accounts.Account
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("zero-value account: got %q, want empty", got)
	}
}

func TestStoredRefreshToken_ZeroValueAccount(t *testing.T) {
	var a accounts.Account
	if got := StoredRefreshToken(a); got != "" {
		t.Errorf("zero-value account: got %q, want empty", got)
	}
}

func TestStoredExpiresAt_ZeroValueAccount(t *testing.T) {
	var a accounts.Account
	if got := StoredExpiresAt(a); got != 0 {
		t.Errorf("zero-value account: got %d, want 0", got)
	}
}

func TestStoredAccessToken_MalformedRawBlob(t *testing.T) {
	a := accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}
	if got := StoredAccessToken(a); got != "" {
		t.Errorf("malformed blob: got %q, want empty", got)
	}
}

func TestStoredRefreshToken_MalformedRawBlob(t *testing.T) {
	a := accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}
	if got := StoredRefreshToken(a); got != "" {
		t.Errorf("malformed blob: got %q, want empty", got)
	}
}

func TestStoredExpiresAt_MalformedRawBlob(t *testing.T) {
	a := accounts.Account{RawBlob: json.RawMessage(`{not valid json`)}
	if got := StoredExpiresAt(a); got != 0 {
		t.Errorf("malformed blob: got %d, want 0", got)
	}
}
