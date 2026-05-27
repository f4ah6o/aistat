package copilot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/drogers0/llm-usage/internal/cred"
	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers"
)

const (
	userEndpoint      = "https://api.github.com/user"
	usageEndpointTmpl = "https://api.github.com/users/%s/settings/billing/premium_request/usage?year=%d&month=%d"
	acceptHeader      = "application/vnd.github+json"
	timeout           = 10 * time.Second
)

// KnownWindows is the set of window keys Copilot's Fetch emits. Documented
// here so the render tripwire test can verify dual-table coverage.
var KnownWindows = []string{"month"}

// planQuota maps GitHub Copilot plan slugs (from /user.plan.name) to their
// monthly premium-request quotas. Source:
// https://docs.github.com/en/copilot/get-started/plans. Unknown slugs
// fail-close — see Fetch for the error path.
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
	warn      func(string)
	now       func() time.Time
}

// Option mutates a Client at construction time.
type Option func(*Client)

// WithWarn installs a callback the provider uses to surface non-fatal warnings
// (e.g. unknown plan name, SKU filter mismatch). The CLI's debug decorator
// passes a stderr writer here when --debug is set; main passes nothing otherwise.
//
// The callback may be invoked from the Fetch goroutine; if the caller's
// closure touches shared state, the caller is responsible for synchronization.
func WithWarn(fn func(string)) Option { return func(c *Client) { c.warn = fn } }

// WithNow overrides the clock-of-record used to compute ResetAfterSeconds
// and the year/month for the billing URL. Defaults to time.Now. Intended for
// tests; production callers should not override.
func WithNow(fn func() time.Time) Option { return func(c *Client) { c.now = fn } }

func New(debug io.Writer, userAgent string, opts ...Option) *Client {
	c := &Client{
		doer: &httpx.Doer{
			Client:       &http.Client{}, // ctx-scoped deadline replaces a per-client Timeout.
			UserAgent:    userAgent,
			ProviderID:   "copilot",
			ExtraHeaders: map[string]string{"Accept": acceptHeader},
			Debug:        debug,
		},
		readToken: cred.ReadGitHubToken,
		userURL:   userEndpoint,
		usageURL: func(login string, year int, month int) string {
			return fmt.Sprintf(usageEndpointTmpl, login, year, month)
		},
		now: time.Now,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) ID() string { return "copilot" }

// SetURLsForTest overrides the GitHub API URLs. Intended for tests and forks;
// not part of the production-use API. Must be called before Fetch.
func (c *Client) SetURLsForTest(userURL string, usageURL func(login string, year int, month int) string) {
	c.userURL = userURL
	c.usageURL = usageURL
}

// SetReadTokenForTest overrides the credential reader. Tests and forks only;
// do not call in production code.
func (c *Client) SetReadTokenForTest(fn func(context.Context) (string, error)) {
	c.readToken = fn
}

type userResp struct {
	Login string `json:"login"`
	Plan  struct {
		Name string `json:"name"`
	} `json:"plan"`
}

type usageItem struct {
	Product       string  `json:"product"`
	Sku           string  `json:"sku"`
	GrossQuantity float64 `json:"grossQuantity"`
}

type usageResp struct {
	UsageItems []usageItem `json:"usageItems"`
}

// classifyCopilot adds GitHub's missing-scope tripwire on top of DefaultClassify.
// GitHub returns 404 with `{"message":"Not Found",...}` from the billing
// endpoint when the token lacks the `user` scope. Install this classifier
// ONLY on the billing call — the /user call must use DefaultClassify so that
// a transient 404 from /user (during GitHub outages or endpoint deprecation)
// is not mis-surfaced as "missing scope".
func classifyCopilot(url string, status int, body []byte) error {
	if status == 404 && strings.Contains(string(body), "Not Found") {
		return fmt.Errorf("%w: %s", providers.ErrAuthMissing, cred.GitHubTokenMissingMessage)
	}
	return httpx.DefaultClassify(url, status, body)
}

func (c *Client) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	// Single shared budget for both HTTP calls — matches the implicit 10s/provider used elsewhere.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	token, err := c.readToken(ctx)
	if err != nil {
		if errors.Is(err, cred.ErrGitHubTokenNotFound) {
			return providers.ProviderOutput{}, fmt.Errorf("%w: %s", providers.ErrAuthMissing, err.Error())
		}
		return providers.ProviderOutput{}, err
	}

	var user userResp
	if err := c.doer.GetJSON(ctx, c.userURL, token, &user, httpx.DefaultClassify); err != nil {
		return providers.ProviderOutput{}, err
	}
	if user.Login == "" {
		return providers.ProviderOutput{}, errors.New("github /user returned empty login")
	}

	quota, ok := planQuota[user.Plan.Name]
	if !ok {
		return providers.ProviderOutput{}, fmt.Errorf(
			"copilot plan %q is not in the known quota table; please file an issue at %s with your plan name (from /user.plan.name)",
			user.Plan.Name, providers.IssueTrackerURL,
		)
	}

	// See claude.go: this `now` and main's checked_at are computed from
	// separate time.Now() calls, so reset_after_seconds + checked_at may
	// differ from resets_at by up to one second.
	now := c.now().UTC().Truncate(time.Second)

	var usage usageResp
	if err := c.doer.GetJSON(ctx, c.usageURL(user.Login, now.Year(), int(now.Month())), token, &usage, classifyCopilot); err != nil {
		return providers.ProviderOutput{}, err
	}

	var gross float64
	var copilotProductItems int
	var sawPremiumSku bool
	for _, it := range usage.UsageItems {
		if it.Product == "Copilot" {
			copilotProductItems++
			if it.Sku == "Copilot Premium Request" {
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

	year, month, _ := now.Date()
	reset := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC) // month=13 → Jan next year (Go normalizes).
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
