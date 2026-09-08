# RUST-009 browser-profile TLS/HTTP/2 feasibility

## Verdict

**Invalidated.** Exact-pinned `wreq = 0.16.1` with `wreq-util = 0.2.0` does not preserve the pinned Go oracle's six browser-profile wire contracts within this repository's safety, license and CGO-free constraints. Keep the Go implementation as the browser-profile transport escape hatch. Do not add `wreq` to the production workspace.

## Hermetic evidence

A localhost-only TLS/HTTP/2 capture harness compared Rust against the pinned Go `azuretls-client v1.13.2` oracle. All six profiles completed HTTP/2 requests, but parity failed:

| Signal | Matching profiles |
|---|---:|
| JA3 | 1/6 |
| JA4 | 3/6 |
| HTTP/2 settings | 5/6 |
| HTTP/2 header order | 0/6 |

The candidate also emitted browser headers absent from the Go oracle. These differences are observable network behavior and are not eligible for normalization. Full per-profile captures are retained in [`rust009-tls-results.json`](rust009-tls-results.json).

## Dependency gate

The candidate requires native BoringSSL-related compilation through `btls`/`btls-sys`, plus `bindgen`/`clang-sys`. Its dependency graph contains transitive unsafe code and fails the current license allowlist through Zlib, ISC and CDLA-Permissive-2.0 licensed components. Older alternatives were rejected because they are deprecated, yanked, or introduce GPL/LGPL licensing.

## Verification boundary

The harness, Go oracle tests, locked offline Rust check and six-profile run passed on macOS. Native Linux and Windows execution was not performed because the candidate already failed the wire and dependency stop gates. This is evidence to stop this implementation path, not FETCH-002 parity.
