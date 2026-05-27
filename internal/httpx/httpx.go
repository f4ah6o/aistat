// Package httpx provides a shared HTTP client wrapper for usage-check providers.
// It owns request setup (Bearer auth, User-Agent, Accept), execution, body read,
// status classification, JSON unmarshal, and optional per-request debug logging.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/drogers0/llm-usage/internal/providers"
)

// Doer is a thin wrapper around *http.Client that provides the request/response
// pipeline shared by every provider. Header application order: Authorization
// and User-Agent are set first, then a default `Accept: application/json`, then
// ExtraHeaders are applied last and override remaining defaults. Authorization
// and User-Agent are reserved — entries in ExtraHeaders for those keys
// (canonical or otherwise) are silently dropped so a misconfigured provider
// cannot accidentally clobber the bearer token or user-agent. Providers needing
// a non-JSON Accept (e.g. Copilot's `application/vnd.github+json`) supply it
// via ExtraHeaders. A future endpoint on the same Doer that needs a different
// Accept must use a per-request header instead — this Doer cannot
// differentiate by URL. Acceptable today since each provider has its own Doer.
//
// ExtraHeaders must not be mutated after construction; it is read by GetJSON
// concurrently with other requests sharing the same Doer.
type Doer struct {
	Client       *http.Client
	UserAgent    string
	ProviderID   string            // included in debug log prefix so concurrent provider lines are greppable
	ExtraHeaders map[string]string // applied last, overrides defaults; must not be mutated post-construction
	Debug        io.Writer         // nil disables per-request logging; pass a *ConcurrencySafeWriter when sharing across providers
}

// maxBodyBytes caps the response body size GetJSON will read. Real provider
// payloads are well under 100 KB; 1 MiB is defensive headroom. Over-limit
// bodies are returned as a plain error (no ErrTransient wrap — see D7 in
// REVIEW_RESOLUTION_PLAN.md): they don't resolve in a 200 ms retry.
const maxBodyBytes = 1 << 20

// Classifier maps a non-200 response to a provider-specific error. The url is
// the FINAL url returned by the server (post-redirect) so the returned error
// can identify which endpoint actually responded.
type Classifier func(url string, status int, body []byte) error

// GetJSON performs GET url with Bearer auth, runs classify on non-200, and
// unmarshals a 200 body into dst. Cancellation precedence: if ctx is already
// cancelled or cancels mid-read, ctx.Err() wins over any non-200 or non-JSON
// error that would otherwise be returned.
func (d *Doer) GetJSON(ctx context.Context, url, token string, dst any, classify Classifier) error {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "application/json")
	for k, v := range d.ExtraHeaders {
		canon := http.CanonicalHeaderKey(k)
		if canon == "Authorization" || canon == "User-Agent" {
			continue
		}
		req.Header.Set(canon, v)
	}

	resp, doErr := d.Client.Do(req)
	elapsed := time.Since(start)

	if doErr != nil {
		d.log(url, doErr.Error(), elapsed)
		if errors.Is(doErr, context.Canceled) || errors.Is(doErr, context.DeadlineExceeded) {
			return doErr
		}
		return fmt.Errorf("%w: %s", providers.ErrTransient, doErr.Error())
	}
	defer resp.Body.Close()

	// Use the final URL after any redirects so logs/errors name the endpoint
	// that actually answered.
	finalURL := url
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if readErr != nil {
		d.log(finalURL, readErr.Error(), elapsed)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("%w: reading response body from %s: %s", providers.ErrTransient, finalURL, readErr.Error())
	}
	// Status classification runs before the size guard so an oversized error
	// page (e.g. a 401 returning a 2 MiB HTML page from a misconfigured
	// proxy) still surfaces as ErrAuthDenied/ErrTransient. The classifier's
	// body argument is capped at maxBodyBytes+1; Snip truncates further.
	if resp.StatusCode != 200 {
		d.log(finalURL, fmt.Sprintf("HTTP %d", resp.StatusCode), elapsed)
		return classify(finalURL, resp.StatusCode, body)
	}
	// Size guard applies to successful responses where we'd otherwise try to
	// unmarshal the body. An oversized 200 is a contract violation we won't
	// pretend to handle.
	if int64(len(body)) > maxBodyBytes {
		d.log(finalURL, fmt.Sprintf("body exceeds %d bytes", maxBodyBytes), elapsed)
		return fmt.Errorf("response body from %s exceeds %d bytes", finalURL, maxBodyBytes)
	}
	d.log(finalURL, "ok", elapsed)
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("non-JSON response from %s: %w: %s", finalURL, err, Snip(body))
	}
	return nil
}

func (d *Doer) log(url, outcome string, elapsed time.Duration) {
	if d.Debug == nil {
		return
	}
	// Single Fprintf so the underlying writer sees one Write call per line.
	// When multiple Doers share a writer, the writer is responsible for
	// serialization (see ConcurrencySafeWriter).
	fmt.Fprintf(d.Debug, "[debug] %s: GET %s -> %s (%dms)\n", d.ProviderID, url, outcome, elapsed.Milliseconds())
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
func DefaultClassify(url string, status int, body []byte) error {
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
// Zero value is ready to use; construct with &ConcurrencySafeWriter{W: w}.
type ConcurrencySafeWriter struct {
	mu sync.Mutex
	W  io.Writer
}

func (c *ConcurrencySafeWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.W.Write(p)
}
