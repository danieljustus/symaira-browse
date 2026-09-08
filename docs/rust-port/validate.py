#!/usr/bin/env python3
"""Validate the Rust-port handoff package without third-party modules."""

from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent


def load(name: str):
    with (ROOT / name).open(encoding="utf-8") as handle:
        return json.load(handle)


def fail(message: str) -> None:
    raise ValueError(message)


def unique_ids(items: list[dict], label: str) -> set[str]:
    raw_ids = [item.get("id") for item in items]
    if any(not isinstance(item, str) or not item for item in raw_ids):
        fail(f"{label}: every item needs a non-empty string id")
    ids = [item for item in raw_ids if isinstance(item, str)]
    if len(ids) != len(set(ids)):
        fail(f"{label}: duplicate ids")
    return set(ids)


def visit(node: str, deps: dict[str, list[str]], active: set[str], done: set[str]) -> None:
    if node in active:
        fail(f"work-items: dependency cycle at {node}")
    if node in done:
        return
    active.add(node)
    for dep in deps[node]:
        visit(dep, deps, active, done)
    active.remove(node)
    done.add(node)


def validate_links() -> None:
    markdown = list(ROOT.glob("*.md"))
    pattern = re.compile(r"\[[^]]+\]\(([^)]+)\)")
    for path in markdown:
        for target in pattern.findall(path.read_text(encoding="utf-8")):
            if "://" in target or target.startswith("#"):
                continue
            local = target.split("#", 1)[0]
            if local and not (path.parent / local).exists():
                fail(f"{path.name}: missing local link {target}")


def main() -> int:
    baseline = load("baseline.json")
    contracts_doc = load("contract-matrix.json")
    work_doc = load("work-items.json")
    if baseline.get("schema_version") != 1:
        fail("baseline: unsupported schema_version")
    contracts = contracts_doc.get("contracts")
    work = work_doc.get("items")
    if not isinstance(contracts, list) or not isinstance(work, list):
        fail("contracts/items must be arrays")
    contract_ids = unique_ids(contracts, "contract-matrix")
    work_ids = unique_ids(work, "work-items")
    allowed_comparisons = {"bytes", "json-semantic", "filesystem", "process", "network", "manual-gate"}
    allowed_contract_status = {"todo", "fixture-ready", "parity", "accepted-difference"}
    for row in contracts:
        required = {"id", "seam", "fixture", "go_oracle", "expected", "comparison", "platforms", "status"}
        missing = required - row.keys()
        if missing:
            fail(f"{row['id']}: missing {sorted(missing)}")
        if row["comparison"] not in allowed_comparisons:
            fail(f"{row['id']}: invalid comparison")
        if row["status"] not in allowed_contract_status:
            fail(f"{row['id']}: invalid status")
        if not row["platforms"]:
            fail(f"{row['id']}: platforms must not be empty")
    deps: dict[str, list[str]] = {}
    for item in work:
        item_deps = item.get("depends_on")
        refs = item.get("contracts")
        if not isinstance(item_deps, list) or not isinstance(refs, list):
            fail(f"{item['id']}: depends_on/contracts must be arrays")
        dangling_deps = set(item_deps) - work_ids
        dangling_refs = set(refs) - contract_ids
        if dangling_deps:
            fail(f"{item['id']}: dangling dependencies {sorted(dangling_deps)}")
        if dangling_refs:
            fail(f"{item['id']}: dangling contracts {sorted(dangling_refs)}")
        status = item.get("status")
        if status not in {"blocked", "ready", "in_progress", "complete"}:
            fail(f"{item['id']}: invalid status {status!r}")
        dependencies_complete = all(
            next(candidate for candidate in work if candidate["id"] == dep).get("status") == "complete"
            for dep in item_deps
        )
        if status == "ready" and not dependencies_complete:
            fail(f"{item['id']}: ready while a dependency is incomplete")
        if status == "blocked" and dependencies_complete:
            fail(f"{item['id']}: blocked despite all dependencies being complete")
        if not item.get("acceptance_commands") or not item.get("stop_rule"):
            fail(f"{item['id']}: acceptance_commands and stop_rule are required")
        deps[item["id"]] = item_deps
    done: set[str] = set()
    for item_id in work_ids:
        visit(item_id, deps, set(), done)
    covered = {contract for item in work for contract in item["contracts"]}
    missing_coverage = contract_ids - covered
    if missing_coverage:
        fail(f"contracts without work item: {sorted(missing_coverage)}")
    validate_links()
    print(f"ok: {len(contract_ids)} contracts, {len(work_ids)} work items, acyclic DAG, links valid")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
