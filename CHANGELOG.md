# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `safari-bidi` engine: an isolated Safari driven over W3C WebDriver BiDi via
  `safaridriver --bidi`. It implements inspection, navigation state, frame and
  tab management, and navigation-target policy enforcement; every other
  optional interface is reported as unsupported (#355).
- `doctor` detects the two causes behind Safari's single, undiagnosable
  session-creation timeout — an already-running Safari holding the automation
  slot, and missing remote-automation permission — and prints the remediation
  for each instead of relaying Apple's message (#355).
- State-encryption keys are provisioned through SymVault, the macOS Keychain,
  or an explicit environment fallback instead of relying on manual setup
  (#346).

### Changed

- `docs/engines.md` records the measured WebDriver BiDi capability matrix for
  Safari 27.0. The measurement disproves five of the six capability gains
  issue #355 anticipated: Safari implements no `input` and no `network` module,
  and `session.subscribe` reports success while delivering no events. As a
  result `safari-bidi` deliberately does not implement `InteractionEngine` —
  a JavaScript `click()` would bypass hit-testing and report success for an
  action a human cannot perform — and its allowlist/SSRF enforcement reaches
  navigation targets only, which it states through
  `NetworkPolicyReporter.Limitations()` (#355).
- Upgrade detection now classifies Homebrew installations by installation
  method, and the affected CLI commands use the unified structured output
  envelope (#347, #348).
- Configuration paths follow XDG conventions, `config show` exposes stable
  runtime settings, and daemon log/state paths share one resolver (#349, #351).
- Unclassified journal commands use the shared `unknown` policy fallback;
  renderer payloads are decoded once and custom injection-pattern caches stay
  bounded to one metadata-aware entry per path (#352, #353, #354).

### Fixed

- Windows daemon configuration tests no longer depend on platform-specific
  path escaping (#361).

## [v0.6.1] - 2026-09-01

### Fixed

- Detached daemon startup is portable across supported platforms (#324).
- Recovery similarity allocations are bounded to prevent excessive memory use (#338).

### Security

- Go vulnerability gates run in CI and release workflows (#318).
- SSRF address checks are hardened and consolidated (#320, #335).
- Daemon URL and session-engine boundaries are enforced (#321).

### Changed

- CLI session streams and policy output are handled consistently (#319).

### Build

- GitHub Actions, Go-version and Syft dependencies were updated (#322, #339, #340).

### Tests

- Windows CI tests are portable across checkout and line-ending behavior (#326).

## [v0.6.0] - 2026-08-27

### Added

- `safari-attach` drives a live, logged-in Safari session through Apple Events (#297, #307).

### Fixed

- Risk decisions can be delegated to the symbrain guard without changing the local fallback (#303).
- macOS release notarization uses App Store Connect API-key authentication (#306).

### Docs

- The README version sample was updated for the v0.6.0 release (#308).

## [v0.5.0] - 2026-08-27

### Added

- The engine capability boundary and browser attach mode are documented and exposed (#301).

### Build

- The exact symaira-corekit dependency pin was updated to v0.14.0 (#285).

### Docs

- The README includes an example CLI session (#294).
- Engine-boundary and measured Safari capability documentation was added (#298).
- References to the internal architecture and planning documents were corrected (#300).
- The README example version was updated to v0.5.0 (#302).

## [v0.4.0] - 2026-08-26

### Added

- The consumable `formflow` web-form automation contract and hardened flows were added (#282).
- Confirmation-link flows, evidence capture and per-host pacing support multi-broker campaigns (#280, #281).
- The hostile-form test corpus covers misleading labels, honeypots, CAPTCHA gates, bot walls and confirmation pages (#281).

### Build

- The exact symaira-corekit dependency pin was updated to v0.13.0 (#279).

### Tests

- Formflow driver-adapter and failure branches are covered (#284).

## [v0.3.1] - 2026-08-25

### Fixed

- CLI argument and flag validation failures now use the stable `invalid_args` error kind, and standard structured daemon responses preserve warnings (#266, #267)
- Browser doctor skips the implicit CDP probe for daemon sessions with ephemeral ports (#262)
- State encryption authenticates metadata headers, rejects plaintext downgrade attempts, and validates keychain key material while preserving legacy reads (#263, #264)
- Windows CI no longer fails the README command-surface regression test on package working-directory or CRLF differences (#276, #277)

### Security

- State metadata is authenticated with AES-256-GCM and prompt-injection scan input is bounded with explicit truncation warnings (#264, #269)

### Performance

- Snapshot prompt-injection scans are memoized for unchanged page documents (#269)

### Changed

- 404 ancestor and sitemap recovery is isolated from the fetch pipeline orchestration (#268)

### Docs

- README now distinguishes MCP-only SymFetch tool names from the actual CLI surface and captures real help output (#265)

## [v0.3.0] - 2026-08-25

### Added

- MCP: expose the SymFetch compatibility contracts `fetch_url`, `fetch_batch` and `wayback_snapshots` on the symbrowse MCP server, so clients can switch from the retired `symfetch` runtime without losing fast fetch, batch fetch, or Wayback discovery (#258, #259)
- CLI: global `--output` flag alongside `--json` for the unified output envelope (#255)

### Docs

- README brought onto the shared Symaira structure (brand H1, badge row, status line, Install, ecosystem links) (#256, #260)

## [v0.2.3] - 2026-08-25

### Added

- README: "Why symbrowse" positioning section and Apache-2.0 license detection for the About sidebar (#241, #242)

### Fixed

- `open`/`read` failed with "browser executable is not configured" even when `doctor` found Chrome; the navigation runtime now falls back to the same platform discovery `doctor` uses, keeping `SYMBROWSE_EXECUTABLE_PATH` as an explicit override (#244, #251)
- `session list`/`session info` silently autostarted a new daemon and Chrome instance; both inspection commands now use the no-autostart transport like `daemon status`/`daemon stop` (#246, #250)
- Flaky, order-dependent tests in `internal/fetch/pipeline` (process-wide `HOME` is now set once per package run instead of per test) (#243, #249)
- README documented `--engine static` as an `open`/`read` flag; the flag only exists on `symbrowse daemon` — the Quick Start now shows the actual two-step static-engine workflow (#245, #251)

### Build

- Makefile default `VERSION` is derived from the latest git tag instead of the stale hard-coded 0.1.1, so `make build && ./symbrowse version` matches the released version (#247, #248)

## [v0.2.2] - 2026-08-24

### Fixed

- symguard delegation now speaks the guard's stdin JSON contract with risk-level
  mapping (`read`→low … `eval`/`credential`→critical); daemon commands are no
  longer denied with "decide: empty request" when symguard is installed (#234)
- `tab`/`frame`/`dialog`/`a11y` commands resolve the daemon socket path first and
  reach a running daemon instead of failing with "socket path is required" (#235)
- Release workflow: removed the raw-binary DMG step (CLI ships via tar.gz +
  Homebrew) (#232)
- Release workflow: a pre-existing release for the tag is reset before
  GoReleaser republishes, so re-runs no longer fail with 422 already_exists (#233)

## [v0.2.1] - 2026-08-24

### Fixed

- Skip all POSIX permission-model tests on Windows — `os.Chmod(0500/0000)` does
  not enforce read-only semantics on Windows ACLs. Skips added to 7 tests across
  `internal/fetch/cache` and `internal/fetch/pipeline` (#216)

### Added

- `.github/CODEOWNERS` for reviewer auto-routing (#223)
- macOS universal `.dmg` build (Intel + Apple Silicon) in release workflow (#225)

## [v0.2.0] - 2026-08-24

### Breaking

- **`--json` output is now the unified envelope** (`{success, data, warnings}`)
  instead of raw JSON payloads. All commands that previously emitted raw JSON
  via a local `--json` flag now route through the root persistent `--json`
  flag and the shared output envelope. `version --json` is unchanged
  (versionkit handshake). SchemaVersion bumped from 1 to 2 in
  `internal/version`.

### Changed

- Memoize state encryption key resolution and add unencrypted metadata header to state files (schema version 2) (#193)
- Bump symaira-corekit from v0.9.1 to v0.11.0 (#214)

## [v0.1.1] - 2026-08-12

### Security

- Guard browser inputs and encrypted allocations (#166)
- Use safe browser string quoting in evaluated predicates (#168)

### Fixes

- Keep wait-condition literals valid JavaScript for all runes — astral-plane
  characters no longer break predicates — and bound `WaitSelector` values
  (#169)

### Infrastructure

- Bump `github.com/danieljustus/symaira-corekit` (#164)
- Bump the actions-dependencies group (GitHub Actions) (#165)
- Allow manual CodeQL analysis runs (#167)
- Add issue template config disabling blank issues (#176)

### Docs

- Add terminal output sample to README (#177)
- Add tests for CLI output helpers (#178)
- Fix Windows-incompatible cache default path test (#179)

## [v0.1.0] - 2026-08-07

Initial release of `symbrowse`, the agent-operable browser automation CLI.

### Features

- Chrome DevTools Protocol engine with browser discovery doctor, navigation,
  waiting, tabs/windows/frames/dialog handling and CDP network layer with
  domain allowlist (#69, #75, #76, #79, #109, #90)
- Static engine as second engine implementation with `--engine-hint`
  escalation contract for `symfetch` (#113, #108)
- Daemon IPC and lifecycle, session registry and isolation, browser session
  ownership contract (#77, #78, #159)
- Accessibility snapshot renderer, stable snapshot refs with tombstones,
  ref interaction commands and snapshot diff (#80, #81, #83, #84, #111)
- `get`/`state` inspection, semantic finder, obstructed-click diagnosis,
  console/errors/eval commands with runtime events (#82, #86, #87, #115)
- Unified output and error schema, unforgeable content boundaries, batch
  command with `--bail`/`--dry-run` (#88, #93, #92)
- MCP stdio server, tool profiles (`--tools`, `--list-profiles`), canonical
  tool IDs with compatibility aliases, risk classes and model-facing
  selection guidance (#103, #104, #156, #157)
- Prompt-injection heuristic scan with `warnings[]` (#107)
- Screenshot command (viewport, full page, element, png/jpeg) with path
  guard (#154)
- Token budget with truncate-and-store (`--max-tokens`, cache get/list/clear)
  (#155)
- Flow schema/parser, flow runner (inputs, assertions, dry-run, diagnostics),
  flow record, failure diagnosis with snapshot diff and repair hints,
  symskills-based flow discovery (#95, #96, #98, #99, #100)
- Network routing, request inspection and HAR export (#116)
- Path-guarded upload/download events with checksums (#117)
- axe-core accessibility audit (a11y) (#110)
- SSRF guard with fetch-compatible defaults (#102)
- Self-update via corekit/updatecheck (#112)
- Risk decisions delegated to symguard with local fallback (#114)

### Fixes

- Windows compatibility: daemon IPC, test fixtures, precedence and doc-snippet
  tests, socket mode-bit assertion (#141, #137, #147, #148)
- Real-Chrome E2E coverage restored, hardened tab-switch timing, headless
  default (#138, #143)

### Security

- Keyless Cosign signing, Syft SBOMs and checksums for all release archives
  (#94)
- CodeQL analysis on pull requests (#145)

### Infrastructure

- Release pipeline with GoReleaser, Homebrew tap publishing (#94)
- CI with coverage gate, fuzz targets and E2E smoke (#119)
- Deterministic browser fixture server for tests (#72)
- Coverage: close polish gaps, lift margins, Chrome CDP method paths (#144,
  #149, #153, #158)

### Docs

- MCP documentation and example configurations, docs dedupe, CI/license
  badges, go install quickstart, issue forms and PR template, Apache-2.0
  LICENSE (#106, #124, #140, #139, #120)

[Unreleased]: https://github.com/danieljustus/symaira-browse/compare/v0.6.1...HEAD
[v0.6.1]: https://github.com/danieljustus/symaira-browse/compare/v0.6.0...v0.6.1
[v0.6.0]: https://github.com/danieljustus/symaira-browse/compare/v0.5.0...v0.6.0
[v0.5.0]: https://github.com/danieljustus/symaira-browse/compare/v0.4.0...v0.5.0
[v0.4.0]: https://github.com/danieljustus/symaira-browse/compare/v0.3.1...v0.4.0
[v0.3.1]: https://github.com/danieljustus/symaira-browse/compare/v0.3.0...v0.3.1
[v0.3.0]: https://github.com/danieljustus/symaira-browse/compare/v0.2.3...v0.3.0
[v0.2.3]: https://github.com/danieljustus/symaira-browse/compare/v0.2.2...v0.2.3
[v0.2.2]: https://github.com/danieljustus/symaira-browse/compare/v0.2.1...v0.2.2
[v0.2.1]: https://github.com/danieljustus/symaira-browse/compare/v0.2.0...v0.2.1
[v0.2.0]: https://github.com/danieljustus/symaira-browse/compare/v0.1.1...v0.2.0
[v0.1.1]: https://github.com/danieljustus/symaira-browse/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/danieljustus/symaira-browse/releases/tag/v0.1.0
