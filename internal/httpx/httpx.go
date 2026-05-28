// Package httpx provides a shared HTTP client wrapper for aistat providers.
// It owns request setup (Bearer auth, User-Agent, Accept), execution, body read,
// status classification, JSON unmarshal, optional per-request debug logging, and
// bounded retry with Retry-After-aware backoff.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

// Doer is a thin wrapper around *http.Client that provides the request/response
// pipeline shared by every provider. Construct with NewDoer.
//
// Header rules:
//   - User-Agent is always set by Doer and cannot be overridden via extraHeaders
//     (those entries are silently dropped by setCommonHeaders).
//   - Authorization is NOT filtered by setCommonHeaders. GetJSON sets it after
//     calling setCommonHeaders, so the bearer token always wins over any
//     Authorization entry in extraHeaders. PostForm does not set Authorization
//     at all; callers may supply it via extraHeaders if a POST endpoint requires
//     it (the Claude refresh endpoint does not — it authenticates via the body).
//   - All other extraHeaders are applied after User-Agent and Accept, so they
//     can override Accept (e.g. Copilot's `application/vnd.github+json`).
//
// Redirect behavior: stdlib follows up to 10 redirects and strips Authorization
// across host changes; RejectSchemeDowngrade (installed by every provider's
// New) additionally aborts HTTPS→HTTP downgrades to prevent bearer-token
// cleartext leakage. Same-scheme same-host redirects are followed.
type Doer struct {
	Client       *http.Client
	UserAgent    string
	ProviderID   string            // included in debug log prefix so concurrent provider lines are greppable
	extraHeaders map[string]string // applied last (User-Agent filtered; Authorization not filtered — see type comment)
	Debug        io.Writer         // nil disables per-request logging; pass a *ConcurrencySafeWriter when sharing across providers
}

// maxBodyBytes caps the response body size GetJSON will read. Real provider
// payloads are well under 100 KB; 1 MiB is defensive headroom. Over-limit
// bodies are returned as a plain error (NOT wrapped in ErrTransient):
// retrying won't shrink a body, so the retry would burn another budget for
// no benefit.
const maxBodyBytes = 1 << 20

// Retry policy constants.
const (
	maxAttempts       = 3
	exponentialBase   = 500 * time.Millisecond
	exponentialFactor = 2.0
	jitterFraction    = 0.20
	maxDelay          = 5 * time.Second
	retryAfterCap     = 10 * time.Second
)

// Classifier maps a non-200 response to a provider-specific error. The url is
// the FINAL url returned by the server (post-redirect) so the returned error
// can identify which endpoint actually responded. The full *http.Response is
// provided so classifiers can inspect headers (e.g. X-RateLimit-*, Retry-After)
// in addition to the status code and body.
type Classifier func(url string, resp *http.Response, body []byte) error

// NewDoer constructs a Doer with the required Client validated non-nil
// (programmer error → panic). Production code should prefer this over Doer
// literals so partial-init bugs become construction-time panics.
func NewDoer(client *http.Client, userAgent, providerID string, extraHeaders map[string]string, debug io.Writer) *Doer {
	if client == nil {
		panic("httpx.NewDoer: client must not be nil")
	}
	return &Doer{
		Client:       client,
		UserAgent:    userAgent,
		ProviderID:   providerID,
		extraHeaders: extraHeaders,
		Debug:        debug,
	}
}

// RejectSchemeDowngrade is an http.Client CheckRedirect policy that aborts
// HTTPS→HTTP redirects to prevent bearer-token cleartext leakage. Initial
// requests (len(via)==0) and HTTP→HTTPS upgrades are permitted; cross-host
// redirects independently strip Authorization per stdlib. Permitting the
// initial HTTP scheme keeps httptest.NewServer-backed tests working.
func RejectSchemeDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	prev := via[len(via)-1]
	if prev.URL.Scheme == "https" && req.URL.Scheme == "http" {
		return fmt.Errorf("refusing HTTPS→HTTP scheme downgrade from %s to %s", prev.URL.Host, req.URL.Host)
	}
	return nil
}

