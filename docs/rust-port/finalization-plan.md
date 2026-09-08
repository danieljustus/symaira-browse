# Go→Rust migration finalization plan

> **For Hermes:** Execute this plan as dependency-ordered vertical slices. Each
> implementation slice gets a focused review, a differential/native evidence
> review, and its own commit. Do not combine independent phases.

**Goal:** Ship `symbrowse` with a Rust primary implementation only after all 88
contract rows, the value gate, native browser evidence, signed dual artifacts,
and a reversible Go rollback path pass.

**Architecture:** The Rust CLI/MCP/daemon is the primary core. `static` is the
owned Rust HTTP transport; `browser` runs the explicitly selected local Chrome,
Safari or Firefox; `compat` is the temporary, versioned Go/AzureTLS sidecar for
legacy wire-profile callers. No requested mode or engine may silently fall back.

**Non-negotiable gates:** The Go oracle remains `652453d`/`v0.8.0`; Go remains
the release default until RUST-016 approves cutover. A browser adapter is only
advertised on a platform after its own native gate passes. The fetch value gate
is currently red: previous paired evidence measured Rust p95 at 30.326 ms versus
Go at 5.894 ms; the maximum permitted Rust p95 is 6.4834 ms.

---

## Operating rules for every phase

1. Work only from the parent migration worktree. Before each slice record
   `pwd -P`, `git rev-parse --show-toplevel`, branch, HEAD, and `git status`.
2. Start with a failing, hermetic Go-generated fixture or a target-contract
   fixture. Do not hand-author oracle outputs.
3. Run the focused Rust test, Go↔Rust differential/harness case, then affected
   format/Clippy tests before review. Treat a native gate as open until it ran on
   that native platform.
4. Keep secrets, real browser profiles, personal cookies and driver endpoints
   outside committed fixtures. Browser live gates use an isolated profile and a
   local fixture server.
5. Commit each completed vertical slice. Do not mark a matrix row `parity` from
   compilation, a fixture-shape assertion, cross-compilation, or a unit test
   that does not exercise the declared path.

## Phase 0 — keep the contract executable

**Scope:** RUST-001 governance; required before every later phase.

1. Run `python3 docs/rust-port/validate.py`, JSON parsing for matrix/DAG, and
   `git diff --check` after any contract or DAG edit.
2. Keep `docs/rust-port/contract-matrix.json` as the only source for the 88-row
   cutover total. Every new row must be assigned to exactly one work item.
3. Maintain `docs/rust-port/browser-transport-contract.md`,
   `architecture.md`, `implementation-plan.md`, `work-items.json`, and
   `README.md` together when browser/transport behavior changes.
4. Preserve `RUST-017` as dependent on RUST-007, RUST-009, RUST-011, RUST-012,
   and RUST-013. It is the barrier that prevents a Chrome-only or silent-fallback
   cutover.

**Exit evidence:** `ok: 88 contracts, 17 work items, acyclic DAG, links valid`.

## Phase 1 — close portable daemon and static-fetch correctness

**Scope:** RUST-006, RUST-007, then RUST-008.

### 1A. Daemon portability and lifecycle (RUST-006)

**Files:** `crates/symbrowse-daemon/src/{server,client,protocol,runtime}.rs`,
`crates/symbrowse-daemon/tests/{lifecycle,protocol_fixture}.rs`,
`port/harness/run.py`, `.github/workflows/ci.yml`.

1. Add native Windows named-pipe lifecycle coverage: concurrent starts,
   peer-equivalent access rules, operation/read timeout, cancellation, child
   reaping and stale endpoint recovery.
2. Add a daemon-path regression for each cleanup invariant: no live endpoint is
   removed, accepted sockets do not inherit a harmful nonblocking state, and
   timeout/cancellation returns the typed response exactly once.
3. Execute daemon fixtures via the installed Rust binary, not just internal
   tests, on macOS/Linux/Windows.

**Acceptance:** `make rust-daemon-contract`; native daemon suites on all declared
platforms; `DMN-001/002/003/004/005/007/008` are individually green.

### 1B. Static fetch semantics (RUST-007)

**Files:** `crates/symbrowse-fetch/src/{client,honest,robots,retry,rate_limit}.rs`,
`crates/symbrowse-fetch/tests/{http,control_contract,retry,robots}.rs`,
`port/fixtures/fetch/control.json`, `port/harness/run.py`.

1. Expand Go-derived control cases until every redirect, resolved address,
   proxy, cookie, decompression, retry/backoff and typed-error branch in
   `FETCH-001/003/004/005` is exercised.
