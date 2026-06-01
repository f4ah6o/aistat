package claude

import (
	"encoding/json"

	"github.com/drogers0/aistat/v2/internal/accounts"
)

// rawOAuth is the minimal Claude credential shape needed to extract token fields
// from an Account's RawBlob. This type stays in the Claude package because token
// parsing is Claude-specific; internal/accounts is provider-neutral.
type rawOAuth struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

// parseStoredRaw parses the Claude-specific OAuth fields from a.RawBlob.
// Returns zero values on empty or malformed raw JSON; never panics.
func parseStoredRaw(a accounts.Account) rawOAuth {
	if len(a.RawBlob) == 0 {
		return rawOAuth{}
	}
	var r rawOAuth
	if err := json.Unmarshal(a.RawBlob, &r); err != nil {
		return rawOAuth{}
	}
	return r
}

// StoredAccessToken returns claudeAiOauth.accessToken from a.RawBlob.
// Returns "" if RawBlob is empty, malformed, or missing the access token field.
func StoredAccessToken(a accounts.Account) string {
	return parseStoredRaw(a).ClaudeAiOauth.AccessToken
}

// StoredRefreshToken returns claudeAiOauth.refreshToken from a.RawBlob.
// Returns "" if absent or if RawBlob is malformed.
func StoredRefreshToken(a accounts.Account) string {
	return parseStoredRaw(a).ClaudeAiOauth.RefreshToken
}

// StoredExpiresAt returns claudeAiOauth.expiresAt (ms since epoch) from a.RawBlob.
// Returns 0 if absent or if RawBlob is malformed.
func StoredExpiresAt(a accounts.Account) int64 {
	return parseStoredRaw(a).ClaudeAiOauth.ExpiresAt
}