// setCommonHeaders sets User-Agent (from Doer) and Accept: application/json,
// then applies extraHeaders, skipping only User-Agent (always Doer-controlled).
// Authorization is not filtered, so an extraHeaders Authorization entry is
// applied here. Callers that need Authorization to take a specific value must
// set it after this call (GetJSON does this; PostForm does not, by design).
func (d *Doer) setCommonHeaders(req *http.Request) {
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range d.extraHeaders {
		if http.CanonicalHeaderKey(k) == "User-Agent" {
			continue
		}
		req.Header.Set(k, v)
	}
}

// doOnce executes one HTTP attempt: derives a child context with timeout,
// attaches it to req, executes, reads the body (capped at maxBodyBytes),
// classifies non-200 responses, and unmarshals a 200 into dst.
//
// Returns (retryAfter, isTransient, err):
//   - retryAfter > 0 when the server set a usable Retry-After header on a
//     transient response. Caller caps before sleeping.
//   - isTransient indicates whether err wraps ErrTransient and a retry is
//     worth attempting.
//   - err is nil on success.
func (d *Doer) doOnce(parentCtx context.Context, req *http.Request, timeout time.Duration, classify Classifier, dst any) (time.Duration, bool, error) {
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()
	req = req.WithContext(ctx)
	origURL := req.URL.String()
	method := req.Method
	start := time.Now()

	resp, doErr := d.Client.Do(req)
	elapsed := time.Since(start)

	if doErr != nil {
		d.log(method, origURL, doErr.Error(), elapsed)
		if errors.Is(doErr, context.Canceled) || errors.Is(doErr, context.DeadlineExceeded) {
			// Parent cancelled/timed out → bare ctx error (real cancellation semantics).
			// Child-only deadline (parent alive) → ErrTransient so the retry loop
			// can run against a fresh per-call budget.
			if parentCtx.Err() != nil {
				return 0, false, doErr
			}
			wrapped := fmt.Errorf("%w: %w", providers.ErrTransient, doErr)
			return 0, true, wrapped
		}
		wrapped := fmt.Errorf("%w: %w", providers.ErrTransient, doErr)
		return 0, true, wrapped
	}
	defer resp.Body.Close()

	// Use the final URL after any redirects so logs/errors name the endpoint
	// that actually answered.
	finalURL := origURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if readErr != nil {
		d.log(method, finalURL, readErr.Error(), elapsed)
		if parentCtx.Err() != nil {
			return 0, false, parentCtx.Err()
		}
		if ctx.Err() != nil {
			wrapped := fmt.Errorf("%w: %w", providers.ErrTransient, ctx.Err())
			return 0, true, wrapped
		}
		wrapped := fmt.Errorf("%w: reading response body from %s: %w", providers.ErrTransient, finalURL, readErr)
		return 0, true, wrapped
	}
	// Status classification runs before the size guard so an oversized error
	// page (e.g. a 401 returning a 2 MiB HTML page from a misconfigured
	// proxy) still surfaces as ErrAuthDenied/ErrTransient. The classifier's
	// body argument is capped at maxBodyBytes+1; Snip truncates further.
	if resp.StatusCode != http.StatusOK {
		d.log(method, finalURL, fmt.Sprintf("HTTP %d", resp.StatusCode), elapsed)
		classifyErr := classify(finalURL, resp, body)
		ra := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		isTransient := errors.Is(classifyErr, providers.ErrTransient)
		return ra, isTransient, classifyErr
	}
	// Size guard applies to successful responses where we'd otherwise try to
	// unmarshal the body. An oversized 200 is a contract violation we won't
	// pretend to handle.
	if int64(len(body)) > maxBodyBytes {
		d.log(method, finalURL, fmt.Sprintf("body exceeds %d bytes", maxBodyBytes), elapsed)
		return 0, false, fmt.Errorf("response body from %s exceeds %d bytes", finalURL, maxBodyBytes)
	}
	d.log(method, finalURL, "ok", elapsed)
	if err := json.Unmarshal(body, dst); err != nil {
		return 0, false, fmt.Errorf("non-JSON response from %s: %w: %s", finalURL, err, Snip(body))
	}
	return 0, false, nil
}

