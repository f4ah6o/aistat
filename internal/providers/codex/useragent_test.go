package codex

import "testing"

func TestDefaultUserAgent(t *testing.T) {
	tests := []struct {
		name    string
		envVal  string
		version string
		want    string
	}{
		{"default", "", "2.1.0", "aistat/2.1.0 (+https://github.com/drogers0/aistat)"},
		{"env override", "custom-ua/9", "2.1.0", "custom-ua/9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AISTAT_CODEX_USER_AGENT", tt.envVal)
			got := DefaultUserAgent(tt.version)
			if got != tt.want {
				t.Errorf("DefaultUserAgent = %q, want %q", got, tt.want)
			}
		})
	}
}
