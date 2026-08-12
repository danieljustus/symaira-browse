# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/danieljustus/symaira-browse/compare/v0.1.1...HEAD
[v0.1.1]: https://github.com/danieljustus/symaira-browse/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/danieljustus/symaira-browse/releases/tag/v0.1.0
