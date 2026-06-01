package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/testutil"
)

const testLogin = "testuser" // matches testdata/user.json's "login" field

func wantNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// routes a httptest server: /user → userFixture; anything matching billing prefix → usageFixture.
func newRoutedClient(t *testing.T, userFixture, usageFixture []byte, userStatus, usageStatus int, opts ...Option) (*Client, *recordedReqs) {
	t.Helper()
	rec := &recordedReqs{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		if r.URL.Path == "/user" {
			w.WriteHeader(userStatus)
			w.Write(userFixture)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/users/") {
			w.WriteHeader(usageStatus)
			w.Write(usageFixture)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	c := New(nil, "aistat-test/0", opts...)
	c.doer.Client = srv.Client()
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }
	c.userURL = srv.URL + "/user"
	c.usageURL = func(login string, year int, month int) string {
		return fmt.Sprintf("%s/users/%s/settings/billing/premium_request/usage?year=%d&month=%d", srv.URL, login, year, month)
	}
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
		{"pro fixture used percent and reset", func(t *testing.T) {
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			m, ok := out.Limits["month"]
			if !ok {
				t.Fatal("missing month limit")
			}
			// Fixture: 23.4 + 126.0 + 10.0 = 159.4 gross. Quota Pro = 300. Used ≈ 53.13%.
			want := 159.4 / 300 * 100
			if math.Abs(m.UsedPercent-want) > 0.01 {
				t.Errorf("used_percent = %v, want ~%v", m.UsedPercent, want)
			}
			if m.RemainingPercent+m.UsedPercent != 100 {
				t.Errorf("used + remaining should be 100, got %v + %v", m.UsedPercent, m.RemainingPercent)
			}
			// Reset is the start of the next UTC month, never in the past.
			if m.ResetAfterSeconds < 0 {
				t.Errorf("reset_after_seconds negative: %v", m.ResetAfterSeconds)
			}
		}},
		{"json rounds to two decimals", func(t *testing.T) {
			// Pro fixture: 23.4 + 126.0 + 10.0 = 159.4 gross, quota 300 → 53.13333…%.
			// Raw struct value is unrounded; JSON output is rounded to 2 dp.
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			raw := out.Limits["month"].UsedPercent
			if raw < 53.133 || raw > 53.134 {
				t.Errorf("raw struct UsedPercent should be unrounded (~53.1333), got %v", raw)
			}
			b, err := json.Marshal(out.Limits["month"])
			wantNoErr(t, err)
			if !strings.Contains(string(b), `"used_percent":53.13`) {
				t.Errorf("JSON should round to 53.13, got %s", string(b))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_UnknownPlan(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"fail closed with issue tracker url", func(t *testing.T) {
			var warnings []string
			c, rec := newRoutedClient(t,
				testutil.LoadFixture(t, "user_unknown_plan.json"),
				testutil.LoadFixture(t, "usage.json"),
				200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			_, err := c.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected error for unknown plan")
			}
			if !strings.Contains(err.Error(), "future_tier") {
				t.Errorf("error should name unknown plan slug, got: %v", err)
			}
			if !strings.Contains(err.Error(), providers.IssueTrackerURL) {
				t.Errorf("error should include issue-tracker URL %q, got: %v", providers.IssueTrackerURL, err)
			}
			if len(warnings) != 0 {
				t.Errorf("warn channel is reserved for SKU drift; got unknown-plan warnings: %v", warnings)
			}
			// Fail-closed must short-circuit before the usage call so we don't burn
			// a second request against the upstream rate limit for data we'll discard.
			if len(rec.reqs) != 1 {
				t.Errorf("unknown plan must short-circuit before the usage call; got %d requests", len(rec.reqs))
			}
		}},
		{"does not invoke warn", func(t *testing.T) {
			var warnings []string
			c, _ := newRoutedClient(t,
				testutil.LoadFixture(t, "user_unknown_plan.json"),
				testutil.LoadFixture(t, "usage.json"),
				200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			_, _ = c.Fetch(context.Background())
			if len(warnings) != 0 {
				t.Errorf("expected zero warnings on unknown plan (fail-closed via error); got: %v", warnings)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_quotaMath(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"zero quota is error", func(t *testing.T) {
			// Inject a zero-quota entry to simulate a future bad config. The quota map
			// is per-Client so this is safe under t.Parallel() if added later.
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200)
			c.quota = map[string]int{"pro": 0}
			_, err := c.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected error on zero quota")
			}
			if !strings.Contains(err.Error(), "zero quota") {
				t.Errorf("error should mention zero quota, got: %v", err)
			}
		}},
		{"overage clamps at 100", func(t *testing.T) {
			overage := []byte(`{"usageItems":[{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":400}]}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), overage, 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 100 {
				t.Errorf("used_percent should be clamped to 100, got %v", out.Limits["month"].UsedPercent)
			}
			if out.Limits["month"].RemainingPercent != 0 {
				t.Errorf("remaining_percent should be 0 at 100%% used")
			}
		}},
		{"empty usage items", func(t *testing.T) {
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), []byte(`{"usageItems":[]}`), 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 0 {
				t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_skuWarn(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"sku mismatch trips warning", func(t *testing.T) {
			// Copilot-product items present but the premium-request SKU is missing —
			// simulates a future GitHub rename of the SKU string.
			body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Premium Request (Renamed)","grossQuantity":100}
	]}`)
			var warnings []string
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 0 {
				t.Errorf("expected 0%% used when no items match, got %v", out.Limits["month"].UsedPercent)
			}
			if len(warnings) != 1 {
				t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
			}
			for _, want := range []string{"copilot:", "none matched", "file an issue"} {
				if !strings.Contains(warnings[0], want) {
					t.Errorf("warning missing %q: %s", want, warnings[0])
				}
			}
		}},
		{"non copilot product does not warn", func(t *testing.T) {
			// Items exist but none with product=="Copilot" — the product gate must
			// suppress the SKU-mismatch warning entirely.
			body := []byte(`{"usageItems":[
		{"product":"Actions","sku":"Compute","grossQuantity":1000}
	]}`)
			var warnings []string
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 0 {
				t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
			}
			if len(warnings) != 0 {
				t.Errorf("expected zero warnings for non-Copilot items, got: %v", warnings)
			}
		}},
		{"non premium copilot items warn", func(t *testing.T) {
			// Cold-start scenario: a user has Copilot-product activity (chat
			// completions, etc.) but has never made a premium request. The warn
			// should fire because the premium SKU was never observed — matches the
			// real "SKU renamed" signal. Documents intent.
			body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Chat","grossQuantity":50}
	]}`)
			var warnings []string
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 0 {
				t.Errorf("expected 0%% used (no premium-request SKU observed), got %v", out.Limits["month"].UsedPercent)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "none matched") {
				t.Errorf("expected SKU-mismatch warning for non-premium Copilot items, got: %v", warnings)
			}
		}},
		{"premium sku present with zero gross does not warn", func(t *testing.T) {
			// Premium SKU was observed (grossQuantity=0) — the !sawPremiumSku gate
			// must suppress the warning even though gross=0.
			body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":0}
	]}`)
			var warnings []string
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 200,
				WithWarn(func(s string) { warnings = append(warnings, s) }),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			if out.Limits["month"].UsedPercent != 0 {
				t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
			}
			if len(warnings) != 0 {
				t.Errorf("expected zero warnings when premium SKU is observed (even at 0), got: %v", warnings)
			}
		}},
		{"non copilot items filtered", func(t *testing.T) {
			body := []byte(`{"usageItems":[
		{"product":"Actions","sku":"Compute","grossQuantity":1000},
		{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":30}
	]}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			want := 30.0 / 300 * 100 // 10%
			if math.Abs(out.Limits["month"].UsedPercent-want) > 0.01 {
				t.Errorf("non-Copilot items leaked into sum: got %v, want ~%v", out.Limits["month"].UsedPercent, want)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_404(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"user endpoint 404 is not misclassified", func(t *testing.T) {
			// A 404 from /user must NOT be classified as "missing user scope" — the
			// scope-missing tripwire is specific to the billing endpoint. A /user
			// 404 (rare; happens during partial GitHub outages or endpoint
			// deprecation) should surface as a bare HTTP 404 error.
			c, _ := newRoutedClient(t, []byte(`{"message":"Not Found"}`), testutil.LoadFixture(t, "usage.json"), 404, 200)
			_, err := c.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected error on /user 404")
			}
			if errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("/user 404 must NOT be classified as ErrAuthMissing; got: %v", err)
			}
			if strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
				t.Errorf("/user 404 must NOT surface the missing-scope message; got: %v", err)
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				t.Errorf("error should mention HTTP 404; got: %v", err)
			}
		}},
		{"missing scope", func(t *testing.T) {
			body := []byte(`{"message":"Not Found","documentation_url":"...","status":"404"}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 404)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("expected ErrAuthMissing on 404, got: %v", err)
			}
			if !strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
				t.Errorf("error should embed exact message, got: %v", err)
			}
		}},
		{"lowercase not found", func(t *testing.T) {
			body := []byte(`{"message":"not found"}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 404)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("lowercase \"not found\" must trip ErrAuthMissing, got: %v", err)
			}
		}},
		{"resource not found", func(t *testing.T) {
			body := []byte(`{"message":"Resource not found"}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 404)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("\"Resource not found\" must trip ErrAuthMissing, got: %v", err)
			}
		}},
		{"unknown message", func(t *testing.T) {
			// A 404 with a JSON body but an unrecognized message must NOT be
			// classified as missing-scope; the cause is genuinely something else.
			body := []byte(`{"message":"Forbidden"}`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 404)
			_, err := c.Fetch(context.Background())
			if errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("unrecognized 404 message must NOT trip ErrAuthMissing, got: %v", err)
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				t.Errorf("expected bare HTTP 404, got: %v", err)
			}
		}},
		{"non-json body", func(t *testing.T) {
			// A 404 with a plain-text body (no JSON shape) must fall through to the
			// default classifier. Under the prior substring match, body containing
			// "Not Found" would have tripped the missing-scope path; the new JSON
			// decode rejects that and we surface as bare HTTP 404.
			body := []byte(`<html>Not Found</html>`)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), body, 200, 404)
			_, err := c.Fetch(context.Background())
			if errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("non-JSON 404 must NOT trip ErrAuthMissing, got: %v", err)
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				t.Errorf("expected bare HTTP 404, got: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestFetch_auth(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"401 is auth denied", func(t *testing.T) {
			c, _ := newRoutedClient(t, []byte(`{"error":"bad creds"}`), nil, 401, 200)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrAuthDenied) {
				t.Errorf("expected ErrAuthDenied, got: %v", err)
			}
		}},
		{"token missing is auth missing", func(t *testing.T) {
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200)
			c.readToken = func(ctx context.Context) (string, error) { return "", cred.ErrGitHubTokenNotFound }
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrAuthMissing) {
				t.Errorf("expected ErrAuthMissing, got: %v", err)
			}
			if !strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
				t.Errorf("expected exact message, got: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
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
			c, _ := newRoutedClient(t, []byte(tt.body), nil, tt.status, 200)
			_, err := c.Fetch(context.Background())
			if !errors.Is(err, providers.ErrTransient) {
				t.Errorf("expected ErrTransient on %d, got: %v", tt.status, err)
			}
		})
	}
}

