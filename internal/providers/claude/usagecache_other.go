//go:build !darwin && !linux

package claude

import (
	"sync"
	"time"

	"github.com/drogers0/aistat/v2/internal/providers"
)


// usageCache is a no-op stub on unsupported platforms. All operations return
// miss/no-op and the disabled warn fires at most once.
type usageCache struct {
	once     sync.Once
	warn     func(string)
	disabled bool
}

// newUsageCache always returns a disabled cache on non-unix platforms.
func newUsageCache(nowFn func() time.Time, warnFn func(string)) *usageCache {
	if warnFn == nil {
		warnFn = func(string) {}
	}
	return &usageCache{
		disabled: true,
		warn:     warnFn,
	}
}

func (c *usageCache) getWithAge(uuid string) (map[string]providers.Limit, time.Duration, bool) {
	c.once.Do(func() {
		c.warn("aistat: claude: usage cache disabled (platform not supported)")
	})
	return nil, 0, false
}

func (c *usageCache) Get(uuid string) (map[string]providers.Limit, bool) {
	m, _, ok := c.getWithAge(uuid)
	return m, ok
}

func (c *usageCache) Put(uuid string, limits map[string]providers.Limit) {
	c.once.Do(func() {
		c.warn("aistat: claude: usage cache disabled (platform not supported)")
	})
}
