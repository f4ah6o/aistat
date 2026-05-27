package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drogers0/llm-usage/internal/httpx"
	"github.com/drogers0/llm-usage/internal/providers/copilot"
)

// TestWarnWiring_CopilotPrefixReachesStderr verifies that the copilot warn
// callback installed by realProviders prefixes lines with "usage-check: ".
// A regression where someone deletes the prefix line in realProviders is
// caught here by the SKU-mismatch path.
func TestWarnWiring_CopilotPrefixReachesStderr(t *testing.T) {
	// Routes:
	//   /user                  → minimal user object (pro plan, so quota lookup succeeds)
	//   /users/{login}/usage   → SKU-mismatch body (Copilot-product, non-premium SKU)
	userBody := `{"login":"test","plan":{"name":"pro"}}`
	skuMismatchBody := `{"usageItems":[{"product":"Copilot","sku":"Copilot Premium Request (Renamed)","grossQuantity":100}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/users/") {
			io.WriteString(w, skuMismatchBody)
			return
		}
		io.WriteString(w, userBody)
	}))
	defer srv.Close()

	var buf bytes.Buffer
	safeStderr := &httpx.ConcurrencySafeWriter{W: &buf}
	chosen := realProviders(safeStderr, false)

	var client *copilot.Client
	for _, p := range chosen {
		if c, ok := p.(*copilot.Client); ok {
			client = c
			break
		}
	}
	if client == nil {
		t.Fatal("realProviders did not return a *copilot.Client")
	}
	client.SetURLsForTest(
		srv.URL+"/user",
		func(login string, year, month int) string {
			return srv.URL + "/users/" + login + "/usage"
		},
	)
	client.SetReadTokenForTest(func(ctx context.Context) (string, error) {
		return "gho_fake", nil
	})

	// Fetch should succeed (the SKU-mismatch path returns 0% used, not an error);
	// the warn callback fires on stderr.
	if _, err := client.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch returned unexpected error: %v", err)
	}

	if got := buf.String(); !strings.Contains(got, "usage-check: copilot:") {
		t.Errorf("expected stderr to contain %q; got %q", "usage-check: copilot:", got)
	}
}