func TestFetch_RequestShape(t *testing.T) {
	fixed := time.Date(2024, time.March, 15, 0, 0, 0, 0, time.UTC)
	c, rec := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200, WithNow(func() time.Time { return fixed }))
	_, err := c.Fetch(context.Background())
	wantNoErr(t, err)
	if len(rec.reqs) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(rec.reqs))
	}
	for _, r := range rec.reqs {
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
	}
	if rec.reqs[0].URL.Path != "/user" {
		t.Errorf("first call path = %s, want /user", rec.reqs[0].URL.Path)
	}
	if !strings.HasPrefix(rec.reqs[1].URL.Path, "/users/"+testLogin+"/") {
		t.Errorf("second call path = %s", rec.reqs[1].URL.Path)
	}
	if got := rec.reqs[1].URL.Query().Get("year"); got != "2024" {
		t.Errorf("second call year query = %q, want 2024", got)
	}
	if got := rec.reqs[1].URL.Query().Get("month"); got != "3" {
		t.Errorf("second call month query = %q, want 3", got)
	}
}

func TestFetch_EmptyLogin(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`{"login":"","plan":{"name":"pro"}}`), testutil.LoadFixture(t, "usage.json"), 200, 200)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty login") {
		t.Errorf("expected empty-login error, got: %v", err)
	}
}