// do wraps doOnce in a bounded retry loop (up to maxAttempts attempts).
// makeReq is called once per attempt to build a fresh *http.Request — required
// because http.Request.Body is single-use. GET requests trivially rebuild;
// PostForm re-encodes the form body from the original url.Values each attempt.
//
// Retry is only attempted when doOnce reports isTransient=true. Non-transient
// errors (auth denied, unknown status, unmarshal failure) return immediately.
// Between attempts, do sleeps via sleepWithCtx; if the parent ctx deadline
// would be exceeded by the sleep, it returns the last error immediately rather
// than burning the deadline on a sleep that will cancel anyway.
func (d *Doer) do(parentCtx context.Context, makeReq func() (*http.Request, error), timeout time.Duration, classify Classifier, dst any) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		req, buildErr := makeReq()
		if buildErr != nil {
			return buildErr
		}
		ra, transient, err := d.doOnce(parentCtx, req, timeout, classify, dst)
		if err == nil {
			return nil
		}
		lastErr = err
		if !transient || attempt == maxAttempts-1 {
			break
		}
		delay := pickDelay(attempt, ra)
		if !sleepWithCtx(parentCtx, delay) {
			break
		}
		d.logRetry(attempt+1, delay, ra > 0)
	}
	return lastErr
}

// GetJSON performs GET url with Bearer auth, runs classify on non-200, and
// unmarshals a 200 body into dst. timeout is the per-attempt deadline derived
// inside doOnce from parentCtx — callers pass their parent ctx unchanged so
// doOnce can distinguish parent cancellation (bare context error) from a
// child-only deadline (wrapped as ErrTransient so the retry loop can retry
// against a fresh per-attempt budget).
func (d *Doer) GetJSON(parentCtx context.Context, url, token string, timeout time.Duration, dst any, classify Classifier) error {
	return d.do(parentCtx, func() (*http.Request, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		d.setCommonHeaders(req)
		// Set Authorization last so the bearer token wins over any Authorization
		// entry in extraHeaders (which setCommonHeaders may have applied).
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	}, timeout, classify, dst)
}

// PostForm performs POST rawURL with an application/x-www-form-urlencoded body
// encoded from values, runs classify on non-200, and unmarshals a 200 into dst.
// No Authorization header is set by PostForm itself — the Claude refresh
// endpoint authenticates via the form body (grant_type + refresh_token).
// Callers that need Authorization on a POST may supply it via extraHeaders
// and it will be forwarded as-is.
//
// values are re-encoded on each retry attempt so the body is byte-equal
// across all attempts (not silently empty after the first send drains the reader).
//
// Redirect note: a 307/308 redirect from the endpoint would send an empty body
// on the redirected request. The Anthropic refresh endpoint is not expected to
// issue 307/308 redirects; document here if that assumption is ever violated.
func (d *Doer) PostForm(parentCtx context.Context, rawURL string, values url.Values, timeout time.Duration, dst any, classify Classifier) error {
	return d.do(parentCtx, func() (*http.Request, error) {
		body := strings.NewReader(values.Encode())
		req, err := http.NewRequest(http.MethodPost, rawURL, body)
		if err != nil {
			return nil, err
		}
		d.setCommonHeaders(req)
		// Set Content-Type after setCommonHeaders so it cannot be overridden by
		// an extraHeaders["Content-Type"] entry.
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}, timeout, classify, dst)
}

// parseRetryAfter parses a Retry-After header value and returns the delay.
// Supports both delta-seconds form ("5" → 5s) and HTTP-date form.
// Returns 0 for empty, unparseable, negative-or-zero seconds, or past-date input.
// The caller is responsible for capping the returned duration.
func parseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	h := strings.TrimSpace(header)
	// Try delta-seconds form first.
	if secs, err := strconv.ParseFloat(h, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	// Try HTTP-date form (RFC 1123, RFC 850, ANSI C asctime).
	t, err := http.ParseTime(h)
	if err != nil {
		return 0
	}
	d := t.Sub(now)
	if d <= 0 {
		return 0
	}
	return d
}

// pickDelay returns the sleep duration for a retry attempt.
// If retryAfter > 0 (from a server Retry-After header), it is used (capped at
// retryAfterCap). Otherwise an exponential schedule with ±jitterFraction
// uniform random jitter is applied. Always returns a non-negative duration.
func pickDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > retryAfterCap {
			return retryAfterCap
		}
		return retryAfter
	}
	// Exponential: 0.5s, 1.0s for attempts 0, 1 (clamped at maxDelay).
	base := float64(exponentialBase) * math.Pow(exponentialFactor, float64(attempt))
	if base > float64(maxDelay) {
		base = float64(maxDelay)
	}
	// Uniform jitter in [base*(1-jitterFraction), base*(1+jitterFraction)].
	// jitter minimum is 1-jitterFraction = 0.80; d is always positive.
	jitter := 1 - jitterFraction + 2*jitterFraction*rand.Float64()
	return time.Duration(base * jitter)
}

