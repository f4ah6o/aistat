package render

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func mkLimit(used float64, secs int) providers.Limit {
	at, _ := time.Parse(time.RFC3339, "2026-05-26T20:00:00Z")
	return providers.Limit{
		UsedPercent:       used,
		RemainingPercent:  100 - used,
		ResetsAt:          at.Add(time.Duration(secs) * time.Second),
		ResetAfterSeconds: secs,
	}
}

func TestText_DesignSample(t *testing.T) {
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude": {Limits: map[string]providers.Limit{
				"five_hour":        mkLimit(2, 4*3600+53*60),
				"seven_day":        mkLimit(21, 2*86400+5*3600),
				"seven_day_sonnet": mkLimit(0, 2*86400+5*3600),
			}},
			"codex": {Limits: map[string]providers.Limit{
				"five_hour":             mkLimit(0, 3*3600+12*60),
				"seven_day":             mkLimit(11, 4*86400+1*3600),
				"code_review_seven_day": mkLimit(0, 4*86400+1*3600),
			}},
			"copilot": {Limits: map[string]providers.Limit{
				"month": mkLimit(4, 5*86400+7*3600),
			}},
		},
	}
	var buf bytes.Buffer
	if err := Text(&buf, r, []string{"claude", "codex", "copilot"}); err != nil {
		t.Fatal(err)
	}
	want := "Claude usage\n- 5-hour: 2.0% (resets in 4h 53m)\n- 7-day: 21.0% (resets in 2d 5h)\n- 7-day sonnet: 0.0% (resets in 2d 5h)\n\nCodex usage\n- 5-hour: 0.0% (resets in 3h 12m)\n- 7-day: 11.0% (resets in 4d 1h)\n- Code review 7-day: 0.0% (resets in 4d 1h)\n\nCopilot usage\n- month: 4.0% (resets in 5d 7h)\n"
	if buf.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestText_SingleProvider(t *testing.T) {
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude": {Limits: map[string]providers.Limit{
				"five_hour": mkLimit(2, 4*3600+53*60),
			}},
		},
	}
	var buf bytes.Buffer
	_ = Text(&buf, r, []string{"claude"})
	want := "Claude usage\n- 5-hour: 2.0% (resets in 4h 53m)\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestText_RequestedButNoneInReport(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, providers.Report{}, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected empty output when no requested providers are in report, got %q", buf.String())
	}
}

func TestText_EmptyRequested(t *testing.T) {
	var buf bytes.Buffer
	_ = Text(&buf, providers.Report{}, nil)
	if buf.Len() != 0 {
		t.Fatalf("expected empty, got %q", buf.String())
	}
}

func TestText_ErrorOnly(t *testing.T) {
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude": {Error: "Claude token not found"},
		},
	}
	var buf bytes.Buffer
	_ = Text(&buf, r, []string{"claude"})
	want := "Claude usage: Claude token not found\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestText_MixedSuccessAndError(t *testing.T) {
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude": {Limits: map[string]providers.Limit{"five_hour": mkLimit(2, 4*3600)}},
			"codex":  {Error: "Codex token not found"},
		},
	}
	var buf bytes.Buffer
	_ = Text(&buf, r, []string{"claude", "codex"})
	want := "Claude usage\n- 5-hour: 2.0% (resets in 4h 0m)\n\nCodex usage: Codex token not found\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestText_UnknownKeyAfterKnown(t *testing.T) {
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude": {Limits: map[string]providers.Limit{
				"five_hour":   mkLimit(2, 3600),
				"new_window":  mkLimit(5, 7200),
				"alpha_extra": mkLimit(3, 1800),
			}},
		},
	}
	var buf bytes.Buffer
	_ = Text(&buf, r, []string{"claude"})
	want := "Claude usage\n- 5-hour: 2.0% (resets in 1h 0m)\n- alpha_extra: 3.0% (resets in 30m)\n- new_window: 5.0% (resets in 2h 0m)\n"
	if buf.String() != want {
		t.Fatalf("got %q want %q", buf.String(), want)
	}
}

func TestFormatResetDuration(t *testing.T) {
	cases := []struct {
		s    int
		want string
	}{
		{5*86400 + 3*3600, "5d 3h"},
		{4*3600 + 12*60, "4h 12m"},
		{45 * 60, "45m"},
		{0, "0m"},
		{-30, "0m"},
	}
	for _, c := range cases {
		got := formatResetDuration(c.s)
		if got != c.want {
			t.Errorf("formatResetDuration(%d) = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestText_TitleCase(t *testing.T) {
	// Sanity check the capitalization helper isn't broken for all three.
	r := providers.Report{
		Providers: map[string]providers.ProviderResult{
			"claude":  {Limits: map[string]providers.Limit{"five_hour": mkLimit(1, 60)}},
			"codex":   {Limits: map[string]providers.Limit{"five_hour": mkLimit(1, 60)}},
			"copilot": {Limits: map[string]providers.Limit{"month": mkLimit(1, 60)}},
		},
	}
	var buf bytes.Buffer
	_ = Text(&buf, r, []string{"claude", "codex", "copilot"})
	s := buf.String()
	for _, want := range []string{"Claude usage", "Codex usage", "Copilot usage"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
}
