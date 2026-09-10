#!/usr/bin/env python3
"""Generate MCP raw-frame fixtures and the Rust registry from the Go oracle."""
from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import time
from collections.abc import Sequence
from pathlib import Path

MAX_ORACLE_OUTPUT_BYTES = 8 << 20
ORACLE_TIMEOUT_SECONDS = 30
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from run_bounded import kill_tree  # noqa: E402

ORACLE_COMMIT = "652453d1595fc302bd69c328e7da8a21dbee28b9"
SOURCE_FILES = (
    "cmd/symbrowse/mcp.go",
    "internal/mcp/profiles.go",
    "internal/mcp/server.go",
    "internal/mcp/tools.go",
)
INITIALIZE = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "fixture", "version": "0"},
    },
}

def compact(value: object) -> bytes:
    return (json.dumps(value, separators=(",", ":"), ensure_ascii=False) + "\n").encode()


def framed(value: object) -> bytes:
    body = compact(value).rstrip(b"\n")
    return b"Content-Length: " + str(len(body)).encode() + b"\r\n\r\n" + body


def oracle_environment() -> tuple[tempfile.TemporaryDirectory[str], dict[str, str]]:
    temporary = tempfile.TemporaryDirectory(prefix="o-", dir="/tmp" if sys.platform == "darwin" else None)
    base = Path(temporary.name)
    runtime = base / "runtime"
    data = base / "data"
    home = base / "home"
    for path in (runtime, data, home):
        path.mkdir(mode=0o700)
    # Start from an explicit allowlist. In particular, do not inherit runner
    # profiles, credential paths, XDG roots, or SYMBROWSE configuration.
    inherited = {"PATH": os.environ.get("PATH", "")}
    environment = {key: value for key, value in inherited.items() if value}
    environment.update({
        "HOME": str(home),
        "USERPROFILE": str(home),
        "LOCALAPPDATA": str(data / "localappdata"),
        "APPDATA": str(data / "appdata"),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"),
        "XDG_CACHE_HOME": str(home / ".cache"),
        "XDG_STATE_HOME": str(home / ".local" / "state"),
        "XDG_RUNTIME_DIR": str(runtime),
        "TMPDIR": str(base / "tmp"),
        "TMP": str(base / "tmp"),
        "TEMP": str(base / "tmp"),
        "SYMBROWSE_RUNTIME_DIR": str(runtime),
        "SYMBROWSE_USER_DATA_DIR": str(data),
        "SYMBROWSE_CONFIG_DIR": str(home / ".config" / "symbrowse"),
        "SYMBROWSE_CACHE_DIR": str(home / ".cache" / "symbrowse"),
        "SYMBROWSE_STATE_DIR": str(home / ".local" / "state" / "symbrowse"),
        "SYMBROWSE_NO_AUTOSTART": "0",
        "LANG": "C",
        "LC_ALL": "C",
        "TZ": "UTC",
    })
    for path in (environment["LOCALAPPDATA"], environment["APPDATA"],
                 environment["XDG_CONFIG_HOME"], environment["XDG_DATA_HOME"],
                 environment["XDG_CACHE_HOME"], environment["XDG_STATE_HOME"],
                 environment["TMPDIR"]):
        Path(path).mkdir(parents=True, exist_ok=True)
    return temporary, environment


def oracle_endpoint(environment: dict[str, str], session: str = "default") -> Path:
    # Keep this precedence identical to internal/daemon/socketBaseDir: macOS
    # uses its Library cache, while every other platform prefers XDG_RUNTIME_DIR
    # when present (including Windows). This matters when roots intentionally
    # differ in a CI fixture.
    if sys.platform == "darwin":
        return Path(environment["HOME"]) / "Library" / "Caches" / "symbrowse" / "run" / f"{session}.sock"
    runtime = environment.get("XDG_RUNTIME_DIR", "")
    if runtime:
        return Path(runtime) / "symbrowse" / f"{session}.sock"
    return Path(environment["LOCALAPPDATA"]) / "symbrowse" / "run" / f"{session}.sock"


def run_oracle(command: Sequence[str], *, input_data: bytes, environment: dict[str, str]) -> tuple[bytes, bytes]:
    stdout = tempfile.TemporaryFile()
    stderr = tempfile.TemporaryFile()
    process = subprocess.Popen(
        list(command),
        stdin=subprocess.PIPE,
        stdout=stdout,
        stderr=stderr,
        env=environment,
        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
        start_new_session=os.name != "nt",
    )
    try:
        try:
            process.communicate(input=input_data, timeout=ORACLE_TIMEOUT_SECONDS)
        except subprocess.TimeoutExpired:
            kill_tree(process)
            raise SystemExit(f"pinned Go oracle timed out after {ORACLE_TIMEOUT_SECONDS}s") from None
        if process.returncode:
            stderr.seek(0)
            detail = stderr.read(MAX_ORACLE_OUTPUT_BYTES + 1).decode(errors="replace")
            raise SystemExit(f"pinned Go oracle exited with {process.returncode}: {detail[:MAX_ORACLE_OUTPUT_BYTES]}")
        if stdout.tell() > MAX_ORACLE_OUTPUT_BYTES or stderr.tell() > MAX_ORACLE_OUTPUT_BYTES:
            raise SystemExit(f"pinned Go oracle output exceeded {MAX_ORACLE_OUTPUT_BYTES} bytes")
        stdout.seek(0)
        stderr.seek(0)
        return stdout.read(MAX_ORACLE_OUTPUT_BYTES + 1), stderr.read(MAX_ORACLE_OUTPUT_BYTES + 1)
    finally:
        stdout.close()
        stderr.close()


