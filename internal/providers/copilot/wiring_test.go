package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drogers0/aistat/v2/internal/providers"
)

// TestWiring_WarnFiresWithCopilotPrefix verifies the provider-side warn
// contract: when the quota snapshot is present but the metered
// `premium_interactions` key is missing (a GitHub quota-key rename), the warn
// callback fires exactly once with a message containing "copilot:",
// "premium_interactions", and the issue-tracker URL. The `cmd/aistat` layer is
// responsible for the outer "aistat: " prefix (tested in
// cmd/aistat/realproviders_test.go).
func TestWiring_WarnFiresWithCopilotPrefix(t *testing.T) {
	missingKeyBody := `{"quota_reset_date_utc":"2026-07-01T00:00:00.000Z","quota_snapshots":{"chat":{"unlimited":true},"completions":{"unlimited":true}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(missingKeyBody))
	}))
	defer srv.Close()

	var warnings []string
	c := New(nil, "aistat-test/0", WithWarn(func(s string) { warnings = append(warnings, s) }))
	c.doer.Client = srv.Client()
	c.url = srv.URL
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch returned unexpected error: %v", err)
	}

	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}
	for _, want := range []string{"copilot:", "premium_interactions", providers.IssueTrackerURL} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning missing %q: %s", want, warnings[0])
		}
	}
}
