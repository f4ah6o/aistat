//go:build fake

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/drogers0/aistat/internal/providers"
)

type fakeProvider struct {
	id         string
	shouldFail bool
}

func newFakeProvider(id string, shouldFail bool) *fakeProvider {
	return &fakeProvider{id: id, shouldFail: shouldFail}
}

func (f *fakeProvider) ID() string { return f.id }

func (f *fakeProvider) Fetch(ctx context.Context) (providers.ProviderOutput, error) {
	if f.shouldFail {
		return providers.ProviderOutput{}, fmt.Errorf("fake injected failure for %s", f.id)
	}
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(used float64, in time.Duration) providers.Limit {
		return providers.Limit{
			UsedPercent:       used,
			RemainingPercent:  100 - used,
			ResetsAt:          now.Add(in),
			ResetAfterSeconds: int(in.Seconds()),
		}
	}
	switch f.id {
	case "claude":
		return providers.ProviderOutput{Limits: map[string]providers.Limit{
			"five_hour":        mk(2, 4*time.Hour+53*time.Minute),
			"seven_day":        mk(21, 2*24*time.Hour+5*time.Hour),
			"seven_day_sonnet": mk(0, 2*24*time.Hour+5*time.Hour),
		}}, nil
	case "codex":
		return providers.ProviderOutput{Limits: map[string]providers.Limit{
			"five_hour":             mk(0, 3*time.Hour+12*time.Minute),
			"seven_day":             mk(11, 4*24*time.Hour+1*time.Hour),
			"code_review_seven_day": mk(0, 4*24*time.Hour+1*time.Hour),
		}}, nil
	case "copilot":
		return providers.ProviderOutput{Limits: map[string]providers.Limit{
			"month": mk(4, 5*24*time.Hour+7*time.Hour),
		}}, nil
	}
	return providers.ProviderOutput{}, fmt.Errorf("unknown fake provider id: %s", f.id)
}
