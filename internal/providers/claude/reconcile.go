package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/providers"
)

// ReconcileInput is the full input set for Reconcile and ResolveActiveUUID.
//
// LookupProfile is called with the live access token when no byte-match is
// found. It must not be nil when LiveBlob is non-nil. The ctx that scopes the
// profile call lives at the callsite (e.g. Fetch); the callback intentionally
// omits ctx so that Reconcile remains a pure function with no I/O of its own.
type ReconcileInput struct {
	LiveBlob      *cred.Credential                      // nil if absent; Raw holds exact live bytes
	Stored        []accounts.Account
	LookupProfile func(accessToken string) (Profile, error)
	Now           time.Time
}

// ReconcileOutput is the result of a Reconcile call.
type ReconcileOutput struct {
	Accounts     []accounts.Account
	ActiveUUID   string           // "" if none
	CaptureWarn  string           // non-empty when fallback applied (D1 step 4)
	Inserted     bool             // true if a new account slot was created
	Upserted     bool             // true if an existing slot was updated
	LiveUnstored *cred.Credential // non-nil when fallback applied; render-only
}

// Reconcile executes the full D1 auto-capture decision tree over the live
// credential and stored account slots. It is a pure function — all I/O
// (keychain read, profile HTTP call) is pre-resolved and passed via in.
//
// The caller is responsible for persisting out.Accounts when out.Inserted or
// out.Upserted is true.
//
// Panics if in.LookupProfile is nil and the live access token does not
// byte-match any stored slot (findActive will call LookupProfile).
func Reconcile(in ReconcileInput) ReconcileOutput {
	out := ReconcileOutput{
		Accounts: make([]accounts.Account, len(in.Stored)),
	}
	// Shallow copy: Account is a value type but RawBlob (json.RawMessage = []byte)
	// is a reference type. The copy produces independent slice headers; however
	// the underlying byte arrays of unmodified RawBlob values are shared with
	// in.Stored. The invariant is "assign, never index-write into an existing
	// RawBlob": every code path that changes RawBlob assigns a fresh slice
	// (json.RawMessage(in.LiveBlob.Raw)), it never appends to or mutates the
	// existing slice in place.
	copy(out.Accounts, in.Stored)

	if in.LiveBlob == nil {
		// D1 branch 3: no live credential; no slot is active this run.
		return out
	}

	matchIdx, prof, profileErr := findActive(in)

	switch {
	case matchIdx >= 0:
		// D1 branch 1: byte-match. Upsert blob fields without a profile call.
		// The Claude CLI may rotate refreshToken/expiresAt without changing the
		// access token, so we always overwrite RawBlob and LastSeenAt.
		slot := out.Accounts[matchIdx]
		slot.RawBlob = json.RawMessage(in.LiveBlob.Raw)
		slot.LastSeenAt = in.Now
		out.Accounts[matchIdx] = slot
		out.ActiveUUID = slot.UUID
		out.Upserted = true

	case profileErr != nil:
		// D1 branch 4: profile failure. Render an unstored live row instead of
		// storing; the caller emits CaptureWarn to stderr.
		out.LiveUnstored = in.LiveBlob
		if errors.Is(profileErr, ErrProfileMissingFields) {
			out.CaptureWarn = "aistat: claude: profile response missing required fields (account.uuid/account.email); rendering live row without storing; file an issue at https://github.com/drogers0/aistat/issues"
		} else {
			out.CaptureWarn = fmt.Sprintf(
				"aistat: claude: could not capture live account profile (%s); rendering live row without storing — run `claude /login` if this persists across runs",
				profileErr.Error(),
			)
		}

	default:
		// D1 branch 2: profile success. D9 identity-drift: UUID wins; overwrite
		// email/display_name/rate_limit_tier if they changed.
		for i, acct := range out.Accounts {
			if acct.UUID == prof.AccountUUID {
				slot := out.Accounts[i]
				slot.Email = prof.Email
				slot.DisplayName = prof.DisplayName
				slot.RateLimitTier = prof.RateLimitTier
				slot.RawBlob = json.RawMessage(in.LiveBlob.Raw)
				slot.LastSeenAt = in.Now
				out.Accounts[i] = slot
				out.ActiveUUID = prof.AccountUUID
				out.Upserted = true
				return out
			}
		}
		// No UUID match — insert a new slot keyed by account.uuid.
		out.Accounts = append(out.Accounts, accounts.Account{
			UUID:          prof.AccountUUID,
			Email:         prof.Email,
			DisplayName:   prof.DisplayName,
			RateLimitTier: prof.RateLimitTier,
			LastSeenAt:    in.Now,
			RawBlob:       json.RawMessage(in.LiveBlob.Raw),
		})
		out.ActiveUUID = prof.AccountUUID
		out.Inserted = true
	}

	return out
}

// ResolveActiveUUID is the read-only D11 variant used by `accounts remove` and
// `switch`'s identify-current-active step. It shares the byte-match and
// profile-lookup logic with Reconcile via findActive but never inserts or
// upserts. The signature pins the no-write guarantee mechanically — the caller
// cannot accidentally act on Inserted/Upserted because those fields do not
// exist on this return type.
//
// Returns:
//   - (uuid, nil): byte-match succeeded, or profile call returned a UUID.
//   - ("", nil): no live blob, 401/403, or ErrProfileMissingFields — the active
//     account is unresolvable without a recoverable error.
//   - ("", err): transient or other non-auth, non-missing-fields failure that
//     the caller may want to surface.
func ResolveActiveUUID(in ReconcileInput) (string, error) {
	if in.LiveBlob == nil {
		return "", nil
	}

	matchIdx, prof, profileErr := findActive(in)

	if matchIdx >= 0 {
		return in.Stored[matchIdx].UUID, nil
	}

	if profileErr != nil {
		if errors.Is(profileErr, providers.ErrAuthDenied) || errors.Is(profileErr, ErrProfileMissingFields) {
			return "", nil
		}
		return "", profileErr
	}

	return prof.AccountUUID, nil
}

// findActive resolves the active account from the live blob without mutating
// anything in in.Stored. It first scans for a byte-match of the live access
// token; if no match is found it calls LookupProfile.
//
// Returns:
//   - matchIdx >= 0: index into in.Stored of the byte-matched slot (first match
//     wins — deterministic on ordered stored slice). No profile call is made.
//   - matchIdx == -1, profileErr == nil: profile succeeded; prof is populated.
//   - matchIdx == -1, profileErr != nil: profile call failed.
//
// Precondition: in.LiveBlob is non-nil.
func findActive(in ReconcileInput) (matchIdx int, prof Profile, profileErr error) {
	// D1 step 1: byte-match. First match wins (pinned behaviour for the
	// pathological case where two stored slots share an access token).
	for i, acct := range in.Stored {
		if StoredAccessToken(acct) == in.LiveBlob.AccessToken {
			return i, Profile{}, nil
		}
	}

	// D1 step 2: no byte-match — call the profile endpoint.
	p, err := in.LookupProfile(in.LiveBlob.AccessToken)
	if err != nil {
		return -1, Profile{}, err
	}
	return -1, p, nil
}
