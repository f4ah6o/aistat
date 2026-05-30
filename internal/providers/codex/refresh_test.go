package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

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
	doer := httpx.NewDoer(srv.Client(), "aistat-test/0", "codex", nil, nil)
	rc := newRefreshClient(doer)
	rc.endpoint = srv.URL + "/oauth/token"
	return rc
}

func TestCodexRefreshExchange_HappyPath(t *testing.T) {
	body := []byte(`{"access_token":"new-access","refresh_token":"new-refresh","id_token":"new-id-tok","expires_in":3600}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)
	before := time.Now().UnixMilli()

	tok, err := rc.Exchange(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.AccessToken != "new-access" {
		t.Errorf("AccessToken = %q, want new-access", tok.AccessToken)
	}
	if tok.RefreshToken != "new-refresh" {
		t.Errorf("RefreshToken = %q, want new-refresh", tok.RefreshToken)
	}
	if tok.IDToken != "new-id-tok" {
		t.Errorf("IDToken = %q, want new-id-tok", tok.IDToken)
	}
	// ExpiresAt must be ≈ now + 3600*1000 ms.
	wantLow := before + 3600*1000
	wantHigh := time.Now().UnixMilli() + 3600*1000
	if tok.ExpiresAt < wantLow || tok.ExpiresAt > wantHigh {
		t.Errorf("ExpiresAt = %d, want in [%d, %d]", tok.ExpiresAt, wantLow, wantHigh)
	}
}

func TestCodexRefreshExchange_NoIDToken(t *testing.T) {
	body := []byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.IDToken != "" {
		t.Errorf("IDToken = %q, want empty when server did not return one", tok.IDToken)
	}
}

func TestCodexRefreshExchange_NoRotation(t *testing.T) {
	body := []byte(`{"access_token":"new-access","expires_in":3600}`)
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

func TestCodexRefreshExchange_InvalidGrant(t *testing.T) {
	body := []byte(`{"error":"invalid_grant","error_description":"Refresh token expired"}`)
	srv := stubRefreshServer(t, 400, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "dead-refresh")
	if !errors.Is(err, ErrRefreshRejected) {
		t.Errorf("expected ErrRefreshRejected, got: %v", err)
	}
}

func TestCodexRefreshExchange_404(t *testing.T) {
	srv := stubRefreshServer(t, 404, []byte("not found"), nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, ErrRefreshEndpointBroken) {
		t.Errorf("expected ErrRefreshEndpointBroken, got: %v", err)
	}
}

func TestCodexRefreshExchange_NonOAuthBody(t *testing.T) {
	body := []byte(`{"status":"ok"}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, ErrRefreshEndpointBroken) {
		t.Errorf("expected ErrRefreshEndpointBroken for missing access_token, got: %v", err)
	}
}

func TestCodexRefreshExchange_503(t *testing.T) {
	srv := stubRefreshServer(t, 503, []byte("service unavailable"), nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient for 503, got: %v", err)
	}
}

func TestCodexRefreshExchange_NetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	endpoint := srv.URL + "/oauth/token"
	srv.Close()

	doer := httpx.NewDoer(&http.Client{}, "aistat-test/0", "codex", nil, nil)
	rc := newRefreshClient(doer)
	rc.endpoint = endpoint

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient for network error, got: %v", err)
	}
}

func TestCodexRefreshExchange_ExpiresAtZero(t *testing.T) {
	body := []byte(`{"access_token":"tok","refresh_token":"r"}`)
	srv := stubRefreshServer(t, 200, body, nil)
	rc := newTestRefreshClient(t, srv)

	tok, err := rc.Exchange(context.Background(), "r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 when expires_in absent", tok.ExpiresAt)
	}
}

func TestCodexRefreshExchange_PostBody(t *testing.T) {
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
		t.Errorf("refresh_token = %q, want my-refresh-token", vals.Get("refresh_token"))
	}
	if codexOAuthClientID != "" {
		if vals.Get("client_id") != codexOAuthClientID {
			t.Errorf("client_id = %q, want %q", vals.Get("client_id"), codexOAuthClientID)
		}
	} else {
		if vals.Has("client_id") {
			t.Errorf("client_id should be absent when constant is empty, got: %q", vals.Get("client_id"))
		}
	}
}

// TestCodexRefreshExchange_401_BareError verifies that 401/403 from the token
// endpoint produce a bare error — codexRefreshClassify does not delegate to
// DefaultClassify, so these statuses must NOT wrap ErrAuthDenied.
func TestCodexRefreshExchange_401_BareError(t *testing.T) {
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

func TestCodexRefreshExchange_InvalidClient(t *testing.T) {
	body := []byte(`{"error":"invalid_client","error_description":"bad client"}`)
	srv := stubRefreshServer(t, 400, body, nil)
	rc := newTestRefreshClient(t, srv)

	_, err := rc.Exchange(context.Background(), "tok")
	if !errors.Is(err, ErrRefreshEndpointBroken) {
		t.Errorf("expected ErrRefreshEndpointBroken for invalid_client, got: %v", err)
	}
	if errors.Is(err, ErrRefreshRejected) {
		t.Errorf("invalid_client must not wrap ErrRefreshRejected (it's an aistat config issue, not an account problem): %v", err)
	}
}
