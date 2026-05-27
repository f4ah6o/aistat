package providers

import (
	"errors"
	"fmt"
)

// ClassifyCredError wraps a credential-reader error in ErrAuthMissing when it
// matches the provided notFound sentinel; returns err unchanged otherwise (so
// ctx.Canceled / DeadlineExceeded and unknown errors pass through). Centralises
// the boilerplate every provider's Fetch opens with.
func ClassifyCredError(err error, notFound error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, notFound) {
		return fmt.Errorf("%w: %w", ErrAuthMissing, err)
	}
	return err
}
