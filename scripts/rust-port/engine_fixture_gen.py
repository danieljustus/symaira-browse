#!/usr/bin/env python3
"""Generate/check the Go-oracle engine-neutral Rust fixture.

The Go test is the production oracle: it runs inside internal/engine so the
unexported stable-ref registry and snapshot diff implementation are exercised.
This wrapper pins the source revision and refuses to label a fixture from a
modified oracle as current.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import subprocess
import tempfile
from pathlib import Path

ORACLE_COMMIT = "652453d1595fc302bd69c328e7da8a21dbee28b9"
SOURCE_FILES = (
    "internal/engine/engine.go",
    "internal/engine/stable_refs.go",
    "internal/engine/snapshot.go",
    "internal/engine/snapshot_diff.go",
    "internal/engine/navigation.go",
)
FIXTURE_RELATIVE = Path("testdata/port/engine/engine-neutral.json")
MANIFEST_RELATIVE = Path("testdata/port/engine/manifest.json")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def source_hashes(root: Path) -> dict[str, str]:
    hashes: dict[str, str] = {}
    for relative in SOURCE_FILES:
        current = (root / relative).read_bytes()
        pinned = subprocess.check_output(["git", "show", f"{ORACLE_COMMIT}:{relative}"], cwd=root)
        if current != pinned:
            raise SystemExit(f"oracle source differs from pinned commit: {relative}")
        hashes[relative] = sha256(current)
    return hashes


def run_oracle(root: Path, output: Path) -> None:
    environment = os.environ.copy()
    environment.update(
        {
            "CGO_ENABLED": "0",
            "GOTOOLCHAIN": "go1.26.6",
            "RUST_PORT_ENGINE_FIXTURE_OUT": str(output),
        }
    )
    subprocess.run(
        ["go", "test", "-count=1", "./internal/engine", "-run", "^TestGenerateRustPortEngineFixture$"],
        cwd=root,
        env=environment,
        check=True,
    )


def manifest(root: Path) -> dict[str, object]:
    fixture = root / FIXTURE_RELATIVE
    data = json.loads(fixture.read_text(encoding="utf-8"))
    return {
        "schema_version": 1,
        "oracle_commit": ORACLE_COMMIT,
        "normalization": "route tree strings omitted; refs/diffs preserved pending issue #401",
        "source_files": source_hashes(root),
        "generator_sha256": sha256(Path(__file__).read_bytes()),
        "fixture": str(FIXTURE_RELATIVE),
        "route_count": len(data["routes"]),
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    root = args.root.resolve()
    fixture = root / FIXTURE_RELATIVE
    manifest_path = root / MANIFEST_RELATIVE
    fixture.parent.mkdir(parents=True, exist_ok=True)
    source_hashes(root)

    with tempfile.TemporaryDirectory(prefix="symbrowse-engine-fixture-") as temporary:
        generated = Path(temporary) / fixture.name
        run_oracle(root, generated)
        generated_bytes = generated.read_bytes()
        if args.check:
            if not fixture.exists() or fixture.read_bytes() != generated_bytes:
                raise SystemExit("engine-neutral Go fixture is stale; rerun without --check")
        else:
            fixture.write_bytes(generated_bytes)

    expected_manifest = manifest(root)
    rendered_manifest = json.dumps(expected_manifest, indent=2, sort_keys=True) + "\n"
    if args.check:
        if not manifest_path.exists() or manifest_path.read_text(encoding="utf-8") != rendered_manifest:
            raise SystemExit("engine-neutral fixture manifest is stale; rerun without --check")
        print(f"engine-neutral fixtures are current ({expected_manifest['route_count']} routes)")
    else:
        manifest_path.write_text(rendered_manifest, encoding="utf-8")
        print(f"generated engine-neutral fixture ({expected_manifest['route_count']} routes)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
