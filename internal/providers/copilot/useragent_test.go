package copilot

import "testing"

func TestDefaultUserAgent(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"default without env override", func(t *testing.T) {
			t.Setenv("AISTAT_COPILOT_USER_AGENT", "")
			got := DefaultUserAgent("2.1.0")
			want := "aistat/2.1.0 (+https://github.com/drogers0/aistat)"
			if got != want {
				t.Errorf("DefaultUserAgent = %q, want %q", got, want)
			}
		}},
		{"env override applied", func(t *testing.T) {
			t.Setenv("AISTAT_COPILOT_USER_AGENT", "custom-ua/9")
			got := DefaultUserAgent("2.1.0")
			want := "custom-ua/9"
			if got != want {
				t.Errorf("DefaultUserAgent with env override = %q, want %q", got, want)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
