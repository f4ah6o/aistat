//go:build darwin

package cred

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

// runSecurity is the command-runner seam used by ReadClaudeCredential and
// WriteClaudeLiveBlob. Tests replace it to assert exact security(1) invocations
// without touching the real keychain.
var runSecurity func(ctx context.Context, args ...string) ([]byte, error) = runSecurityExec

func runSecurityExec(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "security", args...).Output()
}

// claudeUser returns the OS user name for keychain item account fields.
// Falls back to user.Current().Username when $USER is empty.
func claudeUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

// ReadClaudeCredential returns the full credential blob backing the macOS
// Keychain item "Claude Code-credentials". Triggers a system prompt the first
// time per binary (or after a code-signing change) — unavoidable for
// non-interactive keychain reads without registering as a trusted app.
func ReadClaudeCredential(ctx context.Context) (Credential, error) {
	out, err := runSecurity(ctx, "find-generic-password",
		"-s", "Claude Code-credentials", "-w")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Credential{}, ctxErr
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// /usr/bin/security exits 44 (errSecItemNotFound, defined in
			// /usr/include/Security/SecBase.h) when the keychain item is
			// absent. Prefer the exit code over the stderr substring; keep
			// the substring as a fallback in case a future macOS version
			// shifts the exit-code mapping.
			if ee.ExitCode() == 44 {
				return Credential{}, ErrClaudeTokenNotFound
			}
			stderr := strings.TrimSpace(string(ee.Stderr))
			if strings.Contains(stderr, "could not be found") {
				return Credential{}, ErrClaudeTokenNotFound
			}
			return Credential{}, fmt.Errorf("keychain access failed: %s", stderr)
		}
		return Credential{}, fmt.Errorf("keychain access failed: %w", err)
	}
	// security(1) appends exactly one trailing newline to the -w output; strip
	// it so Credential.Raw contains only the JSON payload and round-trips
	// cleanly. TrimSuffix (not TrimRight) removes exactly one byte so a
	// payload that legitimately ends with '\n' is not corrupted.
	out = bytes.TrimSuffix(out, []byte{'\n'})
	return parseClaudeCredFull(out)
}

// ReadClaudeToken returns the OAuth access token from the macOS Keychain item
// "Claude Code-credentials". Triggers a system prompt the first time per
// binary (or after a code-signing change) — unavoidable for non-interactive
// keychain reads without registering as a trusted app.
func ReadClaudeToken(ctx context.Context) (string, error) {
	c, err := ReadClaudeCredential(ctx)
	return c.AccessToken, err
}

// WriteClaudeLiveBlob overwrites the live "Claude Code-credentials" keychain
// item with the given raw credential blob via `security add-generic-password
// -U`. The -U flag matches by (service, account) and updates the value in
// place; per empirical observation on darwin, the item's existing per-binary
// access-control list (which includes the Claude CLI's own binary identity,
// added when `claude /login` first created the item) survives the update.
// The Claude CLI's subsequent reads remain silent without any further ACL
// manipulation, so we do NOT call `security set-generic-password-partition-
// list` — that call would prompt the user for their keychain password on
// every invocation as a macOS security requirement, even though the
// resulting partition-list change is unnecessary in practice.
func WriteClaudeLiveBlob(ctx context.Context, rawBlob []byte) error {
	u := claudeUser()
	if _, err := runSecurity(ctx, "add-generic-password",
		"-U", "-s", "Claude Code-credentials", "-a", u, "-w", string(rawBlob)); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("keychain write failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return fmt.Errorf("keychain write failed: %w", err)
	}
	return nil
}
