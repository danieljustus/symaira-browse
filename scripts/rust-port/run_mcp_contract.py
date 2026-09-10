#!/usr/bin/env python3
"""Run the Rust MCP raw-frame contract against the pinned Go daemon."""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from run_bounded import kill_tree  # noqa: E402
from startup_diagnostics import startup_failure  # noqa: E402


def endpoint(env: dict[str, str], session: str) -> Path:
    if sys.platform == "darwin":
        return Path(env["HOME"]) / "Library" / "Caches" / "symbrowse" / "run" / f"{session}.sock"
    return Path(env["XDG_RUNTIME_DIR"]) / "symbrowse" / f"{session}.sock"


def prepare_endpoint_parent(path: Path) -> None:
    """Create the daemon endpoint directory before starting old Go oracles."""
    path.parent.mkdir(parents=True, exist_ok=True)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--oracle", type=Path, required=True)
    parser.add_argument("--session", default="default")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if not args.command or args.command[0] != "--":
        parser.error("a command after -- is required")
    command = args.command[1:]
    oracle = args.oracle.resolve()
    if not oracle.is_file():
        raise SystemExit(f"pinned Go oracle not found: {oracle}")

    with tempfile.TemporaryDirectory(prefix="m-", dir="/tmp" if sys.platform == "darwin" else None) as root_name:
        root = Path(root_name)
        runtime = root / "runtime"
        home = root / "home"
        data = root / "data"
        for path in (runtime, home, data):
            path.mkdir(mode=0o700)
        env = {"PATH": os.environ.get("PATH", ""), "HOME": str(home),
               "USERPROFILE": str(home), "XDG_RUNTIME_DIR": str(runtime),
               "XDG_CONFIG_HOME": str(home / ".config"),
               "XDG_CACHE_HOME": str(home / ".cache"),
               "XDG_STATE_HOME": str(home / ".local" / "state"),
               "SYMBROWSE_RUNTIME_DIR": str(runtime),
               "SYMBROWSE_USER_DATA_DIR": str(data),
               "SYMBROWSE_CONFIG_DIR": str(home / ".config" / "symbrowse"),
               "SYMBROWSE_CACHE_DIR": str(home / ".cache" / "symbrowse"),
               "SYMBROWSE_STATE_DIR": str(home / ".local" / "state" / "symbrowse"),
               "SYMBROWSE_NO_AUTOSTART": "1", "LANG": "C", "LC_ALL": "C", "TZ": "UTC"}
        for value in (env["XDG_CONFIG_HOME"], env["XDG_CACHE_HOME"], env["XDG_STATE_HOME"]):
            Path(value).mkdir(parents=True, exist_ok=True)
        socket = endpoint(env, args.session)
        prepare_endpoint_parent(socket)
        log = root / "daemon.log"
        process = subprocess.Popen(
            [str(oracle), "daemon", "--session", args.session, "--engine", "static", "--ssrf", "--mcp-mode"],
            cwd=oracle.parent.parent.parent, env=env, stdin=subprocess.DEVNULL,
            stdout=log.open("wb"), stderr=subprocess.STDOUT,
            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
            start_new_session=os.name != "nt")
        try:
            deadline = time.monotonic() + 5
            while time.monotonic() < deadline and not socket.exists():
                if process.poll() is not None:
                    raise startup_failure(
                        log, f"pinned Go daemon exited during startup ({process.returncode})"
                    )
                time.sleep(0.02)
            if not socket.exists():
                raise startup_failure(log, f"pinned Go daemon did not become ready: {socket}")
            env["SYMBROWSE_MCP_DAEMON_ENDPOINT"] = str(socket)
            result = subprocess.run(command, env=env, cwd=Path.cwd())
            return result.returncode
        finally:
            if process.poll() is None:
                kill_tree(process)
            try:
                process.wait(timeout=1)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


if __name__ == "__main__":
    raise SystemExit(main())
