package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/providers/usagecache"
	"github.com/drogers0/aistat/v2/internal/testutil"
)

// ── test infrastructure ──────────────────────────────────────────────────────

// minUsageBody is a minimal valid usage API response for tests that don't
// validate limit values.
var minUsageBody = []byte(`{"rate_limit":{"primary_window":{"used_percent":50,"limit_window_seconds":18000,"reset_at":1779842256}}}`)

// stubStaticServer returns an httptest.Server that always responds with the
// given status and body. Closed on t.Cleanup.
func stubStaticServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// stubCountingServer returns an httptest.Server and an atomic counter that
// increments on each request.
func stubCountingServer(t *testing.T, status int, body []byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// routingUsageSrv returns an httptest.Server that returns different responses
// based on the Bearer token in the Authorization header. Fails the test for
// unknown tokens.
func routingUsageSrv(t *testing.T, routes map[string]struct {
	status int
	body   []byte
}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok := strings.TrimPrefix(auth, "Bearer ")
		resp, ok := routes[tok]
		if !ok {
			t.Errorf("routingUsageSrv: unexpected bearer token %q", tok)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(resp.status)
		_, _ = w.Write(resp.body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noRefreshServer returns a stub refresh server that fails the test if hit.
func noRefreshServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("refresh server must not be called, but received %s %s", r.Method, r.URL.Path)
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// refreshSuccessBody builds a minimal valid OAuth token response JSON.
func refreshSuccessBody(accessToken, refreshToken string) []byte {
	b, _ := json.Marshal(map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"expires_in":    3600,
	})
	return b
}

// buildClient constructs a Client for testing. usageSrv and refreshSrv are the
// test servers; liveCred is nil to simulate missing credential; store is nil to
// default to empty MemoryStore; lookupID is nil to default to cred.ParseCodexIDToken;
// warnBuf is nil to discard warns; nowFn is nil to use testNow.
//
// Cache isolation: sets HOME (and clears XDG_CACHE_HOME) to a per-test TempDir.
func buildClient(
	t *testing.T,
	usageSrv *httptest.Server,
	refreshSrv *httptest.Server,
	liveCred *cred.Credential,
	store accounts.Store,
	lookupID func(string) (string, string, error),
	warnBuf io.Writer,
	nowFn func() time.Time,
) *Client {
	t.Helper()

	// Isolate cache I/O from the developer's real cache directory.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")

	usageDoer := httpx.NewDoer(usageSrv.Client(), "aistat-test/0", "codex", nil, nil)

	refDoer := httpx.NewDoer(refreshSrv.Client(), "aistat-test/0", "codex", nil, nil)
	rc := newRefreshClient(refDoer)
	rc.endpoint = refreshSrv.URL + "/oauth/token"
	rc.timeout = 5 * time.Second

	if store == nil {
		store = accounts.NewMemoryStore()
	}
	if warnBuf == nil {
		warnBuf = io.Discard
	}
	if nowFn == nil {
		nowFn = func() time.Time { return testNow }
	}
	rc.now = nowFn

	readCred := func(context.Context) (cred.Credential, error) {
		if liveCred == nil {
			return cred.Credential{}, cred.ErrCodexTokenNotFound
		}
		return *liveCred, nil
	}

	warnFn := func(s string) { fmt.Fprintln(warnBuf, s) }

	return &Client{
		doer:             usageDoer,
		endpoint:         usageSrv.URL + "/backend-api/wham/usage",
		refresh:          rc,
		store:            store,
		readCredential:   readCred,
		lookupID:         lookupID,
		warn:             warnBuf,
		now:              nowFn,
		baseTimeout:      10 * time.Second,
		perAccountBudget: 3 * time.Second,
		cache:            usagecache.New("codex", nowFn, warnFn),
	}
}

// singleAccountStore returns a MemoryStore with one pre-populated account
// whose AT matches the given live credential's AT (enabling byte-match in
// reconcile without a LookupID call).
func singleAccountStore(t *testing.T, live *cred.Credential) *accounts.MemoryStore {
	t.Helper()
	store := accounts.NewMemoryStore()
	acct := makeCodexAccount("test-uuid", "test@example.com", live.AccessToken, "fake-rt", 0)
	if err := store.Upsert(context.Background(), acct); err != nil {
		t.Fatalf("store.Upsert: %v", err)
	}
	return store
}

// sortedKeys returns sorted keys of a limits map for deterministic assertions.
func keys(m map[string]providers.Limit) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// storeUUIDs returns sorted UUIDs from the store.
func storeUUIDs(t *testing.T, s accounts.Store) []string {
	t.Helper()
	accts, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	ids := make([]string, 0, len(accts))
	for _, a := range accts {
		ids = append(ids, a.UUID)
	}
	sort.Strings(ids)
	return ids
}

// ── rotateRawBlob tests ───────────────────────────────────────────────────────

func TestRotateRawBlob_HappyPath(t *testing.T) {
	// Build a blob with all three token fields plus an unknown top-level field.
	original := []byte(`{"tokens":{"access_token":"old-at","refresh_token":"old-rt","id_token":"old-it","extra_field":"preserve"},"other_top":"preserved"}`)
	tok := Token{
		AccessToken:  "new-at",
		RefreshToken: "new-rt",
		IDToken:      "new-it",
	}
	out, err := rotateRawBlob(original, tok)
	if err != nil {
		t.Fatalf("rotateRawBlob: %v", err)
	}
	// Verify updated fields.
	if got := extractIDToken(out); got != "new-it" {
		t.Errorf("id_token = %q, want %q", got, "new-it")
	}
	at := parseStoredRaw(accounts.Account{RawBlob: out}).Tokens.AccessToken
	if at != "new-at" {
		t.Errorf("access_token = %q, want %q", at, "new-at")
	}
	rt := parseStoredRaw(accounts.Account{RawBlob: out}).Tokens.RefreshToken
	if rt != "new-rt" {
		t.Errorf("refresh_token = %q, want %q", rt, "new-rt")
	}
	// Unknown fields preserved.
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["other_top"] != "preserved" {
		t.Errorf("other_top = %v, want %q", m["other_top"], "preserved")
	}
	tokens, _ := m["tokens"].(map[string]any)
	if tokens["extra_field"] != "preserve" {
		t.Errorf("tokens.extra_field = %v, want %q", tokens["extra_field"], "preserve")
	}
}

func TestRotateRawBlob_NoIDToken(t *testing.T) {
	// Token.IDToken == "" → id_token deleted so StoredExpiresAt returns 0,
	// preventing repeated refresh triggers on a stale near-expiry claim.
	original := []byte(`{"tokens":{"access_token":"old-at","refresh_token":"old-rt","id_token":"stale-jwt"}}`)
	tok := Token{
		AccessToken:  "new-at",
		RefreshToken: "new-rt",
		IDToken:      "",
	}
	out, err := rotateRawBlob(original, tok)
	if err != nil {
		t.Fatalf("rotateRawBlob: %v", err)
	}
	if got := extractIDToken(out); got != "" {
		t.Errorf("id_token = %q, want \"\" (stale id_token must be cleared)", got)
	}
}

func TestRotateRawBlob_MalformedJSON(t *testing.T) {
	_, err := rotateRawBlob([]byte(`not json`), Token{AccessToken: "x"})
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestRotateRawBlob_MissingTokensObject(t *testing.T) {
	_, err := rotateRawBlob([]byte(`{"other":"field"}`), Token{AccessToken: "x"})
	if err == nil {
		t.Fatal("expected error for missing tokens object")
	}
	if !strings.Contains(err.Error(), "tokens missing or wrong type") {
		t.Errorf("error = %q, want 'tokens missing or wrong type'", err.Error())
	}
}

// ── windowLabel tests ─────────────────────────────────────────────────────────

func TestWindowLabel_FiveHour(t *testing.T) {
	if got := windowLabel(18000); got != "five_hour" {
		t.Errorf("windowLabel(18000) = %q, want %q", got, "five_hour")
	}
}

func TestWindowLabel_SevenDay(t *testing.T) {
	if got := windowLabel(604800); got != "seven_day" {
		t.Errorf("windowLabel(604800) = %q, want %q", got, "seven_day")
	}
}

func TestWindowLabel_ThirtyDay(t *testing.T) {
	if got := windowLabel(2592000); got != "thirty_day" {
		t.Errorf("windowLabel(2592000) = %q, want %q", got, "thirty_day")
	}
}

func TestWindowLabel_WithinTolerance(t *testing.T) {
	// 17500 is within 5% of 18000 (lo=17100, hi=18900).
	if got := windowLabel(17500); got != "five_hour" {
		t.Errorf("windowLabel(17500) = %q, want %q", got, "five_hour")
	}
}

func TestWindowLabel_LowerBoundaryInclusive(t *testing.T) {
	// exactly 5% below 18000 = 17100
	if got := windowLabel(17100); got != "five_hour" {
		t.Errorf("windowLabel(17100) = %q, want %q", got, "five_hour")
	}
}

func TestWindowLabel_JustBelowLowerBoundary(t *testing.T) {
	if got := windowLabel(17099); got != "window_17099s" {
		t.Errorf("windowLabel(17099) = %q, want %q", got, "window_17099s")
	}
}

func TestWindowLabel_UpperBoundaryInclusive(t *testing.T) {
	// exactly 5% above 18000 = 18900
	if got := windowLabel(18900); got != "five_hour" {
		t.Errorf("windowLabel(18900) = %q, want %q", got, "five_hour")
	}
}

func TestWindowLabel_JustAboveUpperBoundary(t *testing.T) {
	if got := windowLabel(18901); got != "window_18901s" {
		t.Errorf("windowLabel(18901) = %q, want %q", got, "window_18901s")
	}
}

func TestWindowLabel_UnknownDuration(t *testing.T) {
	if got := windowLabel(86400); got != "window_86400s" {
		t.Errorf("windowLabel(86400) = %q, want %q", got, "window_86400s")
	}
}

func TestWindowLabel_ZeroDuration(t *testing.T) {
	if got := windowLabel(0); got != "window_0s" {
		t.Errorf("windowLabel(0) = %q, want %q", got, "window_0s")
	}
}

// ── original single-account tests (updated for multi-account API) ─────────────

// TestFetch_ResetAfterSecondsTruncated verifies that the sub-second component
// of c.now() is stripped before computing ResetAfterSeconds.
func TestFetch_ResetAfterSecondsTruncated(t *testing.T) {
	frozen := time.Date(2026, 5, 15, 12, 34, 56, 789_000_000, time.UTC)
	resetAt := frozen.Add(3 * time.Hour).Truncate(time.Second).Unix()
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":` +
		strconv.FormatInt(resetAt, 10) + `}}}`)

	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, body)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := 3 * 3600
	if got := out.Accounts[0].Limits["five_hour"].ResetAfterSeconds; got != want {
		t.Errorf("ResetAfterSeconds = %d, want %d (regression: removing .Truncate yields want-1)", got, want)
	}
}

// TestFetch_GoldenFixture_TwoWindows verifies the usage.json fixture is parsed
// to two limits with the correct labels (18000→"five_hour", 604800→"seven_day").
func TestFetch_GoldenFixture_TwoWindows(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, testutil.LoadFixture(t, "usage.json"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(out.Accounts))
	}
	limits := out.Accounts[0].Limits
	if len(limits) != 2 {
		t.Fatalf("expected 2 limits, got %d: %v", len(limits), limits)
	}
	for _, want := range []string{"five_hour", "seven_day"} {
		if _, ok := limits[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	if _, ok := limits["code_review_seven_day"]; ok {
		t.Error("code_review_seven_day should be absent when API returns null")
	}
	fh := limits["five_hour"]
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

// TestFetch_CodeReviewIncluded verifies that code_review_rate_limit is included
// when present with a non-zero reset_at.
func TestFetch_CodeReviewIncluded(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, testutil.LoadFixture(t, "usage_with_code_review.json"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Accounts[0].Limits["code_review_seven_day"]; !ok {
		t.Fatalf("code_review_seven_day should be present, got %v", out.Accounts[0].Limits)
	}
	cr := out.Accounts[0].Limits["code_review_seven_day"]
	if cr.UsedPercent != 33 {
		t.Errorf("code_review used_percent = %v, want 33", cr.UsedPercent)
	}
}

// TestFetch_RequestShape verifies that the usage request has the correct method,
// path, Authorization, and User-Agent headers.
func TestFetch_RequestShape(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	var got http.Request
	usageSrv := testutil.NewStubServer(t, testutil.LoadFixture(t, "usage.json"), 200, &got)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

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

// TestFetch_MissingRateLimit verifies that a response missing rate_limit produces
// a per-account error (not a provider-level error).
func TestFetch_MissingRateLimit(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, []byte(`{}`))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no provider-level error, got: %v", err)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(out.Accounts))
	}
	if !strings.Contains(out.Accounts[0].Error, "missing rate_limit") {
		t.Errorf("Accounts[0].Error = %q, want 'missing rate_limit'", out.Accounts[0].Error)
	}
}

// TestFetch_NullRateLimit verifies that rate_limit:null also produces a
// per-account error.
func TestFetch_NullRateLimit(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, []byte(`{"rate_limit":null}`))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("expected no provider-level error, got: %v", err)
	}
	if !strings.Contains(out.Accounts[0].Error, "missing rate_limit") {
		t.Errorf("Accounts[0].Error = %q, want 'missing rate_limit'", out.Accounts[0].Error)
	}
}

// TestFetch_PrimaryWindowResetAtZero_Skipped verifies that primary_window with
// reset_at=0 is skipped; secondary_window remains present as "seven_day".
func TestFetch_PrimaryWindowResetAtZero_Skipped(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":18000,"reset_at":0},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":1780429056}}}`)
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, body)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Accounts[0].Limits["five_hour"]; ok {
		t.Errorf("five_hour must be skipped when primary_window.reset_at == 0")
	}
	if _, ok := out.Accounts[0].Limits["seven_day"]; !ok {
		t.Errorf("seven_day should still be present alongside skipped five_hour")
	}
}

// TestFetch_SecondaryWindowResetAtZero_Skipped verifies that secondary_window
// with reset_at=0 is skipped; primary_window remains as "five_hour".
func TestFetch_SecondaryWindowResetAtZero_Skipped(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":0}}}`)
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, body)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Accounts[0].Limits["seven_day"]; ok {
		t.Errorf("seven_day must be skipped when secondary_window.reset_at == 0")
	}
	if _, ok := out.Accounts[0].Limits["five_hour"]; !ok {
		t.Errorf("five_hour should still be present alongside skipped seven_day")
	}
}

