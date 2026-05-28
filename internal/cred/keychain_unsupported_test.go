//go:build !darwin && !linux

package cred

import (
	"context"
	"errors"
	"testing"
)

func TestReadClaudeToken_UnsupportedPlatformWrapsSentinel(t *testing.T) {
	_, err := ReadClaudeToken(context.Background())
	if err == nil {
		t.Fatal("expected error on unsupported platform")
	}
	if !errors.Is(err, ErrClaudeTokenNotFound) {
		t.Errorf("error should wrap ErrClaudeTokenNotFound for correct orchestrator classification; got: %v", err)
	}
}
