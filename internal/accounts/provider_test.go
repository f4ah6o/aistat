//go:build darwin || linux

package accounts

import (
	"testing"
)

func TestProvider_ValidateClaude(t *testing.T) {
	if err := ProviderClaude.validate(); err != nil {
		t.Errorf("ProviderClaude.validate(): want nil, got %v", err)
	}
}

func TestProvider_ValidateCodex(t *testing.T) {
	if err := ProviderCodex.validate(); err != nil {
		t.Errorf("ProviderCodex.validate(): want nil, got %v", err)
	}
}

func TestProvider_ValidateEmpty(t *testing.T) {
	if err := Provider("").validate(); err == nil {
		t.Error("Provider(\"\").validate(): want error, got nil")
	}
}

func TestProvider_ValidatePathTraversal(t *testing.T) {
	if err := Provider("../attack").validate(); err == nil {
		t.Error("Provider(\"../attack\").validate(): want error, got nil")
	}
}

func TestProvider_ValidateUnknown(t *testing.T) {
	if err := Provider("unknown").validate(); err == nil {
		t.Error("Provider(\"unknown\").validate(): want error, got nil")
	}
}

func TestProvider_ValidatePathSeparator(t *testing.T) {
	if err := Provider("claude/foo").validate(); err == nil {
		t.Error("Provider(\"claude/foo\").validate(): want error, got nil")
	}
}

// TestOpenStore_InvalidProvider asserts that OpenStore returns an error for
// invalid provider strings on every supported platform.
func TestOpenStore_InvalidProvider(t *testing.T) {
	cases := []Provider{
		Provider(""),
		Provider("../attack"),
		Provider("unknown"),
		Provider("claude/foo"),
	}
	for _, p := range cases {
		t.Run(string(p), func(t *testing.T) {
			// Use a temp home so no real credential dirs are touched.
			t.Setenv("HOME", t.TempDir())
			_, err := OpenStore(p)
			if err == nil {
				t.Errorf("OpenStore(%q): want error, got nil", p)
			}
		})
	}
}
