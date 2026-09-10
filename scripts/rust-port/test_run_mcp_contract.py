import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPT = Path(__file__).with_name("run_mcp_contract.py")
SPEC = importlib.util.spec_from_file_location("run_mcp_contract", SCRIPT)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class RunMcpContractTests(unittest.TestCase):
    def test_prepare_endpoint_parent_creates_native_parent(self):
        with tempfile.TemporaryDirectory() as root:
            endpoint = Path(root) / "runtime" / "symbrowse" / "default.sock"
            MODULE.prepare_endpoint_parent(endpoint)
            self.assertTrue(endpoint.parent.is_dir())

    def test_startup_failure_reports_redacted_bounded_tail_and_artifact(self):
        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            log = root_path / "daemon.log"
            log.write_text("old\n" + "x" * 20000 + "\nTOKEN=secret-value\n", encoding="utf-8")
            artifact_dir = root_path / "artifact"
            with mock.patch.dict("os.environ", {"SYMBROWSE_DIAGNOSTIC_DIR": str(artifact_dir)}):
                failure = MODULE.startup_failure(log, "daemon failed")
            message = str(failure)
            self.assertIn("daemon failed", message)
            self.assertIn("<redacted>", message)
            self.assertNotIn("secret-value", message)
            artifact = artifact_dir / "startup.log"
            self.assertTrue(artifact.is_file())
            self.assertLessEqual(artifact.stat().st_size, 16 * 1024 + 100)
            self.assertNotIn("secret-value", artifact.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
