//go:build linux

package cred

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// captureStderr redirects os.Stderr through an os.Pipe for the duration of fn
// and returns whatever was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = orig })

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()

	fn()
	w.Close()
	return <-done
}

func TestReadClaudeToken_WorldReadableFileWarns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := writeCred(t, dir, `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-abc"}}`, 0o644)

	var token string
	var err error
	stderr := captureStderr(t, func() {
		token, err = ReadClaudeToken(context.Background())
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "sk-ant-oat01-abc" {
		t.Errorf("got token %q, want %q", token, "sk-ant-oat01-abc")
	}
	if !strings.Contains(stderr, "world- or group-readable") {
		t.Errorf("stderr missing security warning: %q", stderr)
	}
	if !strings.Contains(stderr, path) {
		t.Errorf("stderr missing file path %q: %q", path, stderr)
	}
}
