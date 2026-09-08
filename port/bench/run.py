#!/usr/bin/env python3
"""Run small, hermetic Go/Rust release-value probes.

This is deliberately a measurement runner, not a cutover switch.  It covers
CLI, MCP, daemon IPC and a local static-fetch probe when the binary implements
the corresponding surface.  Unsupported or failed workloads remain visible in
the JSON report and never become a passing value gate.
"""
from __future__ import annotations

import argparse
import json
import os
import platform
try:
    import resource
except ImportError:  # pragma: no cover - resource is not available on native Windows
    resource = None  # type: ignore[assignment]
import socket
import statistics
import subprocess
import tempfile
import threading
import time
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any, Sequence

MAX_OUTPUT = 1 << 20
WORKLOADS = ("cli", "mcp", "daemon", "fetch")


@dataclass(frozen=True)
class Probe:
    name: str
    argv: tuple[str, ...]
    stdin: str = ""


class FixtureHandler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:  # noqa: N802 - stdlib callback name
        body = b"<html><head><title>RUST-016</title></head><body><main><p>fixture</p></main></body></html>\n"
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        return


def base_env(root: Path) -> dict[str, str]:
    home = root / "home"
    runtime = root / "runtime"
    cache = root / "cache"
    for path in (home, runtime, cache):
        path.mkdir(mode=0o700, exist_ok=True)
    env = {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"),
        "XDG_CACHE_HOME": str(cache),
        "XDG_STATE_HOME": str(home / ".local" / "state"),
        "XDG_RUNTIME_DIR": str(runtime),
        "TMPDIR": str(root / "tmp"),
        "TMP": str(root / "tmp"),
        "TEMP": str(root / "tmp"),
        "LANG": "C",
        "LC_ALL": "C",
        "TZ": "UTC",
        "TERM": "dumb",
        "NO_COLOR": "1",
        "SYMBROWSE_CHECK_UPDATES": "0",
        "SYMBROWSE_SYMGUARD": "off",
        "SYMBROWSE_ENGINE": "static",
        "SYMBROWSE_ALLOW_PRIVATE": "true",
    }
    (root / "tmp").mkdir(mode=0o700, exist_ok=True)
    for key in ("PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"):
        if key in os.environ:
            env[key] = os.environ[key]
    return env



def child_peak_rss_bytes() -> int | None:
    if resource is None:
        return None
    value = int(resource.getrusage(resource.RUSAGE_CHILDREN).ru_maxrss)
    # macOS reports bytes; Linux and the BSDs report KiB.
    return value if platform.system() == "Darwin" else value * 1024


