package providers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLimitMarshalJSON_UTC(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
	l := Limit{UsedPercent: 2, RemainingPercent: 98, ResetsAt: at, ResetAfterSeconds: 17625}
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	want := `{"used_percent":2,"remaining_percent":98,"resets_at":"2026-05-26T20:00:00+00:00","reset_after_seconds":17625}`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLimitMarshalJSON_NonUTC(t *testing.T) {
	// A non-UTC input must be normalized to "+00:00". The JSON contract
	// documented in README.md is "ISO 8601, always +00:00 for UTC, never Z";
	// MarshalJSON enforces .UTC() at the boundary so a future provider that
	// forgets to call .UTC() does not silently leak a local offset.
	est := time.FixedZone("EST", -5*3600)
	at := time.Date(2026, 5, 26, 15, 0, 0, 0, est) // 20:00 UTC
	l := Limit{ResetsAt: at}
	b, _ := json.Marshal(l)
	if !strings.Contains(string(b), `"resets_at":"2026-05-26T20:00:00+00:00"`) {
		t.Fatalf("non-UTC ResetsAt must be normalized to +00:00; got %s", string(b))
	}
}

func TestReportMarshalJSON_NonUTC(t *testing.T) {
	est := time.FixedZone("EST", -5*3600)
	at := time.Date(2026, 5, 26, 15, 0, 0, 0, est) // 20:00 UTC
	r := Report{CheckedAt: at, Providers: map[string]ProviderResult{}}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"checked_at":"2026-05-26T20:00:00+00:00"`) {
		t.Fatalf("non-UTC CheckedAt must be normalized to +00:00; got %s", string(b))
	}
}

func TestLimitMarshalJSON_FieldOrder(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
	l := Limit{UsedPercent: 2.34, RemainingPercent: 97.66, ResetsAt: at, ResetAfterSeconds: 1}
	b, _ := json.Marshal(l)
	s := string(b)
	idxUsed := strings.Index(s, "used_percent")
	idxRem := strings.Index(s, "remaining_percent")
	idxResets := strings.Index(s, "resets_at")
	idxAfter := strings.Index(s, "reset_after_seconds")
	if !(idxUsed < idxRem && idxRem < idxResets && idxResets < idxAfter) {
		t.Fatalf("field order wrong: %s", s)
	}
	if !strings.Contains(s, `"used_percent":2.34`) {
		t.Fatalf("fractional percent lost precision: %s", s)
	}
}

func TestLimitMarshalJSON_WholeNumberHasNoDecimal(t *testing.T) {
	l := Limit{UsedPercent: 2.0, ResetsAt: time.Unix(1, 0)}
	b, _ := json.Marshal(l)
	if !strings.Contains(string(b), `"used_percent":2,`) {
		t.Fatalf("expected used_percent:2 (no .0), got %s", string(b))
	}
}

func TestLimitMarshalJSON_RejectsZeroResetsAt(t *testing.T) {
	_, err := json.Marshal(Limit{UsedPercent: 50, RemainingPercent: 50, ResetAfterSeconds: 100})
	if err == nil || !strings.Contains(err.Error(), "ResetsAt") {
		t.Fatalf("expected ResetsAt error, got %v", err)
	}
}

func TestLimitMarshalJSON_RoundsFloatArtifact(t *testing.T) {
	l := Limit{UsedPercent: 67.339999999, RemainingPercent: 32.660000001, ResetsAt: time.Unix(1, 0)}
	b, _ := json.Marshal(l)
	s := string(b)
	if !strings.Contains(s, `"used_percent":67.34`) {
		t.Errorf("float artifact not rounded; got %s", s)
	}
	if !strings.Contains(s, `"remaining_percent":32.66`) {
		t.Errorf("float artifact not rounded; got %s", s)
	}
}

func TestReportMarshalJSON(t *testing.T) {
	at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
	r := Report{CheckedAt: at, Providers: map[string]ProviderResult{}}
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"checked_at":"2026-05-26T20:00:00+00:00"`) {
		t.Fatalf("got %s", string(b))
	}
	if strings.Count(string(b), "checked_at") != 1 {
		t.Fatalf("checked_at appeared more than once: %s", string(b))
	}
}

func TestTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"claude", "Claude"},
		{"codex", "Codex"},
		{"copilot", "Copilot"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Title(c.in); got != c.want {
			t.Errorf("Title(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProviderResultOmitempty(t *testing.T) {
	b, _ := json.Marshal(ProviderResult{})
	if string(b) != "{}" {
		t.Fatalf("empty ProviderResult should marshal to {}, got %s", string(b))
	}
	b, _ = json.Marshal(ProviderResult{Error: "boom"})
	if string(b) != `{"error":"boom"}` {
		t.Fatalf("got %s", string(b))
	}
	at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
	b, _ = json.Marshal(ProviderResult{Limits: map[string]Limit{"x": {ResetsAt: at}}})
	if strings.Contains(string(b), `"error"`) {
		t.Fatalf("empty error should be omitted: %s", string(b))
	}
}
