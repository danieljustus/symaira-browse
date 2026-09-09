#!/usr/bin/env python3
"""Discover and run cargo integration tests one at a time with bounded logs."""
from __future__ import annotations

import argparse
import os
import subprocess
import sys
from pathlib import Path

from run_bounded import kill_tree


def cargo_command(args: list[str], *, list_tests: bool = False) -> list[str]:
    command = ["cargo", "test", *args]
    if list_tests:
        command.extend(["--", "--list"])
    return command


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--timeout", type=float, required=True)
    parser.add_argument("--log-dir", type=Path, required=True)
    parser.add_argument("cargo_args", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.cargo_args and args.cargo_args[0] == "--":
        args.cargo_args = args.cargo_args[1:]
    if not args.cargo_args:
        parser.error("cargo test arguments are required")
    args.log_dir.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "PYTHONUNBUFFERED": "1"}

    discovery = subprocess.run(
        cargo_command(args.cargo_args, list_tests=True),
        check=False,
        text=True,
        capture_output=True,
        env=env,
    )
    (args.log_dir / "list.stdout").write_text(discovery.stdout, encoding="utf-8")
    (args.log_dir / "list.stderr").write_text(discovery.stderr, encoding="utf-8")
    if discovery.returncode:
        print(discovery.stderr, file=sys.stderr, end="")
        return discovery.returncode
    names = [
        line.split(":", 1)[0].strip()
        for line in discovery.stdout.splitlines()
        if line.rstrip().endswith(": test")
    ]
    if not names:
        print("no tests discovered", file=sys.stderr)
        return 2
    print(f"discovered {len(names)} tests", flush=True)
    for index, name in enumerate(names, start=1):
        safe = "".join(char if char.isalnum() or char in "._-" else "_" for char in name)
        stdout_path = args.log_dir / f"{index:03d}-{safe}.stdout"
        stderr_path = args.log_dir / f"{index:03d}-{safe}.stderr"
        command = cargo_command(args.cargo_args) + ["--", "--exact", name, "--nocapture", "--test-threads=1"]
        print(f"[{index}/{len(names)}] {name}", flush=True)
        stdout_handle = stdout_path.open("wb")
        stderr_handle = stderr_path.open("wb")
        process = subprocess.Popen(
            command,
            stdout=stdout_handle,
            stderr=stderr_handle,
            env=env,
            creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
            start_new_session=(os.name != "nt"),
        )
        try:
            result = process.wait(timeout=args.timeout)
        except subprocess.TimeoutExpired:
            print(f"TIMEOUT test={name} after {args.timeout:g}s", file=sys.stderr, flush=True)
            kill_tree(process)
            return 124
        finally:
            # The handles are owned by this process and must be closed after
            # wait so Windows can upload the diagnostics directory.
            stdout_handle.close()
            stderr_handle.close()
        if result:
            print(f"FAILURE test={name} exit={result}", file=sys.stderr, flush=True)
            return result
    print("all serial platform tests passed", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
