package claude

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/drogers0/aistat/internal/cred"
	"github.com/drogers0/aistat/internal/httpx"
	"github.com/drogers0/aistat/internal/providers"
)

const (
	endpoint    = "https://api.anthropic.com/api/oauth/usage"
	betaHeader  = "oauth-2025-04-20"
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
			"claude",
			map[string]string{"Anthropic-Beta": betaHeader},
			debug,
		),
		endpoint:  endpoint,
		readToken: cred.ReadClaudeToken,
		now:       time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) ID() string { return "claude" }

type window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	token, err := providers.ReadTokenWithTimeout(ctx, c.readToken, cred.ErrClaudeTokenNotFound, credTimeout)
	if err != nil {
		return providers.ProviderOutput{}, err
	}

	var raw map[string]*window
	if err := c.doer.GetJSON(ctx, c.endpoint, token, timeout, &raw, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}

	// Note: this `now` (production: time.Now; tests: WithNow override) and
	// the orchestrator's checked_at are computed from separate clocks, so
	// reset_after_seconds + checked_at may differ from resets_at by up to
	// one second. Accepted as a known trade-off.
	now := c.now().UTC().Truncate(time.Second)
	limits := map[string]providers.Limit{}
	// Closed set of API keys we surface. Any window not listed
	// (seven_day_opus, seven_day_oauth_apps, tangelo, iguana_necktie, …) is
	// intentionally filtered out.
	for _, key := range []string{"five_hour", "seven_day", "seven_day_sonnet"} {
		win := raw[key]
		if win == nil || win.ResetsAt == nil {
			continue
		}
		resets, err := time.Parse(time.RFC3339Nano, *win.ResetsAt)
		if err != nil {
			return providers.ProviderOutput{}, fmt.Errorf("claude window %s has unparseable resets_at %q: %w", key, *win.ResetsAt, err)
		}
		resets = resets.UTC().Truncate(time.Second)
		secs := int(resets.Sub(now).Seconds())
		if secs < 0 {
			secs = 0
		}
		limits[key] = providers.Limit{
			UsedPercent:       win.Utilization,
			RemainingPercent:  100 - win.Utilization,
			ResetsAt:          resets,
			ResetAfterSeconds: secs,
		}
	}
	return providers.ProviderOutput{Limits: limits}, nil
}