// TestFetch_CodeReviewSkippedOnZeroResetAt verifies that code_review_rate_limit
// is skipped when reset_at == 0.
func TestFetch_CodeReviewSkippedOnZeroResetAt(t *testing.T) {
	body := []byte(`{"rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000,"reset_at":1779842256},"secondary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_at":1780429056}},"code_review_rate_limit":{"used_percent":0,"limit_window_seconds":0,"reset_at":0}}`)
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, body)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Accounts[0].Limits["code_review_seven_day"]; ok {
		t.Errorf("code_review_seven_day must be skipped when reset_at == 0")
	}
}

// TestFetch_Status418IsBareError verifies that HTTP 418 produces a per-account
// error (not ErrTransient), and the provider-level error is nil.
func TestFetch_Status418IsBareError(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 418, []byte("teapot"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		// 418 is not transient; no provider-level error expected.
		t.Fatalf("provider-level error = %v, want nil (418 is bare per-account error)", err)
	}
	if !strings.Contains(out.Accounts[0].Error, "HTTP 418") {
		t.Errorf("Accounts[0].Error = %q, want mention of HTTP 418", out.Accounts[0].Error)
	}
}

// TestFetch_NonJSON200 verifies that a non-JSON 200 response produces a
// per-account error.
func TestFetch_NonJSON200(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, []byte("<html>oops</html>"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("provider-level error = %v, want nil", err)
	}
	if !strings.Contains(out.Accounts[0].Error, "non-JSON") {
		t.Errorf("Accounts[0].Error = %q, want 'non-JSON'", out.Accounts[0].Error)
	}
}

