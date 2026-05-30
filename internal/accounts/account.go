// Package accounts provides a provider-neutral persisted account store.
// Each Account holds opaque credential JSON (RawBlob) plus the identity
// fields shared across providers. Token parsing is provider-specific and
// lives in the respective provider package (e.g. internal/providers/claude).
package accounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Account is a persisted provider identity. RawBlob is the verbatim credential
// JSON from the provider's live store; it is written back byte-for-byte by
// `aistat switch` so unknown fields are never dropped.
type Account struct {
	UUID          string          `json:"uuid"`
	Email         string          `json:"email"`
	DisplayName   string          `json:"display_name"`
	RateLimitTier string          `json:"rate_limit_tier"`
	LastSeenAt    time.Time       `json:"last_seen_at"`
	// RawBlob is the full credential JSON blob as read from the provider's live
	// store. `aistat switch` writes this blob back verbatim.
	RawBlob json.RawMessage `json:"raw_blob"`
}

// NewAccount constructs an Account from a raw credential JSON blob plus the
// identity fields resolved via the provider's profile endpoint. To avoid a
// cycle with provider packages, the constructor takes discrete identity strings
// rather than a provider-specific profile struct.
//
// Returns an error if raw is empty, not valid JSON, or uuid is empty.
func NewAccount(raw json.RawMessage, uuid, email, displayName, rateLimitTier string, now time.Time) (Account, error) {
	if len(raw) == 0 {
		return Account{}, errors.New("accounts: raw credential blob is empty")
	}
	if !json.Valid(raw) {
		return Account{}, fmt.Errorf("accounts: raw credential is not valid JSON")
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
