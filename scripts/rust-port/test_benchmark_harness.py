#!/usr/bin/env python3
"""Regression tests for the paired benchmark daemon command contract."""
from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import time
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
        binary = Path(tempfile.gettempdir()) / "symbrowse"
        self.assertEqual(
            bench_run.daemon_command(binary, "go", static_mode=False),
            [str(binary), "daemon", "--session", "go", "--engine", "static"],
        )
        self.assertEqual(
            bench_run.daemon_command(binary, "rust", static_mode=True),
            [str(binary), "daemon", "--session", "rust", "--mode", "static"],
        )
        self.assertNotIn("--engine", bench_run.daemon_command(binary, "rust", static_mode=True))

    def test_environment_selection_is_implementation_specific(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            go = bench_run.implementation_env(root, "go")
            rust = bench_run.implementation_env(root, "rust")
            self.assertEqual(go["SYMBROWSE_ENGINE"], "static")
            self.assertNotIn("SYMBROWSE_MODE", go)
            self.assertEqual(rust["SYMBROWSE_MODE"], "browser")
            self.assertNotIn("SYMBROWSE_ENGINE", rust)

    def test_startup_failure_is_bounded_and_kills_descendant(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            log = root / "startup.log"
            code = "import subprocess,time; subprocess.Popen(['sleep','30']); time.sleep(30)"
            with log.open("wb") as stderr:
                process = subprocess.Popen([sys.executable, "-c", code], stdout=subprocess.DEVNULL,
                                           stderr=stderr, start_new_session=True)
            started = time.monotonic()
            try:
                result = bench_run.startup_failure(process, "startup failed", log)
                self.assertLess(time.monotonic() - started, 3)
                self.assertEqual(result["status"], "error")
                self.assertIsNotNone(process.poll())
                time.sleep(0.05)
                self.assertEqual(subprocess.run(["pgrep", "-f", "sleep 30"], check=False,
                                                stdout=subprocess.DEVNULL).returncode, 1)
            finally:
                if process.poll() is None:
                    bench_run.terminate_process_tree(process)


if __name__ == "__main__":
    unittest.main()
