package codex

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
	// Undocumented internal endpoint shared with the ChatGPT web app. Shape
	// may change without notice; the limit_window_seconds assertions below
	// act as a tripwire.
	endpoint = "https://chatgpt.com/backend-api/wham/usage"
	timeout  = 10 * time.Second

	primaryWindowSeconds   = 18000  // 5 hours
	secondaryWindowSeconds = 604800 // 7 days
)

type Client struct {
	doer      *httpx.Doer
	endpoint  string
	readToken func(context.Context) (string, error)
}

func New(debug io.Writer, userAgent string) *Client {
	return &Client{
		doer: &httpx.Doer{
			Client:     &http.Client{Timeout: timeout},
			UserAgent:  userAgent,
			ProviderID: "codex",
			Debug:      debug,
		},
		endpoint:  endpoint,
		readToken: cred.ReadCodexToken,
	}
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
	token, err := c.readToken(ctx)
	if err != nil {
		if errors.Is(err, cred.ErrCodexTokenNotFound) {
			return providers.ProviderOutput{}, fmt.Errorf("%w: %s", providers.ErrAuthMissing, err.Error())
		}
		return providers.ProviderOutput{}, err
	}

	var raw response
	if err := c.doer.GetJSON(ctx, c.endpoint, token, &raw, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}
	if raw.RateLimit == nil {
		return providers.ProviderOutput{}, errors.New("codex usage response missing rate_limit object")
	}

	// See claude.go: the orchestrator's checked_at uses a separate time.Now();
	// reset_after_seconds + checked_at may differ from resets_at by up to one
	// second.
	now := time.Now().UTC().Truncate(time.Second)
	limits := map[string]providers.Limit{}

	if w := raw.RateLimit.PrimaryWindow; w != nil {
		if w.LimitWindowSeconds != primaryWindowSeconds {
			return providers.ProviderOutput{}, fmt.Errorf("codex primary_window has unexpected limit_window_seconds=%d (want %d); OpenAI may have changed the /backend-api/wham/usage shape — please file an issue at https://github.com/drogers0/llm-usage/issues", w.LimitWindowSeconds, primaryWindowSeconds)
		}
		limits["five_hour"] = w.toLimit(now)
	}
	if w := raw.RateLimit.SecondaryWindow; w != nil {
		if w.LimitWindowSeconds != secondaryWindowSeconds {
			return providers.ProviderOutput{}, fmt.Errorf("codex secondary_window has unexpected limit_window_seconds=%d (want %d); OpenAI may have changed the /backend-api/wham/usage shape — please file an issue at https://github.com/drogers0/llm-usage/issues", w.LimitWindowSeconds, secondaryWindowSeconds)
		}
		limits["seven_day"] = w.toLimit(now)
	}
	// code_review_rate_limit's limit_window_seconds is intentionally not
	// asserted — Codex hasn't documented an expected value.
	//
	// The ResetAt > 0 guard skips the inactive case where the window object
	// is present but its fields are zero (observed for users who have not
	// used code review). A real activated window will always have a non-zero
	// epoch ResetAt; an epoch-0 value here would render as 1970-01-01 with
	// zero seconds remaining, which is worse than absence.
	if w := raw.CodeReviewRateLimit; w != nil && w.ResetAt > 0 {
		limits["code_review_seven_day"] = w.toLimit(now)
	}

	if len(limits) == 0 {
		limits = nil
	}
	return providers.ProviderOutput{Limits: limits}, nil
}

func (w window) toLimit(now time.Time) providers.Limit {
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
	}
}
