package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/drogers0/aistat/v2/internal/httpx"
	"github.com/drogers0/aistat/v2/internal/providers"
)

const retryBackoff = 200 * time.Millisecond

type Options struct {
	Now          func() time.Time
	Debug        io.Writer
	RetryBackoff time.Duration // zero defaults to retryBackoff; injectable for tests
}

type ExitStatus int

const (
	StatusOK          ExitStatus = 0
	StatusAnyFailed   ExitStatus = 1
	StatusUsageError  ExitStatus = 2
	StatusRenderError ExitStatus = 3
)

// Run fetches every requested provider concurrently, retries each once on
// transient failure, and assembles a Report. The ExitStatus is non-zero iff
// any requested provider's final attempt failed.
func Run(ctx context.Context, requested []string, all []providers.Provider, opts Options) (providers.Report, ExitStatus) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	byID := map[string]providers.Provider{}
	for _, p := range all {
		byID[p.ID()] = p
	}

	backoff := opts.RetryBackoff
	if backoff == 0 {
		backoff = retryBackoff
	}

	checkedAt := opts.Now().UTC().Truncate(time.Second)
	var mu sync.Mutex
	results := map[string]providers.ProviderResult{}
	var anyFailed bool

	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, id := range requested {
		if seen[id] {
			continue
		}
		seen[id] = true
		p, ok := byID[id]
		if !ok {
			continue
		}
		wg.Add(1)
		go func(p providers.Provider) {
			defer wg.Done()
			out, err := fetchWithRetry(ctx, p, opts.Debug, backoff)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				anyFailed = true
				results[p.ID()] = providers.ProviderResult{Error: err.Error()}
				return
			}
			results[p.ID()] = providers.ProviderResult{Limits: out.Limits}
		}(p)
	}
	wg.Wait()

	status := StatusOK
	if anyFailed {
		status = StatusAnyFailed
	}
	return providers.Report{CheckedAt: checkedAt, Providers: results}, status
}

// fetchWithRetry retries once on ErrTransient. Before broadening this
// policy (e.g. retry on additional classifications, scheduled backoff),
// see the doc on httpx.DefaultClassify — GitHub 403 rate-limits today
// misclassify as ErrAuthDenied and would need the Classifier signature
// widened to *http.Response so X-RateLimit-* / Retry-After can inform
// the decision.
func fetchWithRetry(ctx context.Context, p providers.Provider, debug io.Writer, backoff time.Duration) (providers.ProviderOutput, error) {
	out, err := fetchOnce(ctx, p, debug, false)
	if errors.Is(err, providers.ErrTransient) {
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return providers.ProviderOutput{}, ctx.Err()
		}
		out, err = fetchOnce(ctx, p, debug, true)
	}
	return out, err
}

func fetchOnce(ctx context.Context, p providers.Provider, debug io.Writer, retry bool) (providers.ProviderOutput, error) {
	start := time.Now()
	out, err := p.Fetch(ctx)
	elapsed := time.Since(start)
	if debug != nil {
		suffix := ""
		if retry {
			suffix = " [retry]"
		}
		outcome := "ok"
		if err != nil {
			// Sanitize: err.Error() may include an upstream Snip body with
			// embedded newlines (HTML error pages, multi-line JSON). Keep
			// the [debug] summary on one physical line.
			outcome = httpx.SanitizeDebugLine(err.Error())
		}
		// Per-request URL detail comes from httpx.Doer. This summary line
		// names the provider, total elapsed time, and (when applicable) the
		// retry marker so a user can correlate against the underlying GET.
		fmt.Fprintf(debug, "[debug] %s: %s (%dms)%s\n", p.ID(), outcome, elapsed.Milliseconds(), suffix)
	}
	return out, err
}
