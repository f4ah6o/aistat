//go:build !darwin && !linux

package cred

import (
	"context"
	"fmt"
)

// ReadClaudeToken on platforms other than macOS/Linux returns an error wrapped
// in ErrClaudeTokenNotFound so the orchestrator classifies it as auth-missing
// (correct exit-code behavior). The wrapped sentinel's message recommends
// `claude /login`, which doesn't exist on these platforms — accepted as a
// cosmetic cost for the classification correctness.
func ReadClaudeToken(ctx context.Context) (string, error) {
	return "", fmt.Errorf("%w: claude is not supported on this platform (requires macOS or Linux)", ErrClaudeTokenNotFound)
}
