//go:build !darwin && !linux

package cred

import (
	"context"
	"errors"
)

func ReadClaudeToken(ctx context.Context) (string, error) {
	return "", errors.New("claude provider is not supported on this platform (macOS Keychain or Linux ~/.claude/.credentials.json required)")
}
