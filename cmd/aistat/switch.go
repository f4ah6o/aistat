package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drogers0/aistat/v2/internal/accounts"
	"github.com/drogers0/aistat/v2/internal/cred"
	"github.com/drogers0/aistat/v2/internal/orchestrate"
	"github.com/drogers0/aistat/v2/internal/providers"
	"github.com/drogers0/aistat/v2/internal/providers/claude"
	codex "github.com/drogers0/aistat/v2/internal/providers/codex"
)

// switchable is the minimal interface that both claude.Client and codex.Client satisfy.
type switchable interface {
	FetchForSwitch(ctx context.Context) ([]providers.AccountResult, error)
	ReconcileAndPersist(ctx context.Context) error
	PostSwitchVerify(ctx context.Context, target accounts.Account) error
}

// Package-level injection seams — overridden by tests.
var (
	// writeClaudeLiveBlob writes rawBlob to the live Claude credential store.
	writeClaudeLiveBlob = cred.WriteClaudeLiveBlob

	// newSwitchClient constructs the Claude client used by runSwitch.
	newSwitchClient = func(debug io.Writer, ua string, store accounts.Store) switchable {
		return claude.New(debug, ua, claude.WithStore(store))
	}

	// switchLookupActiveUUID resolves the currently-active account UUID from the
	// live Claude credential.
	switchLookupActiveUUID = realSwitchLookupActiveUUID

	// fetchLiveUsage fetches usage limits for the active Claude account's access token.
	fetchLiveUsage = realFetchLiveUsage

	// writeCodexLiveBlob writes rawBlob to the live Codex credential store.
	writeCodexLiveBlob = cred.WriteCodexLiveBlob

	// newCodexSwitchClient constructs the Codex client used by runSwitch.
	newCodexSwitchClient = func(debug io.Writer, ua string, store accounts.Store) switchable {
		return codex.New(debug, ua, codex.WithStore(store))
	}

	// switchLookupCodexActiveUUID resolves the currently-active Codex account UUID.
	switchLookupCodexActiveUUID = realSwitchLookupCodexActiveUUID

	// fetchCodexLiveUsage fetches usage limits for the active Codex account.
	fetchCodexLiveUsage = realFetchCodexLiveUsage
)

// resolveCodexActiveUUID reads the live Codex credential and resolves the
// currently-active account UUID. Returns ("", nil) when unknowable (no live
// blob, parse error). Called by both realSwitchLookupCodexActiveUUID and
// makeRealCodexActiveUUIDResolver.
func resolveCodexActiveUUID(ctx context.Context, stored []accounts.Account) (string, error) {
	credCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cr, err := cred.ReadCodexCredential(credCtx)
	if err != nil {
		return "", nil // ErrCodexTokenNotFound or read failure → "no active account"
	}
	return codex.ResolveActiveUUID(codex.ReconcileInput{
		LiveBlob: &cr,
		Stored:   stored,
		LookupID: func(idToken string) (string, string, error) {
			sub, email, _, err := cred.ParseCodexIDToken(idToken)
			return sub, email, err
		},
		Now: time.Now(),
	})
}

func realSwitchLookupCodexActiveUUID(ctx context.Context, stored []accounts.Account, _ io.Writer) (string, error) {
	return resolveCodexActiveUUID(ctx, stored)
}

func realFetchCodexLiveUsage(ctx context.Context, token, uuid, ua string, debug io.Writer) (map[string]providers.Limit, error) {
	return codex.New(debug, ua).FetchUsage(ctx, token, uuid)
}

func realFetchLiveUsage(ctx context.Context, token, uuid, ua string, debug io.Writer) (map[string]providers.Limit, error) {
	return claude.New(debug, ua).FetchUsage(ctx, token, uuid)
}

// realSwitchLookupActiveUUID reads the live credential and resolves the
// currently-active Claude account UUID.
func realSwitchLookupActiveUUID(ctx context.Context, stored []accounts.Account, debug io.Writer) (string, error) {
	credCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cr, err := cred.ReadClaudeCredential(credCtx)
	if err != nil {
		if errors.Is(err, cred.ErrClaudeTokenNotFound) {
			return "", nil
		}
		return "", nil // treat any read failure as "no active account"
	}
	lookupClient := claude.New(debug, claude.DefaultUserAgent(resolvedVersion()))
	return claude.ResolveActiveUUID(claude.ReconcileInput{
		LiveBlob: &cr,
		Stored:   stored,
		LookupProfile: func(token string) (claude.Profile, error) {
			return lookupClient.GetProfile(ctx, token)
		},
		Now: time.Now(),
	})
}

