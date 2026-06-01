//go:build !darwin && !linux

package usagecache

import (
	"strings"
	"testing"

	"github.com/drogers0/aistat/v2/internal/providers"
)

func TestCacheOther_NewReturnsDisabled(t *testing.T) {
	c := New("claude", nil, nil)
	// On unsupported platforms every call is a miss.
	got, ok := c.Get("any-uuid")
	if got != nil || ok {
		t.Errorf("Get: want (nil, false), got (%v, %v)", got, ok)
	}
}

func TestCacheOther_PutNoOps(t *testing.T) {
	c := New("claude", nil, nil)
	c.Put("any-uuid", map[string]providers.Limit{"x": {}})
	// Still a miss after Put.
	if _, ok := c.Get("any-uuid"); ok {
		t.Error("Get after Put: want miss on unsupported platform, got hit")
	}
}

func TestCacheOther_WarnFiresOnce(t *testing.T) {
	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }
	c := New("claude", nil, warnFn)

	// Warn must not fire at construction.
	if len(warns) != 0 {
		t.Fatalf("warn fired at construction: want 0, got %d", len(warns))
	}

	c.Get("uuid-1")
	if len(warns) != 1 {
		t.Fatalf("warn count after first Get: want 1, got %d", len(warns))
	}

	// Subsequent calls are silent.
	c.Get("uuid-2")
	c.Put("uuid-3", nil)
	if len(warns) != 1 {
		t.Errorf("warn count after repeated calls: want 1, got %d", len(warns))
	}
}

func TestCacheOther_WarnMessageIncludesProvider(t *testing.T) {
	var warns []string
	warnFn := func(s string) { warns = append(warns, s) }
	c := New("claude", nil, warnFn)

	c.Get("any-uuid")
	if len(warns) != 1 {
		t.Fatalf("want 1 warn, got %d", len(warns))
	}
	if !strings.Contains(warns[0], "aistat: claude: usage cache disabled") {
		t.Errorf("warn message: got %q, want substring %q", warns[0], "aistat: claude: usage cache disabled")
	}
}
