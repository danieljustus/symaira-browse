#!/usr/bin/env python3
"""Regression tests for the Chrome full-suite evidence boundary."""
from __future__ import annotations

import ast
import importlib.util
import unittest
from pathlib import Path
from unittest.mock import patch

SCRIPT = Path(__file__).with_name("run.py")
spec = importlib.util.spec_from_file_location("port_harness_run", SCRIPT)
assert spec and spec.loader
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ChromeFullHarnessTests(unittest.TestCase):
    def test_chrome_full_requires_explicit_e2e_precondition(self) -> None:
        with patch.object(module, "run") as command:
            result = __import__("subprocess").run(
                ["python3", str(SCRIPT), "--suite", "chrome-full"],
                capture_output=True,
                text=True,
            )
        self.assertEqual(result.returncode, 1)
        self.assertIn("SYMBROWSE_E2E=1", result.stderr)
        command.assert_not_called()

    def test_chrome_full_contains_daemon_path_not_fixture_only(self) -> None:
        tree = ast.parse(SCRIPT.read_text(encoding="utf-8"))
        names = {
            node.func.id
            for node in ast.walk(tree)
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Name)
        }
        self.assertIn("chrome_daemon_suite", names)
        self.assertIn("start_daemon", names)
        self.assertIn("request", names)


if __name__ == "__main__":
    unittest.main()
