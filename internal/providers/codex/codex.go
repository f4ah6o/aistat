package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/drogers0/aistat/internal/cred"
	"github.com/drogers0/aistat/internal/httpx"
	"github.com/drogers0/aistat/internal/providers"
)

const (
	// Undocumented internal endpoint shared with the ChatGPT web app. Shape
	// may change without notice.
	endpoint    = "https://chatgpt.com/backend-api/wham/usage"
	timeout     = 10 * time.Second
	credTimeout = 10 * time.Second
)

type Client struct {
	doer      *httpx.Doer
	endpoint  string
	readToken func(context.Context) (string, error)
	// now is the clock-of-record for ResetAfterSeconds truncation. New
	// initializes to time.Now; tests override via WithNow.
	now func() time.Time
}

// Option mutates a Client at construction time.
type Option func(*Client)

// WithNow overrides the clock-of-record used to compute ResetAfterSeconds.
// Defaults to time.Now. Intended for tests; production callers should not
// override.
func WithNow(fn func() time.Time) Option { return func(c *Client) { c.now = fn } }

func New(debug io.Writer, userAgent string, opts ...Option) *Client {
	c := &Client{
		doer: httpx.NewDoer(
			&http.Client{CheckRedirect: httpx.RejectSchemeDowngrade},
			userAgent,
			"codex",
			nil,
			debug,
		),
		endpoint:  endpoint,
		readToken: cred.ReadCodexToken,
		now:       time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) ID() string { return "codex" }

type window struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type response struct {
	RateLimit *struct {
		PrimaryWindow   *window `json:"primary_window"`
		SecondaryWindow *window `json:"secondary_window"`
	} `json:"rate_limit"`
	CodeReviewRateLimit *window `json:"code_review_rate_limit"`
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	token, err := providers.ReadTokenWithTimeout(ctx, c.readToken, cred.ErrCodexTokenNotFound, credTimeout)
	if err != nil {
		return providers.ProviderOutput{}, err
	}

	var raw response
	if err := c.doer.GetJSON(ctx, c.endpoint, token, timeout, &raw, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}
	if raw.RateLimit == nil {
		return providers.ProviderOutput{}, errors.New("codex usage response missing rate_limit object")
	}

	// This `now` (production: time.Now; tests: WithNow) and the orchestrator's
	// checked_at are separate clocks; reset_after_seconds + checked_at may
	// differ from resets_at by up to one second.
	now := c.now().UTC().Truncate(time.Second)
	limits := map[string]providers.Limit{}

	if w := raw.RateLimit.PrimaryWindow; w != nil {
		if lim, ok := w.toLimit(now); ok {
			limits["five_hour"] = lim
		}
	}
	if w := raw.RateLimit.SecondaryWindow; w != nil {
		if lim, ok := w.toLimit(now); ok {
			limits["seven_day"] = lim
		}
	}
	if w := raw.CodeReviewRateLimit; w != nil {
		if lim, ok := w.toLimit(now); ok {
			limits["code_review_seven_day"] = lim
		}
	}

	return providers.ProviderOutput{Limits: limits}, nil
}

// toLimit converts a window object to a providers.Limit. Returns ok=false
// when ResetAt is non-positive — observed when an inactive window object is
// returned with zero fields (e.g. a code-review window for a user who has
// never used the feature, or a freshly-created account with no usage in the
// window). Rendering epoch-0 as 1970-01-01T00:00:00Z with zero seconds
// remaining is worse than omitting the window entirely.
func (w window) toLimit(now time.Time) (providers.Limit, bool) {
	if w.ResetAt <= 0 {
		return providers.Limit{}, false
	}
	resets := time.Unix(w.ResetAt, 0).UTC().Truncate(time.Second)
	secs := int(resets.Sub(now).Seconds())
	if secs < 0 {
		secs = 0
	}
	return providers.Limit{
		UsedPercent:       w.UsedPercent,
		RemainingPercent:  100 - w.UsedPercent,
		ResetsAt:          resets,
		ResetAfterSeconds: secs,
	}, true
}
