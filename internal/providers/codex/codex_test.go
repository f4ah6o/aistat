package codex

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/llm-usage/internal/cred"
	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestClient(t *testing.T, body []byte, status int, captureReq *http.Request) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captureReq != nil {
			*captureReq = *r.Clone(context.Background())
		}
		w.WriteHeader(status)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &Client{
		doer:      &httpx.Doer{Client: srv.Client(), UserAgent: "usage-check-test/0", ProviderID: "codex"},
		endpoint:  srv.URL + "/backend-api/wham/usage",
		readToken: func(ctx context.Context) (string, error) { return "fake-jwt", nil },
	}
}

func TestFetch_GoldenFixture_TwoWindows(t *testing.T) {
	c := newTestClient(t, loadFixture(t, "usage.json"), 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Limits) != 2 {
		t.Fatalf("expected 2 limits, got %d: %v", len(out.Limits), out.Limits)
	}
	for _, want := range []string{"five_hour", "seven_day"} {
		if _, ok := out.Limits[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if _, ok := out.Limits["code_review_seven_day"]; ok {
		t.Error("code_review_seven_day should be absent when API returns null")
	}
	fh := out.Limits["five_hour"]
	if fh.UsedPercent != 2 {
		t.Errorf("five_hour used_percent = %v, want 2", fh.UsedPercent)
	}
	if fh.RemainingPercent != 98 {
		t.Errorf("five_hour remaining_percent = %v, want 98", fh.RemainingPercent)
	}
	wantTime := time.Unix(1779842256, 0).UTC()
	if !fh.ResetsAt.Equal(wantTime) {
		t.Errorf("resets_at = %v, want %v", fh.ResetsAt, wantTime)
	}
}

func TestFetch_CodeReviewIncluded(t *testing.T) {
	c := newTestClient(t, loadFixture(t, "usage_with_code_review.json"), 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Limits["code_review_seven_day"]; !ok {
		t.Fatalf("code_review_seven_day should be present, got %v", out.Limits)
	}
	cr := out.Limits["code_review_seven_day"]
	if cr.UsedPercent != 33 {
		t.Errorf("code_review used_percent = %v, want 33", cr.UsedPercent)
	}
}

func TestFetch_RequestShape(t *testing.T) {
	var got http.Request
	c := newTestClient(t, loadFixture(t, "usage.json"), 200, &got)
	_, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" {
		t.Errorf("method = %s", got.Method)
	}
	if got.URL.Path != "/backend-api/wham/usage" {
		t.Errorf("path = %s", got.URL.Path)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer fake-jwt" {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Header.Get("User-Agent"); !strings.Contains(h, "usage-check") {
		t.Errorf("User-Agent missing: %q", h)
	}
}

func TestFetch_ShapeDriftPrimary(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":21600,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":1780429056}}}`)
	c := newTestClient(t, body, 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected error on shape drift")
	}
	if !strings.Contains(err.Error(), "21600") || !strings.Contains(err.Error(), "18000") {
		t.Errorf("error should name both values: %v", err)
	}
	if !strings.Contains(err.Error(), "issue") {
		t.Errorf("error should point at issue tracker: %v", err)
	}
}

func TestFetch_ShapeDriftSecondary(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":86400,"reset_at":1780429056}}}`)
	c := newTestClient(t, body, 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "secondary_window") {
		t.Errorf("expected secondary_window shape-drift error, got: %v", err)
	}
}

func TestFetch_MissingRateLimit(t *testing.T) {
	c := newTestClient(t, []byte(`{}`), 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing rate_limit") {
		t.Errorf("expected missing rate_limit error, got: %v", err)
	}
}

func TestFetch_NullRateLimit(t *testing.T) {
	c := newTestClient(t, []byte(`{"rate_limit":null}`), 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing rate_limit") {
		t.Errorf("expected missing rate_limit error on null, got: %v", err)
	}
}

func TestFetch_CodeReviewSkippedOnZeroResetAt(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":1780429056}},"code_review_rate_limit":{"used_percent":0,"limit_window_seconds":0,"reset_at":0}}`)
	c := newTestClient(t, body, 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Limits["code_review_seven_day"]; ok {
		t.Errorf("code_review_seven_day must be skipped when reset_at == 0")
	}
}

func TestFetch_Status401IsAuthDenied(t *testing.T) {
	c := newTestClient(t, []byte(`{"error":"unauthorized"}`), 401, nil)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("err should wrap ErrAuthDenied: %v", err)
	}
}

func TestFetch_Status408IsTransient(t *testing.T) {
	c := newTestClient(t, []byte("timeout"), 408, nil)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("408 should be transient: %v", err)
	}
}

func TestFetch_Status429IsTransient(t *testing.T) {
	c := newTestClient(t, []byte("too many"), 429, nil)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("429 should be transient: %v", err)
	}
}

func TestFetch_Status503IsTransient(t *testing.T) {
	c := newTestClient(t, []byte("down"), 503, nil)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("503 should be transient: %v", err)
	}
}

func TestFetch_Status418IsBareError(t *testing.T) {
	c := newTestClient(t, []byte("teapot"), 418, nil)
	_, err := c.Fetch(context.Background())
	if errors.Is(err, providers.ErrTransient) || errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("418 should be bare error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 418") {
		t.Errorf("err should mention HTTP 418: %v", err)
	}
}

func TestFetch_NetworkErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	c := &Client{
		doer:      &httpx.Doer{Client: srv.Client(), UserAgent: "usage-check-test/0", ProviderID: "codex"},
		endpoint:  srv.URL,
		readToken: func(ctx context.Context) (string, error) { return "tok", nil },
	}
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("network error should be transient: %v", err)
	}
}

func TestFetch_CancelledContextIsNotTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
	}))
	defer srv.Close()
	c := &Client{
		doer:      &httpx.Doer{Client: srv.Client(), UserAgent: "usage-check-test/0", ProviderID: "codex"},
		endpoint:  srv.URL,
		readToken: func(ctx context.Context) (string, error) { return "tok", nil },
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Fetch(ctx)
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("cancelled context should not be transient: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestFetch_NonJSON200(t *testing.T) {
	c := newTestClient(t, []byte("<html>oops</html>"), 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "non-JSON") {
		t.Errorf("expected non-JSON error, got: %v", err)
	}
}

func TestFetch_TokenMissingIsAuthMissing(t *testing.T) {
	c := newTestClient(t, []byte(`{}`), 200, nil)
	c.readToken = func(ctx context.Context) (string, error) { return "", cred.ErrCodexTokenNotFound }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.CodexTokenMissingMessage) {
		t.Errorf("expected exact message, got: %v", err)
	}
}

func TestFetch_TokenGenericErrorPropagated(t *testing.T) {
	sentinel := errors.New("some auth.json failure")
	c := newTestClient(t, []byte(`{}`), 200, nil)
	c.readToken = func(ctx context.Context) (string, error) { return "", sentinel }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("generic error should propagate, got: %v", err)
	}
}
