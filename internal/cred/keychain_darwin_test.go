//go:build darwin

package cred

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/drogers0/aistat/v2/internal/testutil"
)

// TestWriteClaudeLiveBlob verifies the seam-based and live keychain write paths.
func TestWriteClaudeLiveBlob(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		// seam args verifies that WriteClaudeLiveBlob invokes the security(1)
		// tool with the exact arguments for a single update:
		//
		//	add-generic-password -U -s "Claude Code-credentials" -a <user> -w <blob>
		//
		// The earlier two-step (add + set-generic-password-partition-list) was
		// dropped after empirical observation showed the partition-list call
		// (a) prompts the user for their keychain password every invocation and
		// (b) is unnecessary — the per-binary ACL entry that `claude /login`
		// originally placed on the item survives `-U` updates, so the Claude CLI's
		// reads remain silent without any further ACL manipulation. See the
		// WriteClaudeLiveBlob doc comment.
		//
		// The runSecurity seam is replaced for the duration of this test so no real
		// keychain access occurs. This test always runs (no AISTAT_LIVE_KEYCHAIN guard).
		{"seam args", func(t *testing.T) {
			blob := []byte(`{"claudeAiOauth":{"accessToken":"test-tok"}}`)
			u := claudeUser()

			type call struct{ args []string }
			var calls []call

			orig := runSecurity
			t.Cleanup(func() { runSecurity = orig })
			runSecurity = func(ctx context.Context, args ...string) ([]byte, error) {
				calls = append(calls, call{args: append([]string(nil), args...)})
				return nil, nil
			}

			testutil.WantNoErr(t, WriteClaudeLiveBlob(context.Background(), blob))

			if len(calls) != 1 {
				t.Fatalf("expected 1 security call, got %d", len(calls))
			}

			want := []string{
				"add-generic-password", "-U",
				"-s", "Claude Code-credentials",
				"-a", u,
				"-w", string(blob),
			}
			if !slices.Equal(calls[0].args, want) {
				t.Errorf("call[0] args:\ngot:  %v\nwant: %v", calls[0].args, want)
			}
		}},
		// seam error propagated verifies that an error from the security call
		// surfaces with the documented wrap.
		{"seam error propagated", func(t *testing.T) {
			blob := []byte(`{"claudeAiOauth":{"accessToken":"tok"}}`)

			var calls int
			orig := runSecurity
			t.Cleanup(func() { runSecurity = orig })
			runSecurity = func(ctx context.Context, args ...string) ([]byte, error) {
				calls++
				// Return a non-ExitError so the error falls through to the %w wrap in
				// WriteClaudeLiveBlob. ctx.Err() returns nil (context is not cancelled),
				// so this exercises the plain-error branch, not the context branch.
				return nil, &exec.Error{Name: "security", Err: os.ErrPermission}
			}

			err := WriteClaudeLiveBlob(context.Background(), blob)
			if err == nil {
				t.Fatal("expected error")
			}
			if calls != 1 {
				t.Errorf("expected exactly 1 security call, got %d", calls)
			}
		}},
		// live keychain exercises the real macOS Keychain item (requires AISTAT_LIVE_KEYCHAIN=1).
		{"live keychain", func(t *testing.T) {
			if os.Getenv("AISTAT_LIVE_KEYCHAIN") != "1" {
				t.Skip("skipping live keychain test (set AISTAT_LIVE_KEYCHAIN=1 to enable)")
			}

			ctx := context.Background()
			// Backup the current credential so we can restore it.
			orig, backupErr := ReadClaudeCredential(ctx)
			t.Cleanup(func() {
				if backupErr == nil {
					if err := WriteClaudeLiveBlob(context.Background(), orig.Raw); err != nil {
						t.Logf("warning: failed to restore keychain credential: %v", err)
					}
				}
			})

			sentinel := []byte(`{"claudeAiOauth":{"accessToken":"aistat-live-test-sentinel","refreshToken":"","expiresAt":0}}`)
			testutil.WantNoErr(t, WriteClaudeLiveBlob(ctx, sentinel))

			got, err := ReadClaudeCredential(ctx)
			testutil.WantNoErr(t, err)
			if got.AccessToken != "aistat-live-test-sentinel" {
				t.Errorf("AccessToken: got %q, want %q", got.AccessToken, "aistat-live-test-sentinel")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

// TestReadClaudeCredential verifies the seam-based credential read paths.
func TestReadClaudeCredential(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		// seam verifies that ReadClaudeCredential uses the runSecurity seam and
		// correctly parses the returned JSON. Always runs.
		{"seam", func(t *testing.T) {
			payload := []byte(`{"claudeAiOauth":{"accessToken":"seam-tok","refreshToken":"rt","expiresAt":99}}`)

			orig := runSecurity
			t.Cleanup(func() { runSecurity = orig })
			runSecurity = func(ctx context.Context, args ...string) ([]byte, error) {
				return payload, nil
			}

			c, err := ReadClaudeCredential(context.Background())
			testutil.WantNoErr(t, err)
			if c.AccessToken != "seam-tok" {
				t.Errorf("AccessToken: got %q, want %q", c.AccessToken, "seam-tok")
			}
			if c.RefreshToken != "rt" {
				t.Errorf("RefreshToken: got %q, want %q", c.RefreshToken, "rt")
			}
			if c.ExpiresAt != 99 {
				t.Errorf("ExpiresAt: got %d, want %d", c.ExpiresAt, 99)
			}
			if !bytes.Equal(c.Raw, payload) {
				t.Errorf("Raw mismatch\ngot:  %q\nwant: %q", c.Raw, payload)
			}
		}},
		// seam trailing newline single stripped verifies that security(1)'s
		// trailing newline is stripped (exactly one) before bytes are stored in
		// Credential.Raw.
		{"seam trailing newline single stripped", func(t *testing.T) {
			payload := []byte(`{"claudeAiOauth":{"accessToken":"seam-tok"}}`)
			withNewline := append(append([]byte(nil), payload...), '\n')

			orig := runSecurity
			t.Cleanup(func() { runSecurity = orig })
			runSecurity = func(ctx context.Context, args ...string) ([]byte, error) {
				return withNewline, nil
			}

			c, err := ReadClaudeCredential(context.Background())
			testutil.WantNoErr(t, err)
			if !bytes.Equal(c.Raw, payload) {
				t.Errorf("Raw should not contain trailing newline\ngot:  %q\nwant: %q", c.Raw, payload)
			}
		}},
		// seam trailing newline payload internal preserved verifies that a JSON
		// payload that ends with '\n' before security appends another has exactly
		// one '\n' removed (not both).
		{"seam trailing newline payload internal preserved", func(t *testing.T) {
			// A JSON payload that ends with '\n' before security appends another.
			// TrimSuffix must remove exactly one '\n', not both.
			payloadWithNL := []byte("{\"claudeAiOauth\":{\"accessToken\":\"tok\"}}\n")
			fromSecurity := append(append([]byte(nil), payloadWithNL...), '\n')

			orig := runSecurity
			t.Cleanup(func() { runSecurity = orig })
			runSecurity = func(ctx context.Context, args ...string) ([]byte, error) {
				return fromSecurity, nil
			}

			c, err := ReadClaudeCredential(context.Background())
			testutil.WantNoErr(t, err)
			if !bytes.Equal(c.Raw, payloadWithNL) {
				t.Errorf("Raw should preserve payload-internal newline\ngot:  %q\nwant: %q", c.Raw, payloadWithNL)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
