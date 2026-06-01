//go:build fake

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestCLI_FakeJSON_All(t *testing.T) {
	r := runCLI("--fake")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, r.stdout)
	}
	if _, ok := got["checked_at"]; !ok {
		t.Fatal("missing checked_at")
	}
	provs, _ := got["providers"].(map[string]any)
	for _, id := range []string{"claude", "codex", "copilot"} {
		if _, ok := provs[id]; !ok {
			t.Errorf("missing provider %s", id)
		}
	}
}

func TestCLI_FakeJSON_SingleProvider(t *testing.T) {
	r := runCLI("usage", "claude", "--fake")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	var got map[string]any
	_ = json.Unmarshal([]byte(r.stdout), &got)
	provs, _ := got["providers"].(map[string]any)
	if _, ok := provs["claude"]; !ok {
		t.Error("missing claude")
	}
	if _, ok := provs["codex"]; ok {
		t.Error("codex should be absent when single provider requested")
	}
	if _, ok := got["checked_at"]; !ok {
		t.Error("checked_at must always be present")
	}
}

func TestCLI_FakeText_DesignSampleShape(t *testing.T) {
	r := runCLI("--fake", "-h")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	// Provider order: Claude → Codex → Copilot.
	iC := strings.Index(r.stdout, "Claude usage")
	iCx := strings.Index(r.stdout, "Codex usage")
	iCp := strings.Index(r.stdout, "Copilot usage")
	if !(iC >= 0 && iC < iCx && iCx < iCp) {
		t.Fatalf("wrong section order:\n%s", r.stdout)
	}
	// Sanity-check one line shape with the design's format.
	if !regexp.MustCompile(`- 5-hour: \d+\.\d% \(resets in [^\)]+\)`).MatchString(r.stdout) {
		t.Fatalf("5-hour line missing or malformed:\n%s", r.stdout)
	}
}

func TestCLI_ProviderViaUsageSubcommand(t *testing.T) {
	// Provider specified under the "usage" subcommand.
	r := runCLI("usage", "claude", "--fake", "-h")
	if r.code != 0 {
		t.Fatalf("exit %d, stderr: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "Claude usage") {
		t.Fatalf("missing Claude section: %s", r.stdout)
	}
	if strings.Contains(r.stdout, "Codex usage") {
		t.Fatalf("Codex should be absent: %s", r.stdout)
	}
}

func TestCLI_UnknownSubcmdWithFakeFlag(t *testing.T) {
	// A bare token that isn't a known subcommand → exit 2 with unknown-subcommand error.
	r := runCLI("--fake", "-h", "bogussubcmd")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d (stdout %q)", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, `unknown subcommand "bogussubcmd"`) {
		t.Fatalf("missing unknown-subcommand error: %s", r.stderr)
	}
}

func TestCLI_HumanLongForm(t *testing.T) {
	a := runCLI("--fake", "-h").stdout
	b := runCLI("--fake", "--human").stdout
	if a != b {
		t.Fatalf("-h and --human should match:\n%s\nvs\n%s", a, b)
	}
}

func TestCLI_TrailingPositionalUnderUsage(t *testing.T) {
	// Two positionals under "usage" → second is rejected.
	r := runCLI("usage", "claude", "codex", "--fake")
	if r.code != 2 {
		t.Fatalf("expected exit 2, got %d", r.code)
	}
	if !strings.Contains(r.stderr, "unexpected positional argument: codex") {
		t.Fatalf("missing trailing-positional error: %s", r.stderr)
	}
}

func TestCLI_FakeProviderFailureExits1(t *testing.T) {
	// Round-6 contract: exit 1 for runtime failures, exit 2 reserved for usage
	// / contract errors. The --fake-fail=claude knob injects a hard error in
	// the claude provider's Fetch; the other two still succeed, so the JSON
	// must contain claude.error AND codex.accounts (per-account shape).
	r := runCLI("--fake", "--fake-fail=claude")
	if r.code != 1 {
		t.Fatalf("expected exit 1 for runtime failure, got %d (stderr %q)", r.code, r.stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, r.stdout)
	}
	provs, _ := got["providers"].(map[string]any)
	claude, _ := provs["claude"].(map[string]any)
	if claude["error"] == nil || claude["error"] == "" {
		t.Errorf("expected claude.error to be populated, got %v", claude)
	}
	if claude["limits"] != nil {
		t.Errorf("claude.limits should be null on failure, got %v", claude["limits"])
	}
	codex, _ := provs["codex"].(map[string]any)
	if codex["accounts"] == nil {
		t.Errorf("expected codex.accounts to still be present (a failing sibling does not block successes), got %v", codex)
	}
}