// switchHandle bundles all provider-specific operations for the switch dispatcher.
// Adding a new switchable provider means adding one entry to buildSwitchHandles.
type switchHandle struct {
	id             string
	store          accounts.Store
	ua             string // per-provider User-Agent; set by buildSwitchHandles
	loginHint      string // surfaced in the one-account error: e.g. "run `claude /login` to add another"
	client         switchable
	lookupActive   func(ctx context.Context, stored []accounts.Account, debug io.Writer) (string, error)
	writeLiveBlob  func(ctx context.Context, raw []byte) error
	fetchLiveUsage func(ctx context.Context, token, uuid, ua string, debug io.Writer) (map[string]providers.Limit, error)
	storedAccess   func(a accounts.Account) string // extract access token from RawBlob
}

// buildSwitchHandles opens the per-provider stores and assembles one handle
// per switchable provider. An error opening a store is fatal-closed. Both
// stores are opened unconditionally even for single-provider invocations —
// the cost is one extra keychain/file read which is acceptable given store
// opens are cheap.
func buildSwitchHandles(debugW io.Writer, version string) ([]switchHandle, error) {
	claudeStore, err := openAccountStore(debugW)
	if err != nil {
		return nil, fmt.Errorf("claude: could not open account store: %w", err)
	}
	codexStore, err := openCodexAccountStore(debugW)
	if err != nil {
		return nil, fmt.Errorf("codex: could not open account store: %w", err)
	}
	// Each provider uses its own DefaultUserAgent — do not share a single UA string.
	claudeUA := claude.DefaultUserAgent(version)
	codexUA := codex.DefaultUserAgent(version)
	return []switchHandle{
		{
			id:             "claude",
			store:          claudeStore,
			ua:             claudeUA,
			loginHint:      "run `claude /login` to add another",
			client:         newSwitchClient(debugW, claudeUA, claudeStore),
			lookupActive:   switchLookupActiveUUID,
			writeLiveBlob:  writeClaudeLiveBlob,
			fetchLiveUsage: fetchLiveUsage,
			storedAccess:   func(a accounts.Account) string { return claude.StoredAccessToken(a) },
		},
		{
			id:             "codex",
			store:          codexStore,
			ua:             codexUA,
			loginHint:      "add another ChatGPT account and run `aistat usage` to register it",
			client:         newCodexSwitchClient(debugW, codexUA, codexStore),
			lookupActive:   switchLookupCodexActiveUUID,
			writeLiveBlob:  writeCodexLiveBlob,
			fetchLiveUsage: fetchCodexLiveUsage,
			storedAccess:   func(a accounts.Account) string { return codex.StoredAccessToken(a) },
		},
	}, nil
}

func knownSwitchProvider(p string) bool {
	return p == "claude" || p == "codex"
}

func handleByID(handles []switchHandle, id string) switchHandle {
	for _, h := range handles {
		if h.id == id {
			return h
		}
	}
	panic("handleByID: unknown provider " + id) // caller already validated
}

// fiveHourBucket returns the D13 auto-pick sort bucket for a limits map.
// bucket = floor(five_hour.RemainingPercent / 5). Returns -1 when five_hour is absent.
func fiveHourBucket(limits map[string]providers.Limit) int {
	if limits == nil {
		return -1
	}
	win, ok := limits["five_hour"]
	if !ok {
		return -1
	}
	return int(math.Floor(win.RemainingPercent / 5))
}

// switchBetter reports whether candidate a is preferred over b for D13 auto-pick.
// Primary sort: fiveHourBucket descending. Tiebreak: lastSeen descending (more recent wins).
func switchBetter(a providers.AccountResult, aLastSeen time.Time, b providers.AccountResult, bLastSeen time.Time) bool {
	ba := fiveHourBucket(a.Limits)
	bb := fiveHourBucket(b.Limits)
	if ba != bb {
		return ba > bb
	}
	return aLastSeen.After(bLastSeen)
}

// findAccountByUUID returns a pointer to the first account in stored whose UUID
// equals uuid, or nil if not found.
func findAccountByUUID(stored []accounts.Account, uuid string) *accounts.Account {
	for i := range stored {
		if stored[i].UUID == uuid {
			return &stored[i]
		}
	}
	return nil
}

