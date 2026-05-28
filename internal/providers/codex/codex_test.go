package codex

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/testutil"
)

func newTestClient(t *testing.T, body []byte, status int, captureReq *http.Request) *Client {
	t.Helper()
	srv := testutil.NewStubServer(t, body, status, captureReq)
	return &Client{
		doer:      httpx.NewDoer(srv.Client(), "aistat-test/0", "codex", nil, nil),
		endpoint:  srv.URL + "/backend-api/wham/usage",
		readToken: func(ctx context.Context) (string, error) { return "fake-jwt", nil },
		now:       time.Now,
	}
}

func TestFetch_ResetAfterSecondsTruncated(t *testing.T) {
	// Frozen clock with a non-zero sub-second component. Without truncation,
	// the residue shaves a second off ResetAfterSeconds via int(...) rounding
	// toward zero. Removing .Truncate(time.Second) in codex.go's Fetch yields
	// want-1.
	frozen := time.Date(2026, 5, 15, 12, 34, 56, 789_000_000, time.UTC)
	resetAt := frozen.Add(3 * time.Hour).Truncate(time.Second).Unix()
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":` +
		strconv.FormatInt(resetAt, 10) + `}}}`)
	c := newTestClient(t, body, 200, nil)
	c.now = func() time.Time { return frozen }
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := 3 * 3600
	if got := out.Limits["five_hour"].ResetAfterSeconds; got != want {
		t.Errorf("ResetAfterSeconds = %d, want %d (regression: removing .Truncate(time.Second) yields want-1)", got, want)
	}
}

func TestFetch_GoldenFixture_TwoWindows(t *testing.T) {
	c := newTestClient(t, testutil.LoadFixture(t, "usage.json"), 200, nil)
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
	c := newTestClient(t, testutil.LoadFixture(t, "usage_with_code_review.json"), 200, nil)
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
	c := newTestClient(t, testutil.LoadFixture(t, "usage.json"), 200, &got)
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
	if h := got.Header.Get("User-Agent"); !strings.Contains(h, "aistat") {
		t.Errorf("User-Agent missing: %q", h)
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

func TestFetch_PrimaryWindowResetAtZero_Skipped(t *testing.T) {
	// A primary_window with reset_at=0 (the inactive-window upstream shape)
	// must be skipped uniformly — same guard as code_review_rate_limit. Without
	// this, the window would be emitted with ResetsAt=1970-01-01.
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":0},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":1780429056}}}`)
	c := newTestClient(t, body, 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Limits["five_hour"]; ok {
		t.Errorf("five_hour must be skipped when primary_window.reset_at == 0")
	}
	if _, ok := out.Limits["seven_day"]; !ok {
		t.Errorf("seven_day should still be present alongside skipped five_hour")
	}
}

func TestFetch_SecondaryWindowResetAtZero_Skipped(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":0}}}`)
	c := newTestClient(t, body, 200, nil)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Limits["seven_day"]; ok {
		t.Errorf("seven_day must be skipped when secondary_window.reset_at == 0")
	}
	if _, ok := out.Limits["five_hour"]; !ok {
		t.Errorf("five_hour should still be present alongside skipped seven_day")
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
