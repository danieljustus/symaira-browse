#!/usr/bin/env bash
# Emit one machine-readable cargo-geiger report per production crate.
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out_dir=${GEIGER_OUTPUT_DIR:-"$root/target/hardening/geiger"}
mkdir -p "$out_dir"

# cargo-geiger cannot analyze the repository's virtual workspace manifest. Run
# each real crate manifest explicitly so every production package is covered.
packages=(
  symbrowse-cli
  symbrowse-compat
  symbrowse-core
  symbrowse-daemon
  symbrowse-engine
  symbrowse-engine-chrome
  symbrowse-engine-firefox
  symbrowse-engine-safari
  symbrowse-fetch
  symbrowse-mcp
  symbrowse-protocol
)

for package in "${packages[@]}"; do
  report="$out_dir/$package.json"
  stderr=$(mktemp)
  if ! cargo geiger \
    --manifest-path "$root/crates/$package/Cargo.toml" \
    --all-features \
    --output-format Json \
    --locked \
    >"$report" 2>"$stderr"; then
    printf '%s\n' "$(<"$stderr")" >&2
    rm -f "$stderr"
    exit 1
  fi
  diagnostics=$(<"$stderr")
  rm -f "$stderr"
  printf '%s\n' "$diagnostics" >&2
  case "$diagnostics" in
    *"Failed to parse file:"*)
      printf '%s\n' "cargo-geiger emitted a parser failure for $package; refusing incomplete hardening evidence" >&2
      exit 1
      ;;
  esac
  python3 -c 'import json, sys; json.load(open(sys.argv[1], encoding="utf-8"))' "$report"
done

printf 'cargo-geiger reports: %s\n' "$out_dir"