2. Add the `static` selection metadata/error fixture required by `FETCH-009/010`;
   static must never claim a browser identity.
3. Wire the complete static fetch path through daemon and CLI, then prove the
   same case IDs run in the harness and the Rust test corpus.

**Acceptance:** `make rust-fetch-contract` plus `python3 port/harness/run.py
--suite fetch-control` and a programmatic set-equality check of declared versus
executed case IDs.

### 1C. Static document pipeline (RUST-008)

**Files:** `crates/symbrowse-fetch/src/{dom,render,semantic,relevance,cache,batch,archive,pipeline}.rs`,
`crates/symbrowse-fetch/tests/{render_corpus,pipeline_controls,archive_http}.rs`,
`port/fixtures/fetch/static.json`.

1. Regenerate the full static corpus through the pinned Go generator.
2. Close selector, cache, HTTP-backed batch, partial-failure, Wayback and
   404/410 recovery branches one fixture family at a time.
3. Preserve byte comparison wherever formatting is contractually fixed; declare
   semantic comparison explicitly where byte equality is not the contract.

**Acceptance:** `make rust-fetch-static-slice`, full `fetch-render` harness, and
`FETCH-006/007/008` green.

## Phase 2 — repair and enforce the value gate before browser expansion

**Scope:** RUST-016 performance barrier; this is a blocker, not documentation.

**Files:** `port/bench/run.py`, `port/bench/compare.py`,
`port/results/`, `docs/rust-port/baseline.json`,
`docs/rust-port/rust016-release-gates.md`, `.github/workflows/ci.yml`.

1. Make the fetch probe validate the exact response semantics (final URL,
   status, body/document metadata and errors), not merely `"success":true`.
   Add a deliberately wrong-but-fast candidate negative control.
2. Store raw paired samples, binary revision/digest, run count, cache policy,
   workload fixture identity and p95 calculation in a versioned report. Compute
   the ratio from raw samples; do not type percentages into prose.
3. Re-run Go and Rust from clean, verified source states with the same explicit
   toolchains and isolated XDG/HOME. Measure CLI, MCP, daemon and static fetch
   separately; fetch must be a hard independent <=10% p95 gate.
4. Profile the Rust fetch hot path, fix the measured root cause, and repeat the
   same paired measurement. Keep the Go path default until Rust fetch p95 is at
   most 110% of Go in the required distribution.
5. Add the benchmark gate to CI as an allowed-to-fail *red evidence job* while
   work is incomplete, then make it required before Rust-default cutover. Do
   not weaken the threshold or substitute synthetic success.

**Acceptance:** a fresh report with all four workloads semantically passing,
raw samples present, Rust fetch p95 <= Go p95 × 1.10, and either the 20% binary
or 20% median-RSS value gain. Until then RUST-016 remains blocked.

## Phase 3 — complete Chrome through the production boundary

**Scope:** RUST-012 / `ENG-005`, `ENG-006`, `ENG-007`.

**Files:** `crates/symbrowse-engine/src/{capabilities,lib}.rs`,
`crates/symbrowse-engine-chrome/src/{full,launch,connection,events}.rs`,
`crates/symbrowse-daemon/src/{runtime,spec}.rs`,
`crates/symbrowse-engine-chrome/tests/{full,contract_fixture}.rs`,
`port/harness/run.py`.

1. Replace Chrome-only capability categories with the canonical engine
   `interfaces`/`unsupported` partition. Every true capability requires an
   executable daemon command; otherwise return typed unsupported.
2. Wire tabs, frames, dialogs, network capture/policy/HAR, screenshots/PDF,
   uploads/downloads and accessibility/artifact operations through the daemon.
3. Implement owned-session close: idempotent close, child reaping, no closure of
   attached user Chrome, and removal of only the owned temporary profile.
4. Finish GUID/checksum-backed download events and explicit cookie/storage
   conversion/round-trip tests.
5. Add a native Chrome daemon-path suite that proves navigation/redirect,
   JavaScript, state, timeout, cancellation and cleanup. Run it on macOS,
   Linux and Windows where Chrome is declared supported.

**Acceptance:** full Chrome fixture suite and native daemon suite pass;
`ENG-005/006/007` green. A low-level CDP test alone is insufficient.

## Phase 4 — make Safari truthful and native-ready

**Scope:** RUST-013 / `ENG-008`; macOS only.

