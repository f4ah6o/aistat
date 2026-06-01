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

	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"nil input", func(t *testing.T) {
			if got := classifyCredError(nil, notFound); got != nil {
				t.Errorf("nil input → %v, want nil", got)
			}
		}},
		{"matching sentinel wraps ErrAuthMissing", func(t *testing.T) {
			got := classifyCredError(notFound, notFound)
			if !errors.Is(got, ErrAuthMissing) {
				t.Errorf("missing ErrAuthMissing in chain: %v", got)
			}
			if !errors.Is(got, notFound) {
				t.Errorf("missing inner sentinel in chain: %v", got)
			}
		}},
		{"non-matching passes through", func(t *testing.T) {
			got := classifyCredError(other, notFound)
			if got != other {
				t.Errorf("expected pass-through identity, got %v", got)
			}
			if errors.Is(got, ErrAuthMissing) {
				t.Errorf("non-matching error must not gain ErrAuthMissing: %v", got)
			}
		}},
		{"preserves nested chain", func(t *testing.T) {
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
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
