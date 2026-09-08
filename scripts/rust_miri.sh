#!/usr/bin/env bash
# Run only deterministic/pure Miri targets; see docs/rust-port/hardening.md.
set -euo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
toolchain=${MIRI_TOOLCHAIN:-nightly}

# These targets do not touch files, sockets, subprocesses, clocks or the
# network. Keep the selection explicit so a new I/O test cannot silently enter
# the Miri gate.
PROPTEST_DISABLE_FAILURE_PERSISTENCE=1 cargo +"$toolchain" miri test --manifest-path Cargo.toml -p symbrowse-core \
  --test hardening_properties --all-features --locked
cargo +"$toolchain" miri test --manifest-path Cargo.toml -p symbrowse-daemon \
  --lib protocol::tests:: --all-features --locked
