package cred

import (
	"errors"
	"strings"
	"testing"
)

func TestParseClaudeCred_HappyPath(t *testing.T) {
	got, err := parseClaudeCred([]byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc"}}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sk-ant-oat01-abc" {
		t.Errorf("got %q, want %q", got, "sk-ant-oat01-abc")
	}
}

func TestParseClaudeCred_EmptyToken(t *testing.T) {
	_, err := parseClaudeCred([]byte(`{"claudeAiOauth":{"accessToken":""}}`))
	if !errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("expected ErrClaudeTokenNotFound, got: %v", err)
	}
}

func TestParseClaudeCred_NotJSON(t *testing.T) {
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
}

func TestParseClaudeCred_MissingClaudeAiOauth(t *testing.T) {
	_, err := parseClaudeCred([]byte(`{"other":"field"}`))
	if !errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("JSON without claudeAiOauth should be classified as missing-token; got: %v", err)
	}
}
