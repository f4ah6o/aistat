//go:build !darwin && !linux

package claude

import "testing"

func TestCacheOther_NewReturnsDisabled(t *testing.T) {
	c := newUsageCache(nil, nil)
	if !c.disabled {
		t.Fatal("expected disabled cache on unsupported platform")
	}
}

func TestCacheOther_GetReturnsMiss(t *testing.T) {
	c := newUsageCache(nil, nil)
	got, ok := c.Get("any-uuid")
	if got != nil || ok {
		t.Errorf("Get on unsupported platform: want (nil, false), got (%v, %v)", got, ok)
	}
}

func TestCacheOther_PutNoops(t *testing.T) {
	c := newUsageCache(nil, nil)
	// Should not panic.
	c.Put("any-uuid", nil)
}

func TestCacheOther_WarnFiresOnce(t *testing.T) {
	count := 0
	c := newUsageCache(nil, func(string) { count++ })

	c.Get("uuid")
	c.Get("uuid")
	c.Put("uuid", nil)

	if count != 1 {
		t.Errorf("warn count: want 1, got %d", count)
	}
}
