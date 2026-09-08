#!/usr/bin/env python3
"""Generate/check the Go-oracle upload/download guard fixture."""
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
    "internal/engine/files.go",
    "internal/engine/chrome/files.go",
    "internal/engine/chrome/runtime_events.go",
)
FIXTURE_RELATIVE = Path("testdata/port/engine/file-guards.json")
MANIFEST_RELATIVE = Path("testdata/port/engine/file-guards-manifest.json")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def source_hashes(root: Path) -> dict[str, str]:
    hashes: dict[str, str] = {}
    for relative in SOURCE_FILES:
        current = (root / relative).read_bytes()
        pinned = subprocess.check_output(
            ["git", "show", f"{ORACLE_COMMIT}:{relative}"], cwd=root
        )
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
            "RUST_PORT_FILE_FIXTURE_OUT": str(output),
        }
    )
    subprocess.run(
        [
            "go",
            "test",
            "-count=1",
            "./internal/engine/chrome",
            "-run",
            "^TestGenerateRustPortFileFixture$",
        ],
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
        "source_files": source_hashes(root),
        "generator_sha256": sha256(Path(__file__).read_bytes()),
        "fixture": str(FIXTURE_RELATIVE),
        "upload_case_count": len(data["upload"]),
        "download_collision_handling": data["download"]["collision"][
            "collision_handling"
        ],
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

    with tempfile.TemporaryDirectory(prefix="symbrowse-file-guards-") as temporary:
        generated = Path(temporary) / fixture.name
        run_oracle(root, generated)
        generated_bytes = generated.read_bytes()
        if args.check:
            if not fixture.exists() or fixture.read_bytes() != generated_bytes:
                raise SystemExit("Go file-guard fixture is stale; rerun without --check")
        else:
            fixture.write_bytes(generated_bytes)

    expected_manifest = manifest(root)
    rendered_manifest = json.dumps(expected_manifest, indent=2, sort_keys=True) + "\n"
    if args.check:
        if not manifest_path.exists() or manifest_path.read_text(encoding="utf-8") != rendered_manifest:
            raise SystemExit("Go file-guard fixture manifest is stale; rerun without --check")
        print("file-guard fixtures are current")
    else:
        manifest_path.write_text(rendered_manifest, encoding="utf-8")
        print("generated file-guard fixtures")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
