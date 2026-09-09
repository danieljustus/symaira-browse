#!/usr/bin/env python3
"""Require declared fetch-control IDs to equal both executable corpora."""
import ast
import json
import re
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
fixture = json.loads((ROOT / "port/fixtures/fetch/control.json").read_text())
declared = set(fixture["controls"]["case_ids"])

harness = ast.parse((ROOT / "port/harness/run.py").read_text())
harness_ids = set()
for node in ast.walk(harness):
    if isinstance(node, ast.Assign) and any(
        isinstance(target, ast.Name) and target.id == "FETCH_CONTROL_CASE_IDS"
        for target in node.targets
    ):
        if isinstance(node.value, (ast.List, ast.Tuple)):
            harness_ids = {
                item.value
                for item in node.value.elts
                if isinstance(item, ast.Constant) and isinstance(item.value, str)
            }
        break

rust = (ROOT / "crates/symbrowse-fetch/tests/control_contract.rs").read_text()
rust_ids = set(re.findall(r'"(FETCH-(?:001|003|004|005|009|010)-[a-z-]+)"', rust))

if declared != harness_ids or declared != rust_ids:
    raise SystemExit(
        "fetch case ID mismatch: "
        f"declared-only={sorted(declared - harness_ids - rust_ids)}, "
        f"harness-only={sorted(harness_ids - declared)}, "
        f"rust-only={sorted(rust_ids - declared)}"
    )
print(f"fetch-control case IDs equal: {len(declared)} declared == harness == Rust executed")
