package cred

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseClaudeCred(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"happy path", func(t *testing.T) {
			got, err := parseClaudeCred([]byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc"}}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != "sk-ant-oat01-abc" {
				t.Errorf("got %q, want %q", got, "sk-ant-oat01-abc")
			}
		}},
		{"empty token", func(t *testing.T) {
			_, err := parseClaudeCred([]byte(`{"claudeAiOauth":{"accessToken":""}}`))
			if !errors.Is(err, ErrClaudeTokenNotFound) {
				t.Errorf("expected ErrClaudeTokenNotFound, got: %v", err)
			}
		}},
		{"not json", func(t *testing.T) {
			_, err := parseClaudeCred([]byte("not json"))
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, ErrClaudeTokenNotFound) {
				t.Errorf("malformed JSON should not be classified as missing-token")
			}
			if !strings.Contains(err.Error(), "not valid JSON") {
				t.Errorf("error should mention %q, got: %v", "not valid JSON", err)
			}
		}},
		{"missing claudeAiOauth", func(t *testing.T) {
			_, err := parseClaudeCred([]byte(`{"other":"field"}`))
			if !errors.Is(err, ErrClaudeTokenNotFound) {
				t.Errorf("JSON without claudeAiOauth should be classified as missing-token; got: %v", err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestParseClaudeCredFull(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"happy path", func(t *testing.T) {
			input := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc","refreshToken":"rt-xyz","expiresAt":1234567890}}`)
			c, err := parseClaudeCredFull(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.AccessToken != "sk-ant-oat01-abc" {
				t.Errorf("AccessToken: got %q, want %q", c.AccessToken, "sk-ant-oat01-abc")
			}
			if c.RefreshToken != "rt-xyz" {
				t.Errorf("RefreshToken: got %q, want %q", c.RefreshToken, "rt-xyz")
			}
			if c.ExpiresAt != 1234567890 {
				t.Errorf("ExpiresAt: got %d, want %d", c.ExpiresAt, 1234567890)
			}
			if !bytes.Equal(c.Raw, input) {
				t.Errorf("Raw: got %q, want %q", c.Raw, input)
			}
		}},
		{"raw preserves bytes", func(t *testing.T) {
			// Include whitespace and extra fields to confirm byte-exact preservation.
			input := []byte(`{ "claudeAiOauth": { "accessToken": "tok",  "expiresAt": 42 }, "organizationUuid": "org-1" }`)
			c, err := parseClaudeCredFull(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(c.Raw, input) {
				t.Errorf("Raw not equal to input\ngot:  %q\nwant: %q", c.Raw, input)
			}
		}},
		{"raw is clone", func(t *testing.T) {
			// Mutating the original buffer must not affect Credential.Raw.
			input := []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`)
			c, err := parseClaudeCredFull(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			original := string(c.Raw)
			input[0] = 'X' // mutate original
			if string(c.Raw) != original {
				t.Error("Credential.Raw was not cloned; mutation of source affected it")
			}
		}},
		{"empty access token", func(t *testing.T) {
			_, err := parseClaudeCredFull([]byte(`{"claudeAiOauth":{"accessToken":""}}`))
			if !errors.Is(err, ErrClaudeTokenNotFound) {
				t.Errorf("expected ErrClaudeTokenNotFound, got: %v", err)
			}
		}},
		{"missing refresh token", func(t *testing.T) {
			c, err := parseClaudeCredFull([]byte(`{"claudeAiOauth":{"accessToken":"tok"}}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.RefreshToken != "" {
				t.Errorf("expected empty RefreshToken, got %q", c.RefreshToken)
			}
		}},
		{"missing expires at", func(t *testing.T) {
			c, err := parseClaudeCredFull([]byte(`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"rt"}}`))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.ExpiresAt != 0 {
				t.Errorf("expected zero ExpiresAt, got %d", c.ExpiresAt)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
