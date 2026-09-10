# Rust target architecture

## Shape

Use one internal Cargo workspace in this repository. Keep a single shipped
binary named `symbrowse`; crate boundaries isolate contracts and platform
adapters rather than mirroring Go packages.

```text
symbrowse-cli
  ├─ symbrowse-mcp ───────┐
  ├─ symbrowse-daemon ────┼─ symbrowse-core ── symbrowse-protocol
  └─ engine selection ────┘        │
          ├─ symbrowse-engine-chrome
          ├─ symbrowse-engine-safari  (cfg macOS)
          ├─ symbrowse-engine-firefox
          ├─ symbrowse-fetch (static)
          └─ symbrowse-compat (temporary Go sidecar)
```

### Crates

| Crate | Owns | Must not own |
|---|---|---|
| `symbrowse-protocol` | Serde wire/storage types, stable enums, schema names, risk constants | I/O, subprocesses, runtimes |
| `symbrowse-core` | config precedence, output envelopes, cache, state codecs, policy, journal, flows, stable-ref logic | CDP/MCP framework types |
| `symbrowse-fetch` | honest static HTTP, robots, redirects, parse/render/relevance/injection pipeline | browser process lifecycle or browser impersonation |
| `symbrowse-engine` | protocol-neutral engine ports and capability model | concrete CDP or Safari types |
| `symbrowse-engine-chrome` | Chrome discovery/launch/attach, CDP commands/events | CLI rendering, MCP schemas |
| `symbrowse-engine-safari` | Apple Events attach and WebDriver BiDi adapter | non-macOS unconditional code |
| `symbrowse-engine-firefox` | Firefox discovery/launch/attach and WebDriver BiDi adapter | Chrome/Safari-specific protocol types |
| `symbrowse-compat` | temporary, versioned local IPC client for the pinned Go/AzureTLS browser-profile transport | static/browser transport substitution |
| `symbrowse-daemon` | local IPC, peer validation, sessions, deadlines, autostart/locks | CLI presentation |
| `symbrowse-mcp` | exact tool registry, stdio framing, daemon proxy, MCP budgets/errors | browser implementation |
| `symbrowse-cli` | Clap parser, command dispatch, text/JSON/YAML writers, exit mapping | business logic |

`#![deny(unsafe_code)]` applies to every owned crate. Unsafe in dependencies is
inventoried with `cargo geiger` and constrained through `cargo deny`; an owned
exception requires a documented invariant, focused tests and Miri where
applicable.

## Runtime model

- Use Tokio only at genuine async boundaries: CDP/WebSocket event loops, daemon
  clients, MCP stdio and concurrent fetches.
- Keep parsing, rendering, policy, config, state codecs and stable-ref logic
  synchronous and deterministic.
- Use bounded channels and explicit cancellation tokens. Do not translate Go
  goroutines/channels mechanically.
- One process may host CLI, daemon or MCP mode exactly as today. No resident
  service is required for static fetches or metadata-only commands.
- All logs use `tracing` with a forced stderr writer. MCP stdout is reserved
  exclusively for JSON-RPC frames.

## Dependency direction and adapters

External libraries terminate at adapter crates. Public core types never expose
`chromiumoxide`, `rmcp`, `wreq`, `reqwest`, `clap` or platform API types. This
keeps a replacement or fork local when parity tests expose framework drift.

Use explicit enums and owned domain values. Preserve existing snake_case and
field omission with Serde attributes; never derive a changed wire schema from
Rust naming. Time values are UTC RFC3339/RFC3339Nano according to the pinned
contract row, not a crate default.

## Initial dependency choices

- CLI: `clap`, but compare generated help, parser errors and every inherited
  flag placement against Cobra. A small compatibility parser is preferable to
  changing accepted argv.
- Serialization: `serde`, `serde_json`, `serde_yaml`; TOML parsing via `toml`
  plus an explicit precedence resolver.
- Errors: typed `thiserror` in libraries; `anyhow` only in the binary
  composition boundary.
- MCP: pinned official `rmcp`; retain a raw newline-framing adapter if SDK
  defaults cannot reproduce current frames and `_meta` tool errors.
- Crypto: RustCrypto `aes-gcm`, `rand_core`, `zeroize`/`secrecy`; key lookup
  remains runtime shell-out to `symvault` and macOS `security` for standalone
  parity.
- Local IPC: Tokio Unix sockets on Unix; preserve current Windows behavior
  explicitly instead of pretending Unix socket parity exists there.
- CDP: spike `chromiumoxide` first because it exposes generated commands and a
  raw execute path. Keep it behind `symbrowse-engine-chrome`.
- Transport selection is explicit and mutually exclusive: `static` uses the
  owned Rust HTTP stack; `browser` uses the selected real local Chrome, Safari
  or Firefox; `compat` uses the pinned Go/AzureTLS transport. No mode or engine
  may silently fall back to another one.
- Static HTTP: plain `reqwest`/Hyper is acceptable only for `static`; it must
  not claim browser identity or TLS/HTTP2 impersonation.
- Compat: preserve the legacy TLS/HTTP2 fingerprint only as a clearly named
  transitional mode behind versioned local IPC. It is not evidence of the
  installed browser's current network identity.
- HTML: evaluate `html5ever`/`scraper` plus a renderer through production
  fixtures; no crate is selected until Markdown bytes and DOM edge cases match.
- Safari: retain direct Apple Events subprocess behavior and implement BiDi over
  WebSocket/Serde unless a maintained crate passes the same fixtures.
- Firefox: use the local Firefox WebDriver/BiDi surface in its own adapter; do
  not route Firefox requests through Chrome or a generic HTTP client.

## High-risk seams

1. Legacy compat TLS/HTTP2/HTTP3 fingerprints and decompression behavior.
2. Native Chrome, Safari and Firefox discovery, automation preconditions,
   browser-state isolation and process cleanup.
3. Full CDP event coverage: frames, dialogs, file transfer, network recording,
   accessibility trees, stable refs and attach mode.
4. Cobra parser quirks and text/help compatibility.
5. MCP raw frames, old protocol negotiation and structured tool errors.
6. AES-GCM state v1/v2/v3 bytes, header AAD and atomic file replacement.
7. macOS peer UID checks, Keychain exit semantics and Safari automation.
8. Release signing/notarization, SBOM/signature asset names and rollback.

## Cutover

During migration, build `symbrowse-go` and `symbrowse-rs` for differential
execution. Release prereleases keep an explicit `SYMBROWSE_IMPL=go|rust`
escape hatch or a launcher with Go as the default until the matrix is green.
The final stable archive still exposes only `symbrowse`; keep `v0.8.0` and the
last known-good Go artifacts as rollback points. Remove Go in a separate change
after one stable Rust release, never in the cutover PR. The public in-process Go
package `formflow` is a separate compatibility surface: retain it until all
consumers migrate to a versioned language-neutral boundary or repository-wide
code search plus released consumer versions prove that it is unused.