@contextlib.contextmanager
def run_daemon(oracle: Path, environment: dict[str, str], session: str = "default"):
    endpoint = oracle_endpoint(environment, session)
    stdout = tempfile.TemporaryFile()
    stderr = tempfile.TemporaryFile()
    process = subprocess.Popen(
        [str(oracle), "daemon", "--session", session, "--engine", "static", "--ssrf", "--mcp-mode"],
        cwd=oracle.parent.parent.parent,
        env={**environment, "SYMBROWSE_NO_AUTOSTART": "1"},
        stdin=subprocess.DEVNULL,
        stdout=stdout,
        stderr=stderr,
        creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
        start_new_session=os.name != "nt",
    )
    try:
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and not endpoint.exists():
            if process.poll() is not None:
                stderr.seek(0)
                detail = stderr.read(MAX_ORACLE_OUTPUT_BYTES).decode(errors="replace")
                raise SystemExit(f"pinned Go daemon exited during startup ({process.returncode}): {detail}")
            time.sleep(0.02)
        if not endpoint.exists():
            stderr.seek(0)
            detail = stderr.read(MAX_ORACLE_OUTPUT_BYTES).decode(errors="replace")
            raise SystemExit(f"pinned Go daemon did not create endpoint {endpoint}: {detail}")
        yield process
    finally:
        if process.poll() is None:
            kill_tree(process)
        stdout.close()
        stderr.close()


def run(oracle: Path, frames: Sequence[object], *args: str) -> tuple[bytes, bytes]:
    data = b"".join(compact(frame) for frame in frames)
    temporary, environment = oracle_environment()
    try:
        environment["SYMBROWSE_NO_AUTOSTART"] = "1"
        with run_daemon(oracle, environment):
            return run_oracle(
                [str(oracle), "mcp", "--engine", "static", *args],
                input_data=data,
                environment=environment,
            )
    finally:
        temporary.cleanup()


def write_pair(root: Path, name: str, oracle: Path, frames: Sequence[object], *args: str, check: bool = False) -> dict[str, str]:
    out, err = run(oracle, frames, *args)
    if err:
        raise SystemExit(f"oracle wrote stderr for {name}: {err!r}")
    fixture_dir = root / "testdata" / "port" / "mcp"
    fixture_dir.mkdir(parents=True, exist_ok=True)
    input_data = b"".join(compact(frame) for frame in frames)
    sync_file(fixture_dir / f"{name}.in", input_data, check)
    sync_file(fixture_dir / f"{name}.out", out, check)
    return {"input": f"{name}.in", "output": f"{name}.out"}


