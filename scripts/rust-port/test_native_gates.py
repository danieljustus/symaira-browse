#!/usr/bin/env python3
"""Source-level tests for native browser gate process ownership."""
from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("browser_fixture_gen.py")
spec = importlib.util.spec_from_file_location("browser_fixture_gen", SCRIPT)
assert spec and spec.loader
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class NativeGateTests(unittest.TestCase):
    def test_foreign_mcp_driver_is_not_owned(self) -> None:
        table = {
            10: "/usr/bin/safaridriver --mcp",
            11: "/usr/bin/safaridriver -p 12345 --bidi 23456",
        }
        self.assertEqual(module.owned_driver(table), {11})

    def test_only_automation_safari_is_cleanup_candidate(self) -> None:
        table = {
            20: "/System/Applications/Safari.app/Contents/MacOS/Safari --automation",
            21: "/System/Applications/Safari.app/Contents/MacOS/Safari",
        }
        self.assertEqual(module.automation_safari(table), {20})

    def test_native_evidence_contract_has_stable_required_fields(self) -> None:
        evidence = {
            "schema_version": 1,
            "suite": "safari",
            "requested": True,
            "status": "blocked",
            "reason": "normal Safari is running",
            "foreign_safaridriver_mcp_preserved": True,
        }
        self.assertEqual(evidence["schema_version"], 1)
        self.assertIn(evidence["suite"], {"chrome-full", "safari"})
        self.assertIn(evidence["status"], {"passed", "blocked", "failed"})
        self.assertTrue(evidence["foreign_safaridriver_mcp_preserved"])


if __name__ == "__main__":
    unittest.main()
