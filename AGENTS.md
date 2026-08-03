# Symaira Browse Agent Rules

These repository rules are derived from [PLANUNG.md §0](PLANUNG.md#0-auftrag-an-den-ausführenden-agenten) and apply to every change.

## Project invariants

- Use Go 1.26.5, keep builds CGO-free with `CGO_ENABLED=0`, and preserve the Apache-2.0 license.
- Do not write to `os.Stdout` outside JSON-RPC frames. Route logs through `logkit` to stderr once the corekit wiring is introduced.
- Follow the standalone-first boundary: do not add compile-time imports of sibling Symaira repositories. Integrations must be runtime-detected and have a fallback.
- Every new output must provide a JSON mode with a stable field schema.
- Every new action receives a risk class. Enforcement starts in milestone M4; before then, prepare the risk class in code as a constant.
- `version --json` is the versionkit handshake payload (`{tool, version, schema_version}`) and deliberately bypasses the unified output envelope. Bump `SchemaVersion` in `internal/version` on every incompatible change to any machine-readable JSON output; never add, rename, or reorder fields of the payload itself.

## Architecture and configuration

- Treat [ARCHITEKTUR.md](ARCHITEKTUR.md) as the binding design and [PLANUNG.md](PLANUNG.md) as the implementation plan.
- Follow the repository's TOML and XDG conventions for configuration, cache, and state. Use the `SYMBROWSE_` environment-variable prefix.
- Keep the client, daemon, engine, session, output, policy, and MCP boundaries described by the architecture. Do not introduce a second architecture in an individual issue.
- Keep Chrome/CDP access behind the engine boundary and preserve the CGO-free build requirement.

## Development checks

Before committing, run the checks relevant to the change, at minimum:

```text
make fmt-check
make build
make test
make lint
```

Add focused tests for behavior changes. Do not commit generated binaries, local configuration, credentials, or local audit reports. Keep changes within the issue's allowed scope and document any residual risk.
