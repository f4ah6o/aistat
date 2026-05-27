package providers

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// Sentinel errors that providers wrap with %w so the orchestrator
// can classify retry behavior with a single errors.Is switch.
var (
	ErrAuthMissing = errors.New("auth missing")
	ErrAuthDenied  = errors.New("auth denied")
	ErrTransient   = errors.New("transient failure")
)

// iso8601Layout renders UTC as "+00:00" (not "Z"), matching the documented
// JSON output contract.
const iso8601Layout = "2006-01-02T15:04:05-07:00"

// KnownProviderIDs is the canonical list of provider identifiers in the order
// the CLI documents them. The renderer's default order, the orchestrator's
// requested-set when no provider is specified, and the CLI help text all
// derive from this slice. Treat as immutable.
var KnownProviderIDs = []string{"claude", "codex", "copilot"}

// Title returns the provider ID with its first byte upper-cased. Provider IDs
// in this package are ASCII; do not use for arbitrary strings.
func Title(id string) string {
	if id == "" {
		return ""
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// Provider is the contract every credential+endpoint backend implements.
type Provider interface {
	ID() string
	Fetch(ctx context.Context) (ProviderOutput, error)
}

// ProviderOutput is what a provider returns on success. The map key is the
// limit-window name (e.g. "five_hour", "seven_day", "month").
type ProviderOutput struct {
	Limits map[string]Limit
}

// Limit is one usage window for one provider.
type Limit struct {
	UsedPercent       float64   `json:"used_percent"`
	RemainingPercent  float64   `json:"remaining_percent"`
	ResetsAt          time.Time `json:"resets_at"`
	ResetAfterSeconds int       `json:"reset_after_seconds"`
}

// MarshalJSON emits the four documented fields in the documented order, with
// ResetsAt formatted as "+00:00" instead of Go's default "Z" and percent
// fields rounded to 2 decimal places to suppress float artifacts (e.g. an
// internal value of 67.339999999 becomes 67.34 on the wire). Provider code
// keeps raw values in the struct; rounding is purely a JSON-boundary concern.
// The fields are listed manually rather than via the "type alias + embed"
// trick because Go's encoding/json does not deduplicate struct fields by
// JSON tag.
func (l Limit) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		UsedPercent       float64 `json:"used_percent"`
		RemainingPercent  float64 `json:"remaining_percent"`
		ResetsAt          string  `json:"resets_at"`
		ResetAfterSeconds int     `json:"reset_after_seconds"`
	}{
		UsedPercent:       roundPct(l.UsedPercent),
		RemainingPercent:  roundPct(l.RemainingPercent),
		ResetsAt:          l.ResetsAt.Format(iso8601Layout),
		ResetAfterSeconds: l.ResetAfterSeconds,
	})
}

func roundPct(f float64) float64 { return math.Round(f*100) / 100 }

// Report is the top-level JSON document. The orchestrator populates it;
// renderers consume it. Lives here, not in internal/render, so the
// orchestrator never imports the renderer.
type Report struct {
	CheckedAt time.Time                 `json:"checked_at"`
	Providers map[string]ProviderResult `json:"providers"`
}

func (r Report) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		CheckedAt string                    `json:"checked_at"`
		Providers map[string]ProviderResult `json:"providers"`
	}{
		CheckedAt: r.CheckedAt.Format(iso8601Layout),
		Providers: r.Providers,
	})
}

// ProviderResult is one provider's contribution to the Report. Exactly one of
// Limits / Error is populated in practice; both use omitempty so a
// successful provider serializes without an "error" key and a failed one
// without an empty "limits" key.
type ProviderResult struct {
	Limits map[string]Limit `json:"limits,omitempty"`
	Error  string           `json:"error,omitempty"`
}
