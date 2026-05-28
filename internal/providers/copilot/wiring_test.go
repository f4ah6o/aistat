package copilot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drogers0/aistat/internal/providers"
)

// TestWiring_WarnFiresWithCopilotPrefix verifies the provider-side warn
// contract: when Copilot-product usage items are present but no premium-SKU
// match is observed, the warn callback fires exactly once with a message
// containing "copilot:", "none matched", and the issue-tracker URL. The
// `cmd/aistat` layer is responsible for the outer "aistat: "
// prefix (tested in cmd/aistat/realproviders_test.go).
func TestWiring_WarnFiresWithCopilotPrefix(t *testing.T) {
	userBody := `{"login":"test","plan":{"name":"pro"}}`
	skuMismatchBody := `{"usageItems":[{"product":"Copilot","sku":"Copilot Premium Request (Renamed)","grossQuantity":100}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/users/") {
			_, _ = w.Write([]byte(skuMismatchBody))
			return
		}
		_, _ = w.Write([]byte(userBody))
	}))
	defer srv.Close()

	var warnings []string
	c := New(nil, "aistat-test/0", WithWarn(func(s string) { warnings = append(warnings, s) }))
	c.doer.Client = srv.Client()
	c.userURL = srv.URL + "/user"
	c.usageURL = func(login string, year int, month int) string {
		return srv.URL + "/users/" + login + "/usage"
	}
	c.readToken = func(ctx context.Context) (string, error) { return "gho_fake", nil }

	if _, err := c.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch returned unexpected error: %v", err)
	}

	if len(warnings) != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", len(warnings), warnings)
	}
	for _, want := range []string{"copilot:", "none matched", providers.IssueTrackerURL} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning missing %q: %s", want, warnings[0])
		}
	}
}
