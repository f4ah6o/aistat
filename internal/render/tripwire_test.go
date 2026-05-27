package render

import (
	"testing"

	"github.com/drogers0/aistat/internal/providers/claude"
	"github.com/drogers0/aistat/internal/providers/codex"
	"github.com/drogers0/aistat/internal/providers/copilot"
)

// TestTextLabels_ExactlyMatchKnownWindows asserts both directions of the
// label/window contract for every provider: every key in KnownWindows has a
// textLabels entry (catches a missing label), and every textLabels entry
// corresponds to a key in KnownWindows (catches a stale label after a window
// is removed).
func TestTextLabels_ExactlyMatchKnownWindows(t *testing.T) {
	cases := map[string][]string{
		"claude":  claude.KnownWindows,
		"codex":   codex.KnownWindows,
		"copilot": copilot.KnownWindows,
	}
	for id, windows := range cases {
		present := map[string]bool{}
		for _, kl := range textLabels[id] {
			present[kl.Key] = true
		}
		for _, w := range windows {
			if !present[w] {
				t.Errorf("textLabels[%q] is missing key %q (exported in %s.KnownWindows)", id, w, id)
			}
		}
		allowed := map[string]bool{}
		for _, w := range windows {
			allowed[w] = true
		}
		for _, kl := range textLabels[id] {
			if !allowed[kl.Key] {
				t.Errorf("textLabels[%q] has stale key %q (not in %s.KnownWindows)", id, kl.Key, id)
			}
		}
	}
}
