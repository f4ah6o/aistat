package providers

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestClassifyCredError(t *testing.T) {
	notFound := errors.New("token not found")
	other := errors.New("other failure")

	t.Run("nil_input", func(t *testing.T) {
		if got := classifyCredError(nil, notFound); got != nil {
			t.Errorf("nil input → %v, want nil", got)
		}
	})

	t.Run("matching_sentinel_wraps_ErrAuthMissing", func(t *testing.T) {
		got := classifyCredError(notFound, notFound)
		if !errors.Is(got, ErrAuthMissing) {
			t.Errorf("missing ErrAuthMissing in chain: %v", got)
		}
		if !errors.Is(got, notFound) {
			t.Errorf("missing inner sentinel in chain: %v", got)
		}
	})

	t.Run("non_matching_passes_through", func(t *testing.T) {
		got := classifyCredError(other, notFound)
		if got != other {
			t.Errorf("expected pass-through identity, got %v", got)
		}
		if errors.Is(got, ErrAuthMissing) {
			t.Errorf("non-matching error must not gain ErrAuthMissing: %v", got)
		}
	})

	t.Run("preserves_nested_chain", func(t *testing.T) {
		// Real-world shape: cred reader wraps the sentinel AND an inner
		// fs.ErrPermission via %w: %w. classifyCredError then wraps that.
		// errors.Is should traverse every layer.
		inner := fmt.Errorf("%w: HOME unset: %w", notFound, fs.ErrPermission)
		got := classifyCredError(inner, notFound)
		if !errors.Is(got, ErrAuthMissing) {
			t.Errorf("missing ErrAuthMissing in nested chain: %v", got)
		}
		if !errors.Is(got, notFound) {
			t.Errorf("missing notFound sentinel in nested chain: %v", got)
		}
		if !errors.Is(got, fs.ErrPermission) {
			t.Errorf("missing fs.ErrPermission in nested chain: %v", got)
		}
	})
}
