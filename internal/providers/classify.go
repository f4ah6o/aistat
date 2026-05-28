package providers

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// classifyCredError wraps a credential-reader error in ErrAuthMissing when it
// matches the provided notFound sentinel; returns err unchanged otherwise (so
// ctx.Canceled / DeadlineExceeded and unknown errors pass through). Centralises
// the boilerplate every provider's Fetch opens with.
func classifyCredError(err error, notFound error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, notFound) {
		return fmt.Errorf("%w: %w", ErrAuthMissing, err)
	}
	return err
}

// ReadTokenWithTimeout runs readToken under a derived ctx with the given
// timeout. On a notFound match (errors.Is), wraps as ErrAuthMissing. Any
// other error is returned as-is (including ctx.Err()). Centralises the
// cred-prelude every provider's Fetch opens with.
func ReadTokenWithTimeout(ctx context.Context, readToken func(context.Context) (string, error), notFound error, timeout time.Duration) (string, error) {
	credCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tok, err := readToken(credCtx)
	if err != nil {
		return "", classifyCredError(err, notFound)
	}
	return tok, nil
}
