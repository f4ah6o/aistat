//go:build darwin

package cred

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ReadClaudeToken returns the OAuth access token from the macOS Keychain item
// "Claude Code-credentials". Triggers a system prompt the first time per
// binary (or after a code-signing change) — unavoidable for non-interactive
// keychain reads without registering as a trusted app.
func ReadClaudeToken(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", "Claude Code-credentials", "-w")
	out, err := cmd.Output()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// /usr/bin/security exits 44 (errSecItemNotFound, defined in
			// /usr/include/Security/SecBase.h) when the keychain item is
			// absent. Prefer the exit code over the stderr substring; keep
			// the substring as a fallback in case a future macOS version
			// shifts the exit-code mapping.
			if ee.ExitCode() == 44 {
				return "", ErrClaudeTokenNotFound
			}
			stderr := strings.TrimSpace(string(ee.Stderr))
			if strings.Contains(stderr, "could not be found") {
				return "", ErrClaudeTokenNotFound
			}
			return "", fmt.Errorf("keychain access failed: %s", stderr)
		}
		return "", fmt.Errorf("keychain access failed: %w", err)
	}
	return parseClaudeCred(out)
}
