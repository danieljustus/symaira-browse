# Upstream component evaluation

Research captured 2026-09-06. Versions must be pinned and rechecked when their
implementation slice starts.

| Area | Candidate | Evidence | Decision |
|---|---|---|---|
| Chrome CDP | [`chromiumoxide`](https://github.com/mattsse/chromiumoxide) 0.9.1 | Tokio-only, generated CDP types, launch/attach and raw `Page::execute`; latest observed commit `afcc3a4` (2026-04-03) | **Spike first.** Best fit, but launch parsing, generated-code size and full event coverage need proof. |
| Chrome CDP | [`headless_chrome`](https://github.com/rust-headless-chrome/rust-headless-chrome) 1.0.22 | Active release in 2026, synchronous API; upstream documents missing frames, file chooser, network timing/conditions, WebSocket inspection and HTTP auth | **Reject as primary.** Missing features overlap symbrowse contracts. Keep only as a comparison during the spike. |
| Static browser impersonation | [`wreq`](https://github.com/0x676e67/wreq) 0.16.1 + `wreq-util` 0.2.0 | Hermetic six-profile comparison reached only JA3 1/6, JA4 3/6, HTTP/2 settings 5/6 and header order 0/6. Native BoringSSL/build tooling, transitive unsafe and three non-allowlisted licenses also violate the stop criteria. | **Rejected.** Do not add to production. Retain the pinned Go transport as the browser-profile escape hatch; see [`rust009-tls-feasibility.md`](rust009-tls-feasibility.md). |
| Honest HTTP | `reqwest`/Hyper | Mature Rust HTTP stack, but no browser fingerprint parity | **Use only for `honest`** unless fixtures prove otherwise. |
| MCP | official [`modelcontextprotocol/rust-sdk`](https://github.com/modelcontextprotocol/rust-sdk), crate `rmcp` 3.2.0 | Official SDK, Tokio stdio transport, current release observed 2026-08-31 | **Adopt behind adapter.** Pin exact release; raw-frame differential suite outranks SDK defaults. |
| CLI | `clap` | Standard maintained parser | **Adopt behind compatibility tests.** Cobra help/error/flag placement is the contract. |
| HTML parse/render | [`htmd`](https://crates.io/crates/htmd) 0.5.5, `html5ever` + `scraper` | `htmd` is an Apache-2.0, html5ever-based Turndown-style renderer with custom handlers; no candidate has yet proved parity with the current Go cleanup and Markdown bytes | **Try `htmd` first, select nothing yet.** Run the full fixture corpus before choosing or writing a narrow renderer. |
| Crypto | RustCrypto `aes-gcm` | Standard AES-256-GCM implementation | **Adopt** if Go-generated v1/v2/v3 fixtures round-trip byte-for-byte where deterministic and semantically where nonces are random. |
| Config | `toml` plus owned precedence resolver | Parser available; repository precedence is product-specific | **Adopt parser, own merge logic.** Avoid framework defaults. |
| Safari BiDi | direct Tokio WebSocket + Serde | The protocol is already JSON messages over a driver-managed socket; no evaluated crate covers the exact current surface | **Build narrow adapter**, macOS-only. Do not generalize into a new browser framework. |
| Keychain/symvault | existing subprocess contracts | Standalone-first requires optional runtime discovery and current exit-code semantics | **Preserve shell-out.** Do not add compile-time Symaira coupling or a platform keychain dependency. |

## Reuse decision

Do not fork an existing browser CLI or MCP server. Their command, daemon,
security, storage and release contracts differ too much; adopting one would be
a product rewrite disguised as reuse. Reuse focused protocol libraries behind
owned adapters.

The two mandatory feasibility spikes are:

1. **Chrome engine spike:** launch and attach, AX tree, nested frames, dialogs,
   file upload/download, network events, screenshot and cancellation.
2. **Static transport spike:** capture ClientHello/JA4 and HTTP/2 settings for
   every existing profile on all release platforms and compare with the Go
   oracle.

A private fork is acceptable only when the delta is small, upstreamable and
covered by the differential suite. A permanent broad fork fails the stop rule.