// TestFetch_TokenMissingIsAuthMissing verifies that when no live credential and
// no stored accounts exist, Fetch returns ErrAuthMissing with CodexTokenMissingMessage.
func TestFetch_TokenMissingIsAuthMissing(t *testing.T) {
	usageSrv := stubStaticServer(t, 200, []byte(`{}`))
	// liveCred == nil → readCredential returns ErrCodexTokenNotFound → (nil, nil).
	// store == nil → empty MemoryStore → no accounts → ErrAuthMissing.
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, nil, noLookupCall(t), nil, nil)

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), cred.CodexTokenMissingMessage) {
		t.Errorf("expected exact message, got: %v", err)
	}
}

// TestFetch_TokenGenericErrorPropagated verifies that non-missing errors from
// readCredential propagate directly from Fetch.
func TestFetch_TokenGenericErrorPropagated(t *testing.T) {
	sentinel := errors.New("some auth.json failure")
	usageSrv := stubStaticServer(t, 200, []byte(`{}`))
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, nil, noLookupCall(t), nil, nil)
	// Override readCredential to return the sentinel error (not ErrCodexTokenNotFound).
	c.readCredential = func(context.Context) (cred.Credential, error) {
		return cred.Credential{}, sentinel
	}

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, sentinel) {
		t.Errorf("generic error should propagate, got: %v", err)
	}
}

// ── multi-account Fetch tests ─────────────────────────────────────────────────

// TestFetch_TwoAccount_BothSucceed verifies that two stored accounts both get
// usage fetched and appear in out.Accounts, with one marked active.
func TestFetch_TwoAccount_BothSucceed(t *testing.T) {
	liveA := makeCodexCred("tok-a", "ref-a", 0)
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)

	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), liveA, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := int(count.Load()); got != 2 {
		t.Errorf("usage calls = %d, want 2", got)
	}
	if len(out.Accounts) != 2 {
		t.Fatalf("Accounts len = %d, want 2", len(out.Accounts))
	}
	// Sorted: active first, then by email.
	if !out.Accounts[0].Active {
		t.Error("Accounts[0].Active = false, want true (active first)")
	}
	if out.Accounts[1].Active {
		t.Error("Accounts[1].Active = true, want false")
	}
}

// TestFetch_TwoAccount_EmailOrdering verifies that when no account is active
// (live credential absent), results are sorted by email ascending.
func TestFetch_TwoAccount_EmailOrdering(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "zed@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "alice@example.com", "tok-b", "ref-b", 0)

	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv := stubStaticServer(t, 200, minUsageBody)
	// liveCred nil → no active account, both fetched.
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 2 {
		t.Fatalf("Accounts len = %d, want 2", len(out.Accounts))
	}
	// Sorted by email ascending (no active).
	emails := []string{out.Accounts[0].Email, out.Accounts[1].Email}
	if emails[0] != "alice@example.com" || emails[1] != "zed@example.com" {
		t.Errorf("email order = %v, want [alice, zed]", emails)
	}
}

// TestFetch_StoredRefreshRejected verifies that when an account's refresh
// token is rejected (ErrRefreshRejected), that account gets a per-account
// error and the other account succeeds. Provider-level error is nil.
func TestFetch_StoredRefreshRejected(t *testing.T) {
	// Account A: active, no refresh needed (ExpiresAt=0).
	// Account B: near-expiry → refresh is attempted → rejected.
	frozen := testNow
	nearExpirySec := frozen.Add(10 * time.Second).Unix() // within 30s refreshSkew

	liveA := makeCodexCred("tok-a", "ref-a", 0)
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", nearExpirySec)

	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	invalidGrantBody := []byte(`{"error":"invalid_grant"}`)
	refreshSrv := stubStaticServer(t, 400, invalidGrantBody)
	usageSrv, count := stubCountingServer(t, 200, minUsageBody)

	c := buildClient(t, usageSrv, refreshSrv, liveA, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("provider-level error = %v, want nil (one account succeeded)", err)
	}
	if got := int(count.Load()); got != 1 {
		t.Errorf("usage calls = %d, want 1 (only account A)", got)
	}
	// Find account B in output (bob@example.com).
	var bobAR *providers.AccountResult
	for i := range out.Accounts {
		if out.Accounts[i].Email == "bob@example.com" {
			bobAR = &out.Accounts[i]
		}
	}
	if bobAR == nil {
		t.Fatal("bob@example.com not found in output")
	}
	if !strings.Contains(bobAR.Error, "codex login") {
		t.Errorf("bob.Error = %q, want 'codex login' hint", bobAR.Error)
	}
}

