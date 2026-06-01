package cred

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeTestJWT creates a minimal JWT (header.payload.sig) from the given JSON payload.
// The signature segment is always "sig"; it is not verified by ParseCodexIDToken.
func makeTestJWT(t *testing.T, payloadJSON string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return header + "." + payload + ".sig"
}

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

// --- ParseCodexIDToken tests ---

func TestParseCodexIDToken_HappyPath(t *testing.T) {
	jwt := makeTestJWT(t, `{"sub":"u1","email":"user@example.com","exp":1700000000}`)
	sub, email, expSec, err := ParseCodexIDToken(jwt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub != "u1" {
		t.Errorf("sub = %q, want u1", sub)
	}
	if email != "user@example.com" {
		t.Errorf("email = %q, want user@example.com", email)
	}
	if expSec != 1700000000 {
		t.Errorf("expSec = %d, want 1700000000", expSec)
	}
}

func TestParseCodexIDToken_NoEmail(t *testing.T) {
	jwt := makeTestJWT(t, `{"sub":"u2","exp":1700000000}`)
	sub, email, expSec, err := ParseCodexIDToken(jwt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub != "u2" {
		t.Errorf("sub = %q, want u2", sub)
	}
	if email != "" {
		t.Errorf("email = %q, want empty", email)
	}
	if expSec != 1700000000 {
		t.Errorf("expSec = %d, want 1700000000", expSec)
	}
}

func TestParseCodexIDToken_NoExp(t *testing.T) {
	jwt := makeTestJWT(t, `{"sub":"u3"}`)
	sub, _, expSec, err := ParseCodexIDToken(jwt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sub != "u3" {
		t.Errorf("sub = %q, want u3", sub)
	}
	if expSec != 0 {
		t.Errorf("expSec = %d, want 0 when exp absent", expSec)
	}
}

func TestParseCodexIDToken_MissingSubject(t *testing.T) {
	jwt := makeTestJWT(t, `{}`)
	_, _, _, err := ParseCodexIDToken(jwt)
	if err == nil {
		t.Fatal("expected error for missing sub")
	}
	if !strings.Contains(err.Error(), "sub") {
		t.Errorf("error should mention 'sub', got: %v", err)
	}
}

func TestParseCodexIDToken_TwoSegmentsOnly(t *testing.T) {
	_, _, _, err := ParseCodexIDToken("header.payload")
	if err == nil {
		t.Fatal("expected error for two-segment token")
	}
}

func TestParseCodexIDToken_OneSegment(t *testing.T) {
	_, _, _, err := ParseCodexIDToken("header")
	if err == nil {
		t.Fatal("expected error for one-segment token")
	}
}

func TestParseCodexIDToken_FourSegments(t *testing.T) {
	_, _, _, err := ParseCodexIDToken("a.b.c.d")
	if err == nil {
		t.Fatal("expected error for four-segment token")
	}
}

func TestParseCodexIDToken_EmptySegment(t *testing.T) {
	// Leading-dot (.b.c) and trailing-dot (a.b.) must both be rejected.
	for _, tok := range []string{".b.c", "a.b.", "a..c"} {
		if _, _, _, err := ParseCodexIDToken(tok); err == nil {
			t.Errorf("expected error for empty-segment token %q", tok)
		}
	}
}

func TestParseCodexIDToken_BadBase64Payload(t *testing.T) {
	_, _, _, err := ParseCodexIDToken("header.!!!.sig")
	if err == nil {
		t.Fatal("expected error for bad base64")
	}
}

func TestParseCodexIDToken_BadJSONPayload(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	_, _, _, err := ParseCodexIDToken("header." + payload + ".sig")
	if err == nil {
		t.Fatal("expected error for bad JSON payload")
	}
}

func TestParseCodexIDToken_EmptyIDToken(t *testing.T) {
	_, _, _, err := ParseCodexIDToken("")
	if err == nil {
		t.Fatal("expected error for empty id_token")
	}
}

// --- ReadCodexCredential tests ---

const testIDToken = "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJjb2RleC10ZXN0LXN1YiIsImVtYWlsIjoidGVzdEBleGFtcGxlLmNvbSIsImV4cCI6OTk5OTk5OTk5OX0.testsig"

func TestReadCodexCredential_HappyPath(t *testing.T) {
	body := `{"tokens":{"access_token":"tok","refresh_token":"ref","id_token":"` + testIDToken + `"}}`
	writeAuth(t, body)
	cred, err := ReadCodexCredential(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred.AccessToken != "tok" {
		t.Errorf("AccessToken = %q, want tok", cred.AccessToken)
	}
	if cred.RefreshToken != "ref" {
		t.Errorf("RefreshToken = %q, want ref", cred.RefreshToken)
	}
	if cred.ExpiresAt != 9999999999000 {
		t.Errorf("ExpiresAt = %d, want 9999999999000", cred.ExpiresAt)
	}
	if len(cred.Raw) == 0 {
		t.Error("Raw should be non-empty")
	}
}

func TestReadCodexCredential_NoIDToken(t *testing.T) {
	writeAuth(t, `{"tokens":{"access_token":"tok","refresh_token":"ref"}}`)
	cred, err := ReadCodexCredential(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 when id_token absent", cred.ExpiresAt)
	}
	if cred.AccessToken != "tok" {
		t.Errorf("AccessToken = %q, want tok", cred.AccessToken)
	}
}

func TestReadCodexCredential_MissingAccessToken(t *testing.T) {
	writeAuth(t, `{"tokens":{"access_token":"","refresh_token":"ref"}}`)
	_, err := ReadCodexCredential(context.Background())
	if !errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("expected ErrCodexTokenNotFound, got: %v", err)
	}
}

func TestReadCodexCredential_Missing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := ReadCodexCredential(context.Background())
	if !errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("expected ErrCodexTokenNotFound, got: %v", err)
	}
}

func TestReadCodexCredential_MalformedJSON(t *testing.T) {
	writeAuth(t, `{not json}`)
	_, err := ReadCodexCredential(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("malformed JSON should not wrap ErrCodexTokenNotFound: %v", err)
	}
}

func TestReadCodexCredential_RawPreserved(t *testing.T) {
	body := `{"tokens":{"access_token":"tok"},"extra_field":"preserved"}`
	writeAuth(t, body)
	cred, err := ReadCodexCredential(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(cred.Raw, []byte(body)) {
		t.Errorf("Raw = %q, want %q", cred.Raw, body)
	}
}

func TestReadCodexCredential_MalformedIDTokenSetsExpiresAtZero(t *testing.T) {
	// D4: malformed-but-present id_token sets ExpiresAt=0 without error.
	writeAuth(t, `{"tokens":{"access_token":"tok","id_token":"not.a.valid.jwt.at.all"}}`)
	cred, err := ReadCodexCredential(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: malformed id_token must not cause ReadCodexCredential to fail: %v", err)
	}
	if cred.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 for malformed id_token", cred.ExpiresAt)
	}
	if cred.AccessToken != "tok" {
		t.Errorf("AccessToken = %q, want tok", cred.AccessToken)
	}
}

func TestReadCodexCredential_APIKeyModeFails(t *testing.T) {
	// D7 / A7: auth_mode=="api_key" with no tokens object → fail-closed with ErrCodexTokenNotFound.
	writeAuth(t, `{"auth_mode":"api_key","OPENAI_API_KEY":"sk-test"}`)
	_, err := ReadCodexCredential(context.Background())
	if !errors.Is(err, ErrCodexTokenNotFound) {
		t.Errorf("expected ErrCodexTokenNotFound for API-key mode, got: %v", err)
	}
}

func TestReadCodexToken_StillWorks(t *testing.T) {
	writeAuth(t, `{"tokens":{"access_token":"my-token"}}`)
	got, err := ReadCodexToken(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "my-token" {
		t.Errorf("token = %q, want my-token", got)
	}
}

// --- WriteCodexLiveBlob tests ---

func TestWriteCodexLiveBlob_HappyPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Pre-create the .codex dir so we can confirm mode on write.
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"tokens":{"access_token":"written"}}`)
	if err := WriteCodexLiveBlob(context.Background(), data); err != nil {
		t.Fatalf("WriteCodexLiveBlob: %v", err)
	}
	path := filepath.Join(home, ".codex", "auth.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content = %q, want %q", got, data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 0600", perm)
	}
}

func TestWriteCodexLiveBlob_CreatesDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Do NOT pre-create .codex dir.
	data := []byte(`{"tokens":{"access_token":"new"}}`)
	if err := WriteCodexLiveBlob(context.Background(), data); err != nil {
		t.Fatalf("WriteCodexLiveBlob: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("content = %q, want %q", got, data)
	}
}

func TestWriteCodexLiveBlob_Atomic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodexLiveBlob(context.Background(), []byte(`{"tokens":{"access_token":"x"}}`)); err != nil {
		t.Fatalf("WriteCodexLiveBlob: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".auth-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("temp files left behind: %v", matches)
	}
}

func TestWriteCodexLiveBlob_Overwrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	first := []byte(`{"tokens":{"access_token":"first"}}`)
	second := []byte(`{"tokens":{"access_token":"second"}}`)
	if err := WriteCodexLiveBlob(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := WriteCodexLiveBlob(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("content = %q, want second write %q", got, second)
	}
}

func TestWriteCodexLiveBlob_RoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	body := `{"tokens":{"access_token":"rt-tok","refresh_token":"rt-ref"}}`
	if err := WriteCodexLiveBlob(context.Background(), []byte(body)); err != nil {
		t.Fatal(err)
	}
	cred, err := ReadCodexCredential(context.Background())
	if err != nil {
		t.Fatalf("ReadCodexCredential after write: %v", err)
	}
	if cred.AccessToken != "rt-tok" {
		t.Errorf("AccessToken = %q, want rt-tok", cred.AccessToken)
	}
	if cred.RefreshToken != "rt-ref" {
		t.Errorf("RefreshToken = %q, want rt-ref", cred.RefreshToken)
	}
}
