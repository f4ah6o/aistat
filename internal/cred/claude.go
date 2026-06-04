package cred

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const ClaudeTokenMissingMessage = "claude token not found — run `claude /login` to authenticate"

var ErrClaudeTokenNotFound = errors.New(ClaudeTokenMissingMessage)

// ErrClaudeWriteUnsupported is returned by WriteClaudeLiveBlob on platforms
// that do not support writing the Claude live credential (anything other than
// macOS or Linux).
var ErrClaudeWriteUnsupported = errors.New("writing Claude live credential is not supported on this platform")

// Credential holds the parsed fields of a provider's live credential blob plus
// the exact input bytes. Raw preserves the original payload byte-for-byte so
// callers can pass it onward (e.g. to WriteClaudeLiveBlob or accounts.Account)
// without re-marshaling and without risking field loss.
//
// Immutability convention: Raw is set once by parseClaudeCredFull via
// bytes.Clone and must not be mutated afterward. Callers that need to pass Raw
// to a writer should treat it as read-only.
type Credential struct {
	AccessToken  string
	RefreshToken string
	// ExpiresAt is ms since epoch; 0 if absent. Claude only — populated from the
	// claudeAiOauth.expiresAt field. Codex leaves this 0 (its auth.json has no
	// expiry field); the codex refresh gate decodes the access-token JWT exp on
	// demand in codex.StoredExpiresAt.
	ExpiresAt int64
	Raw       []byte
}

// parseClaudeCredFull parses the JSON payload used by both the macOS Keychain
// item ("Claude Code-credentials") and the Linux ~/.claude/.credentials.json
// file. It validates that the access token is present and copies the input
// bytes verbatim into Credential.Raw via bytes.Clone so subsequent mutations
// of the caller's buffer cannot corrupt the stored copy.
//
// Returns ErrClaudeTokenNotFound when the access token field is empty or
// absent. RefreshToken and ExpiresAt are optional; they are zero on absence.
func parseClaudeCredFull(data []byte) (Credential, error) {
	var raw struct {
		ClaudeAiOauth struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Credential{}, fmt.Errorf("claude credential is not valid JSON: %w", err)
	}
	if raw.ClaudeAiOauth.AccessToken == "" {
		return Credential{}, ErrClaudeTokenNotFound
	}
	return Credential{
		AccessToken:  raw.ClaudeAiOauth.AccessToken,
		RefreshToken: raw.ClaudeAiOauth.RefreshToken,
		ExpiresAt:    raw.ClaudeAiOauth.ExpiresAt,
		Raw:          bytes.Clone(data),
	}, nil
}

// parseClaudeCred extracts the OAuth access token from the JSON payload used
// by both the macOS Keychain item ("Claude Code-credentials") and the Linux
// ~/.claude/.credentials.json file. Returns ErrClaudeTokenNotFound when the
// token field is empty or absent.
func parseClaudeCred(data []byte) (string, error) {
	c, err := parseClaudeCredFull(data)
	return c.AccessToken, err
}
