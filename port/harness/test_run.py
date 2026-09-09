#!/usr/bin/env python3
"""Regression tests for the Chrome full-suite evidence boundary."""
from __future__ import annotations

import ast
import importlib.util
import unittest
from pathlib import Path
from unittest.mock import Mock, patch

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

    def test_windows_chrome_discovery_uses_standard_install_root(self) -> None:
        environment = {"PROGRAMFILES": r"C:\\Program Files"}
        candidates = module.windows_chrome_candidates(environment)
        self.assertIn(r"C:\\Program Files\Google\Chrome\Application\chrome.exe", candidates)

    def test_fixture_server_shutdown_is_explicit(self) -> None:
        server = Mock()
        thread = Mock()
        module.stop_fixture_server(server, thread)
        server.shutdown.assert_called_once_with()
        server.server_close.assert_called_once_with()
        thread.join.assert_called_once_with(timeout=5)

    def test_chrome_daemon_rejects_nonzero_exit_after_successful_stop(self) -> None:
        # Simulated responses exercise harness failure handling, not native evidence.
        process = Mock()
        process.wait.return_value = 17
        process.communicate.return_value = (b"", b"")
        responses = {
            "daemon.ping": {"success": True},
            "open": {"success": True, "data": {"final_url": "http://localhost/final"}},
            "evaluate": {"success": True, "data": {"marker": "rust012-native"}},
            "get.text": {"success": True, "data": "daemon chrome"},
            "snapshot": {"success": True, "data": {"nodes": [{"id": "heading"}]}},
            "wait": {"success": False, "error": {"code": "operation_timeout"}},
            "daemon.stop": {"success": True},
        }
        with (
            patch.object(module, "chrome_executable", return_value=SCRIPT),
            patch.object(module.http.server, "ThreadingHTTPServer") as server,
            patch.object(module.threading, "Thread") as thread,
            patch.object(module, "start_daemon", return_value=process),
            patch.object(module, "wait_for_path"),
            patch.object(module, "request", side_effect=lambda _path, frame, **_kwargs: responses[frame["cmd"]]) as request,
            patch.object(module, "kill_tree") as cleanup,
            patch("builtins.print") as output,
        ):
            with self.assertRaisesRegex(AssertionError, "daemon exited with status 17 after daemon.stop"):
                module.chrome_daemon_suite(
                    SCRIPT.parents[2],
                    {"SYMBROWSE_E2E": "1", "SYMBROWSE_RUST_BINARY": str(SCRIPT)},
                )
        self.assertEqual(request.call_args.args[1]["cmd"], "daemon.stop")
        process.wait.assert_called_once_with(timeout=10)
        cleanup.assert_called_once_with(process)
        server.return_value.shutdown.assert_called_once_with()
        server.return_value.server_close.assert_called_once_with()
        thread.return_value.join.assert_called_once_with(timeout=5)
        output.assert_not_called()


if __name__ == "__main__":
    unittest.main()
