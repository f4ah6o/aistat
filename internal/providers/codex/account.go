package codex

import (
	"encoding/json"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/cred"
)

// rawCodexTokens is the minimal Codex auth.json shape for token field extraction
// from an Account's RawBlob. Provider-specific; internal/accounts is neutral.
type rawCodexTokens struct {
	Tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	} `json:"tokens"`
}

// parseStoredRaw parses the Codex-specific token fields from a.RawBlob.
// Returns zero values on empty or malformed raw JSON; never panics.
func parseStoredRaw(a accounts.Account) rawCodexTokens {
	if len(a.RawBlob) == 0 {
		return rawCodexTokens{}
	}
	var r rawCodexTokens
	if err := json.Unmarshal(a.RawBlob, &r); err != nil {
		return rawCodexTokens{}
	}
	return r
}

// StoredAccessToken returns tokens.access_token from a.RawBlob.
// Returns "" if RawBlob is empty, malformed, or the field is absent.
func StoredAccessToken(a accounts.Account) string {
	return parseStoredRaw(a).Tokens.AccessToken
}

// StoredRefreshToken returns tokens.refresh_token from a.RawBlob.
// Returns "" if absent or if RawBlob is malformed.
func StoredRefreshToken(a accounts.Account) string {
	return parseStoredRaw(a).Tokens.RefreshToken
}

// StoredExpiresAt returns the access-token expiry as milliseconds since epoch,
// derived from the exp claim of tokens.id_token in a.RawBlob.
// Returns 0 if id_token is absent, the JWT is malformed, or exp is absent.
func StoredExpiresAt(a accounts.Account) int64 {
	idTok := parseStoredRaw(a).Tokens.IDToken
	if idTok == "" {
		return 0
	}
	_, _, expSec, err := cred.ParseCodexIDToken(idTok)
	if err != nil {
		return 0
	}
	return expSec * 1000
}
