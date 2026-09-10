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
DIAGNOSTICS_SPEC = importlib.util.spec_from_file_location(
    "startup_diagnostics", SCRIPT.with_name("startup_diagnostics.py")
)
assert DIAGNOSTICS_SPEC and DIAGNOSTICS_SPEC.loader
DIAGNOSTICS = importlib.util.module_from_spec(DIAGNOSTICS_SPEC)
DIAGNOSTICS_SPEC.loader.exec_module(DIAGNOSTICS)


class RunMcpContractTests(unittest.TestCase):
    def test_prepare_endpoint_parent_creates_native_parent(self):
        with tempfile.TemporaryDirectory() as root:
            endpoint = Path(root) / "runtime" / "symbrowse" / "default.sock"
            MODULE.prepare_endpoint_parent(endpoint)
            self.assertTrue(endpoint.parent.is_dir())

    def test_startup_failure_omits_oversized_token_log_and_artifact(self):
        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            log = root_path / "daemon.log"
            log.write_text("x" * 20000 + "\nTOKEN=secret-value\n", encoding="utf-8")
            artifact_dir = root_path / "artifact"
            with mock.patch.dict("os.environ", {"SYMBROWSE_DIAGNOSTIC_DIR": str(artifact_dir)}):
                failure = MODULE.startup_failure(log, "daemon failed")
            message = str(failure)
            self.assertIn("daemon failed", message)
            self.assertIn("<startup diagnostic omitted>", message)
            self.assertNotIn("secret-value", message)
            artifact = artifact_dir / "startup.log"
            self.assertTrue(artifact.is_file())
            artifact_bytes = artifact.read_bytes()
            self.assertLessEqual(len(artifact_bytes), DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES)
            self.assertEqual(artifact_bytes.decode("utf-8"), "<startup diagnostic omitted>")

    def test_authorization_and_quoted_keys_are_omitted(self):
        cases = [
            "Authorization: Bearer sensitive-value",
            "Authorization: Basic sensitive-value",
            '{"token": "sensitive value"}',
            "{'password': 'sensitive value'}",
        ]
        with tempfile.TemporaryDirectory() as root:
            log = Path(root) / "daemon.log"
            for content in cases:
                with self.subTest(content=content):
                    log.write_text(content, encoding="utf-8")
                    self.assertEqual(DIAGNOSTICS.redacted_tail(log), "<startup diagnostic omitted>")

    def test_sensitive_quoted_whitespace_value_is_omitted(self):
        with tempfile.TemporaryDirectory() as root:
            log = Path(root) / "daemon.log"
            log.write_text('socket failure\nTOKEN = "secret value with spaces"\n', encoding="utf-8")
            detail = DIAGNOSTICS.redacted_tail(log)
            self.assertEqual(detail, "<startup diagnostic omitted>")
            self.assertLessEqual(len(detail.encode("utf-8")), DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES)

    def test_sensitive_multiline_value_is_omitted(self):
        with tempfile.TemporaryDirectory() as root:
            log = Path(root) / "daemon.log"
            log.write_text('TOKEN = "first line\nsecond line\nthird line"\n', encoding="utf-8")
            detail = DIAGNOSTICS.redacted_tail(log)
            self.assertEqual(detail, "<startup diagnostic omitted>")
            self.assertNotIn("second line", detail)

    def test_bounded_boundary_includes_omission_marker(self):
        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            exact = root_path / "exact.log"
            exact.write_bytes(b"a" * DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES)
            self.assertEqual(len(DIAGNOSTICS.redacted_tail(exact).encode("utf-8")), DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES)

            oversized = root_path / "oversized.log"
            oversized.write_bytes(b"a" * (DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES + 1))
            detail = DIAGNOSTICS.redacted_tail(oversized)
            self.assertEqual(detail, "<startup diagnostic omitted>")
            self.assertLessEqual(len(detail.encode("utf-8")), DIAGNOSTICS.MAX_STARTUP_DIAGNOSTIC_BYTES)

    def test_benign_startup_error_remains_diagnosable(self):
        with tempfile.TemporaryDirectory() as root:
            log = Path(root) / "daemon.log"
            content = "daemon: listen failed on Windows socket \\.\\pipe\\symbrowse\n"
            log.write_text(content, encoding="utf-8")
            self.assertEqual(DIAGNOSTICS.redacted_tail(log), content)


if __name__ == "__main__":
    unittest.main()
