package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

func newTestProfileClient(t *testing.T, body []byte, status int) *profileClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	doer := httpx.NewDoer(srv.Client(), "aistat-test/0", "claude",
		map[string]string{"Anthropic-Beta": betaHeader}, nil)
	pc := newProfileClient(doer)
	pc.endpoint = srv.URL + "/api/oauth/profile"
	return pc
}

func TestProfileGet(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"full schema", func(t *testing.T) {
			body := []byte(`{
			"account": {
				"uuid": "acct-uuid-1",
				"email": "user@example.com",
				"display_name": "Test User",
				"full_name": "Test User Full"
			},
			"organization": {
				"uuid": "org-uuid-1",
				"name": "Acme Corp",
				"rate_limit_tier": "claude_max_5x"
			},
			"application": {"uuid": "app-uuid-1", "name": "Claude Code", "slug": "claude-code"}
		}`)
			pc := newTestProfileClient(t, body, 200)
			prof, err := pc.Get(context.Background(), "tok-test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prof.AccountUUID != "acct-uuid-1" {
				t.Errorf("AccountUUID = %q, want %q", prof.AccountUUID, "acct-uuid-1")
			}
			if prof.Email != "user@example.com" {
				t.Errorf("Email = %q, want %q", prof.Email, "user@example.com")
			}
			if prof.DisplayName != "Test User" {
				t.Errorf("DisplayName = %q, want %q", prof.DisplayName, "Test User")
			}
			if prof.RateLimitTier != "claude_max_5x" {
				t.Errorf("RateLimitTier = %q, want %q", prof.RateLimitTier, "claude_max_5x")
			}
		}},
		{"empty uuid", func(t *testing.T) {
			body := []byte(`{
			"account": {
				"uuid": "",
				"email": "user@example.com",
				"display_name": "Test User"
			}
		}`)
			pc := newTestProfileClient(t, body, 200)
			_, err := pc.Get(context.Background(), "tok-test")
			if !errors.Is(err, ErrProfileMissingFields) {
				t.Errorf("expected ErrProfileMissingFields, got: %v", err)
			}
		}},
		{"empty email", func(t *testing.T) {
			body := []byte(`{
			"account": {
				"uuid": "acct-uuid-1",
				"email": "",
				"display_name": "Test User"
			}
		}`)
			pc := newTestProfileClient(t, body, 200)
			_, err := pc.Get(context.Background(), "tok-test")
			if !errors.Is(err, ErrProfileMissingFields) {
				t.Errorf("expected ErrProfileMissingFields, got: %v", err)
			}
		}},
		{"personal account no organization", func(t *testing.T) {
			body := []byte(`{
			"account": {
				"uuid": "acct-uuid-personal",
				"email": "personal@example.com",
				"display_name": "Personal User"
			}
		}`)
			pc := newTestProfileClient(t, body, 200)
			prof, err := pc.Get(context.Background(), "tok-test")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if prof.RateLimitTier != "" {
				t.Errorf("RateLimitTier = %q, want empty string for personal account", prof.RateLimitTier)
			}
			if prof.AccountUUID != "acct-uuid-personal" {
				t.Errorf("AccountUUID = %q, want %q", prof.AccountUUID, "acct-uuid-personal")
			}
		}},
		{"401 wraps ErrAuthDenied", func(t *testing.T) {
			pc := newTestProfileClient(t, []byte(`{"error":"unauthorized"}`), 401)
			_, err := pc.Get(context.Background(), "tok-expired")
			if !errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("expected ErrAuthDenied, got: %v", err)
			}
		}},
		{"503 wraps ErrTransient", func(t *testing.T) {
			pc := newTestProfileClient(t, []byte(`{"error":"service unavailable"}`), 503)
			_, err := pc.Get(context.Background(), "tok-test")
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient, got: %v", err)
			}
		}},
		{"non-JSON 200 bare error", func(t *testing.T) {
			pc := newTestProfileClient(t, []byte("<html>oops</html>"), 200)
			_, err := pc.Get(context.Background(), "tok-test")
			if err == nil {
				t.Fatal("expected error for non-JSON body, got nil")
			}
			// Must not be a sentinel — it's a bare error from JSON unmarshal.
			if errors.Is(err, ErrProfileMissingFields) || errors.Is(err, providers.ErrAuthDenied) || errors.Is(err, providers.ErrTransient) {
				t.Errorf("non-JSON 200 should be bare error, got sentinel: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
