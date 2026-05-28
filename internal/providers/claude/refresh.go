package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

// claudeTokenEndpoint is Anthropic's OAuth token endpoint for the Claude CLI.
// Discovered via `strings $(which claude) | grep TOKEN_URL` on Darwin 24.6.0 (macOS 15 Sequoia,
// claude binary): the production config block contains
// `TOKEN_URL:"https://platform.claude.com/v1/oauth/token"`.
//
// Implementation note: the Claude CLI POSTs a JSON body (Content-Type:
// application/json) with fields {refresh_token, client_id, scope} — no
// grant_type field. aistat follows RFC 6749 §6 and sends
// application/x-www-form-urlencoded with grant_type=refresh_token. If the
// server rejects the form-encoded body (e.g. 400 "unsupported content-type"
// or 415), refreshClassify returns a bare HTTP error (not ErrRefreshEndpointBroken).
// ErrRefreshEndpointBroken is reserved for 404 and 200 responses that lack
// an access_token — both indicate the endpoint URL or wire shape is wrong.
const claudeTokenEndpoint = "https://platform.claude.com/v1/oauth/token"

// claudeOAuthClientID is the OAuth client_id for the Claude Code application.
// Discovered via `strings $(which claude) | grep CLIENT_ID` on Darwin 24.6.0 (macOS 15 Sequoia,
// claude binary): `CLIENT_ID:"9d1c250a-e61b-44d9-88ed-5944d1962f5e"`.
// The refresh function in the bundle sends it as `client_id:r8().CLIENT_ID`
// alongside the refresh_token, confirming it is required for token refresh.
const claudeOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

const refreshTimeout = 5 * time.Second

// ErrRefreshRejected is returned when the token endpoint responds with
// {"error":"invalid_grant"} — the stored refresh token is expired or revoked.
// The caller (Step 9, usage path) maps this to a "re-login this account" message.
var ErrRefreshRejected = errors.New("refresh token rejected (invalid_grant)")

// ErrRefreshEndpointBroken is returned when the token endpoint returns HTTP 404
// or a 200 with a non-OAuth-shaped body (e.g. missing access_token). This
// indicates an aistat implementation issue, not an account problem. The caller
// surfaces:
// "aistat: claude: refresh endpoint rejected request (<status>: <body-snip>);
// this is likely an aistat refresh implementation issue, not your account.
// Run 'claude /login' to work around it for this account and file an issue
// at https://github.com/drogers0/aistat/issues".
var ErrRefreshEndpointBroken = errors.New("refresh endpoint broken or returned non-OAuth response")

// Token is the result of a successful token refresh.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64 // ms since epoch; 0 if expires_in was absent in the response
}

// tokenWire is the JSON shape returned by the token endpoint on HTTP 200.
type tokenWire struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // seconds; 0 if absent
}

// tokenErrorWire is the JSON shape returned by the token endpoint on error.
type tokenErrorWire struct {
	Error string `json:"error"`
}

type refreshClient struct {
	doer     *httpx.Doer
	endpoint string
	timeout  time.Duration
	clientID string         // "" if not required by the endpoint
	now      func() time.Time
}

func newRefreshClient(doer *httpx.Doer) *refreshClient {
	return &refreshClient{
		doer:     doer,
		endpoint: claudeTokenEndpoint,
		timeout:  refreshTimeout,
		clientID: claudeOAuthClientID,
		now:      time.Now,
	}
}

// Exchange exchanges a refresh token for a new access token.
// On success, Token.ExpiresAt is computed from now + expires_in*1000.
// Token.RefreshToken holds the new token if the server rotated it, or the
// original refreshToken if the server did not return a new one.
func (r *refreshClient) Exchange(ctx context.Context, refreshToken string) (Token, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if r.clientID != "" {
		values.Set("client_id", r.clientID)
	}

	var wire tokenWire
	if err := r.doer.PostForm(ctx, r.endpoint, values, r.timeout, &wire, refreshClassify); err != nil {
		return Token{}, err
	}

	// A 200 with an empty access_token means the response body is not an
	// OAuth token response — treat as an implementation-drift condition.
	if wire.AccessToken == "" {
		return Token{}, fmt.Errorf("%w: HTTP 200 response from %s is missing access_token", ErrRefreshEndpointBroken, r.endpoint)
	}

	tok := Token{
		AccessToken:  wire.AccessToken,
		RefreshToken: wire.RefreshToken,
	}
	if tok.RefreshToken == "" {
		// Server did not rotate the refresh token — carry the original forward.
		tok.RefreshToken = refreshToken
	}
	if wire.ExpiresIn > 0 {
		tok.ExpiresAt = r.now().UnixMilli() + wire.ExpiresIn*1000
	}
	return tok, nil
}

// refreshClassify maps token-endpoint non-200 responses to errors.
// invalid_grant → ErrRefreshRejected; 404 → ErrRefreshEndpointBroken;
// transient statuses → ErrTransient; other 4xx → bare error.
func refreshClassify(endpointURL string, resp *http.Response, body []byte) error {
	status := resp.StatusCode
	if status == 400 {
		var errResp tokenErrorWire
		if json.Unmarshal(body, &errResp) == nil && errResp.Error == "invalid_grant" {
			return fmt.Errorf("%w: HTTP 400 from %s: %s", ErrRefreshRejected, endpointURL, httpx.Snip(body))
		}
	}
	if status == 404 {
		return fmt.Errorf("%w: HTTP 404 from %s: %s", ErrRefreshEndpointBroken, endpointURL, httpx.Snip(body))
	}
	if status == 408 || status == 429 || status >= 500 {
		return fmt.Errorf("%w: HTTP %d from %s: %s", providers.ErrTransient, status, endpointURL, httpx.Snip(body))
	}
	return fmt.Errorf("HTTP %d from %s: %s", status, endpointURL, httpx.Snip(body))
}
