#!/usr/bin/env python3
"""Regression tests for the paired benchmark daemon command contract."""
from __future__ import annotations

import importlib.util
import sys
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("bench_run", ROOT / "port/bench/run.py")
assert SPEC and SPEC.loader
bench_run = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = bench_run
SPEC.loader.exec_module(bench_run)


class BenchmarkHarnessTests(unittest.TestCase):
    def test_static_daemon_command_matches_each_cli_contract(self) -> None:
        binary = Path("/tmp/symbrowse")
        self.assertEqual(
            bench_run.daemon_command(binary, "go", static_mode=False),
            ["/tmp/symbrowse", "daemon", "--session", "go", "--engine", "static"],
        )
        self.assertEqual(
            bench_run.daemon_command(binary, "rust", static_mode=True),
            ["/tmp/symbrowse", "daemon", "--session", "rust", "--mode", "static"],
        )
        self.assertNotIn("--engine", bench_run.daemon_command(binary, "rust", static_mode=True))


if __name__ == "__main__":
    unittest.main()
