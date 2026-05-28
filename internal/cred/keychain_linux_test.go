//go:build linux

package cred

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeCred(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(claudeDir, ".credentials.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadClaudeToken_HappyPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeCred(t, dir, `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc"}}`, 0o600)
	got, err := ReadClaudeToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "sk-ant-oat01-abc" {
		t.Errorf("got %q, want %q", got, "sk-ant-oat01-abc")
	}
}

func TestReadClaudeToken_MissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := ReadClaudeToken(context.Background())
	if !errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("expected ErrClaudeTokenNotFound, got: %v", err)
	}
}

func TestReadClaudeToken_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeCred(t, dir, "not json", 0o600)
	_, err := ReadClaudeToken(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("malformed JSON should not be classified as missing-token; got: %v", err)
	}
}

func TestReadClaudeToken_EmptyToken(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	writeCred(t, dir, `{"claudeAiOauth":{"accessToken":""}}`, 0o600)
	_, err := ReadClaudeToken(context.Background())
	if !errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("expected ErrClaudeTokenNotFound, got: %v", err)
	}
}
