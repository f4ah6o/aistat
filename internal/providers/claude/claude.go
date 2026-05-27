package claude

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/drogers0/llm-usage/internal/cred"
	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
)

const (
	endpoint   = "https://api.anthropic.com/api/oauth/usage"
	betaHeader = "oauth-2025-04-20"
	timeout    = 10 * time.Second
)

// KnownWindows is the closed set of API keys we surface. Adding an entry here
// is half of the forward-compat step when Anthropic adds a new public window —
// the other half is internal/render/text.go's textLabels["claude"]; a tripwire
// test in internal/render catches the dual-table drift. Any window not listed
// (seven_day_opus, seven_day_oauth_apps, seven_day_cowork, seven_day_omelette,
// tangelo, iguana_necktie, omelette_promotional, …) is intentionally filtered
// out.
var KnownWindows = []string{"five_hour", "seven_day", "seven_day_sonnet"}

type Client struct {
	doer      *httpx.Doer
	endpoint  string
	readToken func(context.Context) (string, error)
}

func New(debug io.Writer, userAgent string) *Client {
	return &Client{
		doer: &httpx.Doer{
			Client:       &http.Client{Timeout: timeout},
			UserAgent:    userAgent,
			ProviderID:   "claude",
			ExtraHeaders: map[string]string{"Anthropic-Beta": betaHeader},
			Debug:        debug,
		},
		endpoint:  endpoint,
		readToken: cred.ReadClaudeToken,
	}
}

func (c *Client) ID() string { return "claude" }

type window struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    *string `json:"resets_at"`
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	token, err := c.readToken(ctx)
	if err != nil {
		if errors.Is(err, cred.ErrClaudeTokenNotFound) {
			return providers.ProviderOutput{}, fmt.Errorf("%w: %s", providers.ErrAuthMissing, err.Error())
		}
		return providers.ProviderOutput{}, err
	}

	var raw map[string]*window
	if err := c.doer.GetJSON(ctx, c.endpoint, token, &raw, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}

	// Note: this `now` and the orchestrator's checked_at are computed from
	// separate time.Now() calls, so reset_after_seconds + checked_at may
	// differ from resets_at by up to one second. Accepted as a known
	// trade-off — using a single time source would require plumbing through
	// the provider interface for marginal value.
	now := time.Now().UTC().Truncate(time.Second)
	limits := map[string]providers.Limit{}
	for _, key := range KnownWindows {
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
	if len(limits) == 0 {
		// Nil out so ProviderResult.Limits is omitempty-suppressed (the
		// map field's omitempty hides nil but not an empty-non-nil map).
		limits = nil
	}
	return providers.ProviderOutput{Limits: limits}, nil
}
