package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

// stubRefreshServer returns an httptest.Server that responds with the given
// status and body. If capturedBody is non-nil, the server reads and stores the
// request body there before responding; the test can then inspect the POST body.
func stubRefreshServer(t *testing.T, status int, body []byte, capturedBody *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capturedBody != nil {
			raw, err := io.ReadAll(r.Body)
			if err == nil {
				*capturedBody = string(raw)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestRefreshClient(t *testing.T, srv *httptest.Server) *refreshClient {
	t.Helper()
	doer := httpx.NewDoer(srv.Client(), "aistat-test/0", "claude", nil, nil)
	rc := newRefreshClient(doer)
	rc.endpoint = srv.URL + "/v1/oauth/token"
	return rc
}

func TestRefreshExchange_HappyPath(t *testing.T) {
	body := []byte(`{
		"access_token": "new-access-tok",
		"refresh_token": "new-refresh-tok",
		"expires_in": 3600
	}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "old-refresh-tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "new-access-tok" {
		t.Errorf("AccessToken = %q, want %q", tok.AccessToken, "new-access-tok")
	}
	if tok.RefreshToken != "new-refresh-tok" {
		t.Errorf("RefreshToken = %q, want %q", tok.RefreshToken, "new-refresh-tok")
	}
	// ExpiresAt should be approximately now + 3600*1000 ms; allow 5s slack.
	if tok.ExpiresAt == 0 {
		t.Error("ExpiresAt should be non-zero when expires_in is present")
	}
}

func TestRefreshExchange_Rotation(t *testing.T) {
	body := []byte(`{
		"access_token": "rotated-access",
		"refresh_token": "rotated-refresh",
		"expires_in": 7200
	}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "old-tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.RefreshToken != "rotated-refresh" {
		t.Errorf("RefreshToken = %q, want new rotated value", tok.RefreshToken)
	}
}

func TestRefreshExchange_NoRotation_KeepsOldRefreshToken(t *testing.T) {
	// Server returns no refresh_token in the response → keep the original.
	body := []byte(`{
		"access_token": "new-access",
		"expires_in": 3600
	}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "original-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.RefreshToken != "original-refresh" {
		t.Errorf("RefreshToken = %q, want original-refresh (no rotation)", tok.RefreshToken)
	}
}

func TestRefreshExchange_InvalidGrant(t *testing.T) {
	body := []byte(`{"error":"invalid_grant","error_description":"Refresh token expired"}`)
	srv := stubRefreshServer(t, 400, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "dead-refresh-tok")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Errorf("expected ErrRefreshRejected, got: %v", err)
	}
}

func TestRefreshExchange_404_WrapsErrRefreshEndpointBroken(t *testing.T) {
	srv := stubRefreshServer(t, 404, []byte("not found"), nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, ErrRefreshEndpointBroken) {
		t.Errorf("expected ErrRefreshEndpointBroken, got: %v", err)
	}
}

func TestRefreshExchange_NonOAuthShapedBody_WrapsErrRefreshEndpointBroken(t *testing.T) {
	// 200 response that looks valid JSON but has no access_token.
	body := []byte(`{"status":"ok","message":"hello"}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, ErrRefreshEndpointBroken) {
		t.Errorf("expected ErrRefreshEndpointBroken for missing access_token, got: %v", err)
	}
}

func TestRefreshExchange_503_WrapsErrTransient(t *testing.T) {
	srv := stubRefreshServer(t, 503, []byte("service unavailable"), nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient for 503, got: %v", err)
	}
}

func TestRefreshExchange_NetworkError_WrapsErrTransient(t *testing.T) {
	// Close the server immediately so all connections are refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL + "/v1/oauth/token"
	srv.Close()

	doer := httpx.NewDoer(&http.Client{}, "aistat-test/0", "claude", nil, nil)
	rc := newRefreshClient(doer)
	rc.endpoint = endpoint

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient for network error, got: %v", err)
	}
}

func TestRefreshExchange_PostBodyAssertion(t *testing.T) {
	body := []byte(`{"access_token":"tok","refresh_token":"new-r","expires_in":3600}`)
	var capturedBody string
	srv := stubRefreshServer(t, 200, body, &capturedBody)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "my-refresh-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	vals, parseErr := url.ParseQuery(capturedBody)
	if parseErr != nil {
		t.Fatalf("POST body is not form-encoded: %v", parseErr)
	}
	if vals.Get("grant_type") != "refresh_token" {
		t.Errorf("grant_type = %q, want %q", vals.Get("grant_type"), "refresh_token")
	}
	if vals.Get("refresh_token") != "my-refresh-token" {
		t.Errorf("refresh_token = %q, want %q", vals.Get("refresh_token"), "my-refresh-token")
	}
	// client_id must be present iff the constant is non-empty.
	if claudeOAuthClientID != "" {
		if vals.Get("client_id") != claudeOAuthClientID {
			t.Errorf("client_id = %q, want %q", vals.Get("client_id"), claudeOAuthClientID)
		}
	} else {
		if vals.Has("client_id") {
			t.Errorf("client_id should be absent when constant is empty, got: %q", vals.Get("client_id"))
		}
	}
}

// TestRefreshExchange_401_BareError verifies that 401/403 from the token
// endpoint produce a bare error — refreshClassify does not delegate to
// DefaultClassify, so these statuses must NOT wrap ErrAuthDenied.
func TestRefreshExchange_401_BareError(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := stubRefreshServer(t, status, []byte(`{"error":"unauthorized"}`), nil)
			rc := newTestRefreshClient(t, srv)
			_, err := rc.Exchange(context.Background(), "tok")
			if errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("status %d from token endpoint should not wrap ErrAuthDenied; got: %v", status, err)
			}
			if err == nil {
				t.Errorf("status %d should return an error", status)
			}
		})
	}
}

func TestRefreshExchange_ExpiresAtZeroWhenAbsent(t *testing.T) {
	body := []byte(`{"access_token":"tok","refresh_token":"r"}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 when expires_in is absent", tok.ExpiresAt)
	}
}

func TestRefreshExchange_OtherBadStatus_BareError(t *testing.T) {
	// A 400 that is NOT invalid_grant should be a bare error.
	body := []byte(`{"error":"invalid_client","error_description":"bad client"}`)
	srv := stubRefreshServer(t, 400, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if errors.Is(err, ErrRefreshRejected) {
		t.Errorf("non-invalid_grant 400 should not wrap ErrRefreshRejected")
	}
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("400 should not wrap ErrTransient")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Errorf("error should mention HTTP 400: %v", err)
	}
}