// runSwitch implements the `aistat switch` subcommand.
func runSwitch(args []string, stdout, stderr io.Writer, g globals) int {
	// 1. Flag setup + two-pass parse (mirrors usage.go).
	fs := flag.NewFlagSet("switch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var toArg string
	fs.StringVar(&toArg, "to", "", "")
	registerGlobalFlags(fs, &g)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return int(orchestrate.StatusUsageError)
	}
	if handled, code := handleGlobals(g, stdout); handled {
		return code
	}
	// Extract optional <provider> positional.
	var providerArg string
	tail := fs.Args()
	if len(tail) > 0 {
		providerArg = tail[0]
		tail = tail[1:]
	}
	// Second parse so fs.NArg() reflects only truly unconsumed positionals.
	if err := fs.Parse(tail); err != nil {
		fmt.Fprintln(stderr, err.Error())
		return int(orchestrate.StatusUsageError)
	}
	if handled, code := handleGlobals(g, stdout); handled {
		return code
	}
	// Reject leftover positionals.
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", fs.Arg(0))
		return int(orchestrate.StatusUsageError)
	}

	// 2. Validate provider arg if given.
	if providerArg != "" && !knownSwitchProvider(providerArg) {
		fmt.Fprintf(stderr, "unknown provider %q — want claude or codex\n", providerArg)
		return int(orchestrate.StatusUsageError)
	}

	// 3. Setup: context, debug writer.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var debugW io.Writer
	if g.Debug {
		debugW = stderr
	}

	// 4. Build handles (fail-closed on store-open error).
	handles, err := buildSwitchHandles(debugW, resolvedVersion())
	if err != nil {
		fmt.Fprintf(stderr, "aistat: %s\n", err)
		return int(orchestrate.StatusUsageError)
	}

	// 5. Route.
	if providerArg != "" {
		h := handleByID(handles, providerArg)
		return runSwitchSingle(ctx, h, toArg, stdout, stderr, debugW)
	} else if toArg != "" {
		return runSwitchInferProvider(ctx, handles, toArg, stdout, stderr, debugW)
	}
	return runSwitchBulk(ctx, handles, stdout, stderr, debugW)
}

// runSwitchSingle performs a switch for a single provider handle.
// It contains the existing pick-target → write → reconcile logic.
func runSwitchSingle(ctx context.Context, h switchHandle, toArg string, stdout, stderr, debugW io.Writer) int {
	stored, err := h.store.List(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "aistat: %s: could not list accounts: %s\n", h.id, err)
		return int(orchestrate.StatusUsageError)
	}

	activeUUID, _ := h.lookupActive(ctx, stored, debugW)
	prevEmail := "none"
	if activeAcct := findAccountByUUID(stored, activeUUID); activeAcct != nil {
		prevEmail = activeAcct.Email
	}

	var target accounts.Account

	if toArg != "" {
		// Explicit --to mode: resolve by email substring or UUID prefix.
		matches := matchAccounts(toArg, stored)
		switch len(matches) {
		case 0:
			fmt.Fprintf(stderr, "no stored account matches %q\n", toArg)
			return int(orchestrate.StatusUsageError)
		case 1:
			// fall through
		default:
			fmt.Fprintf(stderr, "multiple stored accounts match %q, disambiguate by uuid\n", toArg)
			return int(orchestrate.StatusUsageError)
		}
		target = matches[0]
		if target.UUID == activeUUID {
			fmt.Fprintf(stdout, "already on %s\n", target.Email)
			return 0
		}
	} else {
		// Auto-pick mode: fetch usage for non-active accounts.
		if len(stored) == 0 {
			fmt.Fprintf(stderr, "no accounts stored; %s\n", h.loginHint)
			return int(orchestrate.StatusUsageError)
		}
		if len(stored) == 1 && stored[0].UUID == activeUUID {
			fmt.Fprintf(stderr, "only one account stored; nothing to switch to (%s)\n", h.loginHint)
			return int(orchestrate.StatusUsageError)
		}

		candidates, fetchErr := h.client.FetchForSwitch(ctx)
		if fetchErr != nil {
			fmt.Fprintf(stderr, "aistat: %s: auto-pick usage fetch failed: %s\n", h.id, fetchErr)
			return int(orchestrate.StatusUsageError)
		}

		if len(candidates) == 0 {
			fmt.Fprintln(stderr, "auto-pick failed: no accounts produced usable usage data; try `aistat switch --to <email>`")
			return int(orchestrate.StatusUsageError)
		}

		// Rank candidates by bucketed comparator: bucket desc, LastSeenAt desc within bucket.
		best := candidates[0]
		bestAcct := findAccountByUUID(stored, best.UUID)
		for _, c := range candidates[1:] {
			cAcct := findAccountByUUID(stored, c.UUID)
			var cLastSeen, bestLastSeen time.Time
			if cAcct != nil {
				cLastSeen = cAcct.LastSeenAt
			}
			if bestAcct != nil {
				bestLastSeen = bestAcct.LastSeenAt
			}
			if switchBetter(c, cLastSeen, best, bestLastSeen) {
				best = c
				bestAcct = cAcct
			}
		}

		// Compare best candidate with active account ("already on best" check).
		// fetchLiveUsage is read-only: no store mutation.
		if activeAcct := findAccountByUUID(stored, activeUUID); activeAcct != nil {
			activeLimits, liveErr := h.fetchLiveUsage(ctx, h.storedAccess(*activeAcct), activeAcct.UUID, h.ua, debugW)
			if liveErr == nil {
				activeAR := providers.AccountResult{Limits: activeLimits}
				var bestLastSeen time.Time
				if bestAcct != nil {
					bestLastSeen = bestAcct.LastSeenAt
				}
				if !switchBetter(best, bestLastSeen, activeAR, activeAcct.LastSeenAt) {
					fmt.Fprintf(stdout, "already on best account (%s)\n", prevEmail)
					return 0
				}
			}
		}

		if bestAcct == nil {
			fmt.Fprintln(stderr, "auto-pick failed: no accounts produced usable usage data; try `aistat switch --to <email>`")
			return int(orchestrate.StatusUsageError)
		}
		target = *bestAcct
	}

	// Write target's blob to the live credential. This is the first mutation.
	writeCtx, writeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer writeCancel()
	if err := h.writeLiveBlob(writeCtx, []byte(target.RawBlob)); err != nil {
		fmt.Fprintf(stderr, "aistat: %s: write to live credential failed: %s\n", h.id, err)
		return int(orchestrate.StatusUsageError)
	}

	// Post-write reconcile so the store's LastSeenAt reflects the new active.
	_ = h.client.ReconcileAndPersist(ctx)

	fmt.Fprintf(stdout, "switched to %s (uuid %s); was %s\n", target.Email, target.UUID, prevEmail)
	if err := h.client.PostSwitchVerify(ctx, target); err != nil {
		if errors.Is(err, providers.ErrAuthDenied) {
			fmt.Fprintf(stderr, "aistat: %s: switched-to account's tokens are not usable: %s\n", h.id, err)
		}
		// Other errors (network/etc.) are silently ignored — the switch succeeded; verify is courtesy.
	}
	return 0
}