// TestFetch_AllTransient verifies that when all accounts return transient errors,
// Fetch returns ErrTransient at the provider level.
func TestFetch_AllTransient(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)

	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv := stubStaticServer(t, 503, []byte("service unavailable"))
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient when all accounts fail transiently, got: %v", err)
	}
}

// TestFetch_LivePresentLookupFails verifies that when the live credential has a
// syntactically invalid JWT id_token (non-empty but unparseable), extractIDToken
// returns the non-empty string, the empty-string guard does NOT trigger,
// LookupID calls ParseCodexIDToken which fails, and reconcile falls back to
// LiveUnstored → "(live Codex account)" row with usage fetched.
func TestFetch_LivePresentLookupFails(t *testing.T) {
	// Build a blob with an invalid (non-parseable) JWT.
	raw := []byte(`{"tokens":{"access_token":"tok-live","refresh_token":"ref-live","id_token":"not.a.jwt"}}`)
	live := &cred.Credential{
		AccessToken:  "tok-live",
		RefreshToken: "ref-live",
		Raw:          raw,
	}

	usageSrv := stubStaticServer(t, 200, minUsageBody)
	// Empty store → no byte-match → falls back to LookupID → fails → LiveUnstored.
	c := buildClient(t, usageSrv, noRefreshServer(t), live, nil, nil, nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("Accounts len = %d, want 1", len(out.Accounts))
	}
	if out.Accounts[0].Email != "(live Codex account)" {
		t.Errorf("Email = %q, want %q", out.Accounts[0].Email, "(live Codex account)")
	}
	if !out.Accounts[0].Active {
		t.Error("LiveUnstored row should be Active=true")
	}
}

// TestFetch_LiveAbsentZeroStored verifies that with no live credential and no
// stored accounts, Fetch returns ErrAuthMissing.
func TestFetch_LiveAbsentZeroStored(t *testing.T) {
	usageSrv := stubStaticServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, nil, noLookupCall(t), nil, nil)

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrAuthMissing) {
		t.Errorf("expected ErrAuthMissing, got: %v", err)
	}
}

// TestFetch_LiveAbsentStoredPresent verifies that with no live credential but
// stored accounts, all rows appear as non-active.
func TestFetch_LiveAbsentStoredPresent(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)

	usageSrv := stubStaticServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("Accounts len = %d, want 1", len(out.Accounts))
	}
	if out.Accounts[0].Active {
		t.Error("account should be non-active when live credential is absent")
	}
}

// TestFetch_RotatedTokensPersisted verifies that when a refresh succeeds, the
// new tokens are persisted to the store before the next usage fetch.
func TestFetch_RotatedTokensPersisted(t *testing.T) {
	frozen := testNow
	nearExpirySec := frozen.Add(10 * time.Second).Unix()

	// Live credential carries the near-expiry id_token; reconcile byte-matches
	// and propagates the live blob (with id_token) to the stored slot, ensuring
	// StoredExpiresAt returns the near-expiry value and refresh is triggered.
	live := makeCodexCred("tok-a", "ref-a", nearExpirySec)
	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", nearExpirySec)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	refreshSrv := stubStaticServer(t, 200, refreshSuccessBody("tok-a-new", "ref-a-new"))
	usageSrv := stubStaticServer(t, 200, minUsageBody)

	c := buildClient(t, usageSrv, refreshSrv, live, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) == 0 {
		t.Fatal("expected at least one account")
	}

	// Verify tokens were persisted to the store.
	storedAccts, _ := store.List(context.Background())
	if len(storedAccts) != 1 {
		t.Fatalf("store has %d accounts, want 1", len(storedAccts))
	}
	if got := StoredAccessToken(storedAccts[0]); got != "tok-a-new" {
		t.Errorf("stored access_token = %q, want %q", got, "tok-a-new")
	}
	if got := StoredRefreshToken(storedAccts[0]); got != "ref-a-new" {
		t.Errorf("stored refresh_token = %q, want %q", got, "ref-a-new")
	}
}

// TestFetch_ReconcileUpsertBeforeUsageFetches verifies that reconcile persist
// (upsert) happens before usage fetches. A new account (inserted by reconcile)
// must appear in the store even if the subsequent usage fetch fails.
func TestFetch_ReconcileUpsertBeforeUsageFetches(t *testing.T) {
	// Live credential with a far-future expiry (1h) to avoid triggering the
	// near-expiry refresh path (30s skew). Purpose of this test is to verify
	// the upsert-before-usage-fetch ordering, not refresh behavior.
	sub := "uuid-new"
	farFutureSec := testNow.Unix() + 3600
	live := makeCodexCred("tok-new", "ref-new", farFutureSec)

	usageSrv := stubStaticServer(t, 503, []byte("fail")) // usage fails
	// Use fixedLookup so the LookupID step succeeds and inserts the slot.
	c := buildClient(t, usageSrv, noRefreshServer(t), live, nil, fixedLookup(sub, "new@example.com"), nil, nil)

	_, _ = c.Fetch(context.Background())

	// The slot must be in the store even though usage failed.
	if ids := storeUUIDs(t, c.store); len(ids) == 0 || ids[0] != sub {
		t.Errorf("store UUIDs = %v, want [%s]", ids, sub)
	}
}

// TestFetch_CacheHitRecomputesResetAfter verifies that a cache hit recomputes
// ResetAfterSeconds relative to the current time.
func TestFetch_CacheHitRecomputesResetAfter(t *testing.T) {
	live := makeCodexCred("tok-a", "ref-a", 0)
	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	// The usage fixture has reset_at=1779842256 (~5h18m future from frozen).
	frozen := time.Unix(1779842256-19077, 0).UTC() // exactly 19077s before reset
	laterTime := frozen.Add(9000 * time.Second)    // 10077s before reset

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	// First fetch: populates cache.
	out1, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("expected 1 usage call after first Fetch, got %d", count.Load())
	}
	ras1 := out1.Accounts[0].Limits["five_hour"].ResetAfterSeconds

	// Advance time and fetch again (cache hit expected within 90s TTL).
	c.now = func() time.Time { return frozen.Add(5 * time.Second) }
	out2, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Errorf("expected cache hit on second Fetch (no new HTTP call), got %d total calls", count.Load())
	}
	ras2 := out2.Accounts[0].Limits["five_hour"].ResetAfterSeconds
	// ResetAfterSeconds should be 5s less on the second call.
	if ras2 >= ras1 {
		t.Errorf("ResetAfterSeconds did not decrease: first=%d, second=%d", ras1, ras2)
	}
	_ = laterTime // suppress unused warning
}

