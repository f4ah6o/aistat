// Package accounts manages the persisted Claude account store. Each stored
// Account corresponds to one Claude identity the user has authenticated with;
// the raw credential blob is preserved byte-for-byte so aistat switch can
// restore it to Claude's live store without loss.
package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Account is a persisted Claude identity. Token fields are derived from
// RawBlob on demand; they are never stored as separate fields so that RawBlob
// is always the single source of truth.
type Account struct {
	UUID          string          `json:"uuid"`
	Email         string          `json:"email"`
	DisplayName   string          `json:"display_name"`
	RateLimitTier string          `json:"rate_limit_tier"`
	LastSeenAt    time.Time       `json:"last_seen_at"`
	// RawBlob is the full credential JSON blob as read from Claude's live store
	// (top-level object, including `claudeAiOauth` and `organizationUuid`).
	// Writing only the nested object would break Claude. `aistat switch` writes
	// this blob back verbatim.
	RawBlob json.RawMessage `json:"raw_blob"`
}

// rawOAuth is the minimal shape parsed from RawBlob for token access.
type rawOAuth struct {
	ClaudeAiOauth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	} `json:"claudeAiOauth"`
}

func (a Account) parseRaw() rawOAuth {
	if len(a.RawBlob) == 0 {
		return rawOAuth{}
	}
	var r rawOAuth
	if err := json.Unmarshal(a.RawBlob, &r); err != nil {
		return rawOAuth{}
	}
	return r
}

// AccessToken parses RawBlob and returns claudeAiOauth.accessToken.
// Returns "" if RawBlob is malformed or empty (callers treat this as
// equivalent to "no credential," same as cred.ErrClaudeTokenNotFound).
func (a Account) AccessToken() string { return a.parseRaw().ClaudeAiOauth.AccessToken }

// RefreshToken parses RawBlob and returns claudeAiOauth.refreshToken.
// Returns "" if absent or if RawBlob is malformed.
func (a Account) RefreshToken() string { return a.parseRaw().ClaudeAiOauth.RefreshToken }

// ExpiresAt parses RawBlob and returns claudeAiOauth.expiresAt (ms since epoch).
// Returns 0 if absent or if RawBlob is malformed.
func (a Account) ExpiresAt() int64 { return a.parseRaw().ClaudeAiOauth.ExpiresAt }

// NewAccount constructs an Account from the full Claude live credential JSON
// blob plus the identity fields resolved via /api/oauth/profile. To avoid a
// cycle with internal/providers/claude (which owns Profile), the constructor
// takes the four discrete identity strings rather than a Profile struct.
//
// Returns an error if raw is not valid JSON or lacks a non-empty
// claudeAiOauth.accessToken.
func NewAccount(raw json.RawMessage, uuid, email, displayName, rateLimitTier string, now time.Time) (Account, error) {
	if len(raw) == 0 {
		return Account{}, errors.New("accounts: raw credential blob is empty")
	}
	var r rawOAuth
	if err := json.Unmarshal(raw, &r); err != nil {
		return Account{}, fmt.Errorf("accounts: raw credential is not valid JSON: %w", err)
	}
	if r.ClaudeAiOauth.AccessToken == "" {
		return Account{}, errors.New("accounts: raw credential missing claudeAiOauth.accessToken")
	}
	if uuid == "" {
		return Account{}, errors.New("accounts: uuid is required")
	}
	return Account{
		UUID:          uuid,
		Email:         email,
		DisplayName:   displayName,
		RateLimitTier: rateLimitTier,
		LastSeenAt:    now,
		RawBlob:       raw,
	}, nil
}
