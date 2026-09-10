# Go→Rust migration handoff

Status: **migration started; RUST-001/002/003/004/005 complete, RUST-006 is in progress pending native lifecycle evidence, RUST-010/011 slices are complete, and no cutover is approved**.

This directory freezes the starting point for a contract-first Rust port of
`symaira-browse`. The Go implementation remains the executable oracle until
all differential, security, native-platform and release gates pass.

## Pinned oracle

- Commit: `652453d1595fc302bd69c328e7da8a21dbee28b9`
- Stable release: `v0.8.0`
- Machine-readable schema: `8`
- Go toolchain: `go1.26.6`
- Oracle build: `GOTOOLCHAIN=go1.26.6 CGO_ENABLED=0 make build`

The oracle commit is one commit after `v0.8.0`; contract fixtures must name
which of those two references produced them. New Go behavior after this pin
requires an explicit matrix and fixture update before it enters the Rust port.

## Goal and value gate

The port is only worth completing if it preserves every observable contract
and, on paired release builds, achieves at least one of:

1. at least **20% smaller** uncompressed release binary, or
2. at least **20% lower** median peak RSS for the CLI/MCP/daemon benchmark set.

It must also keep startup and representative operation p95 latency within
**10% of Go**, introduce no dynamic runtime dependency, and pass all security,
release and native-platform gates. Rust by itself is not counted as a gain.
The current Go baseline is already fast and compact enough that failure of this
value gate is a valid reason to stop the rewrite.

## Scope

Included:

- the shipped `symbrowse` backend/binary, CLI, output and exit-code contracts;
- stdio MCP server and tool schemas;
- newline-delimited daemon protocol and lifecycle;
- static HTTP, real Chrome CDP, Safari attach/BiDi and Firefox WebDriver/BiDi
  engines, plus a temporary Go/AzureTLS compatibility transport;
- XDG/TOML configuration, policy, cache, journal, flows and encrypted state;
- macOS/Linux/Windows release archives, signing, SBOMs and Homebrew behavior.

Non-goals:

- redesigning commands, schemas, storage or risk policy during the port;
- importing sibling Symaira repositories at compile time;
- reducing the browser surface to Chrome, or representing a static/compat
  transport as a real Chrome, Safari or Firefox browser;
- deleting Go before one stable Rust release has operated without unexplained
  parity defects;
- creating a cross-repository Cargo workspace.

`formflow` is also a public in-process Go package. A Rust binary cannot replace
that API for Go consumers. Keep the Go package buildable until repository-wide
code search and consumer releases prove that no external consumer depends on
it, or introduce a separately versioned language-neutral boundary first. A
binary cutover therefore does not automatically authorize deleting all Go.

## Migration rule

Port vertical slices behind language-neutral fixtures. Each slice runs both
binaries with identical argv, stdin, cwd, environment, HOME/XDG roots, locale,
timezone, clock/seed controls and fixture servers. Compare exit code, stdout,
stderr, files, permissions, socket frames, network exchanges and process
lifecycle. Never normalize an unexplained mismatch.

## Browser transport rule

The transport contract is explicit: `static` is an honest Rust HTTP client;
`browser` uses the explicitly requested local `chrome`, `safari` or `firefox`
engine and its native network/browser state; `compat` retains the pinned
Go/AzureTLS path for legacy callers. A requested mode or engine either works or
returns a typed error. It never silently becomes another mode or browser.

## Stop rules

Stop and reassess when any of these holds:

- the legacy compat TLS/HTTP2 profile cannot be isolated behind its versioned
  local protocol, timeout, restart and rollback contract;
- a requested real Chrome, Safari or Firefox engine cannot meet its own native
  lifecycle/capability gate without a silent fallback or new privilege/install
  contract;
- Chrome CDP coverage cannot support frames, file transfer, network events,
  dialogs, stable refs and attach mode without maintaining a large private fork;
- state v1/v2/v3 bytes cannot round-trip across Go and Rust;
- MCP raw-frame output or CLI flag permutations cannot be preserved;
- the value gate misses after representative release-mode benchmarks;
- the dual-binary rollback path is no longer independently runnable.

## Files

- [`baseline.json`](baseline.json) — measured starting point and limitations.
- [`rust016-release-gates.md`](rust016-release-gates.md) — dual artifact,
  rollback, value-gate evidence and current blockers.
- [`../../port/release/dual-release-manifest.json`](../../port/release/dual-release-manifest.json)
  and [`../../port/release/rollback-matrix.json`](../../port/release/rollback-matrix.json)
  — machine-readable selection and rollback contracts.
- [`value-signal-version.json`](value-signal-version.json) — early paired
  version-slice measurement; explicitly not the cutover value gate.
- [`architecture.md`](architecture.md) — target boundaries and dependency choices.
- [`browser-transport-contract.md`](browser-transport-contract.md) — explicit
  static/browser/compat and Chrome/Safari/Firefox acceptance contract.
- [`upstream-evaluation.md`](upstream-evaluation.md) — reuse/fork/build decision.
- [`contract-matrix.json`](contract-matrix.json) — stable observable contract IDs.
- [`implementation-plan.md`](implementation-plan.md) — ordered vertical slices.
- [`finalization-plan.md`](finalization-plan.md) — dependency-ordered route from
  the current partial port through native evidence, dual release and cutover.
- [`work-items.json`](work-items.json) — machine-readable dependency DAG.
- [`validate.py`](validate.py) — validates IDs, references, DAG and local links.
- [`handoff-2026-09-09.md`](handoff-2026-09-09.md) — verified reconciliation
  checkpoint, applicable evidence, rollback path, ownership boundaries and open
  blockers for a later consolidation decision.

Run `python3 docs/rust-port/validate.py` before changing migration status.
