# Symaira Browse (`symbrowse`)

[![CI](https://github.com/danieljustus/symaira-browse/actions/workflows/ci.yml/badge.svg)](https://github.com/danieljustus/symaira-browse/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

![Symaira Browse social preview](docs/assets/social-preview.png)

> The browser an agent can operate while a person can take over at any time — without losing the session.

## Why symbrowse

- **Agent-operable Chrome sessions** — a real Chrome instance driven over the DevTools Protocol, with durable sessions instead of one-shot page fetches.
- **Stable element references** — deterministic refs across navigation and re-renders, so an agent's plan doesn't break when the DOM reflows.
- **Out-of-band handoff** — hand control to a human for 2FA, CAPTCHA, or approval mid-session, then resume agent control without losing state.
- **Standalone-first** — runs on its own with no compile-time dependency on other Symaira tools; integrations are optional, runtime-only fallbacks.

Typischer Ablauf — `open` → `read` → Out-of-Band-Handoff (Auszug aus einer Session):

```sh
$ ./symbrowse version
symbrowse 0.2.2

$ ./symbrowse --help
Usage:
  symbrowse [command]

Core Commands:
  open           Open a URL in the browser and wait for load
  read           Render the page as markdown (or JSON) in the symfetch output schema
  …

State Commands:
  handoff        Hand the session over to the human without losing it (2FA, CAPTCHA, approval)
  …

Debug Commands:
  version        Print the symbrowse version
  … (weitere Befehle gekürzt)

$ ./symbrowse open "https://example.com" --session research
opened https://example.com (session "research", tab 1)

$ ./symbrowse read --engine-hint --max-tokens 8000
---
title: Example Domain
url: https://example.com/
fetched_at: 2026-08-11T16:42:09Z
lang: en
tokens_est: 213
schema_type: document
js_required: false
---
# Example Domain
This domain is for use in illustrative examples in documents. …
# ⟦ HUMAN STEP ⟧ — the person takes over the live browser (2FA, CAPTCHA, approval); the agent session is preserved.
$ ./symbrowse handoff --reason "login 2FA" --timeout 5m
handoff complete: session "research" back under agent control
```

**Zwei Modi, ein Werkzeug:** `symbrowse read` liest einzelne Seiten über die
Static-Engine (`internal/fetch`) mit Browser-Impersonation und liefert ein
fetch-kompatibles Dokument — das ist der Code des früheren `symfetch`, das am
2026-08-23 hier aufgegangen ist (Repo archiviert, Formula deprecated).
Darüber hinaus ist `symbrowse` der **interaktive Agenten-Browser**: eine echte
Chrome-Session mit Navigation, Stable Refs, State (Cookies/Storage),
Out-of-Band-Handoff für Login/2FA/Freigaben, Journal, Flows und
Netzwerk-Kontrolle — für Abläufe, die mehrere Schritte über eine Session
hinweg brauchen. Beide Pfade teilen sich die `corekit/domkit`-Render-Pipeline
und erzeugen strukturell identisches Markdown.

Status: **v0.x (Feature-Waves A–D)**: Engine-Abstraktion (Chrome + Static),
Domain-Allowlist, SSRF-Guard, MCP-Server, Sessions/State/Journal/OOB,
Flows, Netzwerk-Routing, Console/Eval, Upload/Download, CI-Härtung.

## Quick start

Requirements:

- Go 1.26.5
- A POSIX shell and GNU Make
- Chrome for the default engine (optional: `--engine static` reads without Chrome)

Build and inspect the command:

```sh
make build
./symbrowse --help
./symbrowse version
```

Or install the latest release directly with Go:

```sh
go install github.com/danieljustus/symaira-browse/cmd/symbrowse@latest
```

The build is CGO-free by default. The `VERSION` variable can be overridden for a local build:

```sh
make build VERSION=0.2.2
```

## Development

```sh
make fmt-check  # verify Go formatting
make test       # run tests without CGO
make test-race  # run the race detector
make lint       # golangci-lint, or go vet when it is unavailable
make clean      # remove local build and coverage artifacts
```

The repository is standalone-first. Future integrations with other Symaira tools are runtime integrations with fallbacks, not compile-time sibling-repository dependencies.

## Architecture

`symbrowse` controls a real Chrome through the Chrome DevTools Protocol (or
the JS-free static engine), maintains durable sessions, exposes
deterministic element references, and provides an explicit out-of-band
handoff to a person for login, 2FA, CAPTCHA, and approval workflows.

- [ARCHITEKTUR.md](ARCHITEKTUR.md) — binding architecture and decision record
- [PLANUNG.md](PLANUNG.md) — milestones, issues, and dependency plan
- [AGENTS.md](AGENTS.md) — repository rules for contributors and agents
- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution workflow
- [SECURITY.md](SECURITY.md) — security reporting policy

### Docs

- [docs/tiers.md](docs/tiers.md) — escalation tiers and `read --engine-hint`
- [docs/mcp.md](docs/mcp.md) — MCP server setup and tool profiles
- [docs/errors.md](docs/errors.md) — stable error schema
- [docs/output-schema.md](docs/output-schema.md) — read/output schema
- [docs/benchmarks.md](docs/benchmarks.md) — benchmark results
- [docs/ssrf.md](docs/ssrf.md) — SSRF guard (private/loopback targets; MCP default deny, `--allow-private`)
- [docs/injection.md](docs/injection.md) — prompt-injection scan (`snapshot`, `--no-injection-scan`, pattern list)
- [docs/allowlist.md](docs/allowlist.md) — domain allowlist network policy (`--allowed-domains`)
- [docs/state.md](docs/state.md) — state encryption (symvault, OS keychain, env var; plaintext fallback)

## License

Apache-2.0. See [LICENSE](LICENSE).
