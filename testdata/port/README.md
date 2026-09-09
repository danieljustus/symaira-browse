# Port fixtures

These fixtures are language-neutral inputs for the Go↔Rust black-box harness.
They are pinned to Go oracle commit
`652453d1595fc302bd69c328e7da8a21dbee28b9` and release `v0.8.0`.

## Isolation

Each process gets fresh HOME, USERPROFILE, XDG config/data/cache/state/runtime
roots, temp directories, `LANG=C`, `LC_ALL=C`, `TZ=UTC`, `TERM=dumb` and
`NO_COLOR=1`. Update checks and optional SymBrain guard delegation are disabled.
Case files cannot override those reserved values. The harness captures raw
stdout/stderr, exit code, terminating signal, timeout status and a recursive
manifest of every isolated writable root.

This is process isolation, not an OS sandbox. Current bootstrap cases exercise
only version/config/batch composition and parser paths, so they do not access
Chrome, Safari, the network, Keychain or real credentials. Batch execution is
limited to commands already present in the staged Rust binary. Later suites
that exercise external seams must inject hermetic adapters or run inside an
explicit sandbox; a temporary HOME alone is not sufficient.

## Rules

- Generate oracle artifacts through the production Go binary or loader.
- Keep exact oracle commit/release metadata in every suite.
- Use byte comparison unless a documented contract explicitly permits
  normalization.
- JSON-semantic cases normalize isolated sandbox-root strings and may remove
  only fields named explicitly in `ignore_json_fields`; batch cases use this
  solely for measured `duration_ms`. Every other key and value remains gated.
- Do not commit `port/results/`; it contains host-specific benchmark output.
- Never hand-edit generated golden artifacts after capture.