// TestFetch_LiveUnstoredBypassesCache verifies that the LiveUnstored row always
// calls fetchLimitsFresh (not fetchLimitsCached, which would need a UUID key).
func TestFetch_LiveUnstoredBypassesCache(t *testing.T) {
	// Invalid JWT → lookup fails → LiveUnstored.
	raw := []byte(`{"tokens":{"access_token":"tok-live","id_token":"x.y.z"}}`)
	live := &cred.Credential{AccessToken: "tok-live", Raw: raw}

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, nil, nil, nil, nil)

	_, _ = c.Fetch(context.Background())
	// Must have made a fresh fetch (no UUID to cache on).
	if count.Load() < 1 {
		t.Error("expected at least one fresh usage fetch for LiveUnstored row")
	}
}

// TestFetch_LiveUnstoredUsageFetchFails verifies that when the LiveUnstored row's
// usage fetch fails, the row appears with "(live Codex account)" email and an error.
func TestFetch_LiveUnstoredUsageFetchFails(t *testing.T) {
	raw := []byte(`{"tokens":{"access_token":"tok-live","id_token":"x.y.z"}}`)
	live := &cred.Credential{AccessToken: "tok-live", Raw: raw}

	usageSrv := stubStaticServer(t, 503, []byte("fail"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, nil, nil, nil, nil)

	out, err := c.Fetch(context.Background())
	// ErrTransient because zero succeeded, one transient failure.
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient, got: %v", err)
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("Accounts len = %d, want 1", len(out.Accounts))
	}
	if out.Accounts[0].Email != "(live Codex account)" {
		t.Errorf("Email = %q, want %q", out.Accounts[0].Email, "(live Codex account)")
	}
	if out.Accounts[0].Error == "" {
		t.Error("expected per-account error for failed fetch, got empty")
	}
}

// TestFetch_LiveUnstoredTokenInvalidatedTightens verifies the LiveUnstored row
// applies the revoked-token tightening too: a 401 token_invalidated body on the
// live, unreconciled credential surfaces msgTokensRevoked, not the raw HTTP body.
func TestFetch_LiveUnstoredTokenInvalidatedTightens(t *testing.T) {
	raw := []byte(`{"tokens":{"access_token":"tok-live","id_token":"x.y.z"}}`)
	live := &cred.Credential{AccessToken: "tok-live", Raw: raw}

	invalidatedBody := []byte(`{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated."}}`)
	usageSrv := stubStaticServer(t, 401, invalidatedBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, nil, nil, nil, nil)

	out, _ := c.Fetch(context.Background())
	if len(out.Accounts) != 1 {
		t.Fatalf("Accounts len = %d, want 1", len(out.Accounts))
	}
	ar := out.Accounts[0]
	if ar.Email != "(live Codex account)" {
		t.Errorf("Email = %q, want %q", ar.Email, "(live Codex account)")
	}
	if !strings.Contains(ar.Error, msgTokensRevoked) {
		t.Errorf("ar.Error = %q, want substring %q", ar.Error, msgTokensRevoked)
	}
	if strings.Contains(ar.Error, "HTTP 401") {
		t.Errorf("ar.Error should not contain raw 'HTTP 401' prefix: %q", ar.Error)
	}
}

// TestFetch_AuthDeniedOnly_NilError verifies that when all accounts return 401
// (ErrAuthDenied), the provider-level error is nil (not ErrTransient). D8
// symmetry: ErrTransient requires at least one transient failure.
func TestFetch_AuthDeniedOnly_NilError(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)

	usageSrv := stubStaticServer(t, 401, []byte("unauthorized"))
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Errorf("expected nil provider-level error for auth-denied-only failures, got: %v", err)
	}
	if out.Accounts[0].Error == "" {
		t.Error("expected per-account error, got empty")
	}
}

// TestFetch_MixedTransientAndAuthDenied verifies that when one account has a
// transient error and another has auth-denied, and zero succeeded, the provider
// returns ErrTransient (D8: at least one transient failure with zero success).
func TestFetch_MixedTransientAndAuthDenied(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	// Route: tok-a → 503 (transient), tok-b → 401 (auth denied).
	usageSrv := routingUsageSrv(t, map[string]struct {
		status int
		body   []byte
	}{
		"tok-a": {503, []byte("unavailable")},
		"tok-b": {401, []byte("unauthorized")},
	})
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient (mixed failures, zero success), got: %v", err)
	}
}

// TestFetch_RefreshTransient_ErrTransient verifies that a transient refresh
// failure produces a per-account error and ErrTransient at the provider level.
func TestFetch_RefreshTransient_ErrTransient(t *testing.T) {
	frozen := testNow
	nearExpirySec := frozen.Add(10 * time.Second).Unix()

	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", nearExpirySec)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	refreshSrv := stubStaticServer(t, 503, []byte("unavailable"))
	usageSrv := stubStaticServer(t, 200, minUsageBody)

	c := buildClient(t, usageSrv, refreshSrv, nil, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	_, err := c.Fetch(context.Background())
	if !errors.Is(err, providers.ErrTransient) {
		t.Errorf("expected ErrTransient from transient refresh, got: %v", err)
	}
}

// TestFetch_TwoAccount_CacheHit verifies that both accounts are served from
// cache on a second Fetch call (no HTTP usage calls on second Fetch).
func TestFetch_TwoAccount_CacheHit(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil, nil)

	// First Fetch: populates cache for both accounts.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatalf("expected 2 usage calls on first Fetch, got %d", count.Load())
	}

	// Second Fetch: both served from cache.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Errorf("expected 2 total usage calls (cache hit on second Fetch), got %d", count.Load())
	}
}

// TestFetch_RotatedTokenSharesCacheEntry verifies that after a refresh rotates
// the access token, the cache entry is still keyed by UUID (not token), so the
// second Fetch hits the cache without a second usage call. Also verifies that
// rotateRawBlob clears id_token when the refresh response omits it, so
// StoredExpiresAt returns 0 and the second Fetch skips the refresh path.
func TestFetch_RotatedTokenSharesCacheEntry(t *testing.T) {
	frozen := testNow
	nearExpirySec := frozen.Add(10 * time.Second).Unix()

	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", nearExpirySec)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	refreshSrv, refreshCount := stubCountingServer(t, 200, refreshSuccessBody("tok-a-new", "ref-a-new"))
	usageSrv, usageCount := stubCountingServer(t, 200, minUsageBody)

	c := buildClient(t, usageSrv, refreshSrv, nil, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	// First Fetch: refresh fires (near expiry), usage fetched and cached under uuid-a.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if usageCount.Load() != 1 {
		t.Fatalf("expected 1 usage call on first Fetch, got %d", usageCount.Load())
	}
	if refreshCount.Load() != 1 {
		t.Fatalf("expected 1 refresh call on first Fetch, got %d", refreshCount.Load())
	}

	// Second Fetch: rotated token has no id_token (cleared by rotateRawBlob when
	// refresh response omits it), so StoredExpiresAt=0 → no refresh; UUID-keyed
	// cache still holds the entry → no usage HTTP call.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if usageCount.Load() != 1 {
		t.Errorf("expected cache hit on second Fetch (UUID-keyed), got %d total usage calls", usageCount.Load())
	}
	if refreshCount.Load() != 1 {
		t.Errorf("expected no second refresh (id_token cleared → StoredExpiresAt=0), got %d total refresh calls", refreshCount.Load())
	}
}

