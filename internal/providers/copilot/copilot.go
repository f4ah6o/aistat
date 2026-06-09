package copilot

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

const (
	// copilotUserEndpoint is the Copilot editor/CLI "user" endpoint. It returns
	// the live quota snapshot GitHub computes for the authenticated user — the
	// same numbers the web meter and the official Copilot clients show.
	//
	// We deliberately read this UNDOCUMENTED internal endpoint instead of the
	// documented billing endpoint
	// (https://api.github.com/users/{login}/settings/billing/ai_credit/usage):
	// the billing endpoint reports only consumed/discount/net credit quantities,
	// never the included monthly allotment, so using it would force a hardcoded
	// per-plan quota table that rots whenever GitHub changes an allotment (and
	// cannot see promotional uplifts or the max/pro_plus tiers at all).
	// copilot_internal/user returns the live entitlement, so the percentage is
	// plan-agnostic and self-maintaining. The tradeoff is that an internal
	// endpoint may change shape without notice; we fail closed with an
	// actionable error if it does.
	//
	// Verified 2026-06: a plain request (Authorization + Accept only, no
	// editor/version headers) returns the full snapshot.
	copilotUserEndpoint = "https://api.github.com/copilot_internal/user"

	acceptHeader = "application/vnd.github+json"
	timeout      = 10 * time.Second
	credTimeout  = 10 * time.Second

	// quotaKey is the quota_snapshots entry carrying the AI-credit (formerly
	// premium-request) allotment. The chat/completions entries are token-billed
	// and report unlimited, so this is the only metered window.
	quotaKey = "premium_interactions"
)

type Client struct {
	doer      *httpx.Doer
	readToken func(context.Context) (string, error)
	url       string
	// warn is invoked from the Fetch goroutine; if the caller's closure touches
	// shared state, the caller is responsible for synchronization.
	warn func(string)
	now  func() time.Time
}

// Option mutates a Client at construction time.
type Option func(*Client)

// WithWarn installs a callback for the quota-key-drift tripwire only: it fires
// solely if GitHub renames the `premium_interactions` quota snapshot out from
// under us.
func WithWarn(fn func(string)) Option { return func(c *Client) { c.warn = fn } }

// WithNow overrides the clock used to compute ResetAfterSeconds from the
// snapshot's reset timestamp. Defaults to time.Now. Intended for tests;
// production callers should not override.
func WithNow(fn func() time.Time) Option { return func(c *Client) { c.now = fn } }

func New(debug io.Writer, userAgent string, opts ...Option) *Client {
	c := &Client{
		doer: httpx.NewDoer(
			&http.Client{CheckRedirect: httpx.RejectSchemeDowngrade},
			userAgent,
			"copilot",
			map[string]string{"Accept": acceptHeader},
			debug,
		),
		readToken: cred.ReadGitHubToken,
		url:       copilotUserEndpoint,
		now:       time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) ID() string { return "copilot" }

// quotaSnapshot is one entry of copilot_internal/user's quota_snapshots map.
// has_quota is intentionally not read — it is false even for an account with a
// positive entitlement, so it is not a usable "has a metered pool" signal; we
// gate on Unlimited and Entitlement instead.
type quotaSnapshot struct {
	Entitlement      float64 `json:"entitlement"`
	PercentRemaining float64 `json:"percent_remaining"`
	Unlimited        bool    `json:"unlimited"`
}

type copilotUserResp struct {
	QuotaResetDateUTC time.Time                `json:"quota_reset_date_utc"`
	QuotaSnapshots    map[string]quotaSnapshot `json:"quota_snapshots"`
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	token, err := providers.ReadTokenWithTimeout(ctx, c.readToken, cred.ErrGitHubTokenNotFound, credTimeout)
	if err != nil {
		return providers.ProviderOutput{}, err
	}

	// Auth/permission failures classify via DefaultClassify (401/403 →
	// ErrAuthDenied). A bare 404 is intentionally NOT remapped to "missing
	// scope": that would mislabel a transient outage or an endpoint
	// deprecation, so it surfaces as a plain HTTP 404 instead.
	var resp copilotUserResp
	if err := c.doer.GetJSON(ctx, c.url, token, timeout, &resp, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}

	pool, ok := resp.QuotaSnapshots[quotaKey]
	if !ok {
		// Quota-key-drift tripwire: snapshots are present but the metered key is
		// gone → GitHub likely renamed it. Warn and report no window rather than
		// silently emitting a misleading 0%.
		if len(resp.QuotaSnapshots) > 0 && c.warn != nil {
			c.warn(fmt.Sprintf("copilot: quota_snapshots present but %q key missing — GitHub may have renamed the quota; please file an issue at %s", quotaKey, providers.IssueTrackerURL))
		}
		return providers.ProviderOutput{Limits: map[string]providers.Limit{}}, nil
	}

	// No metered credit pool (an unlimited grant, or no allotment) → N/A,
	// reported as a non-nil empty map so the renderer suppresses the section.
	if pool.Unlimited || pool.Entitlement <= 0 {
		return providers.ProviderOutput{Limits: map[string]providers.Limit{}}, nil
	}

	// percent_remaining is GitHub's own meter value, already clamped at 0 on
	// overage; mirror that clamp into used.
	used := math.Min(100, math.Max(0, 100-pool.PercentRemaining))

	now := c.now().UTC().Truncate(time.Second)
	reset := resp.QuotaResetDateUTC.UTC()
	secs := int(reset.Sub(now).Seconds())
	if secs < 0 {
		secs = 0
	}

	return providers.ProviderOutput{Limits: map[string]providers.Limit{
		"month": {
			UsedPercent:       used,
			RemainingPercent:  100 - used,
			ResetsAt:          reset,
			ResetAfterSeconds: secs,
		},
	}}, nil
}
