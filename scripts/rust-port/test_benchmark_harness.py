#!/usr/bin/env python3
"""Regression tests for the paired benchmark daemon command contract."""
from __future__ import annotations

import importlib.util
import hashlib
import os
import select
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("bench_run", ROOT / "port/bench/run.py")
assert SPEC and SPEC.loader
bench_run = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = bench_run
SPEC.loader.exec_module(bench_run)


class BenchmarkHarnessTests(unittest.TestCase):
    def test_run_once_captures_invalid_utf8_and_hashes_raw_bytes(self) -> None:
        stdout = b"out\xff\r\n"
        stderr = b"err\xfe\r\n"
        for exit_code in (0, 1):
            with self.subTest(exit_code=exit_code), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                code = (f"import sys; sys.stdout.buffer.write({stdout!r}); "
                        f"sys.stderr.buffer.write({stderr!r}); sys.exit({exit_code})")
                result = bench_run.run_once(
                    Path(sys.executable), bench_run.Probe("bytes", ("-c", code)),
                    bench_run.base_env(root), root,
                )
                self.assertEqual(result["status"], "pass" if exit_code == 0 else "error")
                if exit_code == 0:
                    self.assertEqual(result["stdout_bytes"], len(stdout))
                    self.assertEqual(result["stderr_bytes"], len(stderr))
                else:
                    self.assertEqual(result["reason"], "exit 1")
                    self.assertEqual(result["stdout_sha256"], hashlib.sha256(stdout).hexdigest())
                    self.assertEqual(result["stderr_sha256"], hashlib.sha256(stderr).hexdigest())

    def test_run_once_enforces_byte_output_limit(self) -> None:
        for stream in ("stdout", "stderr"):
            for payload in (b"\xc3\xa9" * 4, b"\xc3\xa9" * 4 + b"!"):
                with self.subTest(stream=stream, size=len(payload)), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp)
                    code = f"import sys; sys.{stream}.buffer.write({payload!r})"
                    with patch.object(bench_run, "MAX_OUTPUT", 8):
                        result = bench_run.run_once(
                            Path(sys.executable), bench_run.Probe("limit", ("-c", code)),
                            bench_run.base_env(root), root,
                        )
                    if len(payload) == 8:
                        self.assertEqual(result["status"], "pass")
                        self.assertEqual(result[f"{stream}_bytes"], 8)
                    else:
                        self.assertEqual(result["status"], "error")
                        self.assertEqual(result["reason"], "output limit exceeded")

    def test_run_once_sends_utf8_stdin_without_newline_conversion(self) -> None:
        stdin = "fixture \u00e9\r\n"
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            code = ("import sys; data = sys.stdin.buffer.read(); "
                    f"assert data == {stdin.encode('utf-8')!r}; sys.stdout.buffer.write(data)")
            result = bench_run.run_once(
                Path(sys.executable), bench_run.Probe("stdin", ("-c", code), stdin),
                bench_run.base_env(root), root,
            )
            self.assertEqual(result["status"], "pass")
            self.assertEqual(result["stdout_bytes"], len(stdin.encode("utf-8")))

    def test_run_once_preserves_content_checks_with_invalid_utf8(self) -> None:
        for payload, status in ((b"\xffPaSsWoRd=dummy", "error"),
                                (b"\xffToKeN=dummy", "error"),
                                (b"\xffUNKNOWN COMMAND", "unsupported"),
                                (b"\xffNOT IMPLEMENTED", "unsupported")):
            with self.subTest(payload=payload), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                code = f"import sys; sys.stderr.buffer.write({payload!r}); sys.exit(1)"
                result = bench_run.run_once(
                    Path(sys.executable), bench_run.Probe("content", ("-c", code)),
                    bench_run.base_env(root), root,
                )
                self.assertEqual(result["status"], status)
                self.assertEqual(result["reason"], "secret-like output" if status == "error" else "exit 1")

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

    @unittest.skipUnless(os.name == "posix", "Unix process-group probe")
    def test_startup_failure_is_bounded_and_kills_descendant(self) -> None:
        # A ready, SIGTERM-resistant child owns the pipe. EOF proves this
        # specific descendant stopped, without inspecting unrelated processes.
        child = "import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); print('ready', flush=True); time.sleep(30)"
        for leader_exits in (False, True):
            with self.subTest(leader_exits=leader_exits), tempfile.TemporaryDirectory() as tmp:
                log = Path(tmp) / "startup.log"
                code = f"import subprocess,sys,time; subprocess.Popen([sys.executable,'-c',{child!r}]); "
                code += "sys.exit(0)" if leader_exits else "time.sleep(30)"
                with log.open("wb") as stderr:
                    process = subprocess.Popen([sys.executable, "-c", code], stdout=subprocess.PIPE,
                                               stderr=stderr, start_new_session=True)
                assert process.stdout is not None
                try:
                    self.assertTrue(select.select([process.stdout], [], [], 5)[0], "child not ready")
                    self.assertEqual(process.stdout.readline(), b"ready\n")
                    if leader_exits:
                        process.wait(timeout=3)
                    started = time.monotonic()
                    result = bench_run.startup_failure(process, "startup failed", log)
                    self.assertLess(time.monotonic() - started, 3)
                    self.assertEqual(result["status"], "error")
                    self.assertIsNotNone(process.poll())
                    self.assertTrue(select.select([process.stdout], [], [], 3)[0], "child retained pipe")
                    self.assertEqual(process.stdout.read(1), b"")
                finally:
                    bench_run.terminate_process_tree(process)
                    process.stdout.close()

    def test_launch_failure_closes_descriptor_and_removes_log(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = bench_run.base_env(root)
            descriptors = []
            real_mkstemp = tempfile.mkstemp

            def tracked_mkstemp(*args, **kwargs):
                fd, name = real_mkstemp(*args, **kwargs)
                descriptors.append(fd)
                return fd, name

            with patch.object(bench_run.tempfile, "mkstemp", side_effect=tracked_mkstemp):
                with self.assertRaises(FileNotFoundError):
                    bench_run.launch_daemon([str(root / "missing-binary")], root, env)
            self.assertEqual(list((root / "tmp").iterdir()), [])
            for fd in descriptors:
                with self.assertRaises(OSError):
                    os.fstat(fd)


if __name__ == "__main__":
    unittest.main()
