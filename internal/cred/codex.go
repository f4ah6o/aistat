package cred

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const CodexTokenMissingMessage = "codex token not found at ~/.codex/auth.json — run `codex login`"

var ErrCodexTokenNotFound = errors.New(CodexTokenMissingMessage)

// codexAuthPath returns the path to ~/.codex/auth.json.
func codexAuthPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		// %v (not %w) for the inner os error: callers classify this as
		// auth-missing via ErrCodexTokenNotFound; wrapping the os error
		// with %w would let errors.Is match internal syscall errors,
		// which callers should not depend on. Same discipline as credPath()
		// in internal/cred/keychain_linux.go.
		return "", fmt.Errorf("%w: cannot resolve home directory ($HOME unset): %v", ErrCodexTokenNotFound, err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

// ParseCodexIDToken decodes the base64url-encoded payload of a JWT id_token and
// extracts the sub, email, and exp claims. The signature is NOT verified —
// the Codex CLI already accepted the token. Returns an error if idToken is
// empty, not a three-segment JWT, the payload is bad base64, the payload is
// not valid JSON, or the sub claim is absent.
//
// email is "" if absent; expSec is 0 if absent.
func ParseCodexIDToken(idToken string) (sub, email string, expSec int64, err error) {
	if idToken == "" {
		return "", "", 0, fmt.Errorf("codex id_token is empty")
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", 0, fmt.Errorf("codex id_token: expected 3 non-empty segments, got %d", len(parts))
	}
	payload, decErr := base64.RawURLEncoding.DecodeString(parts[1])
	if decErr != nil {
		return "", "", 0, fmt.Errorf("codex id_token: payload base64 decode: %w", decErr)
	}
	var claims struct {
		Sub   string  `json:"sub"`
		Email string  `json:"email"`
		Exp   float64 `json:"exp"` // JSON number; may be integer or float
	}
	if unmarshalErr := json.Unmarshal(payload, &claims); unmarshalErr != nil {
		return "", "", 0, fmt.Errorf("codex id_token: payload JSON: %w", unmarshalErr)
	}
	if claims.Sub == "" {
		return "", "", 0, fmt.Errorf("codex id_token: missing sub claim")
	}
	return claims.Sub, claims.Email, int64(claims.Exp), nil
}

// rawCodexAuth is the minimal shape of ~/.codex/auth.json for credential extraction.
type rawCodexAuth struct {
	Tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	} `json:"tokens"`
}

// parseCodexCredFull parses the JSON payload of ~/.codex/auth.json.
// access_token is required; its absence returns ErrCodexTokenNotFound.
// id_token is optional: if absent or malformed, ExpiresAt is 0.
// Raw is set to bytes.Clone(data) so the caller's buffer can be reused.
func parseCodexCredFull(data []byte) (Credential, error) {
	var raw rawCodexAuth
	if err := json.Unmarshal(data, &raw); err != nil {
		return Credential{}, fmt.Errorf("codex auth.json is not valid JSON: %w", err)
	}
	if raw.Tokens.AccessToken == "" {
		return Credential{}, ErrCodexTokenNotFound
	}

	var expiresAt int64
	if raw.Tokens.IDToken != "" {
		if _, _, expSec, err := ParseCodexIDToken(raw.Tokens.IDToken); err == nil {
			expiresAt = expSec * 1000
		}
		// malformed id_token → ExpiresAt stays 0; not an error for ReadCodexCredential.
		// T3 reconcile calls ParseCodexIDToken independently on the live credential
		// to extract sub/email for identity; it will surface the error there if needed.
	}

	return Credential{
		AccessToken:  raw.Tokens.AccessToken,
		RefreshToken: raw.Tokens.RefreshToken,
		ExpiresAt:    expiresAt,
		Raw:          bytes.Clone(data),
	}, nil
}

// ReadCodexCredential returns the full credential blob from ~/.codex/auth.json.
// ctx is accepted for signature parity; not used by the current implementation.
func ReadCodexCredential(ctx context.Context) (Credential, error) {
	path, err := codexAuthPath()
	if err != nil {
		return Credential{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Credential{}, ErrCodexTokenNotFound
		}
		return Credential{}, fmt.Errorf("reading codex auth.json: %w", err)
	}
	return parseCodexCredFull(data)
}

// ReadCodexToken returns the OAuth access token from ~/.codex/auth.json.
// ctx is accepted for signature parity; not used by the current implementation.
func ReadCodexToken(ctx context.Context) (string, error) {
	c, err := ReadCodexCredential(ctx)
	return c.AccessToken, err
}

// WriteCodexLiveBlob atomically overwrites ~/.codex/auth.json with rawBlob.
// The parent directory (~/.codex) is created with mode 0700 if absent.
// The file is written with mode 0600 via a temporary file + os.Rename so a
// partial write is never observable. fsync before rename matches the pattern
// in internal/cred/keychain_linux.go and internal/providers/claude/usagecache.go.
// ctx is accepted for signature parity; not used by the current implementation.
func WriteCodexLiveBlob(ctx context.Context, rawBlob []byte) error {
	path, err := codexAuthPath()
	if err != nil {
		return fmt.Errorf("cannot resolve codex auth path: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating codex auth directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".auth-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary codex auth file: %w", err)
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("setting codex auth file mode: %w", err)
	}
	if _, err := tmp.Write(rawBlob); err != nil {
		tmp.Close()
		return fmt.Errorf("writing codex auth file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("syncing codex auth file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing codex auth file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("installing codex auth file: %w", err)
	}
	committed = true
	return nil
}
