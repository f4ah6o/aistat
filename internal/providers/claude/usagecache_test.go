//go:build darwin || linux

package claude

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)

// setupTestCache creates an isolated cache for each test using t.TempDir as
// the home directory. Setting HOME redirects os.UserCacheDir on both darwin
// ($HOME/Library/Caches) and linux ($HOME/.cache when XDG_CACHE_HOME is
// cleared).
func setupTestCache(t *testing.T, nowFn func() time.Time, warnFn func(string)) *usageCache {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", "") // cleared so Linux falls back to $HOME/.cache
	return newUsageCache(nowFn, warnFn)
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
	resetsAt := time.Unix(1748448000, 0).UTC() // second-precision, UTC
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
	c := newUsageCache(nil, warnFn)

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
	resetsAt := time.Unix(1748448000, 0).UTC() // second-precision, no nanoseconds
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
