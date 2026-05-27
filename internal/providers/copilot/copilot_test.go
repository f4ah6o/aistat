package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/llm-usage/internal/cred"
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

	c := New(nil, "usage-check-test/0", opts...)
	c.doer.Client = srv.Client()
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }
	c.userURL = srv.URL + "/user"
	c.usageURL = func(login string, year int, month int) string {
		return srv.URL + "/users/" + login + "/settings/billing/premium_request/usage"
	}
	return c, rec
}

type recordedReqs struct {
	reqs []*http.Request
}

func (r *recordedReqs) add(req *http.Request) {
	r.reqs = append(r.reqs, req.Clone(context.Background()))
}

func TestFetch_JSONRoundsToTwoDecimals(t *testing.T) {
	// Pro fixture: 23.4 + 126.0 + 10.0 = 159.4 gross, quota 300 → 53.13333…%.
	// Raw struct value is unrounded; JSON output is rounded to 2 dp.
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), loadFixture(t, "usage.json"), 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw := out.Limits["month"].UsedPercent
	if raw < 53.133 || raw > 53.134 {
		t.Errorf("raw struct UsedPercent should be unrounded (~53.1333), got %v", raw)
	}
	b, err := json.Marshal(out.Limits["month"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"used_percent":53.13`) {
		t.Errorf("JSON should round to 53.13, got %s", string(b))
	}
}

func TestFetch_GoldenFlow_Pro(t *testing.T) {
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), loadFixture(t, "usage.json"), 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, ok := out.Limits["month"]
	if !ok {
		t.Fatal("missing month limit")
	}
	// Fixture: 23.4 + 126.0 + 10.0 = 159.4 gross. Quota Pro = 300. Used ≈ 53.13%.
	want := 159.4 / 300 * 100
	if abs(m.UsedPercent-want) > 0.01 {
		t.Errorf("used_percent = %v, want ~%v", m.UsedPercent, want)
	}
	if m.RemainingPercent+m.UsedPercent != 100 {
		t.Errorf("used + remaining should be 100, got %v + %v", m.UsedPercent, m.RemainingPercent)
	}
	// Reset is the start of the next UTC month, never in the past.
	if m.ResetAfterSeconds < 0 {
		t.Errorf("reset_after_seconds negative: %v", m.ResetAfterSeconds)
	}
}

func TestFetch_UnknownPlanFallsBackAndWarns(t *testing.T) {
	var warnings []string
	c, _ := newRoutedClient(t,
		loadFixture(t, "user_unknown_plan.json"),
		loadFixture(t, "usage.json"),
		200, 200,
		WithWarn(func(s string) { warnings = append(warnings, s) }),
	)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "future_tier") {
		t.Errorf("warning should name unknown plan: %s", warnings[0])
	}
	if !strings.Contains(warnings[0], "copilot:") {
		t.Errorf("warning should use lowercase copilot: prefix: %s", warnings[0])
	}
	// Fallback quota = 300; same as Pro, so used_percent should match TestFetch_GoldenFlow_Pro.
	want := 159.4 / 300 * 100
	if abs(out.Limits["month"].UsedPercent-want) > 0.01 {
		t.Errorf("fallback quota wrong: used_percent = %v", out.Limits["month"].UsedPercent)
	}
}

func TestFetch_OverageClampsAt100(t *testing.T) {
	overage := []byte(`{"usageItems":[{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":400}]}`)
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), overage, 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits["month"].UsedPercent != 100 {
		t.Errorf("used_percent should be clamped to 100, got %v", out.Limits["month"].UsedPercent)
	}
	if out.Limits["month"].RemainingPercent != 0 {
		t.Errorf("remaining_percent should be 0 at 100%% used")
	}
}

func TestFetch_EmptyUsageItems(t *testing.T) {
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), []byte(`{"usageItems":[]}`), 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits["month"].UsedPercent != 0 {
		t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
	}
}

func TestFetch_SkuMismatchTripsWarning(t *testing.T) {
	// Copilot-product items present but the premium-request SKU is missing —
	// simulates a future GitHub rename of the SKU string.
	body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Premium Request (Renamed)","grossQuantity":100}
	]}`)
	var warnings []string
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 200,
		WithWarn(func(s string) { warnings = append(warnings, s) }),
	)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
}

func TestFetch_NonCopilotProductDoesNotWarn(t *testing.T) {
	// Items exist but none with product=="Copilot" — the product gate must
	// suppress the SKU-mismatch warning entirely.
	body := []byte(`{"usageItems":[
		{"product":"Actions","sku":"Compute","grossQuantity":1000}
	]}`)
	var warnings []string
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 200,
		WithWarn(func(s string) { warnings = append(warnings, s) }),
	)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits["month"].UsedPercent != 0 {
		t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
	}
	if len(warnings) != 0 {
		t.Errorf("expected zero warnings for non-Copilot items, got: %v", warnings)
	}
}

func TestFetch_NonPremiumCopilotItemsWarn(t *testing.T) {
	// Cold-start scenario: a user has Copilot-product activity (chat
	// completions, etc.) but has never made a premium request. The warn
	// should fire because the premium SKU was never observed — matches the
	// real "SKU renamed" signal. Documents intent.
	body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Chat","grossQuantity":50}
	]}`)
	var warnings []string
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 200,
		WithWarn(func(s string) { warnings = append(warnings, s) }),
	)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits["month"].UsedPercent != 0 {
		t.Errorf("expected 0%% used (no premium-request SKU observed), got %v", out.Limits["month"].UsedPercent)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "none matched") {
		t.Errorf("expected SKU-mismatch warning for non-premium Copilot items, got: %v", warnings)
	}
}