func TestFetch_reset(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"reset after seconds truncated", func(t *testing.T) {
			// Frozen clock with a non-zero sub-second component. Without truncation,
			// the 789ms residue shaves a second off ResetAfterSeconds via int(...)
			// rounding toward zero; with truncation, it does not.
			frozen := time.Date(2026, 5, 15, 12, 34, 56, 789_000_000, time.UTC)
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200,
				WithNow(func() time.Time { return frozen }),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			nowTrunc := frozen.Truncate(time.Second)
			reset := time.Date(nowTrunc.Year(), nowTrunc.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			want := int(reset.Sub(nowTrunc).Seconds())
			if got := out.Limits["month"].ResetAfterSeconds; got != want {
				t.Errorf("ResetAfterSeconds = %d, want %d (truncation regression: removing .Truncate(time.Second) in Fetch yields want-1)", got, want)
			}
		}},
		{"reset time is start of next month", func(t *testing.T) {
			c, _ := newRoutedClient(t, testutil.LoadFixture(t, "user.json"), testutil.LoadFixture(t, "usage.json"), 200, 200)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			r := out.Limits["month"].ResetsAt
			if r.Day() != 1 || r.Hour() != 0 || r.Minute() != 0 || r.Second() != 0 {
				t.Errorf("resets_at should be midnight on day 1, got %v", r)
			}
		}},
		{"reset month uses post-billing now", func(t *testing.T) {
			// Wall clock crosses a UTC month boundary between urlNow (pre-billing)
			// and subNow (post-billing). Reset must be the first of the month AFTER
			// subNow's month — not after urlNow's. Driving reset off urlNow's month
			// (Jan) would compute Feb 1 00:00:00, which is in the past relative to
			// subNow (Feb 1 00:00:05) and surface as reset_after_seconds=0.
			urlNow := time.Date(2026, 1, 31, 23, 59, 55, 0, time.UTC)
			subNow := time.Date(2026, 2, 1, 0, 0, 5, 0, time.UTC)
			clocks := []time.Time{urlNow, subNow}
			var idx int
			c, _ := newRoutedClient(t,
				testutil.LoadFixture(t, "user.json"),
				testutil.LoadFixture(t, "usage.json"),
				200, 200,
				WithNow(func() time.Time {
					t := clocks[idx]
					idx++
					return t
				}),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			wantReset := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
			if got := out.Limits["month"].ResetsAt; !got.Equal(wantReset) {
				t.Errorf("ResetsAt = %v, want %v (must reflect subNow's month, not urlNow's)", got, wantReset)
			}
			wantSecs := int(wantReset.Sub(subNow).Seconds())
			if got := out.Limits["month"].ResetAfterSeconds; got != wantSecs {
				t.Errorf("ResetAfterSeconds = %d, want %d", got, wantSecs)
			}
		}},
		{"reset seconds recomputed after billing call", func(t *testing.T) {
			// Two increasing `now` values returned on successive calls.
			// idx==0 used for urlNow; idx==1 used for subNow → reset_after_seconds
			// must reflect base+5s, not base.
			base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
			var idx int
			c, _ := newRoutedClient(t,
				testutil.LoadFixture(t, "user.json"),
				testutil.LoadFixture(t, "usage.json"),
				200, 200,
				WithNow(func() time.Time {
					defer func() { idx++ }()
					return base.Add(time.Duration(idx) * 5 * time.Second)
				}),
			)
			out, err := c.Fetch(context.Background())
			wantNoErr(t, err)
			expectedReset := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
			expectedSecs := int(expectedReset.Sub(base.Add(5 * time.Second)).Seconds())
			if got := out.Limits["month"].ResetAfterSeconds; got != expectedSecs {
				t.Errorf("ResetAfterSeconds=%d, want %d (must use post-billing now)", got, expectedSecs)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// deadlineCapturingTransport wraps an underlying RoundTripper and records the
// request ctx's deadline before each round-trip. Used to verify that two
// in-process calls receive independent per-request deadlines.
type deadlineCapturingTransport struct {
	inner     http.RoundTripper
	deadlines []time.Time
}

func (t *deadlineCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if d, ok := req.Context().Deadline(); ok {
		t.deadlines = append(t.deadlines, d)
	} else {
		t.deadlines = append(t.deadlines, time.Time{})
	}
	return t.inner.RoundTrip(req)
}

func TestFetch_BothCallsGetIndependentDeadlines(t *testing.T) {
	// Two stub responses, served via a routed server; transport captures the
	// per-request ctx deadline. A c.now that advances 5s between captures
	// forces wall-clock separation so the two WithTimeout derivations land at
	// different absolute times without any time.Sleep.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			w.WriteHeader(200)
			w.Write(testutil.LoadFixture(t, "user.json"))
			return
		}
		w.WriteHeader(200)
		w.Write(testutil.LoadFixture(t, "usage.json"))
	}))
	t.Cleanup(srv.Close)
	tr := &deadlineCapturingTransport{inner: srv.Client().Transport}
	base := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	var idx int
	c := New(nil, "aistat-test/0", WithNow(func() time.Time {
		defer func() { idx++ }()
		return base.Add(time.Duration(idx) * 5 * time.Second)
	}))
	c.doer.Client = &http.Client{Transport: tr}
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }
	c.userURL = srv.URL + "/user"
	c.usageURL = func(login string, year int, month int) string {
		return fmt.Sprintf("%s/users/%s/settings/billing/premium_request/usage?year=%d&month=%d", srv.URL, login, year, month)
	}
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tr.deadlines) != 2 {
		t.Fatalf("expected 2 deadlines, got %d", len(tr.deadlines))
	}
	if tr.deadlines[0].IsZero() || tr.deadlines[1].IsZero() {
		t.Fatalf("both calls must have a deadline; got %v", tr.deadlines)
	}
	if tr.deadlines[1].Equal(tr.deadlines[0]) {
		t.Errorf("second call's deadline must differ from the first (proves independent derivation); got both = %v", tr.deadlines[0])
	}
}