// TestNew_WithCacheBypass verifies that WithCacheBypass sets the cacheBypass field.
func TestNew_WithCacheBypass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", "")
	c := New(nil, "test/0", WithCacheBypass(true))
	if !c.CacheBypassEnabled() {
		t.Error("CacheBypassEnabled() = false, want true after WithCacheBypass(true)")
	}
}

// ── FetchForSwitch tests ──────────────────────────────────────────────────────

// TestFetchForSwitch_Happy verifies that FetchForSwitch returns non-active
// accounts with usage data; the active account is excluded.
func TestFetchForSwitch_Happy(t *testing.T) {
	liveA := makeCodexCred("tok-a", "ref-a", 0)
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), liveA, store, noLookupCall(t), nil, nil)

	results, err := c.FetchForSwitch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Only non-active account (B) should be returned.
	if len(results) != 1 {
		t.Fatalf("results len = %d, want 1", len(results))
	}
	if results[0].UUID != "uuid-b" {
		t.Errorf("result UUID = %q, want %q", results[0].UUID, "uuid-b")
	}
	if results[0].Active {
		t.Error("FetchForSwitch result.Active must be false")
	}
	if int(count.Load()) != 1 {
		t.Errorf("usage calls = %d, want 1 (only account B)", count.Load())
	}
}

// TestFetchForSwitch_StoredTokenRejected verifies that an account with a
// 401-rejected stored credential is excluded from the results with a warn.
func TestFetchForSwitch_StoredTokenRejected(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)

	usageSrv := stubStaticServer(t, 401, []byte("unauthorized"))
	var warnBuf bytes.Buffer
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), &warnBuf, nil)

	results, err := c.FetchForSwitch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results (account excluded), got %d", len(results))
	}
	if !strings.Contains(warnBuf.String(), "excluded from auto-pick") {
		t.Errorf("warn = %q, want 'excluded from auto-pick'", warnBuf.String())
	}
}

// TestFetchForSwitch_TransientExclusion verifies that transient failures also
// exclude the account from FetchForSwitch results.
func TestFetchForSwitch_TransientExclusion(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)

	usageSrv := stubStaticServer(t, 503, []byte("unavailable"))
	var warnBuf bytes.Buffer
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), &warnBuf, nil)

	results, err := c.FetchForSwitch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results (transient → excluded), got %d", len(results))
	}
}

// TestFetchForSwitch_NeverRefreshesNeverMutatesStore verifies that FetchForSwitch
// does not refresh tokens and does not mutate the store.
func TestFetchForSwitch_NeverRefreshesNeverMutatesStore(t *testing.T) {
	frozen := testNow
	nearExpirySec := frozen.Add(10 * time.Second).Unix()

	// Account with near-expiry token (would normally trigger refresh in Fetch).
	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", nearExpirySec)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	// Snapshot store state before call.
	snapshot, _ := store.List(context.Background())

	usageSrv := stubStaticServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), nil,
		func() time.Time { return frozen })

	// noRefreshServer will fail the test if refresh is attempted.
	if _, err := c.FetchForSwitch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify store is unchanged.
	after, _ := store.List(context.Background())
	if len(after) != len(snapshot) {
		t.Errorf("store len changed: %d → %d", len(snapshot), len(after))
	}
	if string(after[0].RawBlob) != string(snapshot[0].RawBlob) {
		t.Error("store RawBlob mutated by FetchForSwitch")
	}
}

// TestFetchForSwitch_ActiveUUIDUnresolvable verifies that when the live
// credential has an unparseable id_token (non-empty, fails ParseCodexIDToken),
// ResolveActiveUUID returns ("", nil) and all stored accounts are treated as
// non-active candidates.
func TestFetchForSwitch_ActiveUUIDUnresolvable(t *testing.T) {
	raw := []byte(`{"tokens":{"access_token":"tok-live","id_token":"not.a.jwt"}}`)
	live := &cred.Credential{AccessToken: "tok-live", Raw: raw}

	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, nil, nil, nil)

	results, err := c.FetchForSwitch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Both accounts should be included (none identified as active).
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Active {
			t.Errorf("account %s.Active = true, all should be false", r.Email)
		}
	}
	if int(count.Load()) != 2 {
		t.Errorf("usage calls = %d, want 2", count.Load())
	}
}

// TestFetchForSwitch_UsesCacheHit verifies that FetchForSwitch uses the cache
// populated by a prior Fetch call (same UUID-keyed cache).
func TestFetchForSwitch_UsesCacheHit(t *testing.T) {
	acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	acctB := makeCodexAccount("uuid-b", "bob@example.com", "tok-b", "ref-b", 0)
	liveA := makeCodexCred("tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acctA)
	_ = store.Upsert(context.Background(), acctB)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), liveA, store, noLookupCall(t), nil, nil)

	// First call: Fetch populates cache for both accounts.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Fatalf("Fetch: expected 2 usage calls, got %d", count.Load())
	}

	// FetchForSwitch: should hit cache for account B (non-active).
	if _, err := c.FetchForSwitch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 2 {
		t.Errorf("FetchForSwitch: expected cache hit (no new HTTP call), got %d total calls", count.Load())
	}
}

// ── FetchUsage tests ──────────────────────────────────────────────────────────

// TestFetchUsage_UsesCacheHit verifies that FetchUsage reads from cache when
// a non-expired entry exists for the given UUID.
func TestFetchUsage_UsesCacheHit(t *testing.T) {
	live := makeCodexCred("tok-a", "ref-a", 0)
	acct := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
	store := accounts.NewMemoryStore()
	_ = store.Upsert(context.Background(), acct)

	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	// Fetch populates the cache.
	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("expected 1 call after Fetch, got %d", count.Load())
	}

	// FetchUsage should hit cache.
	if _, err := c.FetchUsage(context.Background(), "tok-a", "uuid-a"); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Errorf("expected cache hit for FetchUsage, got %d total calls", count.Load())
	}
}

