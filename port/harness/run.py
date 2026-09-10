#!/usr/bin/env python3
"""Run bounded, hermetic Go-oracle migration suites."""
from __future__ import annotations

import argparse
import json
import os
import signal
import socket
import stat
import subprocess
import sys
import tempfile
import time
from pathlib import Path

MAX_FRAME_BYTES = 1 << 20


def run(command: list[str], root: Path, env: dict[str, str], *, timeout: int = 600) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, cwd=root, env=env, check=True, timeout=timeout)


def wait_for_path(path: Path, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if path.exists():
            return
        time.sleep(0.02)
    raise AssertionError(f"timed out waiting for {path}")


def kill_tree(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        if os.name == "posix":
            os.killpg(process.pid, signal.SIGTERM)
        else:
            process.terminate()
        process.wait(timeout=2)
    except (OSError, subprocess.TimeoutExpired):
        try:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
        except OSError:
            pass
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass


def daemon_socket_path(runtime: Path, session: str) -> Path:
    if os.name == "nt":
        return Path(r"\\.\pipe") / f"symbrowse-{session}"
    if sys.platform == "darwin":
        return runtime / f"{session}.sock"
    return runtime / "symbrowse" / f"{session}.sock"


def start_daemon(binary: Path, env: dict[str, str], session: str) -> subprocess.Popen[bytes]:
    return subprocess.Popen(
        [str(binary), "daemon", "--session", session],
        cwd=binary.parent.parent.parent,
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        start_new_session=(os.name == "posix"),
    )


def request(socket_path: Path, frame: dict[str, object], *, timeout: float = 3.0) -> dict[str, object]:
    payload = json.dumps(frame, separators=(",", ":")).encode() + b"\n"
    if len(payload) >= MAX_FRAME_BYTES:
        raise ValueError("harness request must stay below the daemon frame limit")
    if os.name == "nt":
        # Python exposes named pipes as byte streams on Windows. Opening the
        # endpoint for each request mirrors the Rust client's one-request
        # connection lifecycle and needs no third-party package.
        with open(socket_path, "r+b", buffering=0) as connection:
            connection.write(payload)
            response = bytearray()
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline and len(response) <= MAX_FRAME_BYTES:
                chunk = connection.read(min(65536, MAX_FRAME_BYTES + 1 - len(response)))
                if not chunk:
                    break
                response.extend(chunk)
                if b"\n" in response:
                    return json.loads(bytes(response).split(b"\n", 1)[0])
        raise AssertionError("daemon closed without a JSON response")
    with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
        connection.settimeout(timeout)
        connection.connect(str(socket_path))
        connection.sendall(payload)
        response = bytearray()
        while len(response) <= MAX_FRAME_BYTES:
            chunk = connection.recv(min(65536, MAX_FRAME_BYTES + 1 - len(response)))
            if not chunk:
                break
            response.extend(chunk)
            if b"\n" in response:
                return json.loads(bytes(response).split(b"\n", 1)[0])
    raise AssertionError("daemon closed without a JSON response")


def wait_for_request(
    socket_path: Path,
    frame: dict[str, object],
    *,
    timeout: float = 5.0,
) -> dict[str, object]:
    deadline = time.monotonic() + timeout
    last_error: OSError | None = None
    while time.monotonic() < deadline:
        try:
            return request(socket_path, frame)
        except (ConnectionRefusedError, FileNotFoundError, TimeoutError, socket.timeout) as error:
            last_error = error
            time.sleep(0.02)
    raise AssertionError(f"timed out waiting for daemon response: {last_error}")


def assert_clean_process(process: subprocess.Popen[bytes], *, timeout: float = 5.0) -> None:
    stdout, stderr = process.communicate(timeout=timeout)
    if stdout:
        raise AssertionError(f"daemon wrote to stdout: {stdout[:200]!r}")
    if len(stderr) > 65536:
        raise AssertionError("daemon stderr exceeded harness output bound")
    lowered = stderr.lower()
    if b"password=" in lowered or b"token=" in lowered:
        raise AssertionError("daemon stderr leaked a secret-like value")


def lifecycle_once(binary: Path, env: dict[str, str], runtime: Path, *, suffix: str) -> None:
    session = f"contract-{suffix}"
    socket_path = daemon_socket_path(runtime, session)
    process = start_daemon(binary, env, session)
    try:
        if os.name == "nt":
            # A named pipe is not visible through Path.exists().
            wait_for_request(socket_path, {"cmd": "daemon.ping", "session": session})
        else:
            wait_for_path(socket_path)
        if os.name == "nt":
            # Named pipes have no filesystem mode bits; access is enforced by
            # the owner-only DACL installed by the Rust listener.
            pass
        else:
            mode = stat.S_IMODE(socket_path.stat().st_mode)
            if mode != 0o600:
                raise AssertionError(f"socket mode is {mode:o}, expected 600")
        status = request(socket_path, {"cmd": "daemon.status", "session": session})
        status_data = status.get("data")
        if not status.get("success") or not isinstance(status_data, dict) or not status_data.get("running"):
            raise AssertionError(f"unexpected daemon.status response: {status}")
        listed = request(socket_path, {"cmd": "session.list", "session": session})
        list_data = listed.get("data")
        sessions = list_data.get("sessions", []) if isinstance(list_data, dict) else []
        if not any(isinstance(item, dict) and item.get("name") == session for item in sessions):
            raise AssertionError(f"session registry omitted {session}: {listed}")
        if os.name != "nt":
            oversized = b"{" + b"x" * MAX_FRAME_BYTES + b"}\n"
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as connection:
                connection.settimeout(2)
                connection.connect(str(socket_path))
                try:
                    connection.sendall(oversized)
                except BrokenPipeError:
                    pass
                else:
                    if connection.recv(1):
                        raise AssertionError("oversized daemon frame received a response")
        stop = request(socket_path, {"cmd": "daemon.stop", "session": session})
        if not stop.get("success"):
            raise AssertionError(f"unexpected daemon.stop response: {stop}")
        process.wait(timeout=5)
        if socket_path.exists():
            raise AssertionError("daemon socket survived clean shutdown")
    finally:
        kill_tree(process)
        if os.name != "nt" and socket_path.exists():
            raise AssertionError("daemon socket survived harness cleanup")
    assert_clean_process(process)


def stale_socket_once(binary: Path, env: dict[str, str], runtime: Path, *, suffix: str) -> None:
    if os.name == "nt":
        # Named-pipe instances disappear with their owning process; the
        # crash-safe mutex is the stale-endpoint recovery mechanism.
        return
    session = f"stale-{suffix}"
    socket_dir = runtime if sys.platform == "darwin" else runtime / "symbrowse"
    socket_dir.mkdir(mode=0o700, exist_ok=True)
    socket_path = socket_dir / f"{session}.sock"
    stale = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    stale.bind(str(socket_path))
    stale.close()
    process = start_daemon(binary, env, session)
    try:
        stop = wait_for_request(
            socket_path,
            {"cmd": "daemon.stop", "session": session},
        )
        if not stop.get("success"):
            raise AssertionError(f"unexpected daemon.stop response: {stop}")
        process.wait(timeout=5)
    finally:
        kill_tree(process)
    assert_clean_process(process)
    if os.name != "nt" and socket_path.exists():
        raise AssertionError("stale-socket recovery left the socket behind")


def race_once(binary: Path, env: dict[str, str], runtime: Path, *, suffix: str, starters: int) -> None:
    session = f"race-{suffix}"
    socket_path = daemon_socket_path(runtime, session)
    processes = [start_daemon(binary, env, session) for _ in range(starters)]
    try:
        if os.name == "nt":
            wait_for_request(socket_path, {"cmd": "daemon.ping", "session": session}, timeout=10.0)
        else:
            wait_for_path(socket_path, timeout=10.0)
        status = request(socket_path, {"cmd": "daemon.status", "session": session})
        if not status.get("success"):
            raise AssertionError(f"race daemon did not become queryable: {status}")
        request(socket_path, {"cmd": "daemon.stop", "session": session})
        for process in processes:
            process.wait(timeout=10)
        for process in processes:
            assert_clean_process(process)
    finally:
        for process in processes:
            kill_tree(process)
    if os.name != "nt" and socket_path.exists():
        raise AssertionError("race cleanup left a live socket")


def daemon_suite(root: Path, env: dict[str, str], *, rounds: int, starters: int) -> None:
    binary_value = env.get("SYMBROWSE_RUST_BINARY")
    if binary_value:
        binary = Path(binary_value)
        if not binary.is_file():
            raise AssertionError(f"installed Rust daemon binary does not exist: {binary}")
    else:
        run(["cargo", "build", "-p", "symbrowse-cli", "--locked"], root, env)
        binary = root / "target" / "debug" / ("symbrowse.exe" if os.name == "nt" else "symbrowse")
    # macOS limits Unix-domain socket paths to 104 bytes. GitHub's TMPDIR is
    # nested under /var/folders/... and leaves too little room for the socket.
    with tempfile.TemporaryDirectory(
        prefix="sb-", dir="/tmp" if sys.platform == "darwin" else None
    ) as directory:
        base = Path(directory)
        data = base / "data"
        home = base / "home"
        for path in (data, home):
            path.mkdir(mode=0o700)
        runtime = (
            home / "Library" / "Caches" / "symbrowse" / "run"
            if sys.platform == "darwin"
            else base / "runtime"
        )
        runtime.mkdir(parents=True, mode=0o700)
        scoped = dict(
            env,
            HOME=str(home),
            XDG_RUNTIME_DIR=str(runtime),
            SYMBROWSE_USER_DATA_DIR=str(data),
            SYMBROWSE_NO_AUTOSTART="1",
        )
        lifecycle_once(binary, scoped, runtime, suffix="one")
        stale_socket_once(binary, scoped, runtime, suffix="one")
        for index in range(rounds):
            race_once(binary, scoped, runtime, suffix=str(index), starters=starters)
    print(f"daemon suite passed ({rounds} rounds x {starters} starters)", flush=True)


FETCH_CONTROL_CASE_IDS = (
    "FETCH-001-profile-selection",
    "FETCH-003-http-semantics",
    "FETCH-004-redirect-proxy-cookie",
    "FETCH-005-robots-retry-rate-limit",
    "FETCH-009-static-selection",
    "FETCH-009-browser-unavailable",
    "FETCH-009-compat-unavailable",
    "FETCH-010-static-honesty",
)


ALL_SUITES = (
    "engine-neutral",
    "fetch-control",
    "fetch-render",
    "workflows",
    "daemon",
    "chrome-full",
    "safari",
    "browser-transport",
    "compat-sidecar",
)


def run_all_suites(root: Path, env: dict[str, str], args: argparse.Namespace) -> None:
    for suite in ALL_SUITES:
        command = [sys.executable, str(Path(__file__).resolve()), "--suite", suite]
        if args.comparison != "bytes" and suite == "fetch-render":
            command.extend(["--comparison", args.comparison])
        if args.native_targets and suite in {"chrome-full", "safari"}:
            command.extend(["--native", "macos"])
        run(command, root, env, timeout=1800)
    if args.native_targets:
        run(
            ["cargo", "test", "--workspace", "--all-targets", "--locked"],
            root,
            env,
            timeout=1800,
        )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--suite", required=True)
    parser.add_argument("--repeat", type=int, default=50)
    parser.add_argument("--comparison", choices=("bytes", "json-semantic", "filesystem"), default="bytes")
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--native", choices=("macos",), default=None)
    parser.add_argument(
        "--native-targets",
        action="store_true",
        help="run real installed-browser gates and write machine-readable evidence",
    )
    args = parser.parse_args()
    if args.native and args.native_targets:
        parser.error("use either --native macos or --native-targets, not both")
    native = args.native_targets or args.native == "macos"
    if args.repeat < 1 or args.repeat > 1000:
        parser.error("--repeat must be between 1 and 1000")
    root = Path(__file__).resolve().parents[2]
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "go1.26.6"})
    if args.suite == "all":
        run_all_suites(root, env, args)
        print("all harness suites passed")
        return 0

    if args.suite == "engine-neutral":
        commands = [
            [sys.executable, "scripts/rust-port/engine_fixture_gen.py", "--check"],
            ["cargo", "nextest", "run", "-p", "symbrowse-engine", "--locked"],
            ["cargo", "test", "-p", "symbrowse-engine", "--doc", "--locked"],
        ]
    elif args.suite == "fetch-control":
        commands = [
            ["go", "run", "./scripts/rust-port/fetch_fixture_gen.go", "--check"],
            ["go", "run", "-tags", "rustport", "./scripts/rust-port/cmd/fetchcontrolgen", "--check"],
            ["go", "test", "./internal/fetch/..."],
            ["cargo", "test", "-p", "symbrowse-fetch", "--all-targets", "--locked"],
        ]
    elif args.suite == "fetch-render":
        if args.comparison != "bytes":
            parser.error("fetch-render only supports --comparison bytes")
        commands = [
            ["go", "run", "./scripts/rust-port/fetch_fixture_gen.go", "--check"],
            ["go", "test", "./internal/fetch/dom/...", "./internal/fetch/render/...", "./internal/fetch/relevance/...", "./internal/fetch/semantic/..."],
            ["cargo", "test", "-p", "symbrowse-fetch", "--test", "render_corpus", "--test", "static_controls", "--test", "pipeline_controls", "--locked"],
        ]
    elif args.suite == "browser-transport":
        fixture = json.loads((root / "testdata/port/core/transport-selection.json").read_text())
        assert fixture["schema_version"] == 1 and len(fixture["cases"]) == 8
        assert {case["id"] for case in fixture["cases"]} == {
            "static-default", "browser-chrome", "browser-safari", "browser-firefox",
            "browser-missing-engine", "static-engine-conflict", "unknown-mode", "unknown-engine",
        }
        commands = [["cargo", "test", "-p", "symbrowse-core", "selection_is_exhaustive", "--locked"]]
    elif args.suite == "compat-sidecar":
        fixture = json.loads((root / "port/harness/cases/compat-sidecar.json").read_text())
        assert fixture["schema_version"] == 1 and len(fixture["cases"]) == 8
        commands = [
            ["go", "build", "-trimpath", "-o", str(root / "dist/symbrowse-compat"), "./cmd/symbrowse"],
            ["cargo", "test", "-p", "symbrowse-compat", "-p", "symbrowse-daemon", "--locked"],
        ]
    elif args.suite == "workflows":
        commands = [
            ["go", "run", "./scripts/rust-port/cmd/workflowgen", "--check"],
            ["cargo", "test", "-p", "symbrowse-core", "--test", "workflows_contract", "--locked"],
        ]
    elif args.suite in {"chrome-full", "safari"}:
        suites = (args.suite,)
        for suite in suites:
            fixture = [
                sys.executable,
                "scripts/rust-port/browser_fixture_gen.py",
                "--suite",
                suite,
                "--check",
            ]
            if native:
                fixture.extend(["--native", "macos"])
            run(fixture, root, env)
            package = "symbrowse-engine-chrome" if suite == "chrome-full" else "symbrowse-engine-safari"
            run(["cargo", "test", "-p", package, "--test", "contract_fixture", "--locked"], root, env)
        print(f"{args.suite} suite passed")
        return 0
    elif args.suite == "daemon":
        daemon_suite(root, env, rounds=1, starters=1)
        return 0
    elif args.suite == "daemon-races":
        daemon_suite(root, env, rounds=args.repeat, starters=50)
        return 0
    else:
        parser.error(f"unsupported suite: {args.suite}")

    for command in commands:
        run(command, root, env)
    if args.suite == "fetch-control":
        print("executed fetch-control case IDs: " + ",".join(FETCH_CONTROL_CASE_IDS), flush=True)
    print(f"{args.suite} suite passed ({args.comparison})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
