package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func TestJSON(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"design sample", func(t *testing.T) {
			// Claude uses the new Accounts shape (active account's limits also
			// projected to top-level limits per D3). Timestamp format checks are
			// anchored on the per-account limits entry.
			checked, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
			mk := func(used float64, secs int) providers.Limit {
				return providers.Limit{
					UsedPercent:       used,
					RemainingPercent:  100 - used,
					ResetsAt:          checked.Add(time.Duration(secs) * time.Second),
					ResetAfterSeconds: secs,
				}
			}
			r := providers.Report{
				CheckedAt: checked,
				Providers: map[string]providers.ProviderResult{
					"claude": {
						Limits: map[string]providers.Limit{"five_hour": mk(2, 17625)},
						Accounts: []providers.AccountResult{
							{
								Email:  "me@example.com",
								UUID:   "uuid-abc",
								Plan:   "default_claude_pro",
								Active: true,
								Limits: map[string]providers.Limit{"five_hour": mk(2, 17625)},
							},
						},
					},
				},
			}
			var buf bytes.Buffer
			if err := JSON(&buf, r); err != nil {
				t.Fatal(err)
			}
			got := buf.String()
			if !strings.Contains(got, `"checked_at": "2026-05-26T20:00:00+00:00"`) {
				t.Fatalf("checked_at format wrong: %s", got)
			}
			// 20:00 UTC + 17625s = 00:53:45 next day UTC.
			if !strings.Contains(got, `"resets_at": "2026-05-27T00:53:45+00:00"`) {
				t.Fatalf("resets_at format wrong: %s", got)
			}
			if strings.Contains(got, `"Z"`) {
				t.Fatalf("found Z suffix; should be +00:00: %s", got)
			}
			if !strings.HasSuffix(got, "\n") {
				t.Fatalf("expected trailing newline")
			}
			if !strings.Contains(got, `"accounts"`) {
				t.Fatalf("missing accounts key: %s", got)
			}
		}},
		{"alphabetical provider order", func(t *testing.T) {
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"copilot": {Limits: map[string]providers.Limit{}},
					"claude":  {Limits: map[string]providers.Limit{}},
					"codex":   {Limits: map[string]providers.Limit{}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			iClaude := strings.Index(s, `"claude"`)
			iCodex := strings.Index(s, `"codex"`)
			iCopilot := strings.Index(s, `"copilot"`)
			if !(iClaude < iCodex && iCodex < iCopilot) {
				t.Fatalf("provider order wrong: %s", s)
			}
		}},
		{"error provider emits limits null", func(t *testing.T) {
			// Contract: failure → "limits": null + "error": <msg>. Combined with
			// success-with-empty-windows → "limits": {} (no error key), scripted
			// callers can distinguish "asked, got nothing" from "failed".
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Error: "auth failure"},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			if !strings.Contains(s, `"limits": null`) {
				t.Fatalf("limits should be null on error: %s", s)
			}
			if !strings.Contains(s, `"error": "auth failure"`) {
				t.Fatalf("missing error: %s", s)
			}
		}},
		{"success empty limits emits empty object", func(t *testing.T) {
			// Counterpart to error provider emits limits null: a successful
			// provider with zero windows must serialize as "limits": {}, not null
			// and not absent.
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Limits: map[string]providers.Limit{}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			if !strings.Contains(s, `"limits": {}`) {
				t.Fatalf("success-with-empty-limits should serialize as {}, got: %s", s)
			}
			if strings.Contains(s, `"error"`) {
				t.Fatalf("no error key on success: %s", s)
			}
		}},
		{"success provider omits error key", func(t *testing.T) {
			at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Limits: map[string]providers.Limit{
						"five_hour": {ResetsAt: at},
					}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			if strings.Contains(buf.String(), `"error"`) {
				t.Fatalf("error key should be omitted on success: %s", buf.String())
			}
		}},
		{"claude accounts ordering", func(t *testing.T) {
			// The JSON renderer preserves slice order. Active account must appear
			// before the inactive one (the orchestrator guarantees active-first).
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Accounts: []providers.AccountResult{
						{Email: "b@example.com", Active: true},
						{Email: "a@example.com", Active: false},
					}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			iB := strings.Index(s, `"b@example.com"`)
			iA := strings.Index(s, `"a@example.com"`)
			if iB >= iA {
				t.Fatalf("active account b should appear before a: %s", s)
			}
		}},
		{"account result error on account row", func(t *testing.T) {
			// Per-account error: the row carries `"error"` + `"limits": null`. We
			// deliberately keep `limits` present (not omitempty) so callers can
			// distinguish `null` (fetch failed) from `{}` (fetched, zero windows
			// recognized) — same convention as Codex/Copilot top-level Limits.
			// The provider-level (top-level) limits key is still absent because the
			// Claude multi-account path doesn't carry a top-level mirror.
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Accounts: []providers.AccountResult{
						{Email: "x@example.com", Active: true, Error: "usage fetch timed out"},
					}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			if !strings.Contains(s, `"limits": null`) {
				t.Fatalf("error account should emit limits: null (not omit); got: %s", s)
			}
			if !strings.Contains(s, `"error": "usage fetch timed out"`) {
				t.Fatalf("missing per-account error: %s", s)
			}
		}},
		{"account result active false", func(t *testing.T) {
			// Active=false must serialize as "active": false — not omitted.
			// This is load-bearing: callers use the field to distinguish stored-
			// but-inactive from stored-and-active accounts.
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"claude": {Accounts: []providers.AccountResult{
						{Email: "x@example.com", Active: false},
					}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			if !strings.Contains(buf.String(), `"active": false`) {
				t.Fatalf("Active=false must not be omitted: %s", buf.String())
			}
		}},
		{"codex accounts ordering", func(t *testing.T) {
			// Same active-first slice-order contract as Claude. Codex multi-account
			// must emit the same JSON shape (top-level "accounts", no top-level
			// "limits" key when Accounts is populated).
			r := providers.Report{
				Providers: map[string]providers.ProviderResult{
					"codex": {Accounts: []providers.AccountResult{
						{Email: "b@example.com", Active: true},
						{Email: "a@example.com", Active: false},
					}},
				},
			}
			var buf bytes.Buffer
			_ = JSON(&buf, r)
			s := buf.String()
			iB := strings.Index(s, `"b@example.com"`)
			iA := strings.Index(s, `"a@example.com"`)
			if iB < 0 || iA < 0 || iB >= iA {
				t.Fatalf("active account b should appear before a: %s", s)
			}
			// When Accounts is populated, top-level "limits" must be omitted for codex
			// just as it is for claude.
			if strings.Contains(s, `"limits":`) && !strings.Contains(s, `"limits": null`) {
				// allow per-account "limits": null/{} but not top-level "limits": ...
				// crude check: find first "codex" then look for "limits" before "accounts"
				ci := strings.Index(s, `"codex"`)
				ai := strings.Index(s[ci:], `"accounts"`)
				li := strings.Index(s[ci:], `"limits"`)
				if li >= 0 && li < ai {
					t.Fatalf("top-level codex.limits should be omitted when accounts is populated: %s", s)
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
