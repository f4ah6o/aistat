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
// decoded from the exp claim of tokens.access_token in a.RawBlob. Returns 0 if
// the access token is absent or not a JWT carrying an exp claim — in which case
// the caller performs no proactive refresh and relies on the usage call (an
// expired-but-opaque token surfaces via the usage endpoint's 401, which maps to
// an actionable `codex login` hint). The OIDC id_token is short-lived and is
// NOT used here — it is identity-only (sub/email); see reconcile.go.
func StoredExpiresAt(a accounts.Account) int64 {
	if expSec, ok := cred.ParseJWTExp(StoredAccessToken(a)); ok {
		return expSec * 1000
	}
	return 0
}
