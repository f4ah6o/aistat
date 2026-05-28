package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

const (
	userEndpoint      = "https://api.github.com/user"
	usageEndpointTmpl = "https://api.github.com/users/%s/settings/billing/premium_request/usage?year=%d&month=%d"
	acceptHeader      = "application/vnd.github+json"
	timeout           = 10 * time.Second
	credTimeout       = 10 * time.Second
)

// planQuota maps GitHub Copilot plan slugs (from /user.plan.name) to their
// monthly premium-request quotas. Source:
// https://docs.github.com/en/copilot/get-started/plans.
//
// Unknown slugs fail-closed (Fetch returns an error → orchestrator marks
// the provider failed → exit 1) by explicit design. We intentionally do NOT
// support an env-var override: silent quota fabrication would lie to
// scripted consumers about percent-used. When GitHub launches a new plan,
// users file an issue and we ship an update.
var planQuota = map[string]int{
	"free":       50,
	"pro":        300,
	"pro_plus":   1500,
	"business":   300,
	"enterprise": 1000,
}

type Client struct {
	doer      *httpx.Doer
	readToken func(context.Context) (string, error)
	userURL   string
	usageURL  func(login string, year int, month int) string
	// warn is invoked from the Fetch goroutine; if the caller's closure
	// touches shared state, the caller is responsible for synchronization.
	warn  func(string)
	now   func() time.Time
	quota map[string]int
}

// Option mutates a Client at construction time.
type Option func(*Client)

// WithWarn installs a callback for the SKU-drift tripwire only. The unknown-plan
// path returns an error and never invokes this callback.
func WithWarn(fn func(string)) Option { return func(c *Client) { c.warn = fn } }

// WithNow overrides the clock-of-record used to compute ResetAfterSeconds
// and the year/month for the billing URL. Defaults to time.Now. Intended for
// tests; production callers should not override.
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
		userURL:   userEndpoint,
		usageURL: func(login string, year int, month int) string {
			return fmt.Sprintf(usageEndpointTmpl, login, year, month)
		},
		now:   time.Now,
		quota: planQuota,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) ID() string { return "copilot" }

type userResp struct {
	Login string `json:"login"`
	Plan  struct {
		Name string `json:"name"`
	} `json:"plan"`
}

type usageItem struct {
	Product       string  `json:"product"`
	SKU           string  `json:"sku"`
	GrossQuantity float64 `json:"grossQuantity"`
}

type usageResp struct {
	UsageItems []usageItem `json:"usageItems"`
}

// classifyCopilot adds GitHub's missing-scope tripwire on top of DefaultClassify.
// GitHub returns 404 with `{"message":"Not Found",...}` from the billing
// endpoint when the token lacks the `user` scope or the account has no Copilot
// premium-request billing data yet. Install this classifier ONLY on the
// billing call — the /user call must use DefaultClassify so that a transient
// 404 from /user (during GitHub outages or endpoint deprecation) is not
// mis-surfaced.
//
// The body is decoded as JSON and the `message` field is matched
// case-insensitively against known GitHub "not-found" phrasings. A 404 with
// no JSON body, or with a `message` we don't recognize, falls through to
// DefaultClassify (surfaces as bare HTTP 404) so we never lie about the
// cause.
func classifyCopilot(url string, resp *http.Response, body []byte) error {
	if resp.StatusCode == 404 {
		var env struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &env) == nil {
			m := strings.ToLower(strings.TrimSpace(env.Message))
			if m == "not found" || m == "resource not found" {
				return fmt.Errorf("%w: %s", providers.ErrAuthMissing, cred.GitHubTokenMissingMessage)
			}
		}
	}
	return httpx.DefaultClassify(url, resp, body)
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	token, err := providers.ReadTokenWithTimeout(ctx, c.readToken, cred.ErrGitHubTokenNotFound, credTimeout)
	if err != nil {
		return providers.ProviderOutput{}, err
	}

	// Each HTTP call gets its own 10s budget via GetJSON's timeout parameter
	// (parity with Claude and Codex single-call providers).
	var user userResp
	if err := c.doer.GetJSON(ctx, c.userURL, token, timeout, &user, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}
	if user.Login == "" {
		return providers.ProviderOutput{}, errors.New("github /user returned empty login")
	}

	quota, ok := c.quota[user.Plan.Name]
	if !ok {
		// Bare error (no sentinel) is intentional: no current consumer would branch on
		// a contract-drift sentinel. Add one if a real use case appears.
		return providers.ProviderOutput{}, fmt.Errorf(
			"copilot plan %q is not in the known quota table; please file an issue at %s with your plan name (from /user.plan.name)",
			user.Plan.Name, providers.IssueTrackerURL,
		)
	}
	if quota <= 0 {
		return providers.ProviderOutput{}, fmt.Errorf(
			"copilot plan %q has zero quota in the known-quota table; this is a code bug, please file an issue at %s",
			user.Plan.Name, providers.IssueTrackerURL,
		)
	}

	// Two `now` captures: urlNow (pre-billing) drives the URL's year/month
	// (queries the just-completed billing period). subNow (post-billing)
	// drives the reset-month math so the reset always reflects the wall clock
	// AFTER the billing call returned — prevents a Jan 31 23:59:55 → Feb 1
	// 00:00:05 call from computing reset = Feb 1 (in the past) using urlNow's
	// January.
	urlNow := c.now().UTC().Truncate(time.Second)

	var usage usageResp
	if err := c.doer.GetJSON(ctx, c.usageURL(user.Login, urlNow.Year(), int(urlNow.Month())), token, timeout, &usage, classifyCopilot); err != nil {
		return providers.ProviderOutput{}, err
	}
	subNow := c.now().UTC().Truncate(time.Second)

	var gross float64
	var copilotProductItems int
	var sawPremiumSku bool
	for _, it := range usage.UsageItems {
		if it.Product == "Copilot" {
			copilotProductItems++
			if it.SKU == "Copilot Premium Request" {
				sawPremiumSku = true
				gross += it.GrossQuantity
			}
		}
	}
	// Silent-degradation tripwire: Copilot-product items present but no
	// premium-request SKU ever observed → GitHub probably renamed the SKU.
	// Keep emitting 0% (a brand-new account with no premium usage produces
	// the same result), but warn the user that the result may be suspect.
	if copilotProductItems > 0 && !sawPremiumSku && c.warn != nil {
		c.warn(`copilot: Copilot-product usageItems present but none matched sku="Copilot Premium Request" — GitHub may have renamed the SKU; please file an issue at ` + providers.IssueTrackerURL)
	}
	// Clamp to 100: the Limit contract uses a [0,100] convention. Overage
	// detail lives in the API's discountQuantity/netQuantity fields but is
	// out of scope for this contract. Float artifacts from the division
	// (e.g. 67.33999999999999) are smoothed at the JSON boundary by
	// Limit.MarshalJSON; raw values stay in the struct.
	used := math.Min(100, (gross/float64(quota))*100)

	year, month, _ := subNow.Date()
	reset := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC) // month=13 → Jan next year (Go normalizes).
	secs := int(reset.Sub(subNow).Seconds())
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