// sleepWithCtx sleeps for d unless the parent context's deadline would be
// exceeded by the sleep, in which case it returns false immediately. Returns
// true if the full duration elapsed; false if the context was cancelled or the
// deadline check short-circuited the sleep.
func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	if deadline, ok := ctx.Deadline(); ok && time.Now().Add(d).After(deadline) {
		return false
	}
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func (d *Doer) log(method, url, outcome string, elapsed time.Duration) {
	if d.Debug == nil {
		return
	}
	// Single Fprintf so the underlying writer sees one Write call per line.
	// When multiple Doers share a writer, the writer is responsible for
	// serialization (see ConcurrencySafeWriter). outcome can carry an
	// upstream error body via Snip — sanitize so embedded newlines don't
	// fracture the [debug] line into multiple physical lines.
	fmt.Fprintf(d.Debug, "[debug] %s: %s %s -> %s (%dms)\n", d.ProviderID, method, url, SanitizeDebugLine(outcome), elapsed.Milliseconds())
}

// logRetry emits one debug line after a sleep before each retry attempt.
// n is passed as attempt+1 from the do loop (first retry → n=1), so the
// displayed attempt number is n+1 (first retry → "retry 2/3").
func (d *Doer) logRetry(n int, delay time.Duration, fromHeader bool) {
	if d.Debug == nil {
		return
	}
	source := "exponential"
	if fromHeader {
		source = fmt.Sprintf("Retry-After: %d", int(delay.Seconds()))
	}
	fmt.Fprintf(d.Debug, "[debug] %s: retry %d/%d after %.1fs (%s)\n",
		d.ProviderID, n+1, maxAttempts, delay.Seconds(), source)
}

// SanitizeDebugLine collapses control characters (notably CR and LF) in s
// using Go's quoted-string escaping, then strips the outer quotes Quote
// would add. Result is always a single physical line, safe to substitute
// into a Fprintf template that ends with a single "\n". Embedded "
// characters survive as "\"" because Quote escapes them before adding the
// wrapping pair, so byte-slice trim is safe regardless of input.
func SanitizeDebugLine(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}

// Snip truncates body to 200 bytes for inclusion in error messages.
func Snip(b []byte) string {
	s := string(b)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}

// DefaultClassify is the shared status mapping used by all providers for
// non-200 responses. Providers wanting an overlay (e.g. Copilot's
// 404 → ErrAuthMissing) wrap this with their own pre-check.
//
// All 403s classify as ErrAuthDenied. GitHub returns 403 for rate-limit /
// abuse-detection too, mislabelled here as auth failure. Accepted:
// aistat is on-demand (one request per provider per invocation);
// rate-limiting from this binary is rare, and surfacing the misclassification
// as a clear auth error beats silent retries into the same wall.
func DefaultClassify(url string, resp *http.Response, body []byte) error {
	status := resp.StatusCode
	switch {
	case status == 401 || status == 403:
		return fmt.Errorf("%w: HTTP %d from %s: %s", providers.ErrAuthDenied, status, url, Snip(body))
	case status == 408 || status == 429 || status >= 500:
		return fmt.Errorf("%w: HTTP %d from %s: %s", providers.ErrTransient, status, url, Snip(body))
	default:
		return fmt.Errorf("HTTP %d from %s: %s", status, url, Snip(body))
	}
}

// ConcurrencySafeWriter wraps an io.Writer with a mutex so concurrent Write
// calls (e.g. from multiple providers' Doers writing to the same stderr) do
// not interleave mid-line. Callers pass the same instance to every Doer.
// Construct with NewConcurrencySafeWriter.
type ConcurrencySafeWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewConcurrencySafeWriter returns a ConcurrencySafeWriter wrapping w.
func NewConcurrencySafeWriter(w io.Writer) *ConcurrencySafeWriter {
	return &ConcurrencySafeWriter{w: w}
}

func (c *ConcurrencySafeWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.w.Write(p)
}
