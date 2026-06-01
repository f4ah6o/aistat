package codex

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

// codexTokenEndpoint is the OAuth token endpoint for the Codex CLI.
//
// Confirmed via binary inspection of the Codex CLI (aarch64-apple-darwin build):
//   strings .../codex-darwin-arm64/vendor/aarch64-apple-darwin/codex/codex | grep "oauth/token"
// Output: "https://auth.openai.com/oauth/token" appears adjacent to the refresh
// flow strings ("access_token", "refresh_token", "client_id", "grant_type").
// Also visible: CODEX_REFRESH_TOKEN_URL_OVERRIDE env var, confirming this is
// the default refresh endpoint.
const codexTokenEndpoint = "https://auth.openai.com/oauth/token"

// codexOAuthClientID is the OAuth client_id for the Codex CLI application.
//
// Confirmed via binary inspection of the Codex CLI (aarch64-apple-darwin build):
//   strings .../codex-darwin-arm64/vendor/aarch64-apple-darwin/codex/codex | grep "app_E"
// Output: "app_EMoamEEZ73f0CkXaXp7hrann" appears in the POST body format string
// alongside "access_token", "refresh_token", "client_id", "grant_type".
// Also verified from the live access_token JWT payload field "client_id".
const codexOAuthClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const codexRefreshTimeout = 5 * time.Second

// ErrRefreshRejected is returned when the token endpoint responds with
// {"error":"invalid_grant"} — the stored refresh token is expired or revoked.
// The caller (T3 reconcile) maps this to a "re-login this account" message.
var ErrRefreshRejected = errors.New("refresh token rejected (invalid_grant)")

// ErrRefreshEndpointBroken is returned when the token endpoint returns HTTP 404
// or a 200 with a non-OAuth-shaped body. Indicates an aistat implementation
// issue, not an account problem.
var ErrRefreshEndpointBroken = errors.New("refresh endpoint broken or returned non-OAuth response")

// Token is the result of a successful Codex token refresh.
type Token struct {
	AccessToken  string
	RefreshToken string
	IDToken      string // new id_token if rotated; "" if server did not return one
	ExpiresAt    int64  // ms since epoch; 0 if expires_in was absent
}

// tokenWire is the JSON shape returned by the Codex token endpoint on HTTP 200.
type tokenWire struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
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
	clientID string
	now      func() time.Time
}

func newRefreshClient(doer *httpx.Doer) *refreshClient {
	return &refreshClient{
		doer:     doer,
		endpoint: codexTokenEndpoint,
		timeout:  codexRefreshTimeout,
		clientID: codexOAuthClientID,
		now:      time.Now,
	}
}

// Exchange exchanges a refresh token for a new access token.
// On success, Token.ExpiresAt is now + expires_in*1000.
// Token.RefreshToken holds the new token if the server rotated it, or the
// original refreshToken otherwise. Token.IDToken is "" if the server did not
// return a new id_token.
func (r *refreshClient) Exchange(ctx context.Context, refreshToken string) (Token, error) {
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
	}
	if r.clientID != "" {
		values.Set("client_id", r.clientID)
	}

	var wire tokenWire
	if err := r.doer.PostForm(ctx, r.endpoint, values, r.timeout, &wire, codexRefreshClassify); err != nil {
		return Token{}, err
	}

	if wire.AccessToken == "" {
		return Token{}, fmt.Errorf("%w: HTTP 200 response from %s is missing access_token", ErrRefreshEndpointBroken, r.endpoint)
	}

	tok := Token{
		AccessToken:  wire.AccessToken,
		RefreshToken: wire.RefreshToken,
		IDToken:      wire.IDToken,
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken
	}
	if wire.ExpiresIn > 0 {
		tok.ExpiresAt = r.now().UnixMilli() + wire.ExpiresIn*1000
	}
	return tok, nil
}

// codexRefreshClassify maps token-endpoint non-200 responses to errors.
// invalid_grant → ErrRefreshRejected; invalid_client → ErrRefreshEndpointBroken
// (indicates codexOAuthClientID constant is wrong, not an account problem);
// 404 → ErrRefreshEndpointBroken; transient statuses → ErrTransient; other 4xx → bare error.
func codexRefreshClassify(endpointURL string, resp *http.Response, body []byte) error {
	status := resp.StatusCode
	if status == 400 {
		var errResp tokenErrorWire
		if json.Unmarshal(body, &errResp) == nil {
			switch errResp.Error {
			case "invalid_grant":
				return fmt.Errorf("%w: HTTP 400 from %s: %s", ErrRefreshRejected, endpointURL, httpx.Snip(body))
			case "invalid_client":
				// codexOAuthClientID constant is wrong; this is an aistat config problem.
				return fmt.Errorf("%w: HTTP 400 invalid_client from %s — update codexOAuthClientID: %s", ErrRefreshEndpointBroken, endpointURL, httpx.Snip(body))
			}
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
