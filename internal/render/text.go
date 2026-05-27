package render

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/drogers0/llm-usage/internal/providers"
)

// textLabels holds the human-facing label and the display order for every
// known limit key per provider. Update this table whenever a provider adds
// a new known limit key (mirrors the provider package's own window list).
var textLabels = map[string][]struct{ Key, Label string }{
	"claude":  {{"five_hour", "5-hour"}, {"seven_day", "7-day"}, {"seven_day_sonnet", "7-day sonnet"}},
	"codex":   {{"five_hour", "5-hour"}, {"seven_day", "7-day"}, {"code_review_seven_day", "Code review 7-day"}},
	"copilot": {{"month", "month"}},
}

// Text writes the human-readable rendering of report r for the providers in
// requested (in the given order).
func Text(w io.Writer, r providers.Report, requested []string) error {
	if len(requested) == 0 {
		return nil
	}
	var sections []string
	for _, id := range requested {
		result, ok := r.Providers[id]
		if !ok {
			continue
		}
		title := providers.Title(id) + " usage"
		if result.Error != "" {
			sections = append(sections, title+": "+result.Error)
			continue
		}
		var lines []string
		lines = append(lines, title)
		known := textLabels[id]
		seen := map[string]bool{}
		for _, kl := range known {
			limit, ok := result.Limits[kl.Key]
			if !ok {
				continue
			}
			seen[kl.Key] = true
			lines = append(lines, formatLimitLine(kl.Label, limit))
		}
		var unknown []string
		for k := range result.Limits {
			if !seen[k] {
				unknown = append(unknown, k)
			}
		}
		sort.Strings(unknown)
		for _, k := range unknown {
			lines = append(lines, formatLimitLine(k, result.Limits[k]))
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	if _, err := io.WriteString(w, strings.Join(sections, "\n\n")+"\n"); err != nil {
		return err
	}
	return nil
}

func formatLimitLine(label string, l providers.Limit) string {
	return fmt.Sprintf("- %s: %.1f%% (resets in %s)", label, l.UsedPercent, formatResetDuration(l.ResetAfterSeconds))
}

func formatResetDuration(seconds int) string {
	if seconds <= 0 {
		return "0m"
	}
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}
