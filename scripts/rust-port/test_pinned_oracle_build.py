#!/usr/bin/env python3
"""Focused integration tests for the pinned oracle build boundary."""

from __future__ import annotations

import os
import re
import subprocess
import tempfile
import unittest
from pathlib import Path


class PinnedOracleBuildTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.repo = Path(__file__).resolve().parents[2]
        makefile = (cls.repo / "Makefile").read_text()
        def match(pattern: str, text: str) -> str:
            found = re.search(pattern, text, re.M)
            if found is None:
                raise RuntimeError(f"missing test configuration: {pattern}")
            return found.group(1)
        cls.commit = match(r"^PORT_ORACLE_COMMIT := ([0-9a-f]+)$", makefile)
        cls.release = match(r"^PORT_ORACLE_RELEASE := (.+)$", makefile)
        cls.go_version = match(r'^go ([0-9.]+)$', (cls.repo / "go.mod").read_text())
        cls.cases = cls.repo / "testdata/port/bootstrap/cases.json"

    def _run(self, args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env.update({"GOTOOLCHAIN": f"go{self.go_version}", "CGO_ENABLED": "0"})
        return subprocess.run(args, cwd=self.repo, env=env, text=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              check=check)

    def _harness(self, binary: Path) -> subprocess.CompletedProcess[str]:
        return self._run([
            "go", "run", "./scripts/rust-port/cmd/diffharness",
            "--left", str(binary), "--right", str(binary),
            "--cases", str(self.cases),
            "--expect-oracle-commit", self.commit,
            "--expect-oracle-release", self.release,
            "--verify-left-go-revision",
        ], check=False)

    def test_pinned_binary_is_accepted_by_diffharness(self) -> None:
        with tempfile.TemporaryDirectory(prefix="pinned-oracle-test-") as directory:
            binary = Path(directory) / "pinned-oracle"
            result = self._run([
                "python3", "scripts/rust-port/pinned_oracle_build.py",
                "--repo", str(self.repo), "--commit", self.commit,
                "--release", self.release, "--output", str(binary),
                "--go", "go", "--go-version", self.go_version,
            ], check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            harness = self._harness(binary)
            self.assertEqual(harness.returncode, 0, harness.stderr)

    def test_active_current_revision_is_rejected(self) -> None:
        current = self._run(["git", "rev-parse", "HEAD"]).stdout.strip()
        self.assertNotEqual(current, self.commit)
        current_release = self._run(["git", "describe", "--tags", "--abbrev=0", current]).stdout.strip()
        with tempfile.TemporaryDirectory(prefix="active-oracle-test-") as directory:
            binary = Path(directory) / "active-oracle"
            result = self._run([
                "python3", "scripts/rust-port/pinned_oracle_build.py",
                "--repo", str(self.repo), "--commit", current,
                "--release", current_release, "--output", str(binary),
                "--go", "go", "--go-version", self.go_version,
            ], check=False)
            self.assertEqual(result.returncode, 0, result.stderr)
            harness = self._harness(binary)
            self.assertNotEqual(harness.returncode, 0)
            self.assertIn("left Go oracle provenance", harness.stderr)
            self.assertIn(self.commit, harness.stderr)


if __name__ == "__main__":
    unittest.main()
