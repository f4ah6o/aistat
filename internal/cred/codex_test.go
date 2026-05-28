package cred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAuth(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestReadCodexToken_HappyPath(t *testing.T) {
	writeAuth(t, `{"tokens":{"access_token":"tok-abc"}}`)
	got, err := ReadCodexToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tok-abc" {
		t.Errorf("token = %q, want tok-abc", got)
	}
}

func TestReadCodexToken_Missing(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no .codex dir
	_, err := ReadCodexToken(context.Background())
	if !errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("expected ErrCodexTokenNotFound, got: %v", err)
	}
}

func TestReadCodexToken_MalformedJSON(t *testing.T) {
	writeAuth(t, `{ this is not json }`)
	_, err := ReadCodexToken(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("error should mention 'not valid JSON', got: %v", err)
	}
	if errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("malformed JSON should not wrap ErrCodexTokenNotFound: %v", err)
	}
}

func TestReadCodexToken_EmptyToken(t *testing.T) {
	writeAuth(t, `{"tokens":{"access_token":""}}`)
	_, err := ReadCodexToken(context.Background())
	if !errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("expected ErrCodexTokenNotFound, got: %v", err)
	}
}
