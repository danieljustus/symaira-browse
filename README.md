# Symaira Browse

[![CI](https://github.com/danieljustus/symaira-browse/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-browse/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/danieljustus/symaira-browse)](https://github.com/danieljustus/symaira-browse/releases/latest)
[![Coverage](https://img.shields.io/badge/coverage-gated-blue)](https://github.com/danieljustus/symaira-browse/actions/workflows/ci.yml)
[![License](https://img.shields.io/github/license/danieljustus/symaira-browse)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev/)

![Symaira Browse](docs/assets/social-preview.svg)

> The browser an agent can operate while a person can take over at any time — without losing the session.

**Status:** pre-1.0 — the command surface is stabilizing under [SemVer](https://semver.org/); see [CHANGELOG.md](CHANGELOG.md) for the release history.

## Why symbrowse

- **Agent-operable Chrome sessions** — a real Chrome instance driven over the DevTools Protocol, with durable sessions instead of one-shot page fetches. If JavaScript, redirects, cookies, or interaction are needed, this is the tool.
- **Plain-HTTP fetch built in** — the absorbed `symfetch` static engine (`fetch_url`, `fetch_batch`, `wayback_snapshots`) serves fast static reads and Wayback discovery without a browser session.
- **Stable element references** — deterministic `@ref`s across navigation and re-renders, so an agent's plan doesn't break when the DOM reflows.
- **Out-of-band handoff** — hand control to a human for 2FA, CAPTCHA, or approval mid-session, then resume agent control without losing state.
- **Standalone-first** — runs on its own with no compile-time dependency on other Symaira tools; integrations are optional, runtime-only fallbacks.

## Install

**Homebrew:**

```sh
brew tap danieljustus/tap
brew install symbrowse
```

**Go:**

```sh
go install github.com/danieljustus/symaira-browse/cmd/symbrowse@latest
```

**From source** (requires Go 1.26.5, POSIX shell, GNU Make; CGO-free):

```sh
make build
./symbrowse version
```

## Quick start

```sh
./symbrowse open "https://example.com" --session research
./symbrowse snapshot --session research
./symbrowse read --engine-hint --session research
```

`open` loads the page in a real Chrome, `snapshot` renders the interactive
tree with stable `@ref`s, and `read` returns the page as markdown in the
SymFetch output schema. Without Chrome, use the JS-free static engine:
`./symbrowse daemon --session <name> --engine static`.

## Usage

`symbrowse` has two modes in one binary:

- **Static fetch** — `fetch_url` / `fetch_batch` / `wayback_snapshots`
  (and `read` for single pages) use the absorbed `symfetch` pipeline: fast
  plain-HTTP reads with browser-impersonating TLS, no browser needed.
- **Interactive agent browser** — `open`, `snapshot`, `click`, `fill`,
  `type`, `press`, `wait`, `find`, `get`, `back`/`forward`/`reload` drive a
  real Chrome session with stable refs, cookies/storage state,
  out-of-band handoff, journal, flows, and network control.

```sh
$ ./symbrowse version
symbrowse v0.3.0

$ ./symbrowse --help
Usage:
  symbrowse [command]

Core Commands:
  open           Open a URL in the browser and wait for load
  read           Render the page as markdown (or JSON) in the symfetch output schema
  fetch_url      Fetch a URL with the plain-HTTP pipeline (no browser)
  fetch_batch    Fetch multiple URLs concurrently, results in input order
  wayback_snapshots  List Wayback Machine snapshots for a URL
  …

State Commands:
  handoff        Hand the session over to the human without losing it (2FA, CAPTCHA, approval)
  …

Debug Commands:
  version        Print the symbrowse version
  … (weitere Befehle gekürzt)
```

## MCP / Agent integration

`symbrowse mcp` runs a JSON-RPC-2.0 MCP server over stdio. Tools proxy to the
local daemon; every session is isolated; the domain allowlist and the SSRF
guard apply in MCP mode. The three SymFetch contracts are exposed as
first-class tools, so clients can switch from the retired `symfetch` runtime
without losing fast fetch, batch fetch, or Wayback discovery.

```sh
symbrowse mcp                 # default profile (core), SSRF guard on
symbrowse mcp --tools all     # full tool surface
symbrowse mcp --allow-private # allow private/loopback targets explicitly
```

See [docs/mcp.md](docs/mcp.md) for setup, tool profiles, the SymFetch
migration, and the Hermes config switch.

## Configuration

Configuration follows XDG conventions with a `SYMBROWSE_` environment prefix
and a `config.toml` (via `configkit`). Key settings: domain allowlist,
SSRF guard, cache TTL and directory, state retention, autosave policy, and
Chrome executable override. See [docs/state.md](docs/state.md) for state
encryption and [docs/allowlist.md](docs/allowlist.md) for the network policy.

## Documentation

- [docs/tiers.md](docs/tiers.md) — escalation tiers and `read --engine-hint`
- [docs/mcp.md](docs/mcp.md) — MCP server setup, tool profiles, SymFetch migration
- [docs/errors.md](docs/errors.md) — stable error schema
- [docs/output-schema.md](docs/output-schema.md) — read/output schema
- [docs/benchmarks.md](docs/benchmarks.md) — benchmark results
- [docs/ssrf.md](docs/ssrf.md) — SSRF guard (private/loopback targets; MCP default deny, `--allow-private`)
- [docs/injection.md](docs/injection.md) — prompt-injection scan
- [docs/allowlist.md](docs/allowlist.md) — domain allowlist network policy
- [docs/state.md](docs/state.md) — state encryption and retention

## Ecosystem

Symaira Browse is part of the [Symaira](https://symaira.com) product family:
it is the browser/agent-navigation core, complementing the Markdown-vault
workspace (`symdesk`), the credential vault (`symvault`), and the FRITZ!Box
controller (`symfritz`). It talks to the shared `corekit` render pipeline
(`domkit`) and follows the same CGO-free, zero-stdio-pollution conventions
as its siblings.

## Contributing · Security · License

- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution workflow
- [SECURITY.md](SECURITY.md) — security reporting policy
- [AGENTS.md](AGENTS.md) — repository rules for contributors and agents

Licensed under the Apache-2.0 license. See [LICENSE](LICENSE).
