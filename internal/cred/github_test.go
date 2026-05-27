package cred

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

// TestReadGitHubToken_ContextCancelledReturnsCtxErr verifies the new
// ctx.Err() check at the top of the error path: a pre-cancelled ctx
// returns context.Canceled directly — NOT wrapped in
// ErrGitHubTokenNotFound — matching httpx.GetJSON's cancellation
// semantics.
//
// Requires `gh` on PATH; skips otherwise.
func TestReadGitHubToken_ContextCancelledReturnsCtxErr(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH; skipping")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ReadGitHubToken(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
	if errors.Is(err, ErrGitHubTokenNotFound) {
		t.Errorf("cancelled ctx must not wrap ErrGitHubTokenNotFound: %v", err)
	}
}

// TestReadGitHubToken_DeadlineKillsCommand supplements the pre-cancelled
// case by exercising the killed-mid-execution branch (cmd.Output()
// returns *exec.ExitError("signal: killed") with ctx.Err() non-nil).
// The new ctx.Err() check intercepts both cmd.Output() error types
// identically; on very fast machines this test may skip rather than
// exercise the path, which is acceptable — the pre-cancelled test above
// reliably verifies the fix's mechanism.
func TestReadGitHubToken_DeadlineKillsCommand(t *testing.T) {
	if _, err := exec.LookPath("gh"); err != nil {
		t.Skip("gh not on PATH; skipping")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := ReadGitHubToken(ctx)
	// Skip both when gh completed cleanly AND when gh errored for a
	// non-deadline reason (e.g. unconfigured auth on a CI host). The
	// killed-process branch we want to exercise requires ctx.Err() to be
	// non-nil after the call returns.
	if err == nil || ctx.Err() == nil {
		t.Skip("deadline not exercised; gh returned before timeout or finished without context expiry")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got: %v", err)
	}
	if errors.Is(err, ErrGitHubTokenNotFound) {
		t.Errorf("deadline-killed cmd must not wrap ErrGitHubTokenNotFound: %v", err)
	}
}

// TestReadGitHubToken_PreservesExecErrorChainOnMissingBinary pins the contract
// that the %w double-wrap (sentinel + inner) preserves *exec.Error in the
// chain so future error-classification logic can use errors.As to detect
// missing-binary cases.
func TestReadGitHubToken_PreservesExecErrorChainOnMissingBinary(t *testing.T) {
	t.Setenv("PATH", "") // forces exec.LookPath inside CommandContext to fail
	_, err := ReadGitHubToken(context.Background())
	if !errors.Is(err, ErrGitHubTokenNotFound) {
		t.Fatalf("expected ErrGitHubTokenNotFound, got %v", err)
	}
	var pe *exec.Error
	if !errors.As(err, &pe) {
		t.Errorf("inner *exec.Error not preserved in chain: %v", err)
	}
}
