# agent-usage

`agent-usage` reports Claude Code, Codex, and OpenCode Go usage limits from the terminal.

JSON is the default output. Pass `--human` for a compact text view.

## Install

From crates.io:

```bash
cargo install agent-usage
```

From GitHub release binaries with cargo-binstall:

```bash
cargo binstall agent-usage
```

## Usage

```bash
agent-usage                         # same as `agent-usage usage`
agent-usage usage                   # report all configured providers
agent-usage usage claude            # report Claude only
agent-usage usage codex             # report Codex only
agent-usage usage opencodego        # report OpenCode Go only
agent-usage usage --refresh         # bypass the 90 s usage cache
agent-usage --human usage           # human-readable output
agent-usage --debug usage codex     # request/debug lines on stderr
```

Example:

```text
Opencodego usage
- 5-hour: 0.0% (resets in 5h 0m)
- 7-day: 53.0% (resets in 1d 21h)
- Monthly: 100.0% (resets in 6d 4h)
```

## Authentication

| Provider | Setup |
|---|---|
| Claude | `claude /login` |
| Codex | `codex login` |
| OpenCode Go | `agent-usage opencodego setup` on macOS Chrome, or set `OPENCODE_GO_WORKSPACE_ID` and `OPENCODE_GO_AUTH_COOKIE` |

`agent-usage opencodego setup` stores the workspace ID and auth cookie in:

```text
~/Library/Application Support/opencode-bar/opencode-go.json
```

The file is written with owner-only permissions.

## Release

This project uses CalVer:

```text
YYYY.M.PATCH
```

The first Rust-only release is `2026.6.0`.

Release tags use the matching `vYYYY.M.PATCH` format. GitHub release binaries are produced with cargo-dist, and crates.io publishing is intended to use crates.io Trusted Publishing.

## Development

```bash
cargo fmt --check
cargo check --all-targets
cargo test
cargo clippy --all-targets -- -D warnings
cargo publish --dry-run
dist generate --check
dist manifest --artifacts=all --output-format=json --no-local-paths
```

Live provider checks require local credentials and may fail when upstream usage endpoints rate-limit.

## Acknowledgements

- This project originated from [drogers0/aistat](https://github.com/drogers0/aistat).
- The OpenCode Go usage handling references [opgginc/opencode-bar](https://github.com/opgginc/opencode-bar).

## License

[MIT](LICENSE)
