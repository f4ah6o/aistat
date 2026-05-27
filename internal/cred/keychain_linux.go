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
// If the credential file is world- or group-readable, a single warning line
// is written to stderr and the read proceeds — surfacing the security signal
// without refusing to read.
func ReadClaudeToken(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve home directory ($HOME unset): %s", ErrClaudeTokenNotFound, err.Error())
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	fi, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrClaudeTokenNotFound
		}
		return "", fmt.Errorf("reading claude credentials: %w", err)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		fmt.Fprintf(os.Stderr, "usage-check: claude: %s is world- or group-readable (mode %04o); consider chmod 600\n", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading claude credentials: %w", err)
	}
	return parseClaudeCred(data)
}
