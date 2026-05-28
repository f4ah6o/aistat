package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func TestJSON_DesignSample(t *testing.T) {
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
			"claude": {Limits: map[string]providers.Limit{
				"five_hour": mk(2, 17625),
			}},
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
}

func TestJSON_AlphabeticalProviderOrder(t *testing.T) {
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
}

func TestJSON_ErrorProviderEmitsLimitsNull(t *testing.T) {
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
}

func TestJSON_SuccessEmptyLimitsEmitsEmptyObject(t *testing.T) {
	// Counterpart to TestJSON_ErrorProviderEmitsLimitsNull: a successful
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
}

func TestJSON_SuccessProviderOmitsErrorKey(t *testing.T) {
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
}