def run_once(binary: Path, probe: Probe, env: dict[str, str], cwd: Path) -> dict[str, object]:
    started = time.perf_counter_ns()
    rss_before = child_peak_rss_bytes()
    try:
        result = subprocess.run(
            [str(binary), *probe.argv],
            cwd=cwd,
            env=env,
            input=probe.stdin,
            text=True,
            capture_output=True,
            timeout=15,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return {
            "status": "error",
            "reason": "timeout",
            "duration_ns": time.perf_counter_ns() - started,
            "peak_rss_bytes": child_peak_rss_bytes(),
        }
    duration = time.perf_counter_ns() - started
    rss_after = child_peak_rss_bytes()
    peak_rss = None if rss_before is None or rss_after is None else max(0, rss_after - rss_before)
    stdout = result.stdout[:MAX_OUTPUT]
    stderr = result.stderr[:MAX_OUTPUT]
    if len(result.stdout) > MAX_OUTPUT or len(result.stderr) > MAX_OUTPUT:
        return {"status": "error", "reason": "output limit exceeded", "duration_ns": duration, "peak_rss_bytes": peak_rss}
    lowered = (stdout + stderr).lower()
    if b"password=" in lowered.encode() or b"token=" in lowered.encode():
        return {"status": "error", "reason": "secret-like output", "duration_ns": duration, "peak_rss_bytes": peak_rss}
    if result.returncode == 0 and not stdout and not stderr:
        return {
            "status": "unsupported",
            "reason": "binary accepted command without observable output",
            "duration_ns": duration,
            "peak_rss_bytes": peak_rss,
        }
    if result.returncode != 0:
        return {
            "status": "unsupported" if "unknown command" in stderr.lower() or "not implemented" in stderr.lower() else "error",
            "reason": f"exit {result.returncode}",
            "duration_ns": duration,
            "peak_rss_bytes": peak_rss,
            "stdout_sha256": __import__("hashlib").sha256(stdout.encode()).hexdigest(),
            "stderr_sha256": __import__("hashlib").sha256(stderr.encode()).hexdigest(),
        }
    return {
        "status": "pass",
        "duration_ns": duration,
        "peak_rss_bytes": peak_rss,
        "stdout_bytes": len(stdout),
        "stderr_bytes": len(stderr),
    }


def summarize(samples: list[dict[str, object]]) -> dict[str, object]:
    passed = [
        int(duration)
        for item in samples
        if item.get("status") == "pass"
        and isinstance((duration := item.get("duration_ns")), (int, float))
    ]
    statuses = [str(item.get("status")) for item in samples]
    if len(passed) != len(samples):
        return {"status": statuses[0] if statuses else "error", "samples": samples}
    ordered = sorted(passed)
    p95 = ordered[max(0, (len(ordered) * 95 + 99) // 100 - 1)]
    peak_rss = [
        int(value)
        for item in samples
        if item.get("status") == "pass"
        and isinstance((value := item.get("peak_rss_bytes")), (int, float))
        and value > 0
    ]
    summary = {
        "status": "pass",
        "samples": len(passed),
        "median_duration_ns": int(statistics.median(passed)),
        "p95_duration_ns": p95,
        "statuses": statuses,
    }
    if peak_rss:
        summary["median_peak_rss_bytes"] = int(statistics.median(peak_rss))
    return summary


def daemon_probe(binary: Path, env: dict[str, str], root: Path, runs: int) -> dict[str, object]:
    if os.name != "posix":
        return {"status": "unsupported", "reason": "Unix socket probe requires a native Unix host"}
    results: list[dict[str, object]] = []
    session = "rust016"
    socket_paths = [Path(env["XDG_RUNTIME_DIR"]) / "symbrowse" / f"{session}.sock"]
    if platform.system() == "Darwin":
        socket_paths.append(
            Path(env["HOME"])
            / "Library"
            / "Caches"
            / "symbrowse"
            / "run"
            / f"{session}.sock"
        )
    for _ in range(runs):
        process = subprocess.Popen(
            [str(binary), "daemon", "--session", session, "--engine", "static"],
            cwd=root,
            env=env,
            stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            start_new_session=True,
        )
        started = time.perf_counter_ns()
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and not any(path.exists() for path in socket_paths):
                if process.poll() is not None:
                    break
                time.sleep(0.02)
            socket_path = next((path for path in socket_paths if path.exists()), None)
            if socket_path is None:
                results.append({"status": "unsupported" if process.poll() == 0 else "error", "reason": "daemon socket did not appear"})
                continue
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.settimeout(3)
                connection.connect(str(socket_path))
                connection.sendall((json.dumps({"cmd": "daemon.ping", "session": session}) + "\n").encode())
                response = connection.recv(1 << 16)
            if b'"success":true' not in response:
                results.append({"status": "error", "reason": "daemon ping failed"})
            else:
                results.append({"status": "pass", "duration_ns": time.perf_counter_ns() - started})
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.settimeout(3)
                connection.connect(str(socket_path))
                connection.sendall((json.dumps({"cmd": "daemon.stop", "session": session}) + "\n").encode())
                connection.recv(1 << 16)
            process.wait(timeout=5)
        except (OSError, subprocess.TimeoutExpired) as error:
            results.append({"status": "error", "reason": str(error)})
        finally:
            if process.poll() is None:
                process.kill()
                process.wait(timeout=3)
            for socket_path in socket_paths:
                if socket_path.exists():
                    socket_path.unlink()
    return summarize(results)


def fetch_probe(binary: Path, env: dict[str, str], root: Path, runs: int) -> dict[str, object]:
    server = ThreadingHTTPServer(("127.0.0.1", 0), FixtureHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    session = "rust016-fetch"
    socket_paths = [Path(env["XDG_RUNTIME_DIR"]) / "symbrowse" / f"{session}.sock"]
    if platform.system() == "Darwin":
        socket_paths.append(
            Path(env["HOME"])
            / "Library"
            / "Caches"
            / "symbrowse"
            / "run"
            / f"{session}.sock"
        )
    process = subprocess.Popen(
        [str(binary), "daemon", "--session", session, "--engine", "static"],
        cwd=root,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=True,
    )
    try:
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and not any(path.exists() for path in socket_paths):
            if process.poll() is not None:
                break
            time.sleep(0.02)
        socket_path = next((path for path in socket_paths if path.exists()), None)
        if socket_path is None:
            return {"status": "error", "reason": "fetch daemon socket did not appear"}
        samples = []
        for index in range(runs):
            started = time.perf_counter_ns()
            frame = {
                "cmd": "fetch.url",
                "session": session,
                "args": {
                    "url": f"http://127.0.0.1:{server.server_port}/fixture.html?run={index}",
                    "no_cache": True,
                },
            }
            try:
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                    connection.settimeout(15)
                    connection.connect(str(socket_path))
                    connection.sendall((json.dumps(frame) + "\n").encode())
                    response = connection.recv(MAX_OUTPUT + 1)
                duration = time.perf_counter_ns() - started
                if len(response) > MAX_OUTPUT:
                    samples.append({"status": "error", "reason": "output limit exceeded"})
                elif b'"success":true' not in response:
                    samples.append({"status": "error", "reason": "fetch daemon request failed"})
                else:
                    samples.append({"status": "pass", "duration_ns": duration})
            except OSError as error:
                samples.append({"status": "error", "reason": str(error)})
        return summarize(samples)
    finally:
        socket_path = next((path for path in socket_paths if path.exists()), None)
        if socket_path is not None:
            try:
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                    connection.settimeout(3)
                    connection.connect(str(socket_path))
                    connection.sendall(
                        (json.dumps({"cmd": "daemon.stop", "session": session}) + "\n").encode()
                    )
                    connection.recv(1 << 16)
            except OSError:
                pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=3)
        for path in socket_paths:
            if path.exists():
                path.unlink()
        server.shutdown()
        thread.join(timeout=3)
        server.server_close()


def run_binary(binary: Path, selected: set[str], runs: int, root: Path) -> dict[str, object]:
    if not binary.is_file() or not os.access(binary, os.X_OK):
        return {"status": "blocked", "reason": f"missing or non-executable binary: {binary}"}
    env = base_env(root)
    probes = {
        "cli": Probe("cli", ("version", "--json")),
        "mcp": Probe(
            "mcp",
            ("mcp", "--engine", "static"),
            '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"rust016","version":"0"}}}\n'
            '{"jsonrpc":"2.0","id":2,"method":"tools/list"}\n',
        ),
    }
    result: dict[str, object] = {"binary": str(binary), "size_bytes": binary.stat().st_size}
    for name, probe in probes.items():
        if name in selected:
            result[name] = summarize([run_once(binary, probe, env, root) for _ in range(runs)])
    if "daemon" in selected:
        result["daemon"] = daemon_probe(binary, env, root, runs)
    if "fetch" in selected:
        result["fetch"] = fetch_probe(binary, env, root, runs)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", type=Path)
    parser.add_argument("--rust", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--runs", type=int, default=3)
    parser.add_argument("--workload", action="append", choices=WORKLOADS)
    parser.add_argument("--strict", action="store_true", help="fail if either binary lacks a selected workload")
    args = parser.parse_args(argv)
    if args.runs < 1 or args.runs > 100:
        parser.error("--runs must be between 1 and 100")
    selected = set(args.workload or WORKLOADS)
    temp_parent = "/tmp" if platform.system() == "Darwin" else None
    with tempfile.TemporaryDirectory(prefix="rust016-bench-", dir=temp_parent) as raw:
        root = Path(raw)
        report: dict[str, Any] = {
            "schema_version": 1,
            "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "host": {"system": platform.system(), "machine": platform.machine()},
            "runs_per_workload": args.runs,
            "workloads": sorted(selected),
            "binaries": {},
            "gate": "blocked",
            "limitations": [
                "Peak RSS is collected from child-process resource usage where the host exposes it; daemon RSS remains unavailable in this portable runner.",
                "The fetch probe is a local HTTP fixture and does not certify real browser/CDP behavior.",
                "Unsupported candidate surfaces remain a BLOCK for cutover, not a passing result.",
            ],
        }
        for name, path in (("go", args.go), ("rust", args.rust)):
            if path is None:
                report["binaries"][name] = {"status": "blocked", "reason": "binary argument not supplied"}
                continue
            report["binaries"][name] = run_binary(path.resolve(), selected, args.runs, root)
        rust_result = report["binaries"].get("rust")
        if isinstance(rust_result, dict):
            if isinstance(rust_result.get("size_bytes"), int):
                report["candidate_size_bytes"] = rust_result["size_bytes"]
            rss_values = [
                workload["median_peak_rss_bytes"]
                for name, workload in rust_result.items()
                if name in selected
                and isinstance(workload, dict)
                and isinstance(workload.get("median_peak_rss_bytes"), int)
            ]
            if rss_values:
                report["candidate_median_peak_rss_bytes"] = int(statistics.median(rss_values))
        go_result = report["binaries"].get("go")
        if isinstance(go_result, dict) and isinstance(go_result.get("size_bytes"), int):
            report["reference_size_bytes"] = go_result["size_bytes"]
        all_pass = True
        for binary_result in report["binaries"].values():
            if not isinstance(binary_result, dict) or binary_result.get("status") == "blocked":
                all_pass = False
                continue
            for workload in selected:
                if not isinstance(binary_result.get(workload), dict) or binary_result[workload].get("status") != "pass":
                    all_pass = False
        report["gate"] = "pass" if all_pass else "blocked"
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        print(json.dumps(report, indent=2))
        if args.strict and not all_pass:
            return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
