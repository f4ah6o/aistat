//go:build linux

package cred

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ReadClaudeToken returns the OAuth access token from
// ~/.claude/.credentials.json (the file `claude /login` writes on Linux).
// ctx is accepted for signature parity; this implementation does no
// cancellable I/O.
//
// File permissions are the Claude Code CLI's responsibility (it created the
// file); aistat is a read-only observer and does not police them.
func ReadClaudeToken(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve home directory ($HOME unset): %w", ErrClaudeTokenNotFound, err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrClaudeTokenNotFound
		}
		return "", fmt.Errorf("reading claude credentials: %w", err)
	}
	return parseClaudeCred(data)
}
