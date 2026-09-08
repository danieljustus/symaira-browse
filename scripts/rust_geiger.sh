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
  symbrowse-core
  symbrowse-daemon
  symbrowse-engine
  symbrowse-engine-chrome
  symbrowse-engine-safari
  symbrowse-fetch
  symbrowse-mcp
  symbrowse-protocol
)

for package in "${packages[@]}"; do
  cargo geiger \
    --manifest-path "$root/crates/$package/Cargo.toml" \
    --all-features \
    --output-format Json \
    --locked \
    >"$out_dir/$package.json"
done

printf 'cargo-geiger reports: %s\n' "$out_dir"
