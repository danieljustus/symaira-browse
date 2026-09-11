# RUST-016 release, rollback, and value gates

Status: **prepared, blocked, no cutover**

Evidence captured: `2026-09-07T10:31:35Z` on the local darwin/arm64 host.
The existing Go release remains the oracle and default. The release workflow now
has an explicit, non-default `workflow_dispatch` dual-build input; enabling it
still does not enable Rust cutover or change the Go default.

## Artifact contract

The existing `.goreleaser.yml` conventions are authoritative:

- six target tuples: `darwin-amd64`, `darwin-arm64`, `linux-amd64`,
  `linux-arm64`, `windows-amd64`, `windows-arm64`;
- archive names: `symbrowse_<version>_<os>_<arch>.tar.gz`, with `.zip` for
  Windows;
- archive payload binary: `symbrowse` (or `symbrowse.exe` for Windows), plus
  `LICENSE`, `README.md`, and `AGENTS.md`;
- SHA-256 `checksums.txt`, covering all six archives and their six
  `<archive>.sbom` files, matching the published v0.8.0 release;
- SPDX 2.3 SBOM: `<archive>.sbom`;
- signature and certificate companions: `<archive>.sig` and `<archive>.pem`.
  The verifier accepts the base64-encoded PEM certificate format used by the
  existing v0.8.0 release as well as direct PEM;
- per-implementation `signature-inputs.json`, binding every archive digest to
  its required signature and certificate names; and
- top-level `platform-proofs.json`, binding every archive to a cross-build or
  native-runtime proof. Cross-built Windows artifacts remain external runtime
  proof until a Windows runner exercises them.

The dual prerelease namespace is:

```text
dual/
  go/
    <GoReleaser archive and companions>
  rust/
    <GoReleaser archive and companions>
  dual-release-manifest.json
  platform-proofs.json
```

The implementation directory prevents identical Go/Rust asset names from
colliding while preserving the names consumed by the existing release process.
`port/release/verify.py` requires all six targets, the exact binary name,
checksums, SPDX documents, signatures, and certificates. Missing assets,
wrong binaries, checksum mismatches, invalid SPDX documents, and missing or
empty signatures fail closed.

## Explicit selection and rollback contract

`SYMBROWSE_IMPL` is explicit and has no implicit `auto` mode:

| Request/state | Result | Fallback |
|---|---|---|
| unset (documented as `go`) + Go available | Go | n/a |
| `go` + Go available | Go | never selects Rust |
| `rust` + Rust available and integrity-valid | Rust | n/a |
| `rust` + Rust unavailable | Go | permitted availability fallback |
| `rust` + Rust signature/checksum/certificate failure | BLOCK | never downgrade integrity failure |
| `go` + Go unavailable | BLOCK | no Rust substitution |
| Rust unavailable + Go unavailable | BLOCK | none |
| any implicit/unknown selector | BLOCK | no heuristic selection |

The manifest encodes this as `default=go`, `opt_in=rust`,
`availability_failure=fallback_go`, and `integrity_failure=block_no_fallback`.
Rust-default cutover is intentionally not enabled.

## Local evidence

Commands run from this worktree:

```text
python3 port/release/verify.py --self-test
PASS RUST-016 verifier self-tests

python3 port/release/verify.py --oracle-tag v0.8.0 --implementation go \
  --candidate target/release-evidence/oracle-layout/dual
PASS: six real v0.8.0 Go archives, checksums, SPDX SBOMs, signatures and
      base64-encoded PEM certificates validated.

python3 port/release/verify.py --oracle-tag v0.8.0 --package-dry-run \
  --go-binary target/release-evidence/go-oracle/symbrowse \
  --rust-binary target/release/symbrowse \
  --output target/release-evidence/rust016-dry-run
PASS: actual Go v0.8.0 darwin/arm64 and locally built Rust darwin/arm64
      binaries packaged into separate dual/go and dual/rust archives.
```

The local dry-run intentionally emits no signing material. Running the full
verifier against it returned non-zero with an archive-matrix BLOCK before it
could accept the incomplete single-target set. The self-test separately proves
missing signatures and wrong binaries fail closed.

Rust release build evidence:

```text
cargo build --release -p symbrowse-cli --bin symbrowse --locked
PASS: target/release/symbrowse (Mach-O arm64)
{"tool":"symbrowse","version":"dev","schema_version":8}
```

The harness now accepts the release gate command directly:

```text
python3 port/harness/run.py --suite all --native-targets
PASS: all implemented suites and host-native workspace targets
BLOCKED externally: browser-driver proof is opt-in and Windows runtime proof is not inferred from cross-builds
```

`port/release/build_dual.py` builds and archives every target for which the Go
and Rust toolchains/linkers are present. It writes checksums, SPDX documents,
platform proofs, and unsigned signature-input manifests; it never fabricates a
signature or certificate. On this isolated worktree the Rust darwin/arm64
artifact built, while the Go oracle source was unavailable, so a paired release
and value gate remain blocked.


`port/bench/run.py` covers executable surfaces without changing the product:

- CLI `version --json`;
- MCP initialize plus `tools/list` over stdio;
- daemon static-engine ping over the native Unix socket and clean stop;
- local static HTTP fetch probe (`read <fixture-url> --engine static`).

Evidence command:

```text
python3 port/bench/run.py --rust target/release/symbrowse \
  --output port/results/rust016-benchmark.json --runs 2
```

Observed Rust candidate result: CLI, MCP, and daemon probes passed; fetch was
reported `unsupported`; no Go binary was supplied in that run. The report's
gate is therefore `blocked`. `port/bench/compare.py` requires paired Go/Rust
results, all four representative workloads, p95 regression <=10%, and either
20% uncompressed-size reduction or 20% median RSS reduction. RSS is not
invented by the portable runner; use the existing `portbench`/platform tools
for that measurement.

## Phase 2 paired benchmark evidence

Evidence captured on `2026-09-08T14:04:57Z` from the clean local darwin/arm64
worktree is stored in `port/results/rust016-benchmark-v2.json` (schema 2,
30 raw samples per workload). The report binds both binary SHA-256 digests,
source revision, fixture identity, cache policy, and nearest-rank p95 calculation.
The semantic probe checks final URL, HTTP status, rendered body, document
metadata, and error-shaped responses. Its deliberately fast `success=true` /
wrong-title-and-body negative control was rejected for both binaries.

The report's `compare.py` output is the canonical programmatic comparison and
contains the four p95 ratios plus the independent fetch hard-gate result. The
Rust release binary is 7,826,384 bytes versus Go's 18,264,882 bytes, so the
binary-size value criterion is evidenced in the report. RUST-016 remains blocked
because native release artifacts/signatures and the other release gates are not
complete; this evidence does not authorize cutover.

## Remaining blockers

1. Produce and verify Rust archives for all six targets with the exact binary
   names and archive layout.
2. Generate release SBOMs with the release tool and attach real signatures and
   certificates; local dry-run signing/notarization was not performed.
3. Rebuild both current-candidate binaries from the integrated parent while
   retaining v0.8.0 as the rollback oracle.
4. Run paired native Go/Rust CLI, MCP, daemon, and fetch measurements on the
   required platforms, including peak RSS, then evaluate the value gate.
5. Exercise the full dual implementation matrix on signed artifacts before any
   cutover proposal. No signing, notarization, native-target runtime, or Rust
   default claim is made by this evidence.

## 2026-09-11 benchmark-harness handover run

The startup blocker was reproduced on the pinned Go oracle (`652453d1`, release
`v0.8.0`) and the Rust candidate built from the handover base
`1a383d8651e1461712a4da43cdf0683884e46a3a`. Go completed all four real local
workloads (CLI, MCP, daemon ping/stop, and static HTTP fetch). Rust CLI and MCP
completed, but daemon and fetch could not start: the candidate exited before
creating its socket with `invalid transport selection: unknown browser engine
"static": use chrome, safari, or firefox`.

The benchmark harness now records the child startup diagnostic instead of
collapsing it to “socket did not appear”, and its regression test preserves the
separate Go and Rust static-selection argument contracts. This is a harness
repair only; no product protocol or backend code was changed. The paired
30-run release gate was therefore not cleared: a genuine four-workload pair was
not executable, so the RUST-016 release gate remains **BLOCKED**. The raw
30-run report and comparison output must be captured after the candidate's
transport-selection incompatibility is resolved; no synthetic p95 is reported.
