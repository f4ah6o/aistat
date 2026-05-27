package claude

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

func loadFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/usage.json")
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
		doer: &httpx.Doer{
			Client:       srv.Client(),
			UserAgent:    "usage-check-test/0",
			ProviderID:   "claude",
			ExtraHeaders: map[string]string{"Anthropic-Beta": betaHeader},
		},
		endpoint:  srv.URL + "/api/oauth/usage",
		readToken: func(ctx context.Context) (string, error) { return "sk-ant-oat01-fake", nil },
	}
}

func TestFetch_GoldenFixture(t *testing.T) {
	c := newTestClient(t, loadFixture(t), 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Limits) != 3 {
		t.Fatalf("expected 3 limits, got %d: %v", len(out.Limits), keys(out.Limits))
	}
	for _, want := range []string{"five_hour", "seven_day", "seven_day_sonnet"} {
		if _, ok := out.Limits[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	// Bonus windows should not appear.
	for _, unwanted := range []string{"seven_day_omelette", "seven_day_opus", "tangelo", "iguana_necktie"} {
		if _, ok := out.Limits[unwanted]; ok {
			t.Errorf("bonus window %s should be filtered out", unwanted)
		}
	}
	fh := out.Limits["five_hour"]
	if fh.UsedPercent != 47.0 {
		t.Errorf("five_hour used_percent = %v, want 47.0", fh.UsedPercent)
	}
	if fh.RemainingPercent != 53.0 {
		t.Errorf("five_hour remaining_percent = %v, want 53.0", fh.RemainingPercent)
	}
	if fh.ResetsAt.Nanosecond() != 0 {
		t.Errorf("resets_at not truncated to second: %v", fh.ResetsAt)
	}
	wantTime, _ := time.Parse(time.RFC3339, "2026-05-26T22:00:00Z")
	if !fh.ResetsAt.Equal(wantTime) {
		t.Errorf("resets_at = %v, want %v", fh.ResetsAt, wantTime)
	}
}

func TestFetch_NullResetsAtIsSkipped(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":10.0,"resets_at":"2026-05-26T22:00:00+00:00"},"seven_day_omelette":{"utilization":50.0,"resets_at":null}}`)
	c := newTestClient(t, body, 200, nil)
	out, _ := c.Fetch(context.Background())
	if _, ok := out.Limits["seven_day_omelette"]; ok {
		t.Errorf("seven_day_omelette should be excluded when resets_at is null")
	}
}

func TestFetch_RequestShape(t *testing.T) {
	var got http.Request
	c := newTestClient(t, loadFixture(t), 200, &got)
	_, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "GET" {
		t.Errorf("method = %s, want GET", got.Method)
	}
	if got.URL.Path != "/api/oauth/usage" {
		t.Errorf("path = %s", got.URL.Path)
	}
	if h := got.Header.Get("Authorization"); h != "Bearer sk-ant-oat01-fake" {
		t.Errorf("Authorization = %q", h)
	}
	if h := got.Header.Get("Anthropic-Beta"); h != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", h)
	}
	if h := got.Header.Get("User-Agent"); !strings.Contains(h, "usage-check") {
		t.Errorf("User-Agent missing: %q", h)
	}
}

func TestFetch_Status401IsAuthDenied(t *testing.T) {
	c := newTestClient(t, []byte(`{"error":"unauthorized"}`), 401, nil)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("err should wrap ErrAuthDenied: %v", err)
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("err should include body: %v", err)
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
	srv.Close() // shut down before any request
	c := &Client{
		doer:      &httpx.Doer{Client: srv.Client(), UserAgent: "usage-check-test/0", ProviderID: "claude"},
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
		doer:      &httpx.Doer{Client: srv.Client(), UserAgent: "usage-check-test/0", ProviderID: "claude"},
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

func TestFetch_BadResetsAt(t *testing.T) {
	body := []byte(`{"five_hour":{"utilization":10.0,"resets_at":"yesterday"}}`)
	c := newTestClient(t, body, 200, nil)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unparseable resets_at") {
		t.Errorf("expected unparseable error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "five_hour") {
		t.Errorf("error should name window: %v", err)
	}
}

func TestFetch_TokenMissingIsAuthMissing(t *testing.T) {
	c := newTestClient(t, []byte(`{}`), 200, nil)
	c.readToken = func(ctx context.Context) (string, error) { return "", cred.ErrClaudeTokenNotFound }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.ClaudeTokenMissingMessage) {
		t.Errorf("expected exact message, got: %v", err)
	}
}

func TestFetch_TokenGenericErrorPropagated(t *testing.T) {
	sentinel := errors.New("some keychain failure")
	c := newTestClient(t, []byte(`{}`), 200, nil)
	c.readToken = func(ctx context.Context) (string, error) { return "", sentinel }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("generic error should propagate, got: %v", err)
	}
	if errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("generic err should not be classified as ErrAuthMissing")
	}
}

func keys(m map[string]providers.Limit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