// TestFetchUsage_EmptyUUIDBypassesCache verifies that passing an empty UUID
// skips cache and always calls the usage endpoint.
func TestFetchUsage_EmptyUUIDBypassesCache(t *testing.T) {
	usageSrv, count := stubCountingServer(t, 200, minUsageBody)
	c := buildClient(t, usageSrv, noRefreshServer(t), nil, nil, noLookupCall(t), nil, nil)

	for i := 0; i < 3; i++ {
		if _, err := c.FetchUsage(context.Background(), "tok-x", ""); err != nil {
			t.Fatal(err)
		}
	}
	if count.Load() != 3 {
		t.Errorf("expected 3 fresh fetches for empty UUID, got %d", count.Load())
	}
}

// ── slot-vs-duration fixture tests ────────────────────────────────────────────

// TestFetch_FreeAccount_WeeklyInPrimary verifies that a free account whose
// weekly cap appears in the primary_window slot is labeled "seven_day" (not
// "five_hour"). Fixes the slot-vs-duration bug (D5).
func TestFetch_FreeAccount_WeeklyInPrimary(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, testutil.LoadFixture(t, "usage_free_weekly_primary.json"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	limits := out.Accounts[0].Limits
	if _, ok := limits["five_hour"]; ok {
		t.Error("five_hour must not appear for a free account with weekly primary_window")
	}
	if _, ok := limits["seven_day"]; !ok {
		t.Errorf("seven_day must appear when primary_window.limit_window_seconds=604800, got: %v", keys(limits))
	}
}

// TestFetch_PaidAccount_BothWindows verifies that a paid account with both
// primary (18000→"five_hour") and secondary (604800→"seven_day") windows is
// labeled correctly.
func TestFetch_PaidAccount_BothWindows(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, testutil.LoadFixture(t, "usage_paid_both_windows.json"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	limits := out.Accounts[0].Limits
	if _, ok := limits["five_hour"]; !ok {
		t.Errorf("five_hour must appear for primary_window.limit_window_seconds=18000, got: %v", keys(limits))
	}
	if _, ok := limits["seven_day"]; !ok {
		t.Errorf("seven_day must appear for secondary_window.limit_window_seconds=604800, got: %v", keys(limits))
	}
}

// TestFetch_UnknownDurationBucket verifies that a window with an unknown
// limit_window_seconds (86400) falls through to "window_86400s".
func TestFetch_UnknownDurationBucket(t *testing.T) {
	live := makeCodexCred("fake-jwt", "fake-rt", 0)
	store := singleAccountStore(t, live)

	usageSrv := stubStaticServer(t, 200, testutil.LoadFixture(t, "usage_unknown_duration.json"))
	c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

	out, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	limits := out.Accounts[0].Limits
	if _, ok := limits["window_86400s"]; !ok {
		t.Errorf("window_86400s must appear for unknown limit_window_seconds=86400, got: %v", keys(limits))
	}
}

// ── refreshErrorMessage tests (Step 5 / B#3) ──────────────────────────────────

// TestRefreshErrorMessage_StaleRefreshToken verifies that the real upstream
// "already been used" body returns msgStaleRefresh even when ErrRefreshRejected
// is also wrapped (the strings.Contains branch must win over errors.Is).
func TestRefreshErrorMessage_StaleRefreshToken(t *testing.T) {
err := fmt.Errorf("%w: HTTP 401 from https://auth.openai.com/oauth/token: Your refresh token has already been used to generate a new access token. Please try signing in again.", ErrRefreshRejected)
got := refreshErrorMessage(err)
if got != msgStaleRefresh {
t.Errorf("refreshErrorMessage = %q, want %q", got, msgStaleRefresh)
}
}

// TestRefreshErrorMessage_PassthroughKeepsExisting verifies that ErrRefreshRejected
// and ErrRefreshEndpointBroken still return their existing strings.
func TestRefreshErrorMessage_PassthroughKeepsExisting(t *testing.T) {
rejErr := fmt.Errorf("%w: HTTP 400 bad grant", ErrRefreshRejected)
got := refreshErrorMessage(rejErr)
if !strings.Contains(got, "account credential expired") {
t.Errorf("ErrRefreshRejected: refreshErrorMessage = %q, want 'account credential expired'", got)
}

brokenErr := fmt.Errorf("%w: HTTP 404 from endpoint", ErrRefreshEndpointBroken)
got2 := refreshErrorMessage(brokenErr)
if !strings.Contains(got2, "refresh endpoint rejected request") {
t.Errorf("ErrRefreshEndpointBroken: refreshErrorMessage = %q, want 'refresh endpoint rejected request'", got2)
}
}

// ── Fetch token_revoked tightening tests (Step 5 / D1) ───────────────────────

// TestFetch_TokenRevokedSurfacesTightString verifies that a 401 token_revoked
// response produces a tightened ar.Error containing msgTokensRevoked, not the
// raw "HTTP 401" prefix.
func TestFetch_TokenRevokedSurfacesTightString(t *testing.T) {
revokedBody := []byte(`{"error":{"code":"token_revoked","message":"Encountered invalidated oauth token"}}`)

acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
store := accounts.NewMemoryStore()
_ = store.Upsert(context.Background(), acctA)
live := makeCodexCred("tok-a", "ref-a", 0)

usageSrv := stubStaticServer(t, 401, revokedBody)
c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

out, _ := c.Fetch(context.Background())
if len(out.Accounts) != 1 {
t.Fatalf("expected 1 account, got %d", len(out.Accounts))
}
ar := out.Accounts[0]
if !strings.Contains(ar.Error, msgTokensRevoked) {
t.Errorf("ar.Error = %q, want substring %q", ar.Error, msgTokensRevoked)
}
if strings.Contains(ar.Error, "HTTP 401") {
t.Errorf("ar.Error should not contain raw 'HTTP 401' prefix: %q", ar.Error)
}
}

// TestFetch_TokenInvalidatedSurfacesTightString verifies the usage endpoint's
// other "dead token" code — token_invalidated, which OpenAI returns in place of
// token_revoked at its discretion — also tightens to msgTokensRevoked end-to-end
// through the HTTP/classifier layer, not just in the isRevokedTokenErr unit test.
func TestFetch_TokenInvalidatedSurfacesTightString(t *testing.T) {
invalidatedBody := []byte(`{"error":{"code":"token_invalidated","message":"Your authentication token has been invalidated."}}`)

acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
store := accounts.NewMemoryStore()
_ = store.Upsert(context.Background(), acctA)
live := makeCodexCred("tok-a", "ref-a", 0)

usageSrv := stubStaticServer(t, 401, invalidatedBody)
c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

out, _ := c.Fetch(context.Background())
if len(out.Accounts) != 1 {
t.Fatalf("expected 1 account, got %d", len(out.Accounts))
}
ar := out.Accounts[0]
if !strings.Contains(ar.Error, msgTokensRevoked) {
t.Errorf("ar.Error = %q, want substring %q", ar.Error, msgTokensRevoked)
}
if strings.Contains(ar.Error, "HTTP 401") {
t.Errorf("ar.Error should not contain raw 'HTTP 401' prefix: %q", ar.Error)
}
}

// TestFetch_AuthDeniedOtherBodyFallsThrough verifies that a 401 with a non-token_revoked
// body does not trigger the tightening (no false-positive).
func TestFetch_AuthDeniedOtherBodyFallsThrough(t *testing.T) {
otherBody := []byte(`{"error":{"code":"some_other_error","message":"unexpected"}}`)

acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
store := accounts.NewMemoryStore()
_ = store.Upsert(context.Background(), acctA)
live := makeCodexCred("tok-a", "ref-a", 0)

usageSrv := stubStaticServer(t, 401, otherBody)
c := buildClient(t, usageSrv, noRefreshServer(t), live, store, noLookupCall(t), nil, nil)

out, _ := c.Fetch(context.Background())
if len(out.Accounts) != 1 {
t.Fatalf("expected 1 account, got %d", len(out.Accounts))
}
ar := out.Accounts[0]
if ar.Error == "" {
t.Error("ar.Error must be non-empty for auth-denied")
}
if strings.Contains(ar.Error, msgTokensRevoked) {
t.Errorf("non-token_revoked body should not trigger tightening: %q", ar.Error)
}
}

// TestIsRevokedTokenErr locks the two real upstream "dead token" codes to the
// tightened message. The chatgpt.com usage endpoint has returned both
// "token_revoked" and "token_invalidated" interchangeably for the same
// condition; both must map to msgTokensRevoked.
func TestIsRevokedTokenErr(t *testing.T) {
cases := []struct {
name string
err  error
want bool
}{
{"token_revoked usage body", fmt.Errorf("%w: HTTP 401: {\"error\":{\"code\":\"token_revoked\"}}", providers.ErrAuthDenied), true},
{"token_invalidated usage body", fmt.Errorf("%w: HTTP 401 from https://chatgpt.com/backend-api/wham/usage: {\"error\":{\"code\":\"token_invalidated\",\"message\":\"Your authentication token has been invalidated.\"}}", providers.ErrAuthDenied), true},
{"other auth-denied body", fmt.Errorf("%w: HTTP 401: some_other_error", providers.ErrAuthDenied), false},
{"invalidated text but not auth-denied", errors.New("token_invalidated"), false},
}
for _, tc := range cases {
if got := isRevokedTokenErr(tc.err); got != tc.want {
t.Errorf("%s: isRevokedTokenErr = %v, want %v", tc.name, got, tc.want)
}
}
}

// ── FetchForSwitch warn-hint tests (Step 5 / D3) ──────────────────────────────

// TestFetchForSwitch_TokenRevokedWarnHint verifies that a 401 token_revoked response
// produces a warn containing "`codex login`" and "stored credential rejected".
func TestFetchForSwitch_TokenRevokedWarnHint(t *testing.T) {
revokedBody := []byte(`{"error":{"code":"token_revoked","message":"Encountered invalidated oauth token"}}`)

acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
store := accounts.NewMemoryStore()
_ = store.Upsert(context.Background(), acctA)

usageSrv := stubStaticServer(t, 401, revokedBody)
var warnBuf bytes.Buffer
c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), &warnBuf, nil)

results, err := c.FetchForSwitch(context.Background())
if err != nil {
t.Fatal(err)
}
if len(results) != 0 {
t.Errorf("expected 0 results (account excluded), got %d", len(results))
}
warn := warnBuf.String()
if !strings.Contains(warn, "`codex login`") {
t.Errorf("warn should contain '`codex login`': %q", warn)
}
if !strings.Contains(warn, "stored credential rejected") {
t.Errorf("warn should contain 'stored credential rejected': %q", warn)
}
if strings.Contains(warn, "`aistat usage`") {
t.Errorf("token_revoked should not suggest `aistat usage`: %q", warn)
}
}

