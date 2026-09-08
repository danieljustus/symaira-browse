#!/usr/bin/env python3
"""Generate/check the committed Rust hardening inventory.

This inventory is intentionally separate from cargo-geiger's verbose report.
It records every resolved dependency and the owned-unsafe scan; CI runs the
pinned geiger command and uploads its machine-readable report as evidence.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import tomllib
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "security" / "rust-hardening-inventory.json"
UNSAFE_DECL = re.compile(r"\bunsafe\s+(?:\{|fn\b|impl\b|trait\b|extern\b)")


def metadata() -> dict:
    result = subprocess.run(
        ["cargo", "metadata", "--format-version", "1", "--locked"],
        cwd=ROOT,
        check=True,
        capture_output=True,
        text=True,
    )
    return json.loads(result.stdout)


def owned_unsafe() -> list[dict[str, object]]:
    found = []
    for path in sorted((ROOT / "crates").glob("*/src/**/*.rs")):
        for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
            if UNSAFE_DECL.search(line):
                found.append({"path": path.relative_to(ROOT).as_posix(), "line": line_number})
    return found


def inventory() -> dict:
    lock = tomllib.loads((ROOT / "Cargo.lock").read_text(encoding="utf-8"))
    packages = []
    for package in lock.get("package", []):
        packages.append(
            {
                "name": package["name"],
                "version": package["version"],
                "source": package.get("source", "workspace"),
            }
        )
    packages.sort(key=lambda package: (package["name"], package["version"]))
    workspace = [package["name"] for package in metadata()["packages"]]
    return {
        "schema_version": 1,
        "scope": "production Cargo workspace; fuzz workspace is inventoried separately",
        "owned_unsafe": owned_unsafe(),
        "owned_unsafe_policy": "deny(unsafe_code) in every owned crate",
        "dependencies": packages,
        "workspace_packages": sorted(workspace),
        "geiger": {
            "command": "cargo geiger --workspace --all-features --output-format Json",
            "status": "ci_required",
            "report": "CI artifact; rerun when Cargo.lock changes",
        },
        "honest_blockers": [
            {
                "id": "dependency-unsafe-report",
                "status": "requires_ci_tool_run",
                "human_required": False,
                "reason": "Unsafe use in transitive dependencies is not inferred from Cargo metadata; cargo-geiger must produce the report.",
            }
        ],
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    current = inventory()
    if args.write:
        OUTPUT.parent.mkdir(parents=True, exist_ok=True)
        OUTPUT.write_text(json.dumps(current, indent=2) + "\n", encoding="utf-8")
    if args.check or not args.write:
        if not OUTPUT.is_file():
            print(f"missing inventory: {OUTPUT}")
            return 1
        recorded = json.loads(OUTPUT.read_text(encoding="utf-8"))
        if recorded != current:
            print(json.dumps({"recorded": recorded, "current": current}, indent=2, sort_keys=True))
            return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
