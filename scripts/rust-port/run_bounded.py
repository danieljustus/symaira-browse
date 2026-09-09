#!/usr/bin/env python3
"""Run one CI phase with bounded output and descendant cleanup."""
from __future__ import annotations

import argparse
import os
import signal
import subprocess
import sys
from pathlib import Path


def kill_tree(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    if os.name == "nt":
        subprocess.run(
            ["taskkill", "/PID", str(process.pid), "/T", "/F"],
            check=False,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
    else:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except ProcessLookupError:
            return
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
    try:
        process.wait(timeout=5)
    except subprocess.TimeoutExpired:
        pass


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--timeout", type=float, required=True)
    parser.add_argument("--stdout", type=Path, required=True)
    parser.add_argument("--stderr", type=Path, required=True)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if not args.command:
        parser.error("a command is required")
    if args.command[0] == "--":
        args.command = args.command[1:]
    args.stdout.parent.mkdir(parents=True, exist_ok=True)
    args.stderr.parent.mkdir(parents=True, exist_ok=True)
    print("+", " ".join(args.command), flush=True)
    creationflags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
    with args.stdout.open("wb") as stdout, args.stderr.open("wb") as stderr:
        process = subprocess.Popen(
            args.command,
            stdout=stdout,
            stderr=stderr,
            env={**os.environ, "PYTHONUNBUFFERED": "1"},
            creationflags=creationflags,
            start_new_session=(os.name != "nt"),
        )
        try:
            return process.wait(timeout=args.timeout)
        except subprocess.TimeoutExpired:
            print(
                f"TIMEOUT after {args.timeout:g}s; killing process tree (pid={process.pid})",
                file=sys.stderr,
                flush=True,
            )
            kill_tree(process)
            return 124


if __name__ == "__main__":
    raise SystemExit(main())