func TestFetch_PremiumSkuPresentWithZeroGrossDoesNotWarn(t *testing.T) {
	// Premium SKU was observed (grossQuantity=0) — the !sawPremiumSku gate
	// must suppress the warning even though gross=0.
	body := []byte(`{"usageItems":[
		{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":0}
	]}`)
	var warnings []string
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 200,
		WithWarn(func(s string) { warnings = append(warnings, s) }),
	)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out.Limits["month"].UsedPercent != 0 {
		t.Errorf("expected 0%% used, got %v", out.Limits["month"].UsedPercent)
	}
	if len(warnings) != 0 {
		t.Errorf("expected zero warnings when premium SKU is observed (even at 0), got: %v", warnings)
	}
}

func TestFetch_NonCopilotItemsFiltered(t *testing.T) {
	body := []byte(`{"usageItems":[
		{"product":"Actions","sku":"Compute","grossQuantity":1000},
		{"product":"Copilot","sku":"Copilot Premium Request","grossQuantity":30}
	]}`)
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := 30.0 / 300 * 100 // 10%
	if abs(out.Limits["month"].UsedPercent-want) > 0.01 {
		t.Errorf("non-Copilot items leaked into sum: got %v, want ~%v", out.Limits["month"].UsedPercent, want)
	}
}

func TestFetch_404MissingScope(t *testing.T) {
	body := []byte(`{"message":"Not Found","documentation_url":"...","status":"404"}`)
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), body, 200, 404)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing on 404, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
		t.Errorf("error should embed exact message, got: %v", err)
	}
}

func TestFetch_401IsAuthDenied(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`{"error":"bad creds"}`), nil, 401, 200)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthDenied) {
		t.Errorf("expected ErrAuthDenied, got: %v", err)
	}
}

func TestFetch_503IsTransient(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`down`), nil, 503, 200)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient on 503, got: %v", err)
	}
}

func TestFetch_429IsTransient(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`rl`), nil, 429, 200)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient on 429, got: %v", err)
	}
}

func TestFetch_408IsTransient(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`to`), nil, 408, 200)
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient on 408, got: %v", err)
	}
}

func TestFetch_NetworkErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	c := New(nil, "usage-check-test/0")
	c.doer.Client = srv.Client()
	c.userURL = srv.URL + "/user"
	c.readToken = func(ctx context.Context) (string, error) { return "tok", nil }
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
	c := New(nil, "usage-check-test/0")
	c.doer.Client = srv.Client()
	c.userURL = srv.URL + "/user"
	c.readToken = func(ctx context.Context) (string, error) { return "tok", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Fetch(ctx)
	if errors.Is(err, providers.ErrTransient) {
		t.Errorf("cancelled context should not be transient: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestFetch_TokenMissingIsAuthMissing(t *testing.T) {
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), loadFixture(t, "usage.json"), 200, 200)
	c.readToken = func(ctx context.Context) (string, error) { return "", cred.ErrGitHubTokenNotFound }
	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.GitHubTokenMissingMessage) {
		t.Errorf("expected exact message, got: %v", err)
	}
}

func TestFetch_RequestShape(t *testing.T) {
	c, rec := newRoutedClient(t, loadFixture(t, "user.json"), loadFixture(t, "usage.json"), 200, 200)
	_, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
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
		if !strings.Contains(r.Header.Get("User-Agent"), "usage-check") {
			t.Errorf("User-Agent missing: %q", r.Header.Get("User-Agent"))
		}
	}
	if rec.reqs[0].URL.Path != "/user" {
		t.Errorf("first call path = %s, want /user", rec.reqs[0].URL.Path)
	}
	if !strings.HasPrefix(rec.reqs[1].URL.Path, "/users/REDACTED/") {
		t.Errorf("second call path = %s", rec.reqs[1].URL.Path)
	}
}

func TestFetch_EmptyLogin(t *testing.T) {
	c, _ := newRoutedClient(t, []byte(`{"login":"","plan":{"name":"pro"}}`), loadFixture(t, "usage.json"), 200, 200)
	_, err := c.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "empty login") {
		t.Errorf("expected empty-login error, got: %v", err)
	}
}

func TestFetch_ResetTimeStartOfNextMonth(t *testing.T) {
	c, _ := newRoutedClient(t, loadFixture(t, "user.json"), loadFixture(t, "usage.json"), 200, 200)
	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := out.Limits["month"].ResetsAt
	if r.Day() != 1 || r.Hour() != 0 || r.Minute() != 0 || r.Second() != 0 {
		t.Errorf("resets_at should be midnight on day 1, got %v", r)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