**Files:** `crates/symbrowse-engine-safari/src/{attach,bidi,lib}.rs`,
`crates/symbrowse-daemon/src/safari_runtime.rs`,
`crates/symbrowse-engine-safari/tests/{attach,bidi,contract_fixture}.rs`,
`port/harness/run.py`, macOS CI workflow section.

1. Add typed Safari prerequisite diagnostics: application availability,
   Automation permission, selected window/tab, SafariDriver/Remote Automation
   and loopback session readiness. Never configure macOS permissions silently.
2. Make attach navigation report the settled redirect URL rather than requiring
   equality to the original URL. Add native redirect/timeout tests.
3. Align all Safari capability claims with actual operations. Either implement
   tabs/frames/network/cookies/storage through the adapter or mark each one
   explicitly unsupported; remove daemon bypasses that claim support elsewhere.
4. Add a real Safari attach suite using an isolated/pinned test tab and a real
   SafariDriver/BiDi suite covering session creation, navigation, JS,
   cookies/storage where supported, timeout and cleanup.
5. Preserve macOS cfg gates and detach semantics: never terminate the user’s
   Safari during adapter cleanup.

**Acceptance:** macOS arm64 and amd64 native evidence for the declared Safari
surface. If Remote Automation is absent, the expected result is a typed
precondition error, not a skipped green test or fallback.

## Phase 5 — implement the multi-browser transport boundary

**Scope:** RUST-017 / `CFG-006`, `FETCH-009/010/011`, `ENG-009/010`.

### 5A. Explicit selection and no-fallback behavior

**Files:** `crates/symbrowse-core/src/{config,error,profiles}.rs`,
`crates/symbrowse-daemon/src/{spec,runtime,protocol}.rs`,
`crates/symbrowse-cli/src/main.rs`, `crates/symbrowse-mcp/src/{registry,proxy}.rs`,
`testdata/port/`, `port/harness/run.py`.

1. Model `mode=static|browser|compat` as a typed enum. Model
   `engine=chrome|safari|firefox` only for browser mode.
2. Add precedence fixtures for TOML/environment/CLI, omitted/conflicting/unknown
   values and every unavailable engine. Surface selected mode/engine in the
   existing machine-readable result schema only through a deliberate schema
   contract update; preserve `version --json` unchanged.
3. Make dispatch exhaustive on `(mode, engine)` and return stable typed errors
   for invalid combinations. Add negative cases proving that Firefox never
   becomes Chrome and Safari never becomes static/compat.

### 5B. Compatibility sidecar

**Files:** create `crates/symbrowse-compat/`; update `Cargo.toml`,
`crates/symbrowse-daemon`, `internal/fetch/fetch/azuretls.go` only where the Go
sidecar entrypoint needs an explicit boundary; add
`port/harness/cases/compat-*.json`.

1. Define the versioned local NDJSON protocol: handshake, request ID, request,
   response, typed error, timeout/cancellation, child exit/restart and endpoint
   permission schema.
2. Keep AzureTLS profile behavior only behind `compat`. Do not expose it as the
   identity/version of a real installed browser.
3. Prove private local endpoint permissions, bounded output, restart after a
   crashed sidecar, no fallback on sidecar/integrity errors, and Go rollback.

### 5C. Firefox adapter

**Files:** create `crates/symbrowse-engine-firefox/`; update `Cargo.toml`,
`crates/symbrowse-engine`, daemon/config/CLI/MCP selection code;
create Firefox fixture generator/corpus and native tests.

1. Choose an owned WebDriver/BiDi adapter only after a small discovery/attach
   spike proves maintained dependencies, loopback validation, timeouts and
   process-tree cleanup on macOS/Linux/Windows.
2. Implement canonical capabilities in vertical families: navigation/redirect,
   JavaScript/inspection, cookies/storage, interactions, tabs/frames,
   screenshots/downloads/response capture. Unsupported operations must be
   explicit.
3. Add native Firefox tests on all three OS families and negative unavailable
   tests. Do not treat the old Go TLS `firefox` preset as a Firefox automation
   oracle; use target-contract functional fixtures.

**Acceptance:** `make rust-browser-contract`, a new compat-sidecar suite, and
native Chrome/Safari/Firefox gates pass independently. Only then may
`CFG-006`, `FETCH-009/010/011`, `ENG-009/010` become green.

## Phase 6 — finish user-facing orchestration

**Scope:** RUST-014 / FLOW, SES and STATE contracts.

**Files:** `crates/symbrowse-core/src/{flows,runner,trace,journal,oob,settings,profiles,session}.rs`,
`crates/symbrowse-daemon`, `crates/symbrowse-cli`, `crates/symbrowse-mcp`,
`crates/symbrowse-core/tests/workflows_contract.rs`, and Go-generated workflow
fixtures.

