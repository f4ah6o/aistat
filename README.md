# aistat

Read-only fork for showing **Claude Code** and **Codex** usage limits from one terminal command.

This repository is a fork of [`drogers0/aistat`](https://github.com/drogers0/aistat). The upstream repository is kept as a reference only. Installation, issue tracking, and local changes for this fork should use `f4ah6o/aistat`.

## Scope

This fork intentionally narrows the original project.

| Area | Status |
|---|---|
| Claude usage | kept |
| Codex usage | kept |
| Copilot usage | omitted |
| `aistat switch` | removed from CLI dispatch |
| `aistat accounts` | removed from CLI dispatch |
| Persistent multi-account store | not opened by the CLI |
| Live credential rotation | not exposed |

The CLI still reads the credential locations that the upstream Claude Code and Codex CLIs already create. It does not provide a login flow.

## Usage

```console
$ aistat -h
Claude usage
- personal@example.com [Max 5x]
  - 5-hour: 92.0% (resets in 4h 53m)
  - 7-day: 71.0% (resets in 2d 5h)
  - 7-day sonnet: 58.0% (resets in 2d 5h)

Codex usage
- 5-hour: 2.0% (resets in 2h 26m)
- 7-day: 0.0% (resets in 6d 21h)
- Code review 7-day: 0.0% (resets in 6d 21h)
```

Commands:

```bash
aistat                  # same as `aistat usage`
aistat usage            # report Claude and Codex usage
aistat usage claude     # report Claude only
aistat usage codex      # report Codex only
aistat usage --human    # human-readable output
aistat usage --refresh  # bypass usage cache and force a fresh read
```

Unsupported by design:

```bash
aistat switch
aistat accounts
aistat usage copilot
```

## Authentication

| Provider | Existing setup command |
|---|---|
| Claude | `claude /login` |
| Codex | `codex login` |

## Installation

Build from a pinned local checkout:

```bash
git clone https://github.com/f4ah6o/aistat.git
cd aistat

git log -1 --oneline
go build -trimpath -o ~/.local/bin/aistat ./cmd/aistat
~/.local/bin/aistat --version
```

The module path is `github.com/f4ah6o/aistat/v2`.

## How it works

The CLI constructs only Claude and Codex providers. Each provider receives a per-process memory account store, so the CLI does not open the platform account store used by the original multi-account switching workflow.

Provider endpoints retained from upstream:

| Provider | Endpoint family |
|---|---|
| Claude | `api.anthropic.com/api/oauth/usage`, profile/refresh helpers used by the upstream provider implementation |
| Codex | `chatgpt.com/backend-api/wham/usage`, token refresh helper used by the upstream provider implementation |

The rendered JSON keeps the upstream shape for limits:

```json
{
  "checked_at": "2026-05-28T01:00:00+00:00",
  "providers": {
    "claude": { "limits": { "five_hour": {} } },
    "codex": { "limits": { "five_hour": {} } }
  }
}
```

## Upstream reference

Original project: [`drogers0/aistat`](https://github.com/drogers0/aistat)

This fork may continue to read upstream implementation details, but upstream installation scripts, release artifacts, and account-switching workflow are not part of this fork's supported surface.

## License

[MIT](LICENSE) © 2026 drogers0 and contributors
