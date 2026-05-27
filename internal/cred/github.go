package cred

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const GitHubTokenMissingMessage = "github token unavailable — run `gh auth login` (if 'Not Found', run `gh auth refresh -h github.com -s user` to add the required scope)"

var ErrGitHubTokenNotFound = errors.New(GitHubTokenMissingMessage)

// ReadGitHubToken shells out to `gh auth token`. Returns ErrGitHubTokenNotFound
// (the only sentinel callers should match against) if gh is not on PATH or has
// no token configured.
func ReadGitHubToken(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "auth", "token")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Preserve context errors so callers can distinguish cancellation
		// from missing auth — matches httpx.GetJSON's cancellation
		// semantics. Without this, a Ctrl-C or provider timeout that kills
		// the subprocess would be reported as "github token unavailable".
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var ee *exec.ExitError
		var pe *exec.Error
		switch {
		case errors.As(err, &pe):
			return "", fmt.Errorf("%w: gh not on PATH: %w", ErrGitHubTokenNotFound, pe)
		case errors.As(err, &ee):
			return "", fmt.Errorf("%w: gh auth token failed: %s: %w", ErrGitHubTokenNotFound, strings.TrimSpace(stderr.String()), ee)
		}
		return "", fmt.Errorf("%w: gh auth token failed: %w", ErrGitHubTokenNotFound, err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", ErrGitHubTokenNotFound
	}
	return token, nil
}
