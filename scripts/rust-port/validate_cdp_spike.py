#!/usr/bin/env python3
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ORACLE = "652453d1595fc302bd69c328e7da8a21dbee28b9"
probe = json.loads((ROOT / "docs/rust-port/rust011-cdp-probe.json").read_text())
value = json.loads((ROOT / "docs/rust-port/rust011-value-signal.json").read_text())
assert probe["schema_version"] == 1
assert probe["host"]["oracle_commit"] == ORACLE
assert probe["candidate"]["version"] == "0.9.1"
for mode in ("launch", "attach"):
    result = probe["probe"][mode]
    assert result["exit_code"] == 0
    assert result["title"] == "symbrowse-cdp"
    assert result["ax_node_count"] > 0
    assert result["screenshot_bytes"] > 0
assert value["schema_version"] == 1
assert value["binary"]["vcs_revision"] == ORACLE
assert value["runs_per_workload"] == 30
comparison = value["comparisons"][0]
assert any(
    comparison[name] <= -10.0
    for name in (
        "binary_size_change_percent",
        "p95_latency_change_percent",
        "median_peak_rss_change_percent",
    )
)
print("PASS RUST-011 CDP feasibility artifacts and early value signal")
