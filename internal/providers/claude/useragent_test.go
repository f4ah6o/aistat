package claude

import "testing"

func TestDefaultUserAgent(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"default", func(t *testing.T) {
			t.Setenv("AISTAT_CLAUDE_USER_AGENT", "")
			got := DefaultUserAgent("2.1.0")
			want := "claude-code/2.1.0"
			if got != want {
				t.Errorf("DefaultUserAgent = %q, want %q", got, want)
			}
		}},
		{"env override", func(t *testing.T) {
			t.Setenv("AISTAT_CLAUDE_USER_AGENT", "custom-ua/9")
			got := DefaultUserAgent("2.1.0")
			want := "custom-ua/9"
			if got != want {
				t.Errorf("DefaultUserAgent with env override = %q, want %q", got, want)
			}
		}},
		{"dev build substitutes semver", func(t *testing.T) {
			t.Setenv("AISTAT_CLAUDE_USER_AGENT", "")
			for _, in := range []string{"", "dev"} {
				got := DefaultUserAgent(in)
				want := "claude-code/0.0.0-dev"
				if got != want {
					t.Errorf("DefaultUserAgent(%q) = %q, want %q", in, got, want)
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