// TestFetchForSwitch_OtherAuthDeniedKeepsAistatUsageHint verifies that a 401 with
// a non-token_revoked body keeps the existing `aistat usage` hint.
func TestFetchForSwitch_OtherAuthDeniedKeepsAistatUsageHint(t *testing.T) {
otherBody := []byte(`{"error":{"code":"unauthorized","message":"bad token"}}`)

acctA := makeCodexAccount("uuid-a", "alice@example.com", "tok-a", "ref-a", 0)
store := accounts.NewMemoryStore()
_ = store.Upsert(context.Background(), acctA)

usageSrv := stubStaticServer(t, 401, otherBody)
var warnBuf bytes.Buffer
c := buildClient(t, usageSrv, noRefreshServer(t), nil, store, noLookupCall(t), &warnBuf, nil)

results, err := c.FetchForSwitch(context.Background())
if err != nil {
t.Fatal(err)
}
if len(results) != 0 {
t.Errorf("expected 0 results, got %d", len(results))
}
warn := warnBuf.String()
if !strings.Contains(warn, "`aistat usage`") {
t.Errorf("non-token_revoked body should keep `aistat usage` hint: %q", warn)
}
}

// ── PostSwitchVerify real-impl test (Step 5 / A3#1) ───────────────────────────

// TestCodexPostSwitchVerify_RealImplWrapsErrAuthDenied exercises the real Codex
// PostSwitchVerify against a live httptest.Server returning 401 token_revoked.
// This guards the load-bearing %w wrap and bare-phrase contracts; stub-based
// switch_test.go cases cannot catch a future implementer who omits %w.
func TestCodexPostSwitchVerify_RealImplWrapsErrAuthDenied(t *testing.T) {
revokedBody := []byte(`{"error":{"code":"token_revoked","message":"Encountered invalidated oauth token for user"}}`)
usageSrv := stubStaticServer(t, 401, revokedBody)

target := makeCodexAccount("uuid-target", "target@example.com", "tok-target", "ref-target", 0)
c := buildClient(t, usageSrv, noRefreshServer(t), nil, nil, noLookupCall(t), nil, nil)

err := c.PostSwitchVerify(context.Background(), target)
if err == nil {
t.Fatal("expected error, got nil")
}
if !errors.Is(err, providers.ErrAuthDenied) {
t.Errorf("expected errors.Is(err, ErrAuthDenied) = true; err = %v", err)
}
if !strings.Contains(err.Error(), msgTokensRevoked) {
t.Errorf("err.Error() should contain msgTokensRevoked: %q", err.Error())
}
if strings.HasPrefix(err.Error(), "aistat: codex:") {
t.Errorf("PostSwitchVerify must return bare phrase (no 'aistat: codex:' prefix): %q", err.Error())
}
}
