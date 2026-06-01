package accounts

import "fmt"

// Provider identifies which provider's account store to open.
// The value is used as the filename stem on Linux and as a component of the
// keychain service name on darwin, so it must be a stable lower-case ASCII
// string. Do not change existing values — they are part of the on-disk /
// keychain path contract.
type Provider string

const (
	// ProviderClaude is the Claude provider. Maps to:
	//   Linux:  ~/.config/aistat/accounts/claude.json  (lock: .claude.lock)
	//   Darwin: keychain service prefix aistat:accounts:claude:
	ProviderClaude Provider = "claude"

	// ProviderCodex is the Codex provider. Maps to:
	//   Linux:  ~/.config/aistat/accounts/codex.json  (lock: .codex.lock)
	//   Darwin: keychain service prefix aistat:accounts:codex:
	ProviderCodex Provider = "codex"
)

// validate returns an error if p is not a member of the allowed provider set.
// Allowed values must be non-empty, consist only of [a-z0-9_-], and be one of
// ProviderClaude or ProviderCodex.
func (p Provider) validate() error {
	s := string(p)
	if s == "" {
		return fmt.Errorf("accounts: invalid provider %q: must not be empty", s)
	}
	for _, b := range []byte(s) {
		if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-') {
			return fmt.Errorf("accounts: invalid provider %q: contains disallowed character", s)
		}
	}
	if p != ProviderClaude && p != ProviderCodex {
		return fmt.Errorf("accounts: invalid provider %q: not in allowed set", s)
	}
	return nil
}
