//go:build darwin || linux

package usagecache

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

// setupTestCache creates an isolated Cache for each test using t.TempDir as
// the home directory. Setting HOME redirects os.UserCacheDir on both darwin
// ($HOME/Library/Caches) and linux ($HOME/.cache when XDG_CACHE_HOME is
// cleared).
func setupTestCache(t *testing.T, nowFn func() time.Time, warnFn func(string)) *Cache {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "") // cleared so Linux falls back to $HOME/.cache
	return New("claude", nowFn, warnFn)
}

func makeLimits(resetsAt time.Time) map[string]providers.Limit {
	return map[string]providers.Limit{
		"five_hour": {
			UsedPercent:       25.0,
			RemainingPercent:  75.0,
			ResetsAt:          resetsAt,
			ResetAfterSeconds: 3600,
		},
	}
}

func TestCache_RoundTrip(t *testing.T) {
	resetsAt := time.Unix(1748448000, 0).UTC()
	limits := makeLimits(resetsAt)

	c := setupTestCache(t, nil, nil)

	c.Put("uuid-1", limits)
	got, ok := c.Get("uuid-1")
	if !ok {
		t.Fatal("Get after Put: want hit, got miss")
	}
	if len(got) != len(limits) {
		t.Fatalf("Get: got %d entries, want %d", len(got), len(limits))
	}
	l := got["five_hour"]
	if l.UsedPercent != 25.0 || l.RemainingPercent != 75.0 || l.ResetAfterSeconds != 3600 {
		t.Errorf("Get: numeric fields mismatch: %+v", l)
	}
}

func TestCache_GetUnknownUUID(t *testing.T) {
	c := setupTestCache(t, nil, nil)
	got, ok := c.Get("nonexistent-uuid")
	if got != nil || ok {
		t.Errorf("Get unknown UUID: want (nil, false), got (%v, %v)", got, ok)
	}
}

func TestCache_ExpiredEntry(t *testing.T) {
	now := time.Unix(1748448000, 0).UTC()
	nowFn := func() time.Time { return now }

	c := setupTestCache(t, nowFn, nil)
	c.Put("uuid-1", makeLimits(now.Add(3600*time.Second)))

	// Advance past TTL
	now = now.Add(c.ttl + time.Second)

	got, ok := c.Get("uuid-1")
	if got != nil || ok {
		t.Errorf("Get expired entry: want (nil, false), got (%v, %v)", got, ok)
	}
}

func TestCache_CorruptFile(t *testing.T) {
	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }

	c := setupTestCache(t, nil, warnFn)

	// Write corrupt JSON directly to the data file.
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(c.path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, ok := c.Get("uuid-1")
	if got != nil || ok {
		t.Errorf("Get on corrupt file: want (nil, false), got (%v, %v)", got, ok)
	}
	if len(warns) != 1 {
		t.Errorf("warn count after corrupt Get: want 1, got %d", len(warns))
	}

	// Second Get does not fire warn again.
	c.Get("uuid-1")
	if len(warns) != 1 {
		t.Errorf("warn count after second Get: want 1, got %d (once must suppress)", len(warns))
	}
}

func TestCache_CorruptOverwritesOnPut(t *testing.T) {
	c := setupTestCache(t, nil, nil)
	resetsAt := time.Unix(1748448000, 0).UTC()

	// Establish uuid-2 in a valid file first.
	c.Put("uuid-2", makeLimits(resetsAt))
	if _, ok := c.Get("uuid-2"); !ok {
		t.Fatal("precondition: uuid-2 not found before corruption")
	}

	// Corrupt the file externally.
	if err := os.WriteFile(c.path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Put uuid-1: reads the corrupt file, discards it, overwrites via atomic
	// rename with only the new entry. uuid-2 is intentionally lost — the cache
	// is fail-open and entries will be repopulated on their next fetch.
	c.Put("uuid-1", makeLimits(resetsAt))

	got, ok := c.Get("uuid-1")
	if !ok {
		t.Fatal("Get uuid-1 after Put-over-corrupt: want hit, got miss")
	}
	if len(got) == 0 {
		t.Error("Get uuid-1 after Put-over-corrupt: empty limits")
	}

	// uuid-2 was lost when the corrupt file was overwritten.
	_, ok = c.Get("uuid-2")
	if ok {
		t.Error("Get uuid-2 after corrupt overwrite: want miss (data lost), got hit")
	}
}

func TestCache_ConcurrentPuts(t *testing.T) {
	c := setupTestCache(t, nil, nil)

	resetsAt := time.Unix(1748448000, 0).UTC()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); c.Put("uuid-a", makeLimits(resetsAt)) }()
	go func() { defer wg.Done(); c.Put("uuid-b", makeLimits(resetsAt)) }()
	wg.Wait()

	_, okA := c.Get("uuid-a")
	_, okB := c.Get("uuid-b")
	if !okA || !okB {
		t.Errorf("concurrent Puts: want both UUIDs present; uuid-a=%v uuid-b=%v", okA, okB)
	}
}

