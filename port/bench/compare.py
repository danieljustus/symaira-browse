#!/usr/bin/env python3
"""Compare a RUST-016 benchmark report against the measured Go baseline.

The command is conservative: it reports a BLOCK when required workloads are
missing, when the candidate has no representative evidence, or when the value
thresholds are not met.  It does not turn an incomplete candidate into a cutover
recommendation.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any, Sequence

VALUE_SIZE_REDUCTION = 20.0
VALUE_RSS_REDUCTION = 20.0
MAX_P95_REGRESSION = 10.0
REQUIRED_WORKLOADS = ("cli", "mcp", "daemon", "fetch")


def pct(reference: float, candidate: float) -> float:
    if reference <= 0:
        return 0.0
    return (candidate / reference - 1.0) * 100.0


def load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"{path} is not a JSON object")
    return value


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("baseline", type=Path)
    parser.add_argument("report", type=Path)
    parser.add_argument("--reference", default="go")
    parser.add_argument("--candidate", default="rust")
    args = parser.parse_args(argv)
    try:
        baseline = load(args.baseline)
        report = load(args.report)
    except (OSError, ValueError, json.JSONDecodeError) as error:
        print(f"BLOCK: {error}", file=sys.stderr)
        return 1

    result: dict[str, Any] = {"schema_version": 1, "gate": "blocked", "workloads": {}, "reasons": []}
    if report.get("runs_per_workload") != 30:
        result["reasons"].append("exactly 30 runs per workload are required")
    if report.get("gate") != "pass":
        result["reasons"].append("benchmark report did not pass its paired execution gate")
    binaries = report.get("binaries")
    if not isinstance(binaries, dict):
        result["reasons"].append("benchmark report has no binaries object")
        print(json.dumps(result, indent=2))
        return 1
    reference = binaries.get(args.reference)
    candidate = binaries.get(args.candidate)
    if not isinstance(reference, dict) or not isinstance(candidate, dict):
        result["reasons"].append("paired Go and Rust benchmark results are required")
        print(json.dumps(result, indent=2))
        return 1

    comparable = []
    for name in REQUIRED_WORKLOADS:
        left = reference.get(name)
        right = candidate.get(name)
        if not isinstance(left, dict) or left.get("status") != "pass":
            result["reasons"].append(f"{args.reference} workload {name} is not executable")
            continue
        if not isinstance(right, dict) or right.get("status") != "pass":
            result["reasons"].append(f"{args.candidate} workload {name} is not executable")
            continue
        if left.get("samples") != 30 or right.get("samples") != 30:
            result["reasons"].append(
                f"workload {name} does not contain 30 complete paired samples"
            )
            continue
        left_p95 = float(left.get("p95_duration_ns", 0))
        right_p95 = float(right.get("p95_duration_ns", 0))
        change = pct(left_p95, right_p95)
        result["workloads"][name] = {"p95_change_percent": change}
        comparable.append(change)

    baseline_release = baseline.get("release", {})
    baseline_measurements = baseline.get("measurements", {})
    baseline_size = baseline_release.get("v0_8_0_darwin_arm64_uncompressed_bytes") or baseline_release.get("current_build_uncompressed_bytes")
    candidate_size = report.get("candidate_size_bytes")
    size_reduction = None
    if isinstance(baseline_size, (int, float)) and isinstance(candidate_size, (int, float)) and baseline_size:
        size_reduction = (1.0 - candidate_size / baseline_size) * 100.0
        result["size_reduction_percent"] = size_reduction

    baseline_rss = baseline_measurements.get("version_peak_rss", {}).get("median_bytes")
    candidate_rss = report.get("candidate_median_peak_rss_bytes")
    rss_reduction = None
    if isinstance(baseline_rss, (int, float)) and isinstance(candidate_rss, (int, float)) and baseline_rss:
        rss_reduction = (1.0 - candidate_rss / baseline_rss) * 100.0
        result["rss_reduction_percent"] = rss_reduction

    p95_ok = len(comparable) == len(REQUIRED_WORKLOADS) and max(comparable, default=float("inf")) <= MAX_P95_REGRESSION
    value_ok = (size_reduction is not None and size_reduction >= VALUE_SIZE_REDUCTION) or (
        rss_reduction is not None and rss_reduction >= VALUE_RSS_REDUCTION
    )
    if not comparable:
        result["reasons"].append("no complete representative workload pair")
    elif len(comparable) != len(REQUIRED_WORKLOADS):
        result["reasons"].append("every representative workload must have a Go/Rust pair")
    if not p95_ok:
        result["reasons"].append("p95 regression gate is missing or exceeds 10%")
    if not value_ok:
        result["reasons"].append("neither the 20% size nor 20% median RSS gain is evidenced")
    if not result["reasons"]:
        result["gate"] = "pass"
    print(json.dumps(result, indent=2))
    return 0 if result["gate"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
