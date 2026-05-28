# aistat

A single static **Go binary** that reports your Claude, Codex, and Copilot usage from the terminal — and (Claude only) switches between multiple stored Claude accounts without a browser round-trip.

```
$ aistat -h
Claude usage
- personal@example.com (active) [Max 5x]
  - 5-hour: 2.0% (resets in 4h 53m)
  - 7-day: 21.0% (resets in 2d 5h)
  - 7-day sonnet: 0.0% (resets in 2d 5h)
- work@example.com [Max 20x]
  - 5-hour: 71.0% (resets in 5m)
  - 7-day: 44.0% (resets in 5d 9h)

Codex usage
- 5-hour: 2.0% (resets in 2h 26m)
- 7-day: 0.0% (resets in 6d 21h)
- Code review 7-day: 0.0% (resets in 6d 21h)  # appears only with recent code-review activity

Copilot usage
- month: 67.3% (resets in 5d 1h)
```

JSON is the default; `-h`/`--human` opts into the text rendering above.

## Install

Prebuilt binaries are available on the [Releases page](https://github.com/drogers0/aistat/releases).

For Go users, install from source:

```
go install github.com/drogers0/aistat/v2/cmd/aistat@latest
```

Requires Go 1.22+. Claude, Codex, and Copilot all work on macOS and Linux. The Claude provider reads from the macOS Keychain item populated by `claude /login`, or from `~/.claude/.credentials.json` on Linux. The Claude provider is macOS/Linux-only (it reads platform-specific credential stores); Codex and Copilot work everywhere a Go toolchain runs.

## Usage

`aistat` uses a subcommand surface as of v2.1.0:

```
aistat                            # default: same as `aistat usage`
aistat usage                      # report usage across all providers/accounts
aistat usage <provider>           # report a single provider (claude | codex | copilot)
aistat switch                     # auto-switch claude to the account with the most headroom
aistat switch --to <email|uuid>   # switch to a specific stored claude account
aistat accounts list              # list stored claude accounts
aistat accounts remove <id>       # remove a stored claude account (by email substring or uuid prefix)

aistat -h, --human                # render human-readable text (affects `usage` only)
aistat --debug                    # per-request + per-provider lines on stderr
aistat --version                  # print version and exit
aistat --help                     # print help and exit
```

The pre-v2.1.0 positional-provider form (`aistat claude`) is gone — use `aistat usage claude`.

## Multiple Claude accounts

`aistat` quietly captures every Claude account you sign into. The first time you run `aistat usage` after a `claude /login`, `aistat` reads the live keychain credential, looks the account up via Anthropic's `/api/oauth/profile` endpoint, and stores the credential blob keyed by `account.uuid`. On subsequent runs the access token is byte-matched to the stored slot, so there's no extra profile call in steady state.

Stored accounts are kept:

- **macOS:** one keychain item per account at service `aistat:accounts:claude:<uuid>`, plus a small index item. A darwin-only `flock` on `$HOME/Library/Caches/aistat/store.lock` serializes concurrent `aistat` invocations.
- **Linux:** one JSON file at `~/.config/aistat/accounts/claude.json` (mode `0600`), with `flock` across read/mutate/rename.

`aistat accounts list` shows every stored account with a `(stale)` suffix if its `last_seen_at` is more than 30 days old. `aistat accounts remove <id>` deletes one. The currently-active account is protected — switch to a different account via `aistat switch --to <email>` or run `claude /logout` first.

### `aistat switch`

`aistat switch` rewrites the live `Claude Code-credentials` keychain item to a different stored account, with no browser round-trip:

- **Auto-pick** (`aistat switch` with no arg): picks the stored account with the most `five_hour` headroom. Accounts whose stored access token has expired are excluded with a `(stored credential rejected; run \`aistat usage\` to refresh)` warn on stderr — `aistat usage` will refresh+re-store, and a subsequent `aistat switch` will pick it up.
- **Explicit** (`aistat switch --to <email|uuid>`): switches to a specific stored account. Argument matching: 8+ hex/dash characters is a UUID prefix; anything else is an email substring (case-insensitive).

The auto-pick comparator buckets candidates by `floor(remaining_percent / 5)` (so 87% and 89% live in the same bucket, 83% lives in a lower one), then breaks ties by most-recent `last_seen_at`. This means a freshly-reset account (100% remaining) outranks a 95%-remaining one even if you were about to hammer the fresh account — auto-pick optimizes *relative headroom*, not "has enough quota for the workload you're about to start." For nuanced cases use `--to` explicitly.

After the switch, `aistat` reconciles the multi-account store so the now-active slot's `last_seen_at` is current. If the keychain write fails, the multi-account store is untouched and `aistat` exits 2 with the failure reason.

### Security note

`aistat switch` writes to the Claude CLI's own keychain item only under an explicit user action; the regular `aistat usage` path is observation-only and never mutates the live keychain.

On macOS, after each `add-generic-password -U` write `aistat` follows up with `security set-generic-password-partition-list -S "apple-tool:,apple:"` so the Claude CLI's next read does not prompt. The first time `aistat` widens the partition list on a fresh keychain item macOS shows a one-time "Always Allow" prompt; subsequent runs do not. **If you observe a system prompt on the *Claude CLI's* next credential read after `aistat switch`, please file an issue** — that's the trigger for a v2.1.1 CGO fallback.

Multi-account storage aggregates several Claude credentials in one place. `aistat accounts list` flags any account with `last_seen_at > 30 days` as `(stale)` so you can prune it with `aistat accounts remove`.

## How it works

Each provider has one credential source and a small set of HTTPS endpoints:

| Provider | Credential | Endpoints |
|----------|------------|-----------|
| Claude   | macOS Keychain item `Claude Code-credentials` (populated by `claude /login`), or `~/.claude/.credentials.json` on Linux | `api.anthropic.com/api/oauth/usage` (usage), `api.anthropic.com/api/oauth/profile` (account identity for multi-account capture), `platform.claude.com/v1/oauth/token` (refresh for stored accounts) |
| Codex    | `~/.codex/auth.json` (populated by `codex login`) | `chatgpt.com/backend-api/wham/usage` |
| Copilot  | `gh auth token` (populated by `gh auth login`; needs the `user` scope) | `api.github.com/users/{login}/settings/billing/premium_request/usage` |

Providers are fetched in parallel. A failing provider does not block the others; each failed provider's error message is surfaced in the JSON (`providers.<id>.error`) and as `<Cap> usage: <error>` in text mode. See [Exit codes](#exit-codes) below.

For multi-account Claude, the per-account fetches run sequentially within the Claude provider with a dynamic timeout budget of `10s + 3s × N` where N is the number of stored accounts. A single transient failure on one of several accounts does not flip the overall exit code — it's surfaced as a per-account error in the JSON and the next run resolves it.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | All requested providers succeeded. |
| 1 | One or more requested providers failed at runtime (provider as a whole produced no account rows AND `Fetch` returned an error). |
| 2 | Usage / contract error: unknown subcommand, unknown provider, malformed flags, store-open failure on a write-bound subcommand. |
| 3 | Stdout write error (broken pipe, disk full). |

## Diagnostics on stderr

Even without `--debug`, providers may emit diagnostic lines to stderr. All start with `aistat:`.

```
aistat: copilot: Copilot-product usageItems present but none matched
  sku="Copilot Premium Request" — GitHub may have renamed the SKU; please
  file an issue at https://github.com/drogers0/aistat/issues

aistat: claude: could not capture live account profile (<reason>); rendering live row
  without storing — run `claude /login` if this persists across runs

aistat: claude: <email>: stored credential rejected (run `aistat usage` to refresh);
  excluded from auto-pick

aistat: claude: refresh endpoint rejected request (<status>: <body-snip>); this is
  likely an aistat refresh implementation issue, not your account. Run `claude /login`
  to work around it for this account and file an issue at https://github.com/drogers0/aistat/issues
```

The exit code and stdout payload are unaffected — these are heads-ups that the underlying number may be stale or that one account is excluded from auto-pick. With `--debug`, additional per-request and per-provider lines are also written to stderr.

## Authentication

If a provider's credential is missing, the error message names the exact command to fix it. For Copilot, the `user` scope is required:

```
gh auth refresh -h github.com -s user
```

For Claude, `claude /login`. For Codex, `codex login`.

If GitHub returns a Copilot plan slug `aistat` doesn't recognize, the provider fails closed with a message naming the slug and a link to file an issue.

## Output contract

```json
{
  "checked_at": "2026-05-28T01:00:00+00:00",
  "providers": {
    "claude": {
      "accounts": [
        { "email": "personal@example.com", "plan": "default_claude_max_5x",  "active": true,  "limits": {...} },
        { "email": "work@example.com",     "plan": "default_claude_max_20x", "active": false, "limits": {...} }
      ]
    },
    "codex":   { "limits": { ... } },
    "copilot": { "limits": { "month": { ... } } }
  }
}
```

For Claude, `accounts` is the **only** view — the per-account row whose `active: true` carries the live account's limits. There is no top-level `limits` mirror; scripts that care about "the active account's headroom" should filter `accounts` for `active: true` and read its `limits`. Codex and Copilot stay single-account: each emits a flat top-level `limits` and no `accounts`.

Each account row always emits a `limits` field — `{five_hour: …, …}` on a successful fetch with recognized windows, `{}` on a successful fetch that returned no recognized windows, and `null` (alongside `error`) when the fetch itself failed. Same convention as Codex/Copilot's top-level `limits`.

`email` is the JSON identifier; UUIDs live in `~/.config/aistat/accounts/claude.json` (Linux) / the macOS keychain index, and surface in `aistat accounts list`'s text output and `aistat switch`'s confirmation line — that's where you read them when you want to use `accounts remove <uuid-prefix>` or `switch --to <uuid-prefix>`.

Every `Limit` has the same four fields: `used_percent`, `remaining_percent`, `resets_at` (ISO 8601, always `+00:00` for UTC, never `Z`), `reset_after_seconds`. The top-level `providers` map is alphabetically sorted.

`AccountResult.active` is intentionally not `omitempty` — `"active": false` is meaningful and must serialize.

## License

MIT — see [LICENSE](LICENSE).