func TestCache_Disabled(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	// Resolve the cache dir path, then block it by creating a file in its place.
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir: %v", err)
	}
	// Create the "aistat" ancestor as a regular file so MkdirAll("aistat/usage") fails.
	ancestor := filepath.Join(cacheBase, "aistat")
	if err := os.MkdirAll(filepath.Dir(ancestor), 0700); err != nil {
		t.Fatalf("MkdirAll ancestor parent: %v", err)
	}
	if err := os.WriteFile(ancestor, []byte("block"), 0600); err != nil {
		t.Fatalf("WriteFile ancestor: %v", err)
	}

	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }
	c := New("claude", nil, warnFn)

	if !c.disabled {
		t.Fatal("expected disabled cache when dir is blocked")
	}

	// Warn must NOT fire at construction — only on first use.
	if len(warns) != 0 {
		t.Errorf("warn fired at construction: want 0, got %d: %v", len(warns), warns)
	}

	// First Get fires the warn.
	got, ok := c.Get("any-uuid")
	if got != nil || ok {
		t.Errorf("disabled Get: want (nil, false), got (%v, %v)", got, ok)
	}
	if len(warns) != 1 {
		t.Errorf("warn count after first Get: want 1, got %d", len(warns))
	}

	// Subsequent calls are silent.
	c.Get("any-uuid")
	c.Put("any-uuid", nil)
	if len(warns) != 1 {
		t.Errorf("warn count after repeated calls: want 1, got %d", len(warns))
	}
}

// TestCache_PathNaming pins the exact on-disk location for the claude data file.
// This is the provider-neutral equivalent of the old claude-package path test.
func TestCache_PathNaming(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	cacheBase, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir: %v", err)
	}

	c := New("claude", nil, nil)

	wantPath := filepath.Join(cacheBase, "aistat", "usage", "claude-v1.json")
	if c.path != wantPath {
		t.Errorf("cache path: got %q, want %q", c.path, wantPath)
	}
}

// TestCache_LockNaming pins the exact on-disk location for the claude lock file.
func TestCache_LockNaming(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	cacheBase, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir: %v", err)
	}

	c := New("claude", nil, nil)

	wantLock := filepath.Join(cacheBase, "aistat", "usage", "claude.cache.lock")
	if c.lockPath != wantLock {
		t.Errorf("lock path: got %q, want %q", c.lockPath, wantLock)
	}
}

func TestCache_TTLEnvVar(t *testing.T) {
	t.Setenv("AISTAT_USAGE_CACHE_TTL", "1s")

	now := time.Unix(1748448000, 0).UTC()
	nowFn := func() time.Time { return now }

	c := setupTestCache(t, nowFn, nil)

	if c.ttl != time.Second {
		t.Errorf("TTL: want 1s from env, got %v", c.ttl)
	}

	c.Put("uuid-1", makeLimits(now.Add(3600*time.Second)))

	// Before TTL expires: hit.
	now = now.Add(500 * time.Millisecond)
	_, ok := c.Get("uuid-1")
	if !ok {
		t.Error("Get before TTL: want hit, got miss")
	}

	// After TTL expires (age > ttl): miss.
	now = now.Add(time.Second) // total 1.5s since Put; TTL=1s
	_, ok = c.Get("uuid-1")
	if ok {
		t.Error("Get after TTL: want miss, got hit")
	}
}

func TestCache_ResetsAtPreserved(t *testing.T) {
	resetsAt := time.Unix(1748448000, 0).UTC()
	limits := map[string]providers.Limit{
		"five_hour": {
			UsedPercent:       50.0,
			RemainingPercent:  50.0,
			ResetsAt:          resetsAt,
			ResetAfterSeconds: 7200,
		},
	}

	c := setupTestCache(t, nil, nil)
	c.Put("uuid-1", limits)

	got, ok := c.Get("uuid-1")
	if !ok {
		t.Fatal("Get after Put: miss")
	}
	if !got["five_hour"].ResetsAt.Equal(resetsAt) {
		t.Errorf("ResetsAt not preserved: want %v, got %v", resetsAt, got["five_hour"].ResetsAt)
	}
}

// TestCache_WarnPrefix asserts that warn strings emitted by a "claude" cache
// are prefixed with "aistat: claude: usage cache".
func TestCache_WarnPrefix(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }

	c := New("claude", nil, warnFn)

	// Force a corrupt file to trigger the corrupt-file warn.
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(c.path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	c.Get("uuid-1")
	if len(warns) != 1 {
		t.Fatalf("want 1 warn, got %d: %v", len(warns), warns)
	}
	if !strings.HasPrefix(warns[0], "aistat: claude: usage cache") {
		t.Errorf("warn prefix: got %q, want prefix %q", warns[0], "aistat: claude: usage cache")
	}
}

