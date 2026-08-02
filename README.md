# Symaira Browse (`symbrowse`)

> The browser an agent can operate while a person can take over at any time — without losing the session.

Status: **foundation scaffold**. The repository currently provides the standalone Go/Cobra entrypoint and development tooling; browser and daemon capabilities are implemented in later milestones.

## Quick start

Requirements:

- Go 1.26.5
- A POSIX shell and GNU Make

Build and inspect the command:

```sh
make build
./symbrowse --help
./symbrowse version
```

The build is CGO-free by default. The `VERSION` variable can be overridden for a local build:

```sh
make build VERSION=0.1.0
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

`symbrowse` will control a real Chrome through the Chrome DevTools Protocol, maintain durable sessions, expose deterministic element references, and provide an explicit out-of-band handoff to a person for login, 2FA, CAPTCHA, and approval workflows.

- [ARCHITEKTUR.md](ARCHITEKTUR.md) — binding architecture and decision record
- [PLANUNG.md](PLANUNG.md) — milestones, issues, and dependency plan
- [AGENTS.md](AGENTS.md) — repository rules for contributors and agents
- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution workflow
- [SECURITY.md](SECURITY.md) — security reporting policy

## License

Apache-2.0. See [LICENSE](LICENSE).