// runSwitchBulk fans out switch across all providers with ≥2 stored accounts.
func runSwitchBulk(ctx context.Context, handles []switchHandle, stdout, stderr, debugW io.Writer) int {
	var eligible []switchHandle
	for _, h := range handles {
		stored, err := h.store.List(ctx)
		if err != nil {
			fmt.Fprintf(stderr, "aistat: %s: could not list accounts: %s\n", h.id, err)
			continue
		}
		if len(stored) >= 2 {
			eligible = append(eligible, h)
		}
	}
	if len(eligible) == 0 {
		fmt.Fprintln(stderr, "no providers have multiple stored accounts; nothing to switch")
		return 0
	}
	exitCode := 0
	for _, h := range eligible {
		fmt.Fprintf(stdout, "[%s]\n", h.id)
		code := runSwitchSingle(ctx, h, "", stdout, stderr, debugW)
		if code != 0 {
			exitCode = code
		}
	}
	return exitCode
}

// runSwitchInferProvider handles `aistat switch --to <id>` with no provider.
// It searches all stores for <id> and dispatches to runSwitchSingle when
// exactly one provider matches. Ambiguous cross-provider matches exit 2.
func runSwitchInferProvider(ctx context.Context, handles []switchHandle, toArg string, stdout, stderr, debugW io.Writer) int {
	type match struct {
		h    switchHandle
		acct accounts.Account
	}
	var matches []match
	var listErrs []string
	for _, h := range handles {
		stored, err := h.store.List(ctx)
		if err != nil {
			listErrs = append(listErrs, fmt.Sprintf("aistat: %s: could not list accounts: %s", h.id, err))
			continue
		}
		for _, m := range matchAccounts(toArg, stored) {
			matches = append(matches, match{h, m})
		}
	}
	if len(matches) == 0 {
		for _, e := range listErrs {
			fmt.Fprintln(stderr, e)
		}
		fmt.Fprintf(stderr, "no stored account matches %q\n", toArg)
		return int(orchestrate.StatusUsageError)
	}
	if len(matches) == 1 {
		return runSwitchSingle(ctx, matches[0].h, toArg, stdout, stderr, debugW)
	}
	// More than one match — check if they're from different providers.
	providerSet := map[string]bool{}
	for _, m := range matches {
		providerSet[m.h.id] = true
	}
	if len(providerSet) > 1 {
		fmt.Fprintf(stderr, "multiple providers match %q; specify provider: aistat switch <provider> --to %s\n", toArg, toArg)
		return int(orchestrate.StatusUsageError)
	}
	// All matches in the same provider — single-provider disambiguation.
	fmt.Fprintf(stderr, "multiple stored accounts match %q, disambiguate by uuid\n", toArg)
	return int(orchestrate.StatusUsageError)
}
