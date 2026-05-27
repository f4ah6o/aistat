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

// ProjectURL is the upstream repository for this binary. Used in the User-Agent
// (so endpoint owners can identify the client) and as the prefix for
// IssueTrackerURL.
const ProjectURL = "https://github.com/drogers0/aistat"

// IssueTrackerURL is the upstream issue tracker. Cited in provider error
// messages emitted when an upstream-API shape change is detected, so users
// can file a bug with the exact context already in the message.
const IssueTrackerURL = ProjectURL + "/issues"

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
// internal value of 67.339999999 becomes 67.34 on the wire). A zero ResetsAt
// is rejected here as a defensive contract guard — every provider must populate
// the field, and silently emitting "0001-01-01T00:00:00+00:00" would be worse
// than failing loudly. The fields are listed manually rather than via the
// "type alias + embed" trick because Go's encoding/json does not deduplicate
// struct fields by JSON tag.
func (l Limit) MarshalJSON() ([]byte, error) {
	if l.ResetsAt.IsZero() {
		return nil, errors.New("Limit.MarshalJSON: ResetsAt is zero — provider must populate this field")
	}
	return json.Marshal(struct {
		UsedPercent       float64 `json:"used_percent"`
		RemainingPercent  float64 `json:"remaining_percent"`
		ResetsAt          string  `json:"resets_at"`
		ResetAfterSeconds int     `json:"reset_after_seconds"`
	}{
		UsedPercent:       roundPct(l.UsedPercent),
		RemainingPercent:  roundPct(l.RemainingPercent),
		ResetsAt:          l.ResetsAt.UTC().Format(iso8601Layout),
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
		CheckedAt: r.CheckedAt.UTC().Format(iso8601Layout),
		Providers: r.Providers,
	})
}

// ProviderResult is one provider's contribution to the Report.
//
// Error is set if and only if Fetch returned non-nil. On success, Limits
// may be empty: a provider responded but returned no recognized window
// (e.g. an upstream schema change renamed every known window key). Scripted
// callers should treat "absent error + empty limits" as "provider responded
// with nothing to show," distinct from a failure.
//
// Invariant for provider implementations: do NOT set Limits to a non-nil
// empty map after parsing. Always nil it out when the parsed window set
// is empty so the omitempty tag suppresses the "limits": {} key uniformly.
//
// Both fields use omitempty so a successful provider serializes without an
// "error" key and a failed one without an empty "limits" key.
type ProviderResult struct {
	Limits map[string]Limit `json:"limits,omitempty"`
	Error  string           `json:"error,omitempty"`
}
