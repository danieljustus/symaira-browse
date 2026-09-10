#!/usr/bin/env bash
# Run bounded libFuzzer smoke tests without touching tracked seeds.
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
runs=${FUZZ_RUNS:-64}
fuzz_toolchain=${FUZZ_TOOLCHAIN:-nightly}
cargo_fuzz=${CARGO_FUZZ:-cargo fuzz}
read -r -a cargo_fuzz_cmd <<<"$cargo_fuzz"

if ! [[ "$runs" =~ ^[1-9][0-9]*$ ]] || (( runs > 4096 )); then
  printf 'FUZZ_RUNS must be an integer from 1 through 4096\n' >&2
  exit 2
fi

if ! command -v "${cargo_fuzz_cmd[0]}" >/dev/null 2>&1; then
  printf 'cargo-fuzz is required; install the pinned tool with: cargo install cargo-fuzz --version 0.13.1 --locked\n' >&2
  exit 2
fi
if ! rustup run "$fuzz_toolchain" rustc --version >/dev/null 2>&1; then
  printf 'fuzz toolchain %s is required; install it with rustup\n' "$fuzz_toolchain" >&2
  exit 2
fi

before=$(python3 "$root/scripts/fuzz_corpus_hashes.py" --check --digest)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/symbrowse-fuzz.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

for target in daemon_mcp_frames flow_yaml state_headers urls html_boundaries; do
  mkdir -p "$tmp/$target"
  cp -p "$root/fuzz/corpus/$target"/* "$tmp/$target/"
  log="$tmp/$target.log"
  # libFuzzer receives the copied corpus path, never the tracked directory.
  if ! (cd "$root" && RUSTUP_TOOLCHAIN="$fuzz_toolchain" "${cargo_fuzz_cmd[@]}" run \
      "$target" "$tmp/$target" -- "-runs=$runs") >"$log" 2>&1; then
    tail -n 8 "$log" >&2 || true
    exit 1
  fi
  actual=""
  while IFS= read -r line; do
    if [[ "$line" =~ ^Done\ ([0-9]+)\ runs\ in\  ]]; then
      actual=${BASH_REMATCH[1]}
    fi
  done <"$log"
  seed_files=("$tmp/$target"/*)
  maximum=$((runs + ${#seed_files[@]} + 1))
  if [[ -z "$actual" ]] || (( actual < runs || actual > maximum )); then
    printf 'fuzz target %s completed an invalid number of executions: requested minimum %s, actual %s, maximum %s\n' "$target" "$runs" "${actual:-missing}" "$maximum" >&2
    tail -n 8 "$log" >&2 || true
    exit 1
  fi
  printf '%s: %s executions (requested minimum %s)\n' "$target" "$actual" "$runs"
done

after=$(python3 "$root/scripts/fuzz_corpus_hashes.py" --check --digest)
test "$before" = "$after" || {
  printf 'tracked fuzz seed hash changed during smoke: %s -> %s\n' "$before" "$after" >&2
  exit 1
}
printf 'seed hash unchanged: %s\n' "$after"