// TestCache_InvalidProvider asserts that an invalid provider string returns a
// disabled cache that warns once with the invalid name in the message.
func TestCache_InvalidProvider(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }

	c := New("../attack", nil, warnFn)
	if !c.disabled {
		t.Fatal("expected disabled cache for invalid provider")
	}

	// Warn must NOT fire at construction — only on first use.
	if len(warns) != 0 {
		t.Fatalf("warn fired at construction: want 0, got %d", len(warns))
	}

	got, ok := c.Get("any-uuid")
	if got != nil || ok {
		t.Errorf("disabled Get: want (nil, false), got (%v, %v)", got, ok)
	}
	if len(warns) != 1 {
		t.Fatalf("warn count after Get: want 1, got %d", len(warns))
	}
	if !strings.Contains(warns[0], "../attack") {
		t.Errorf("warn message does not contain invalid name: %q", warns[0])
	}
	// The invalid-provider format intentionally omits a provider prefix; the
	// invalid string lives inside the parenthesized reason instead.
	if !strings.HasPrefix(warns[0], "aistat: usage cache disabled") {
		t.Errorf("warn prefix: got %q, want prefix %q", warns[0], "aistat: usage cache disabled")
	}

	// Second call and Put are silent.
	c.Get("any-uuid")
	c.Put("any-uuid", nil)
	if len(warns) != 1 {
		t.Errorf("warn count after repeated calls: want 1, got %d", len(warns))
	}
}

// TestCache_WarnPrefix_ReadError exercises the read-error warn path
// ("aistat: claude: usage cache: read error: ..."). Triggered by placing a
// directory at the cache file path so os.ReadFile fails with EISDIR.
func TestCache_WarnPrefix_ReadError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }

	c := New("claude", nil, warnFn)
	if err := os.MkdirAll(c.path, 0700); err != nil {
		t.Fatalf("mkdir at cache path: %v", err)
	}
	c.Get("uuid-1")
	if len(warns) != 1 {
		t.Fatalf("want 1 warn, got %d: %v", len(warns), warns)
	}
	if !strings.HasPrefix(warns[0], "aistat: claude: usage cache: read error:") {
		t.Errorf("warn prefix: got %q, want prefix %q", warns[0], "aistat: claude: usage cache: read error:")
	}
}

// TestCache_WarnPrefix_WriteFailed exercises the write-failed warn path
// ("aistat: claude: usage cache: write failed: ..."). Triggered by making
// the cache directory read-only after the lock file is created but before
// atomicWrite tries to create the tmp file.
func TestCache_WarnPrefix_WriteFailed(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }

	c := New("claude", nil, warnFn)
	// First Put creates the cache dir + lock file.
	c.Put("uuid-1", nil)
	if len(warns) != 0 {
		t.Fatalf("warn fired on first Put: %v", warns)
	}
	dir := filepath.Dir(c.path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatalf("chmod readonly: %v", err)
	}
	defer os.Chmod(dir, 0700) //nolint:errcheck // restore for cleanup
	c.Put("uuid-2", nil)
	if len(warns) != 1 {
		t.Fatalf("want 1 warn after readonly Put, got %d: %v", len(warns), warns)
	}
	if !strings.HasPrefix(warns[0], "aistat: claude: usage cache: write failed:") {
		t.Errorf("warn prefix: got %q, want prefix %q", warns[0], "aistat: claude: usage cache: write failed:")
	}
}

// TestCache_CodexIsolation asserts that New("codex", ...) writes to
// codex-v1.json / codex.cache.lock, separate from the claude files.
func TestCache_CodexIsolation(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "")

	cacheBase, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir: %v", err)
	}
	dir := filepath.Join(cacheBase, "aistat", "usage")

	resetsAt := time.Unix(1748448000, 0).UTC()
	cClaude := New("claude", nil, nil)
	cCodex := New("codex", nil, nil)

	// Pin file names.
	wantCodexPath := filepath.Join(dir, "codex-v1.json")
	wantCodexLock := filepath.Join(dir, "codex.cache.lock")
	if cCodex.path != wantCodexPath {
		t.Errorf("codex cache path: got %q, want %q", cCodex.path, wantCodexPath)
	}
	if cCodex.lockPath != wantCodexLock {
		t.Errorf("codex lock path: got %q, want %q", cCodex.lockPath, wantCodexLock)
	}

	// Write to each cache and assert isolation.
	cClaude.Put("uuid-claude", makeLimits(resetsAt))
	cCodex.Put("uuid-codex", makeLimits(resetsAt))

	if _, ok := cClaude.Get("uuid-codex"); ok {
		t.Error("claude cache should not see codex entry")
	}
	if _, ok := cCodex.Get("uuid-claude"); ok {
		t.Error("codex cache should not see claude entry")
	}
	if _, ok := cClaude.Get("uuid-claude"); !ok {
		t.Error("claude cache: want hit for uuid-claude, got miss")
	}
	if _, ok := cCodex.Get("uuid-codex"); !ok {
		t.Error("codex cache: want hit for uuid-codex, got miss")
	}
}
