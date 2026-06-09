package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/testutil"
)

// newRoutedClient serves a single copilot_internal/user response (the provider
// makes exactly one HTTP call) and records every request for shape assertions.
func newRoutedClient(t *testing.T, fixture []byte, status int, opts ...Option) (*Client, *recordedReqs) {
	t.Helper()
	rec := &recordedReqs{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(status)
		w.Write(fixture)
	}))
	t.Cleanup(srv.Close)

	c := New(nil, "aistat-test/0", opts...)
	c.doer.Client = srv.Client()
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }
	c.url = srv.URL
	return c, rec
}

type recordedReqs struct {
	reqs []*http.Request
}

func (r *recordedReqs) add(req *http.Request) {
	r.reqs = append(r.reqs, req.Clone(context.Background()))
}

func TestFetch_goldenFlow(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"metered used percent and reset", func(t *testing.T) {
			fixed := time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "copilot_user.json"), 200, WithNow(func() time.Time { return fixed }))
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			m, ok := out.Limits["month"]
			if !ok {
				t.Fatal("missing month limit")
			}
			// premium_interactions.percent_remaining = 40 → used 60.
			if m.UsedPercent != 60 {
				t.Errorf("used_percent = %v, want 60", m.UsedPercent)
			}
			if m.RemainingPercent+m.UsedPercent != 100 {
				t.Errorf("used + remaining should be 100, got %v + %v", m.UsedPercent, m.RemainingPercent)
			}
			// Reset comes straight from quota_reset_date_utc.
			wantReset := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			if !m.ResetsAt.Equal(wantReset) {
				t.Errorf("resets_at = %v, want %v", m.ResetsAt, wantReset)
			}
			if got := m.ResetAfterSeconds; got != int(wantReset.Sub(fixed).Seconds()) {
				t.Errorf("reset_after_seconds = %d, want %d", got, int(wantReset.Sub(fixed).Seconds()))
			}
		}},
		{"overage clamps at 100", func(t *testing.T) {
			// percent_remaining 0 with negative quota_remaining (account is over budget).
			body := []byte(`{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"premium_interactions":{"entitlement":1500,"percent_remaining":0.0,"unlimited":false,"quota_remaining":-21.7}}}`)
			c, _ := newRoutedClient(t, body, 200)
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 100 {
				t.Errorf("used_percent should clamp to 100, got %v", out.Limits["month"].UsedPercent)
			}
			if out.Limits["month"].RemainingPercent != 0 {
				t.Errorf("remaining_percent should be 0 at 100%% used, got %v", out.Limits["month"].RemainingPercent)
			}
		}},
		{"json rounds to two decimals", func(t *testing.T) {
			// percent_remaining 26.553 → used 73.447; raw struct unrounded, JSON 2dp.
			body := []byte(`{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"premium_interactions":{"entitlement":1500,"percent_remaining":26.553,"unlimited":false}}}`)
			c, _ := newRoutedClient(t, body, 200)
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			raw := out.Limits["month"].UsedPercent
			if raw < 73.446 || raw > 73.448 {
				t.Errorf("raw struct UsedPercent should be unrounded (~73.447), got %v", raw)
			}
			b, err := json.Marshal(out.Limits["month"])
			testutil.WantNoErr(t, err)
			if !strings.Contains(string(b), `"used_percent":73.45`) {
				t.Errorf("JSON should round to 73.45, got %s", string(b))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_notApplicable(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		// Unlimited grant: no metered window.
		{"unlimited pool", `{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"premium_interactions":{"entitlement":1500,"percent_remaining":100.0,"unlimited":true}}}`},
		// No allotment (e.g. a free plan with zero credit pool).
		{"zero entitlement", `{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"premium_interactions":{"entitlement":0,"percent_remaining":100.0,"unlimited":false}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var warnings []string
			c, _ := newRoutedClient(t, []byte(tt.body), 200, WithWarn(func(s string) { warnings = append(warnings, s) }))
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			if out.Limits == nil {
				t.Fatal("Limits must be non-nil (empty) for the N/A case, got nil")
			}
			if len(out.Limits) != 0 {
				t.Errorf("expected empty limits for N/A, got %v", out.Limits)
			}
			if len(warnings) != 0 {
				t.Errorf("N/A case must not warn, got: %v", warnings)
			}
			// The empty-but-non-nil map must serialize as `"limits":{}`.
			b, err := json.Marshal(providers.ProviderResult{Limits: out.Limits})
			testutil.WantNoErr(t, err)
			if !strings.Contains(string(b), `"limits":{}`) {
				t.Errorf("N/A must serialize as limits:{}, got %s", string(b))
			}
		})
	}
}

func TestFetch_missingQuotaKey(t *testing.T) {
	// quota_snapshots present (chat/completions) but premium_interactions is
	// gone → GitHub likely renamed the quota key. Warn once and report no window.
	body := []byte(`{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"chat":{"unlimited":true},"completions":{"unlimited":true}}}`)
	var warnings []string
	c, _ := newRoutedClient(t, body, 200, WithWarn(func(s string) { warnings = append(warnings, s) }))
	out, err := c.Fetch(context.Background())
	testutil.WantNoErr(t, err)
	if len(out.Limits) != 0 {
		t.Errorf("expected empty limits when the metered key is missing, got %v", out.Limits)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}
	for _, want := range []string{"copilot:", "premium_interactions", providers.IssueTrackerURL} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning missing %q: %s", want, warnings[0])
		}
	}
}

func TestFetch_reset(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"reset after seconds truncated", func(t *testing.T) {
			// Frozen clock with a non-zero sub-second component. Truncating now to
			// the second keeps int(...) from shaving a second off ResetAfterSeconds.
			frozen := time.Date(2026, 6, 15, 12, 34, 56, 789_000_000, time.UTC)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "copilot_user.json"), 200,
				WithNow(func() time.Time { return frozen }))
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			reset := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			want := int(reset.Sub(frozen.Truncate(time.Second)).Seconds())
			if got := out.Limits["month"].ResetAfterSeconds; got != want {
				t.Errorf("ResetAfterSeconds = %d, want %d", got, want)
			}
		}},
		{"reset in the past clamps to zero", func(t *testing.T) {
			// now after the reset date → ResetAfterSeconds floors at 0.
			after := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "copilot_user.json"), 200,
				WithNow(func() time.Time { return after }))
			out, err := c.Fetch(context.Background())
			testutil.WantNoErr(t, err)
			if got := out.Limits["month"].ResetAfterSeconds; got != 0 {
				t.Errorf("ResetAfterSeconds = %d, want 0 (reset in the past)", got)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_RequestShape(t *testing.T) {
	c, rec := newRoutedClient(t, testutil.LoadFixture(t, "copilot_user.json"), 200)
	_, err := c.Fetch(context.Background())
	testutil.WantNoErr(t, err)
	if len(rec.reqs) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(rec.reqs))
	}
	r := rec.reqs[0]
	if r.Method != "GET" {
		t.Errorf("method = %s", r.Method)
	}
	if r.Header.Get("Authorization") != "Bearer gho_fake" {
		t.Errorf("Authorization wrong: %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept wrong: %q", r.Header.Get("Accept"))
	}
	if !strings.Contains(r.Header.Get("User-Agent"), "aistat") {
		t.Errorf("User-Agent missing: %q", r.Header.Get("User-Agent"))
	}
	// No editor/version header is required for this endpoint (verified).
	if got := r.Header.Get("X-Github-Api-Version"); got != "" {
		t.Errorf("unexpected version header: %q", got)
	}
}

func TestFetch_auth(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		run    func(t *testing.T, err error)
	}{
		{"401 is auth denied", 401, `{"message":"Bad credentials"}`, func(t *testing.T, err error) {
			if !errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("expected ErrAuthDenied, got: %v", err)
			}
		}},
		{"403 is auth denied", 403, `{"message":"Forbidden"}`, func(t *testing.T, err error) {
			if !errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("expected ErrAuthDenied, got: %v", err)
			}
		}},
		{"bare 404 is not remapped to auth missing", 404, `{"message":"Not Found"}`, func(t *testing.T, err error) {
			if errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("404 must NOT be classified as ErrAuthMissing; got: %v", err)
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				t.Errorf("expected bare HTTP 404, got: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRoutedClient(t, []byte(tt.body), tt.status)
			_, err := c.Fetch(context.Background())
			if err == nil {
				t.Fatalf("expected error for status %d", tt.status)
			}
			tt.run(t, err)
		})
	}
}

func TestFetch_tokenMissing(t *testing.T) {
	c, _ := newRoutedClient(t, testutil.LoadFixture(t, "copilot_user.json"), 200)
	c.readToken = func(ctx context.Context) (string, error) { return "", cred.ErrGitHubTokenNotFound }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
		t.Errorf("expected exact missing-token message, got: %v", err)
	}
}

func TestFetch_transient(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"503 service unavailable", 503, "down"},
		{"429 rate limited", 429, "rl"},
		{"408 request timeout", 408, "to"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := newRoutedClient(t, []byte(tt.body), tt.status)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient on %d, got: %v", tt.status, err)
			}
		})
	}
}
