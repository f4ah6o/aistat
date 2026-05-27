package render

import (
	"testing"

	"github.com/drogers0/llm-usage/internal/providers/claude"
	"github.com/drogers0/llm-usage/internal/providers/codex"
	"github.com/drogers0/llm-usage/internal/providers/copilot"
)

// TestTextLabels_CoverEveryKnownWindow asserts that every window key exported
// by a provider package appears in textLabels for that provider. Catches the
// drift where someone adds a window to a provider but forgets to add a label
// here.
func TestTextLabels_CoverEveryKnownWindow(t *testing.T) {
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
	}
}
