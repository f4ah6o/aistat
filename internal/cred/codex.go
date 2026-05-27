package cred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const CodexTokenMissingMessage = "codex token not found at ~/.codex/auth.json — run `codex login`"

var ErrCodexTokenNotFound = errors.New(CodexTokenMissingMessage)

// ReadCodexToken returns the OAuth access token from ~/.codex/auth.json.
// The ctx parameter is accepted for signature parity with other credential
// readers; this implementation does no I/O that can be cancelled.
func ReadCodexToken(ctx context.Context) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: cannot resolve home directory ($HOME unset): %w", ErrCodexTokenNotFound, err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrCodexTokenNotFound
		}
		return "", fmt.Errorf("reading codex auth.json: %w", err)
	}
	var auth struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", fmt.Errorf("codex auth.json is not valid JSON: %w", err)
	}
	if auth.Tokens.AccessToken == "" {
		return "", ErrCodexTokenNotFound
	}
	return auth.Tokens.AccessToken, nil
}
