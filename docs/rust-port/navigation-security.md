# Navigation admission and warning security (#442)

This bounded contract applies to the `e86c1db46ad758d89372640473a5311525e3edf1`
base. Go remains the executable oracle and operational fallback. RUST-013 and
ENG-008 remain incomplete.

## Admission and warning contract

With an active domain allowlist, explicit `open`/`goto` targets are checked
before daemon handler/engine dispatch. The in-process runtime uses the same
check, including flow steps that call its browser dispatcher. Admission-denied
requests must not enter engine request history. Safe denial messages retain
Go's message structure and ordinary `operation_failed` error code.

Successful response warnings pass through the existing Redactor. Warning
kind, severity, ordering and counts are unchanged. Message, ref and excerpt
credentials are scrubbed. URL userinfo is replaced in full, secret query values
are replaced individually, and public host/path/query information survives.
MCP also scrubs received warning objects, including warnings from the Go daemon.
No stdout diagnostics are introduced; the MCP test parses every output line as
an identified JSON-RPC response.

## Executable evidence and limits

`internal/daemon/navigation_security_port_test.go` drives Go's production
`NavigationRuntime.Handle` with the existing injectable engine boundary. It
records successful navigation, denied navigation and successful inspection.
`testdata/port/daemon/navigation-security.json` freezes success, error message,
engine navigation-call count and ordered warnings. Production Go source SHA-256
hashes and the base commit are included. No URL or warning normalizers are used.
These observations deliberately exclude navigation/inspection data payloads;
they are an admission/warning contract, not whole-engine parity proof.

`crates/symbrowse-daemon/tests/navigation_security.rs` compares the same sequence
through the production socket server and its existing injectable handler. It
also exercises the in-process production runtime for Chrome, Firefox and both
Safari modes, proving denial before browser initialization. Credential-bearing
open/goto denials do not reach the handler. A later successful response is
checked against separately seeded engine warning history containing synthetic
credentials. The MCP integration test uses the real socket proxy and stdio
framer against that server.

**Blocked engine-history differential gate:** issue #442 refers to denial
history in PR #441's `a96c3ab5df26f0794aa3122367b4d5c4911b344f`. The required base
has no corresponding Rust network-policy reporter or daemon warning conversion.
Consequently these tests cannot claim to execute that missing engine history
path. No code or worktree from #441 is imported. Recovery is to integrate its
reviewed reporter implementation separately, retain this daemon admission check,
and run the source-bound Go/Rust sequence through that engine's injectable
transport, including independent subrequest denials and native engine gates.

**GO_KNOWN_DEFECT_442_CREDENTIAL_POLICY_WARNING:** Go's production
`networkPolicyWarnings` emits credential-bearing engine-history URLs verbatim.
The named fixture freezes this defect, with fake credentials only. Rust's
warning redaction is the explicit security exception requested by #442, not an
unreported parity improvement. Go source is unchanged. A Go warning-boundary
repair and re-freeze remain separate follow-up work.

## Reproduce the focused gates

Set `CARGO_TARGET_DIR` to an isolated candidate directory on the verified build
volume. Set `CGO_ENABLED=0` and `GOTOOLCHAIN=go1.26.6`. Use this checkout's absolute
Cargo manifest, denoted by `$manifest` below.

```sh
go run ./scripts/rust-port/cmd/sourcecheck \
  --oracle e86c1db46ad758d89372640473a5311525e3edf1 \
  --paths internal/daemon/navigation.go,internal/daemon/navigation_frames.go,internal/daemon/inspect_frames.go,internal/daemon/protocol.go,internal/engine/navigation.go,internal/policy/allowlist.go
go test ./internal/daemon -run '^TestNavigationAdmissionSequencePort$' -count=1 -v
cargo test --manifest-path "$manifest" -p symbrowse-daemon --test navigation_security daemon_admission_sequence_matches_go_and_redacts_later_warnings --locked -- --exact
cargo test --manifest-path "$manifest" -p symbrowse-daemon --test navigation_security runtime_rejects_navigation_before_browser_initialization --locked -- --exact
cargo test --manifest-path "$manifest" -p symbrowse-mcp --test navigation_security mcp_admission_and_later_warning_frames_are_clean --locked -- --exact
cargo fmt --manifest-path "$manifest" --all -- --check
cargo clippy --manifest-path "$manifest" -p symbrowse-daemon -p symbrowse-mcp --all-targets --all-features --locked -- -D warnings
```

Each exact Rust invocation must report `1 passed`; zero matching tests fail the
gate. To deliberately regenerate the fixture after source verification, run the
Go test with `SYMBROWSE_PORT_FIXTURE_UPDATE=1`. Repository-required Go
`make fmt-check`, `make build`, `make test`, and `make lint` also apply before
commit. None of these focused gates authorizes native-engine parity, migration
cutover, release, or removal of Go.