def source_hashes(root: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    for path in SOURCE_FILES:
        data = subprocess.check_output(["git", "show", f"{ORACLE_COMMIT}:{path}"], cwd=root)
        result[path] = hashlib.sha256(data).hexdigest()
    return result


def rust_raw(value: str) -> str:
    for hashes in range(3, 12):
        delimiter = "#" * hashes
        if f'"{delimiter}' not in value:
            return f'r{delimiter}"{value}"{delimiter}'
    raise SystemExit("could not choose a Rust raw-string delimiter")


def generate_registry(root: Path, all_output: bytes, check: bool) -> None:
    response = json.loads(all_output)
    tools = response["result"]["tools"]
    registry = root / "crates" / "symbrowse-mcp" / "src" / "generated_registry.rs"
    registry.parent.mkdir(parents=True, exist_ok=True)
    text = "// @generated by scripts/rust-port/mcp_fixture_gen.py; do not edit.\n"
    text += "pub const ALL_TOOLS_JSON: &str = " + rust_raw(json.dumps(tools, separators=(",", ":"), ensure_ascii=False)) + ";\n"
    if check:
        existing = registry.read_text(encoding="utf-8") if registry.exists() else ""
        if existing != text:
            raise SystemExit(f"generated registry is stale: {registry}")
    else:
        registry.write_text(text, encoding="utf-8")


def sync_file(path: Path, content: bytes, check: bool) -> None:
    if check:
        existing = path.read_bytes() if path.exists() else b""
        if existing != content:
            raise SystemExit(f"MCP fixture is stale: {path}")
    else:
        path.write_bytes(content)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--oracle", type=Path, required=True)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    root = args.root.resolve()
    oracle = args.oracle.resolve()
    if not oracle.is_file():
        raise SystemExit(f"oracle binary not found: {oracle}")

    fixture_dir = root / "testdata" / "port" / "mcp"
    fixture_names: dict[str, dict[str, str]] = {}
    fixture_names["initialize"] = write_pair(
        root, "initialize", oracle, [INITIALIZE], check=args.check
    )
    fixture_names["framed_initialize"] = write_pair_raw(
        root, "framed_initialize", oracle, framed(INITIALIZE), check=args.check
    )
    fixture_names["initialized"] = write_pair(
        root,
        "initialized",
        oracle,
        [{"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}}],
        check=args.check,
    )
    list_request = [{"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}}]
    fixture_names["tools_core"] = write_pair(
        root, "tools_core", oracle, list_request, check=args.check
    )
    fixture_names["tools_nav"] = write_pair(
        root, "tools_nav", oracle, list_request, "--tools", "nav", check=args.check
    )
    fixture_names["tools_all"] = write_pair(
        root, "tools_all", oracle, list_request, "--tools", "all", check=args.check
    )
    fixture_names["malformed"] = write_pair_raw(
        root, "malformed", oracle, b"{not-json}\n", check=args.check
    )
    fixture_names["unknown_method"] = write_pair(
        root,
        "unknown_method",
        oracle,
        [{"jsonrpc": "2.0", "id": 3, "method": "bogus", "params": {}}],
        check=args.check,
    )
    fixture_names["unknown_tool"] = write_pair(
        root,
        "unknown_tool",
        oracle,
        [{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "nope", "arguments": {}}}],
        check=args.check,
    )
    fixture_names["missing_argument"] = write_pair(
        root,
        "missing_argument",
        oracle,
        [{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "open", "arguments": {}}}],
        check=args.check,
    )
    fixture_names["tool_error"] = write_pair(
        root,
        "tool_error",
        oracle,
        [{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": {"name": "fetch_url", "arguments": {"url": "http://127.0.0.1:1"}}}],
        check=args.check,
    )
    fixture_names["notifications"] = write_pair(
        root,
        "notifications",
        oracle,
        [
            {"jsonrpc": "2.0", "method": "notifications/initialized", "params": {}},
            {"jsonrpc": "2.0", "method": "notifications/cancelled", "params": {"requestId": 1}},
            {"jsonrpc": "2.0", "method": "bogus", "params": {}},
        ],
        check=args.check,
    )
    fixture_dir.mkdir(parents=True, exist_ok=True)
    sync_file(fixture_dir / "eof.in", b"", args.check)
    sync_file(fixture_dir / "eof.out", b"", args.check)
    fixture_names["eof"] = {"input": "eof.in", "output": "eof.out"}

    all_out, all_err = run(oracle, list_request, "--tools", "all")
    if all_err:
        raise SystemExit(f"oracle wrote stderr for tools_all: {all_err!r}")
    generate_registry(root, all_out, args.check)

    manifest = {
        "schema_version": 1,
        "oracle_commit": ORACLE_COMMIT,
        "oracle_release": "v0.8.0",
        "source_files": source_hashes(root),
        "fixtures": fixture_names,
    }
    manifest_path = fixture_dir / "manifest.json"
    rendered = json.dumps(manifest, indent=2, sort_keys=True) + "\n"
    if args.check:
        expected = manifest_path.read_text(encoding="utf-8") if manifest_path.exists() else ""
        for name, paths in fixture_names.items():
            for key in ("input", "output"):
                if not (fixture_dir / paths[key]).exists():
                    raise SystemExit(f"missing MCP fixture: {paths[key]}")
        if expected != rendered:
            raise SystemExit("MCP fixture manifest is stale; run mcp_fixture_gen.py without --check")
        print("MCP fixtures are current")
        return 0
    manifest_path.write_text(rendered, encoding="utf-8")
    print(f"generated {len(fixture_names)} MCP raw-frame fixture cases")
    return 0


def write_pair_raw(root: Path, name: str, oracle: Path, data: bytes, *args: str, check: bool = False) -> dict[str, str]:
    temporary, environment = oracle_environment()
    try:
        environment["SYMBROWSE_NO_AUTOSTART"] = "1"
        with run_daemon(oracle, environment):
            out, err = run_oracle(
                [str(oracle), "mcp", "--engine", "static", *args],
                input_data=data,
                environment=environment,
            )
    finally:
        temporary.cleanup()
    proc = subprocess.CompletedProcess([], 0, stdout=out, stderr=err)
    if proc.stderr:
        raise SystemExit(f"oracle wrote stderr for {name}: {proc.stderr!r}")
    fixture_dir = root / "testdata" / "port" / "mcp"
    fixture_dir.mkdir(parents=True, exist_ok=True)
    sync_file(fixture_dir / f"{name}.in", data, check)
    sync_file(fixture_dir / f"{name}.out", proc.stdout, check)
    return {"input": f"{name}.in", "output": f"{name}.out"}


if __name__ == "__main__":
    raise SystemExit(main())
