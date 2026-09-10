#!/usr/bin/env python3
"""Verify the immutable seed corpus used by the bounded fuzz smoke."""
from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CORPUS = ROOT / "fuzz" / "corpus"
MANIFEST = CORPUS / "hashes.json"


def entries() -> dict[str, str]:
    return {
        path.relative_to(CORPUS).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(CORPUS.rglob("*"))
        if path.is_file() and path != MANIFEST
    }


def digest(values: dict[str, str]) -> str:
    encoded = "".join(f"{name}\0{checksum}\n" for name, checksum in values.items()).encode()
    return hashlib.sha256(encoded).hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--write", action="store_true")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--digest", action="store_true")
    args = parser.parse_args()
    current = entries()
    if args.write:
        MANIFEST.write_text(
            json.dumps({"schema_version": 1, "seeds": current}, indent=2) + "\n",
            encoding="utf-8",
        )
    if args.check or not args.write:
        if not MANIFEST.is_file():
            raise SystemExit(f"missing seed manifest: {MANIFEST}")
        recorded = json.loads(MANIFEST.read_text(encoding="utf-8"))
        expected = recorded.get("seeds")
        if recorded.get("schema_version") != 1 or expected != current:
            print(json.dumps({"expected": expected, "actual": current}, sort_keys=True))
            return 1
    if args.digest or args.write:
        print(digest(current))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