1. Finish exact flow validation/error-line parity before execution.
2. Port record/replay, OOB hard stops, resume confirmation, cookie/storage,
   journal/watch, authentication and settings as separate fixture families.
3. Run browser-backed flows against each available engine; missing capabilities
   must produce typed, policy-preserving errors.
4. Retain the public Go `formflow` package until consumer search and release
   evidence permit a separately versioned migration.

**Acceptance:** all `FLOW-*`, `SES-*`, and `STATE-006` rows are executable and
green, with risk/confirmation behavior preserved.

## Phase 7 — hardening and native CI

**Scope:** RUST-015.

**Files:** `fuzz/`, `scripts/{rust_miri.sh,fuzz_smoke.sh,rust_geiger.sh}`,
`.github/workflows/{ci.yml,rust-hardening.yml}`, `deny.toml`, relevant parser
and lifecycle tests.

1. Extend fuzz/property cases for daemon/compat frames, browser selection,
   URL/fetch boundaries, flow parser, state headers and HTML/parser limits.
2. Run Miri only on appropriate pure crates and document every transitive unsafe
   inventory finding; owned crates remain `#![deny(unsafe_code)]`.
3. Add targeted mutation coverage for policy, selection/no-fallback, state and
   stable-ref logic.
4. Make Rust format/check/Clippy/nextest/doctest/audit/deny mandatory on PRs.
   Keep the existing weekly hardening workflow for costly fuzz/Miri/geiger work.
5. Add native macOS/Linux/Windows release-binary tests: CLI flags, MCP raw
   framing, daemon lifecycle, static fetch, browser prerequisites and Windows
   process-tree cleanup. Cross-builds produce artifacts only; they are not
   runtime evidence.

**Acceptance:** `make rust-hardening`; all required PR/native workflows green;
no unresolved advisory, license, unsafe-inventory or fuzz regression.

## Phase 8 — release rehearsal, dual rollout and cutover

**Scope:** RUST-016 / REL and PERF contracts.

**Files:** `port/release/{build_dual.py,verify.py,rollback-matrix.json,dual-release-manifest.json}`,
`port/bench/{run.py,compare.py}`, `.github/workflows/release.yml`, release docs.

1. Build Go and Rust artifacts for all six target tuples from the same verified
   source revision. Validate archive layout, exact names, checksums, SPDX SBOMs,
   Cosign signature/certificate and `version --json` from extracted binaries.
2. Change release publication to stage/draft verification before promotion; do
   not delete a last-known-good published release before replacement artifacts
   are verified. Make signing/notarization failure fail closed for release-claimed
   macOS artifacts.
3. Exercise the signed-artifact selector matrix: Go default, Rust opt-in, Rust
   unavailable fallback, integrity failure block, Go unavailable block,
   interrupted replacement and rollback. Test the real updater path, not only a
   mocked updater.
4. Publish a dual prerelease with Go default and Rust opt-in. Collect native
   smoke, browser capability and measured value evidence without promoting Rust
   by default.
5. Promote Rust to default only after all 88 rows and the value gate are green.
   Keep Go artifact/selector rollback for one stable Rust release. Removing Go
   afterward is a separate reviewed change.

**Acceptance:** `python3 port/release/verify.py --oracle-tag v0.8.0 --candidate
dist/`, signed native artifacts, the complete rollback matrix, all 88 contract
rows green, and fresh value-gate evidence.

## Final command matrix

Run this only after the individual phase gates are green; a stopped command
chain leaves later gates unexecuted.

```text
python3 docs/rust-port/validate.py
make fmt-check && make build && make test && make lint
make rust-gates
make rust-hardening
make rust-native-browser-contract
python3 port/harness/run.py --suite all --native-targets
python3 port/bench/compare.py docs/rust-port/baseline.json port/results/rust-release.json
python3 port/release/verify.py --oracle-tag v0.8.0 --candidate dist/
git diff --check
```

## Current blockers to resolve first

1. RUST fetch p95 is materially above the <=10% ceiling; performance is a hard
   blocker, not an optimization backlog.
2. Safari Remote Automation has not completed a live session; its missing host
   prerequisite must remain a typed blocked native gate.
3. Firefox adapter, fixture corpus, native evidence and compat sidecar do not
   yet exist.
4. Windows native daemon/process cleanup and all-platform release-binary
   evidence remain open.
5. Signed six-target dual artifacts and an end-to-end rollback rehearsal remain
   open.
