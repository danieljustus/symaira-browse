# RUST-011 Chrome CDP feasibility and value gate

Captured 2026-09-06 on macOS 27.0 arm64 with Google Chrome 152.0.7977.82.
This report implements the RUST-011 feasibility/value spike only.

## Result

`chromiumoxide` 0.9.1 is feasible for a narrow adapter. The real probe launched
Chrome with a private profile, connected to the DevTools endpoint, navigated a
deterministic data URL, evaluated JavaScript, retrieved the full accessibility
tree, captured a PNG, and observed a `Page.loadEventFired` event. The same slice
attached to an independently launched Chrome endpoint and produced the same
observable DOM/evaluation/AX results. See `rust009-cdp-probe.json` for the raw
reports.

The adapter is deliberately not a full engine port. It does not claim parity for
frames, dialogs, file transfer, network interception, stable refs, or the full
session lifecycle. Those remain explicit follow-up gates, not inferred from this
probe.

## Pinned upstream decision

- Primary: `chromiumoxide = 0.9.1` with exact generated crates
  `chromiumoxide_cdp = 0.9.1`, `chromiumoxide_pdl = 0.9.1`, and
  `chromiumoxide_types = 0.9.1`.
- Observed upstream `main`: `afcc3a4313f2087249b4490d94e54bf8e3bfaccf`.
- Exact supporting pins in `Cargo.lock`: `async-tungstenite 0.32.1`,
  `tokio 1.53.1`, and `futures 0.3.34`.
- `headless_chrome 1.0.22` remains rejected as the primary candidate because
  its documented missing surface overlaps this product's frame, network,
  file-transfer, WebSocket-inspection, and HTTP-auth contracts.

The generated CDP surface is approximately 60,000 lines upstream. This is an
important size and compile-time cost, but it did not require a fork for the
launch/attach slice.

## Measured value signal

The existing neutral `portbench` was run with the identical deterministic
`version --json` argv, 30 process runs per side, fresh HOME/XDG/temp roots, and
the pinned Go oracle binary from commit
`652453d1595fc302bd69c328e7da8a21dbee28b9`:

```text
GOTOOLCHAIN=go1.26.6 CGO_ENABLED=0 go run ./scripts/rust-port/cmd/portbench \
  --binary ./target/port/symbrowse-go \
  --candidate ./target/release/symbrowse-cdp-spike \
  --workload version-json --runs 30 \
  --output docs/rust-port/rust009-value-signal.json
```

| Metric | Go oracle | Rust CDP spike | Change |
|---|---:|---:|---:|
| stripped binary | 18,264,882 B | 4,375,104 B | **-76.05%** |
| p95 process latency | 8.734 ms | 4.097 ms | **-53.09%** |
| median peak RSS | 25,264,128 B | 2,228,224 B | **-91.18%** |

This exceeds the delegated `>10%` early-signal threshold. It is **not** the
full-product cutover value gate: the Rust candidate is a spike binary whose
only production-like command is the version handshake. The integrated Rust CLI,
MCP and daemon now build, but this measurement still does not represent their
combined release binary or Chrome steady-state behavior.

## Stop/continue decision

**No cutover approval.** Continue only with a bounded Chrome adapter and keep
the Go implementation as the oracle/rollback path. Stop before RUST-012/full
Chrome feature work if the remaining frame, dialog, file, network, and
attach-lifecycle cases require a broad permanent fork, or if the integrated
release candidate fails the repository's representative value gate. The early
spike win is evidence to investigate further, not permission to claim a Rust
rewrite win for the product.

## Verification

- `cargo test -p symbrowse-engine-chrome --all-targets --locked`: pass.
- `SYMBROWSE_E2E=1 ... cargo test ... real_launch_probe_is_opt_in`: pass against
  real Chrome.
- `cargo clippy -p symbrowse-engine-chrome --all-targets --locked -- -D warnings`:
  pass.
- `cargo test -p symbrowse-engine-chrome --doc --locked`: pass.
- `cargo check -p symbrowse-engine-chrome --all-targets --target
  x86_64-pc-windows-msvc --locked`: pass.
- The integrated workspace and full Rust CLI build after the daemon slice;
  the spike package is formatted and passes its package-level checks.
- Native Windows runtime and release measurements were not run because this
  host has no Windows runner.
- `python3 docs/rust-port/validate.py`: pass (82 contracts, 16 work items).
