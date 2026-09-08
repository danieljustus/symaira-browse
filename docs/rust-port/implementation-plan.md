# Symaira Browse Go→Rust Migration Implementation Plan

> **For Hermes:** Use subagent-driven-development skill to implement this plan task-by-task.

**Goal:** Replace the Go backend with an idiomatic Rust implementation only after executable parity, native release verification, reversible cutover and a measured value gain.

**Architecture:** Keep Go as the pinned oracle and port externally testable vertical slices through a neutral process harness. Use an internal Cargo workspace with protocol/core types pointing inward and CLI, MCP, daemon, fetch and browser adapters pointing toward them; no sibling-repository imports.

**Tech Stack:** Rust 1.98 stable (repin when implementation starts), Cargo workspace, Serde, Clap, Tokio, official `rmcp`, Chromiumoxide feasibility spike, conditional `wreq` feasibility spike, RustCrypto, nextest, Miri, cargo-fuzz, cargo-audit and cargo-deny.

---

This document expands [`work-items.json`](work-items.json). Each work item is an
independently reviewable issue/PR. Within each item, keep the cycle small:
fixture/test fails → minimal implementation → differential case passes →
focused Rust gates → commit. Do not combine unrelated slices into a “big bang”
rewrite.

## RUST-001: Neutral Go-oracle harness and paired baseline

**Objective:** Make process behavior and performance reproducible before Rust code exists.

**Files:**

- Create: `scripts/rust-port/internal/diff/*.go`
- Create: `scripts/rust-port/cmd/{diffharness,portbench}/`
- Create: `testdata/port/bootstrap/cases.json`
- Create: `testdata/port/README.md`
- Update: `docs/rust-port/baseline.json`

**Steps:**

1. Build the oracle at the pinned commit as `symbrowse-go` with Go 1.26.6.
2. Define a case schema carrying argv, stdin, env allowlist, cwd, clock/seed,
   expected files and comparison mode.
3. Capture exit code, raw stdout/stderr, files/modes, process cleanup and socket/network fixture transcripts.
4. Run every bootstrap case Go↔Go; deliberately corrupt one expected result and verify the harness fails.
5. Add paired startup/RSS distributions for version/help/config and MCP
   process-mode fixture loads. Capture daemon steady-state with its IPC slice,
   where lifecycle and cleanup can be measured honestly.
6. Run `make port-contract`; proceed only after self-equality, negative controls
   and the tracked baseline are verified.

## RUST-002: Workspace and version/protocol slice

**Objective:** Establish a pinned, safe Rust workspace and the smallest exact external contract.

**Files:**

- Create: `rust-toolchain.toml`, `Cargo.toml`, `Cargo.lock`, `deny.toml`
- Create: `crates/symbrowse-protocol/{Cargo.toml,src/lib.rs}`
- Create: `crates/symbrowse-cli/{Cargo.toml,src/main.rs}`
- Test: `crates/symbrowse-cli/tests/version.rs`

**Steps:**

1. Pin stable Rust and install rustfmt/Clippy serially before parallel file writes.
2. Add `#![deny(unsafe_code)]`, explicit `rust-version` and license metadata.
3. Add a failing fixture for text and JSON version output including field order/newline.
4. Implement only `version`; inject version/schema at build time without changing the payload.
5. Run exact Go↔Rust version cases, format, Clippy, nextest and doctests.

## RUST-003: Configuration, output and error core

**Objective:** Freeze and implement the reusable non-browser command substrate.

**Progress:** Complete. The 25-code error taxonomy, representative
byte-exact JSON/YAML/text envelopes, Unicode budget primitives, deterministic
configuration defaults/precedence/validation, and ten `config show` CLI
differential cases are implemented, including malformed, mistyped and unknown
TOML input. Encryption-key redaction is asserted in Go and Rust;
`autosave_key` is documented as a visible named-state target, not key material.
The persistent output cache now matches Go IDs, metadata timestamps, TTL
expiry/listing, Unix modes and fail-closed truncation; a normalized Go cache
fixture is readable by Rust. Early `batch` composition now preserves ordering,
nested JSON, bail/continue semantics, stdin handling, all 83 risk classes and
byte-compatible text/JSON/YAML output across 24 differential cases. At this
stage it executes the migrated `version` and `config show` commands; browser and
daemon commands become executable as their later vertical slices land, so
`CLI-006` remains fixture-ready rather than claiming full-product parity.

**Files:**

