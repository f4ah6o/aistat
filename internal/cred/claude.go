package cred

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ClaudeTokenMissingMessage = "claude token not found — run `claude /login` to authenticate"

var ErrClaudeTokenNotFound = errors.New(ClaudeTokenMissingMessage)

// parseClaudeCred extracts the OAuth access token from the JSON payload used
// by both the macOS Keychain item ("Claude Code-credentials") and the Linux
// ~/.claude/.credentials.json file. Returns ErrClaudeTokenNotFound when the
// token field is empty or absent. Unexported; only the platform-tagged
// keychain_*.go files call it.
func parseClaudeCred(data []byte) (string, error) {
	var cred struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(data, &cred); err != nil {
		return "", fmt.Errorf("claude credential is not valid JSON: %w", err)
	}
	if cred.ClaudeAiOauth.AccessToken == "" {
		return "", ErrClaudeTokenNotFound
	}
	return cred.ClaudeAiOauth.AccessToken, nil
}
