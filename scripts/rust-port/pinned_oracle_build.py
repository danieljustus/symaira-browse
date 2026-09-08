#!/usr/bin/env python3
"""Build the frozen Go oracle from a verified detached worktree."""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

_FULL_COMMIT = re.compile(r"^[0-9a-f]{40}$")


def _run(args: list[str], *, cwd: Path, env: dict[str, str] | None = None) -> str:
    result = subprocess.run(
        args, cwd=cwd, env=env, check=False, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode:
        detail = result.stderr.strip() or result.stdout.strip()
        raise RuntimeError(f"{' '.join(args)} failed ({result.returncode}): {detail}")
    return result.stdout.strip()


def _build_info(go: str, binary: Path, *, cwd: Path) -> dict[str, str]:
    text = _run([go, "version", "-m", str(binary)], cwd=cwd)
    info: dict[str, str] = {}
    for line in text.splitlines():
        for key in ("vcs.revision", "vcs.modified"):
            match = re.search(rf"\b{re.escape(key)}(?:=|\s+)([^\s]+)", line)
            if match:
                info[key] = match.group(1)
    return info


def _version(go: str, binary: Path, *, cwd: Path) -> str:
    result = subprocess.run([str(binary), "version", "--json"], cwd=cwd,
                            check=False, text=True, stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(f"version --json failed ({result.returncode}): {result.stderr.strip()}")
    import json
    try:
        payload = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        raise RuntimeError(f"version --json is invalid: {error}") from error
    version = payload.get("version")
    if not isinstance(version, str):
        raise RuntimeError("version --json has no string version")
    return version


def build(*, repo: Path, commit: str, release: str, output: Path,
          go: str, go_version: str) -> None:
    repo = repo.resolve()
    if not _FULL_COMMIT.fullmatch(commit):
        raise RuntimeError("oracle commit must be a full 40-character lowercase SHA")
    resolved = _run(["git", "rev-parse", f"{commit}^{{commit}}"], cwd=repo)
    if resolved != commit:
        raise RuntimeError(f"oracle commit resolved to {resolved}, expected {commit}")
    described = _run(["git", "describe", "--tags", "--abbrev=0", commit], cwd=repo)
    if described != release:
        raise RuntimeError(f"oracle release is {described}, expected {release}")

    with tempfile.TemporaryDirectory(prefix="symaira-oracle-") as temporary:
        root = Path(temporary)
        seed = root / "repository"
        worktree = seed / "source"
        artifact = root / "oracle"
        cache = root / "gocache"
        added = False
        try:
            # A linked worktree has a .git file. Go's build-VCS probe can walk
            # past that file into the active checkout; a local shared seed
            # gives the temporary worktree a self-contained Git root while
            # retaining the worktree semantics and avoiding any downloader.
            _run(["git", "clone", "--shared", "--no-checkout", str(repo), str(seed)], cwd=repo)
            _run(["git", "worktree", "add", "--detach", str(worktree), commit], cwd=seed)
            added = True
            head = _run(["git", "rev-parse", "HEAD"], cwd=worktree)
            if head != commit:
                raise RuntimeError(f"detached worktree HEAD is {head}, expected {commit}")
            status = _run(["git", "status", "--porcelain", "--untracked-files=all"], cwd=worktree)
            if status:
                raise RuntimeError("oracle worktree is not clean")
            env = os.environ.copy()
            env.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": f"go{go_version}",
                        "GOCACHE": str(cache),
                        # Go's VCS probe otherwise walks through a linked
                        # worktree and can select the active checkout.
                        "GIT_DIR": _run(["git", "rev-parse", "--git-dir"], cwd=worktree),
                        "GIT_WORK_TREE": str(worktree)})
            _run([go, "build", "-trimpath", "-ldflags",
                  f"-s -w -X main.version={release}", "-o", str(artifact),
                  "./cmd/symbrowse"], cwd=worktree, env=env)
            info = _build_info(go, artifact, cwd=worktree)
            if info.get("vcs.revision") != commit:
                raise RuntimeError(f"embedded revision is {info.get('vcs.revision')!r}, expected {commit}")
            if info.get("vcs.modified") != "false":
                raise RuntimeError(f"embedded source is modified: {info.get('vcs.modified')!r}")
            if _version(go, artifact, cwd=worktree) != release:
                raise RuntimeError("embedded release does not match expected release")
            output.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(artifact, output)
            final_info = _build_info(go, output, cwd=repo)
            if final_info.get("vcs.revision") != commit or final_info.get("vcs.modified") != "false":
                raise RuntimeError("copied oracle failed provenance verification")
        finally:
            if added:
                _run(["git", "worktree", "remove", "--force", str(worktree)], cwd=seed)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--release", required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--go", default="go")
    parser.add_argument("--go-version", required=True)
    args = parser.parse_args()
    try:
        build(repo=args.repo, commit=args.commit, release=args.release,
              output=args.output, go=args.go, go_version=args.go_version)
    except (OSError, RuntimeError) as error:
        print(f"FAIL pinned Go oracle: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