- Create: `crates/symbrowse-core/src/{config,output,error,budget,cache,batch}.rs`
- Test: `crates/symbrowse-core/tests/{config,output,error,budget}.rs`
- Extend: `port/harness/cases/cli-*.json`, `config-*.json`

**Steps:**

1. Generate precedence fixtures from the Go loader for every documented field.
2. Add output-envelope, error-enum, YAML/text and UTF-8 truncation fixtures.
3. Implement explicit merges: defaults < global TOML < project TOML < env < flags.
4. Keep secret variables out of `config show` and diagnostics.
5. Add the minimum Clap compatibility layer required by inherited flag permutations.
6. Port `batch` as an early CLI composition case: preserve per-item order,
   nested JSON decoding, `--bail` and side-effect-free dry-run behavior.
7. Run `config-output-cli` differential suite before adding more commands.

## RUST-004: State, cache and policy/security core

**Objective:** Port persistence and security rules without data or policy drift.

**Progress:** Complete for this deterministic-core scope. Go production code now generates deterministic
plaintext and AES-256-GCM fixtures for schema versions 1, 2 and 3. Rust reads
all six files, reproduces their encrypted bytes through private fixed-nonce
test vectors, uses fresh OS-random nonces in the public encryption path,
requires the correct key, rejects oversized ciphertext before allocation, and
rejects v3 header/body tampering. V3 fixtures use the Go production encoder;
the fixture and dependency-security gates run natively in CI. The Rust store
now covers save/load/list/remove, metadata redaction, expiry/older-than cleanup,
0600/0700 permissions, fsynced atomic rename, Unix `O_NOFOLLOW`, v2 header-only
retention and v3 authenticated retention. Key material is zeroized on drop.
Rust also prevents the plaintext-to-encrypted re-save corruption found in the
Go oracle; the coordinated oracle fix is tracked in
[#399](https://github.com/danieljustus/symaira-browse/issues/399). The injected
key-resolver core now matches nine Go-generated precedence/error outcomes,
caches only successful resolutions, supports invalidation, and coalesces
concurrent probes without holding its mutex across provider calls. Runtime
SymVault/Keychain adapters now enforce bounded output and timeouts, terminate
subprocess trees, map provider exit semantics, and pass key material via stdin
instead of process arguments. Key initialization now preserves existing keys,
serializes cross-process creation, verifies provider readback and falls back to
a one-time environment instruction only when no secure provider is available.
Go-generated policy fixtures now cover allowlists, SSRF rebinding/private ranges,
all 83 command risk classifications, mode defaults, explicit rules and explain
output. Prompt-injection scanning, numeric/hex entity decoding, nonce-bound
content boundaries and timeout-bounded fail-closed Symbrain guard integration
are covered. Upload/download guards, full secret-redaction corpus and the
remaining state CLI lifecycle are assigned to their consuming slices below.

The executable DAG assigns browser-backed state lifecycle to RUST-013,
autostart installation and final redaction auditing to RUST-015, and filesystem
upload/download guards to RUST-010. MCP daemon autostart/handoff remains in
RUST-006. This keeps the daemon foundation independent from later browser and
packaging work.

**Files:**

- Create: `crates/symbrowse-core/src/{state,cache,policy,journal}.rs`
- Create: `port/fixtures/state/{v1,v2,v3}/`
- Test: `crates/symbrowse-core/tests/{state_vectors,atomic_write,policy}.rs`

**Steps:**

1. Generate plaintext/encrypted v1/v2/v3 fixtures through Go production code.
2. Add fixed-key/nonce AES-GCM vectors and random-nonce semantic round trips.
3. Implement key resolution via optional `symvault`, macOS `security`, env, then visible plaintext fallback.
4. Test tamper, truncation, permissions, symlink, read-only and interrupted-write cases.
5. Port SSRF, allowlist, upload/download containment, risk and redaction corpora.
6. Run Miri on pure core code and the full state/security differential suite.

## RUST-005: MCP stdio vertical slice

**Objective:** Serve the exact current MCP surface without stdout pollution.

**Progress:** Complete for the standalone MCP transport/proxy scope. An owned
Rust stdio adapter matches the pinned Go frames for initialize, newline and
Content-Length framing, profile tool lists, ping, malformed/unknown requests,
notifications, EOF, argument failures and typed tool-error transport. The
generated registry freezes 18 tools and exact aliases. A real Rust-MCP → pinned
Go-daemon `open` call was exercised successfully. Rust-native daemon autostart
remains assigned to RUST-006.

**Files:**

- Create: `crates/symbrowse-mcp/{Cargo.toml,src/lib.rs}`
- Create: `crates/symbrowse-mcp/src/{registry,transport,proxy,error}.rs`
- Test: `crates/symbrowse-mcp/tests/raw_frames.rs`

**Steps:**

1. Freeze initialize, tools/list, aliases, descriptions and schemas as raw Go frames.
2. Add malformed request, notification, EOF, cancellation and typed tool-error fixtures.
3. Integrate exact-pinned `rmcp` behind an owned adapter.
4. If SDK bytes differ, preserve semantics only where the contract permits; otherwise use the owned newline framing adapter.
5. Redirect all tracing/logging to stderr and test with logging enabled.
6. Pass official conformance plus the stricter repository raw-frame suite.

## RUST-006: Daemon IPC, lifecycle and MCP handoff

**Objective:** Reproduce protected local IPC, concurrency, process ownership,
lifecycle control and MCP daemon handoff.

**Progress:** In progress. The Rust daemon has the Go byte-level frame schema,
one-MiB limits, multi-frame connections, Unix same-user peer checks, private
socket permissions, stale-socket/start-lock handling, status/stop, idle and
operation budgets with cooperative cancellation. Rust MCP autostart/status/stop
was exercised end to end. Windows named-pipe parity remains before this phase
can close.

**Files:**

- Create: `crates/symbrowse-daemon/src/{server,client,protocol,session,start_lock}.rs`
- Test: `crates/symbrowse-daemon/tests/{frames,start_race,peer,lifecycle}.rs`

**Steps:**

1. Add 1 MiB boundary, malformed frame, timeout and multi-frame fixtures.
2. Implement Unix socket mode/peer checks and platform-specific behavior explicitly.
3. Port stale-socket recovery under an inter-process startup lock.
4. Stress 50 concurrent-start rounds and prove no live socket is unlinked.
5. Port status, idle expiry, stop, autostart and no-autostart behavior.
6. Verify cancellation leaves no child process, socket or partial frame behind.

## RUST-007: Honest HTTP and fetch-control slice

**Objective:** Port deterministic transport control before browser impersonation.

**Progress:** In progress. `symbrowse-fetch` provides an honest HTTP client with
method/header/body preservation, cookie sessions, redirect-hop policy checks,
resolved-address pinning, bounded compressed/decompressed bodies, robots rules,
retry/backoff and per-host circuit/rate control. Pinned-source control fixtures,
20 static vectors and hermetic HTTP tests pass. Review still found incomplete
redirect/proxy/error coverage and missing production-pipeline integration; the
existing fixtures are representative slices, not full FETCH-001/003/004/005
parity. Browser profiles remain on the explicit Go fallback.

**Files:**

- Create: `crates/symbrowse-fetch/src/{client,honest,robots,retry,rate_limit}.rs`
- Test: `crates/symbrowse-fetch/tests/{http,redirect,proxy,robots,retry}.rs`

**Steps:**

1. Use only hermetic HTTP/DNS/proxy fixtures.
2. Port honest profile methods, headers, bodies, decoding, limits and named cookies.
3. Enforce SSRF/allowlist on every redirect hop and resolved address.
4. Port deterministic clocks for retry/backoff/circuit tests.
5. Match error code and retryability for 4xx/408/429/5xx/network failures.

## RUST-008: Static DOM/rendering/relevance/cache

**Objective:** Reproduce the SymFetch-compatible document pipeline byte-for-byte.

**Progress:** Partial foundation integrated. Rust now has HTML5 parsing,
deterministic cleanup, semantic extraction, Markdown/JSON rendering, Unicode
budgeting, BM25 section ranking, bounded ordered batch helpers, and bounded
Wayback/recovery-candidate helpers. Twenty pinned-source Go-generated vectors and
focused control tests pass through `make rust-fetch-static-slice`. This is not
the complete corpus: full selector semantics, response-cache integration,
HTTP-backed batching, and end-to-end CDX/404/410/Wayback recovery remain. Therefore
FETCH-006/007/008 stay `todo` and RUST-008 stays blocked behind RUST-007.

**Files:**

- Create: `crates/symbrowse-fetch/src/{dom,render,semantic,relevance,archive}.rs`
- Create: `port/fixtures/fetch/`
- Test: `crates/symbrowse-fetch/tests/render_corpus.rs`

**Steps:**

1. Export every existing static testserver route and expected artifact via Go.
2. Evaluate parser/render candidates against the complete corpus; record failures.
3. Implement only missing cleanup/Markdown rules in owned code.
4. Port JSON-LD, language, BM25/top-k, selectors, frontmatter and link handling.
5. Port response/output caches, Wayback and 404/410 recovery.
6. Require byte equality where docs fix formatting and semantic equality only where declared.

## RUST-009: Legacy browser-profile transport decision

**Objective:** Decide whether Rust can preserve the pinned legacy on-wire
profile contract without pretending it is a current installed browser.

**Progress:** Stop rule triggered; this candidate path is invalidated. A
localhost-only six-profile comparison of exact-pinned `wreq 0.16.1` plus
`wreq-util 0.2.0` against the pinned Go transport matched JA3 for 1/6, JA4 for
3/6, HTTP/2 settings for 5/6 and header order for 0/6 profiles. The dependency
graph also requires native BoringSSL/build tooling, contains transitive unsafe
and fails the repository license allowlist. No production dependency was
added. `FETCH-002` stays available only through the explicitly named Go
compatibility transport. It is neither the static Rust transport nor evidence
of a current Chrome, Safari or Firefox identity. See
`rust009-tls-feasibility.md` and the retained per-profile evidence file.

**Files:**

- Create: `crates/symbrowse-fetch/src/impersonated.rs`
- Create: `port/fixtures/fingerprints/`
- Test: `crates/symbrowse-fetch/tests/fingerprints.rs`
- Update: `docs/rust-port/upstream-evaluation.md`

**Steps:**

1. Capture Go ClientHello/JA4, ALPN, HTTP/2 settings and header ordering for six profiles.
2. Spike exact-pinned `wreq` on native macOS, Linux and Windows.
3. Compare redirects, proxies, cookies, decompression and HTTP versions, not just JA3 strings.
4. Inventory BoringSSL/native build, licenses, advisories and transitive unsafe.
5. Apply the stop rule: accept, small upstreamable patch, retain a versioned Go
   compat sidecar, or stop the port.

## RUST-010: Protocol-neutral engine, stable refs and file guards

**Objective:** Port deterministic engine-domain behavior and filesystem guards
before CDP.

Progress: **complete**. The protocol-neutral `symbrowse-engine` crate covers the optional
capability partition, snapshot rendering/diffs, stable ref keys, preservation,
tombstones and invalidation over all 17 registered Go fixture routes. The fixture
generator refuses modified production oracle sources and produced the same digest
across five fresh Go processes. Route tree strings are explicitly omitted while
issue #401 tracks the Go oracle's chained replacement bug; refs and diffs remain
oracle-derived. Upload guards reject traversal, symlink and root escapes with a
Go-generated filesystem corpus. Download behavior intentionally preserves the
weaker Go oracle contract while stronger collision/integrity enforcement is
tracked in issue #402.

**Files:**

- Create: `crates/symbrowse-engine/src/{lib,capabilities,refs,snapshot,diff}.rs`
- Test: `crates/symbrowse-engine/tests/{capabilities,refs,snapshot}.rs`

**Steps:**

1. Freeze all 18 optional interface names and capability partition ordering.
2. Port stable-ref allocation, tombstones, subtree filters and snapshot diffs.
3. Run the 17-route deterministic benchmark and retain ≥80% refs and <200 median diff tokens.
4. Add property tests for ref normalization and snapshot serialization.
5. Run Miri before connecting a concrete browser.

## RUST-011: Chrome CDP feasibility and lifecycle

**Objective:** Prove launch/attach and required raw CDP coverage before the expensive engine port.

**Files:**

- Create: `crates/symbrowse-engine-chrome/{Cargo.toml,src/lib.rs}`
- Create: `crates/symbrowse-engine-chrome/src/{launch,connection,events}.rs`
- Test: `crates/symbrowse-engine-chrome/tests/spike.rs`

**Steps:**

1. Pin Chromiumoxide and implement discovery, headed/headless launch and endpoint attach.
2. Exercise raw navigation, runtime evaluate, AX tree, screenshot and cancellation.
3. Prove nested frames, dialogs, file chooser/download and network event access.
4. Compare browser argv, ownership, timeout and cleanup against Go.
5. Apply the private-fork stop rule before implementing full features.

**Progress:** Complete. Exact-pinned Chromiumoxide supports private headless
launch and existing-endpoint attach; real Chrome probes verified navigation,
evaluation, AX-tree, screenshot and load events. The early version-slice signal
passed in all measured dimensions, but is explicitly not the full-product
cutover gate. See `rust011-cdp-feasibility.md` and the two JSON evidence files.
Nested-frame, dialog, file and network feature families remain RUST-012 work.

## RUST-012: Chrome features

**Objective:** Complete the Chrome vertical slices through existing CLI/daemon contracts.

**Progress:** In progress. The Chromiumoxide adapter now exercises interactions,
inspection, nested frames, dialog handling, network capture/emulation, screenshots
and downloadable artifacts. Focused tests, Clippy and an opt-in native Chrome
launch smoke pass with owned profile/process cleanup. Go-oracle differential
cases and CLI/daemon wiring remain before ENG-005/006/007 can claim parity.

**Files:**

- Create: `crates/symbrowse-engine-chrome/src/{interaction,inspection,tabs,frames,dialogs,network,files,screenshot,a11y,settings}.rs`
- Test: matching focused modules plus `port/harness/cases/chrome-*.json`

**Steps:**

1. Port one externally testable feature family at a time.
2. For each family: Go fixture → failing Rust case → minimal adapter → differential pass.
3. Keep capabilities false until that family passes.
4. Run native real-Chrome smoke and flows after every family.
5. Do not paper over CDP version differences with fabricated partial results.

## RUST-013: Safari adapters

**Objective:** Preserve macOS-only attach and BiDi behavior without infecting portable crates.

**Progress:** In progress. A macOS-gated crate now covers the live-session
AppleScript attach boundary and an isolated safaridriver BiDi boundary. URL
allowlist/SSRF checks run before navigation side effects; AppleScript travels
through stdin; subprocess trees, input/output, BiDi commands and cleanup are
bounded; and BiDi endpoints must be loopback `ws`/`wss` URLs without userinfo.
Injected native tests cover lifecycle, policy, capabilities, protocol errors and
descendant cleanup. A real Safari attach/BiDi session, Go-generated frame
fixtures, CLI/daemon wiring and native amd64 macOS execution remain, so ENG-008
stays `todo` and this item is not complete.

**Files:**

- Create: `crates/symbrowse-engine-safari/src/{lib,attach,bidi}.rs`
- Test: `crates/symbrowse-engine-safari/tests/{attach,bidi}.rs`

**Steps:**

1. Freeze Apple Events subprocess argv/errors and safaridriver lifecycle frames.
2. Keep all implementation under macOS cfg gates.
3. Implement the narrow BiDi messages needed by existing capabilities.
4. Run native arm64 and amd64 macOS tests.
5. Verify no new entitlement, permission or installation requirement appears.

## RUST-017: Explicit multi-browser and compatibility boundary

**Objective:** Make transport choice truthful and preserve all three requested
browser families without reducing them to a Chrome or TLS-profile abstraction.

**Files:**

- Create: `crates/symbrowse-engine-firefox/{Cargo.toml,src/lib.rs}`
- Create: `crates/symbrowse-compat/{Cargo.toml,src/lib.rs}`
- Update: fetch/engine selection, daemon protocol, CLI/MCP schemas and
  `docs/rust-port/browser-transport-contract.md`
- Test: hermetic browser-transport and compat-sidecar fixture suites

**Steps:**

1. Add explicit `static`, `browser` and `compat` mode selection and stable typed
   unavailable/precondition errors. Expose selected mode and browser engine in
   machine-readable output without changing unrelated envelope fields.
2. Keep static HTTP honest: it may implement normal document/API semantics but
   must not claim browser impersonation.
3. Define the versioned, request-ID-bearing local compat protocol and verify
   endpoint permissions, timeout, child restart/exit and rollback behavior.
4. Gate Chrome, Safari and Firefox independently. Browser mode must use the
   named local engine's native surface and must never substitute another engine
   or compat transport.
5. Implement Firefox discovery/launch/attach and WebDriver/BiDi capabilities
   behind its own crate, with native macOS/Linux/Windows evidence.
6. Retain Safari under macOS cfg gates; report Remote Automation prerequisites
   directly rather than attempting hidden system configuration.

**Stop rule:** Do not advertise an engine on a platform until its own native
fixture suite passes. Do not remove Go compat while legacy wire parity is still
required by released callers.

## RUST-014: Flows, sessions and remaining operations

**Objective:** Port orchestration only after underlying adapters are trustworthy.

**Progress:** The protocol-neutral ownership lifecycle is fixture-ready. Rust
also has preliminary flow parsing/planning, draft/trace transforms, journal,
OOB prompt, profile and settings cores with a source-checked representative
fixture. Review found strict YAML/error-line differences, incomplete trace and
recording behavior, unhardened journal concurrency/symlink handling, and no OOB
process adapter. Browser-backed execution, storage, auth, state capture/restore
and settings mutation are still absent. Only the limited SES-001 lifecycle
claim is fixture-ready; the remaining FLOW/SES/STATE rows stay pending.
The Go oracle also accepts multiple action fields in one step despite its
"exactly one" validation text; issue #403 tracks the coordinated fix, so Rust
must preserve the pinned behavior until both implementations and fixtures move
together.

**Files:**

- Create: `crates/symbrowse-core/src/{flows,session,oob,auth,settings}.rs`
- Extend: CLI/daemon/MCP adapters and corpus fixtures

**Steps:**

1. Port flow parse/validate/dry-run before execution.
2. Port trace/record/replay and then browser-backed flow execution.
3. Port human handoff/resume hard stops with explicit confirmation.
4. Port cookies/storage/journal/watch/OOB/auth/settings and profiles.
5. Preserve the public Go `formflow` package until repository-wide consumer
   search and released consumer versions prove it unused, or design and ship a
   separately versioned language-neutral replacement before removing it.
6. Run hostile/captcha/confirmation formflow corpus without weakening risk policy.

## RUST-015: Hardening

**Objective:** Turn parity into a security and maintenance gate.

**Progress:** Infrastructure is prepared without claiming functional parity.
Five isolated fuzz targets, immutable seed hashes, pure property tests, a
scoped Miri script, feature/dependency/unsafe inventory gates and a scheduled
hardening workflow are present. The fuzz workspace has its own exact-pinned
lockfile and never enters production resolution. RUST-015 remains blocked
until the functional predecessors and native platform gates close.

**Files:**

- Create: `fuzz/fuzz_targets/*.rs`
- Create: `.github/workflows/rust-ci.yml`
- Update: `deny.toml`, Cargo feature policy and migration docs

**Steps:**

1. Fuzz daemon/MCP frames, flow parser, state headers, URLs and HTML boundaries.
2. Run Miri for pure crates and mutation testing for policy/state/ref logic.
3. Run fmt, check, Clippy, nextest, doctests, feature checks, coverage, audit and deny.
4. Inventory unsafe dependencies and document each accepted transitive source.
5. Add native macOS/Linux/Windows CI without weakening existing Go gates.

## RUST-016: Release, rollback and cutover decision

**Objective:** Prove shipping parity and decide from measurements, not enthusiasm.

**Progress:** Verification tooling is prepared, not approved for cutover. The
fail-closed verifier checks six-target dual Go/Rust archive layouts, exact
binary names, checksums, SPDX SBOMs, signatures and certificates. Explicit
selection and rollback matrices keep Go default and permit fallback only for
availability failures, never integrity failures. Self-tests pass; signed
six-target Rust artifacts, native benchmarks, notarization and the final value
gate remain outstanding.

**Files:**

- Create: `port/release/verify.py`
- Update: `.github/workflows/release.yml`, release configuration, Homebrew generation
- Update: `docs/rust-port/baseline.json`, `README.md`, rollback docs

**Steps:**

1. Produce dual Go/Rust prerelease assets for all six targets.
2. Verify archive contents/names, checksums, SPDX SBOMs, Cosign assets, macOS signing/notarization and Homebrew.
3. Port and verify runtime `upgrade check/apply`, including signature failure,
   wrong-asset rejection, atomic replacement and rollback.
4. Exercise Go-default, Rust opt-in, Rust-default and forced-Go rollback matrices.
5. Run paired release-mode performance distributions on native runners.
6. Cut over only if all 88 contracts pass, including static, compat and each
   declared Chrome/Safari/Firefox native gate, and the 20% size-or-RSS gate
   holds with ≤10% p95 regression.
7. Keep Go for one stable Rust release; remove it later in a separate reviewed change.

## Final verification

```text
python3 docs/rust-port/validate.py
cargo fmt --all --check
cargo check --workspace --all-targets --all-features
cargo clippy --workspace --all-targets --all-features -- -D warnings
cargo nextest run --workspace --all-features
cargo test --workspace --doc --all-features
cargo hack check --workspace --each-feature --no-dev-deps
cargo +nightly miri test --workspace
cargo audit
cargo deny check
python3 port/harness/run.py --suite all --native-targets
python3 port/release/verify.py --oracle-tag v0.8.0 --candidate dist/
python3 port/bench/compare.py docs/rust-port/baseline.json port/results/rust-release.json
```
