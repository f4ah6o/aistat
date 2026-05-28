package claude

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/drogers0/aistat/v2/internal/httpx"
)

const (
	profileEndpoint = "https://api.anthropic.com/api/oauth/profile"
	profileTimeout  = 3 * time.Second
)

// ErrProfileMissingFields is returned when the profile endpoint responds with
// HTTP 200 but the required account.uuid or account.email fields are empty.
// The caller (reconcile path, D1 step 4) maps this to the distinct diagnostic:
// "aistat: claude: profile response missing required fields (account.uuid/account.email);
// rendering live row without storing; file an issue at https://github.com/drogers0/aistat/issues".
var ErrProfileMissingFields = errors.New("profile response missing required fields (account.uuid/account.email)")

// Profile holds the identity fields extracted from GET /api/oauth/profile.
type Profile struct {
	AccountUUID   string
	Email         string
	DisplayName   string
	RateLimitTier string // empty on personal accounts (no organization block)
}

// profileWire is the JSON shape returned by GET /api/oauth/profile.
// Only the fields aistat consumes are declared; extras are silently ignored.
type profileWire struct {
	Account struct {
		UUID        string `json:"uuid"`
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"account"`
	// Organization is absent on personal accounts.
	Organization *struct {
		RateLimitTier string `json:"rate_limit_tier"`
	} `json:"organization"`
}

type profileClient struct {
	doer     *httpx.Doer
	endpoint string
	timeout  time.Duration
}

func newProfileClient(doer *httpx.Doer) *profileClient {
	return &profileClient{
		doer:     doer,
		endpoint: profileEndpoint,
		timeout:  profileTimeout,
	}
}

// Get fetches the profile for the given access token.
// Error classification:
//   - 401/403 → wraps providers.ErrAuthDenied (via httpx.DefaultClassify)
//   - 408/429/5xx → wraps providers.ErrTransient (via httpx.DefaultClassify)
//   - Other 4xx → bare error
//   - HTTP 200 with empty account.uuid or account.email → ErrProfileMissingFields
func (p *profileClient) Get(ctx context.Context, accessToken string) (Profile, error) {
	var wire profileWire
	if err := p.doer.GetJSON(ctx, p.endpoint, accessToken, p.timeout, &wire, httpx.DefaultClassify); err != nil {
		return Profile{}, err
	}
	if wire.Account.UUID == "" || wire.Account.Email == "" {
		return Profile{}, fmt.Errorf("%w: got uuid=%q email=%q", ErrProfileMissingFields, wire.Account.UUID, wire.Account.Email)
	}
	prof := Profile{
		AccountUUID: wire.Account.UUID,
		Email:       wire.Account.Email,
		DisplayName: wire.Account.DisplayName,
	}
	if wire.Organization != nil {
		prof.RateLimitTier = wire.Organization.RateLimitTier
	}
	return prof, nil
}
