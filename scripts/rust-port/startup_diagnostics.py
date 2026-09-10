"""Bounded, redacted diagnostics for pinned daemon startup failures."""
from __future__ import annotations

import os
import re
from pathlib import Path

MAX_STARTUP_DIAGNOSTIC_BYTES = 16 << 10
_DIAGNOSTIC_OMITTED = "<startup diagnostic omitted>"
_SECRET_MARKER = re.compile(
    r"(?is)(?<![A-Za-z0-9_])(?:authorization|token|password|secret|api[_-]?key)[\"']?\s*[:=]\s*"
)


def _bounded_text(text: str, limit: int) -> str:
    """Keep diagnostic output within limit UTF-8 bytes."""
    encoded = text.encode("utf-8", errors="replace")
    if len(encoded) <= limit:
        return text
    return encoded[:limit].decode("utf-8", errors="ignore")


def redacted_tail(path: Path, limit: int = MAX_STARTUP_DIAGNOSTIC_BYTES) -> str:
    """Return a bounded startup diagnostic, omitting unsafe or oversized logs."""
    if limit <= 0:
        return ""
    try:
        with path.open("rb") as stream:
            data = stream.read(limit + 1)
    except OSError as error:
        return _bounded_text(f"<unable to read daemon log: {error}>", limit)
    if len(data) > limit:
        return _bounded_text(_DIAGNOSTIC_OMITTED, limit)
    text = data.decode("utf-8", errors="replace")
    if _SECRET_MARKER.search(text):
        return _bounded_text(_DIAGNOSTIC_OMITTED, limit)
    return _bounded_text(text, limit)


def preserve_startup_diagnostic(log: Path, *, directory: Path | None = None) -> Path | None:
    """Write a bounded redacted startup log outside the temporary run root."""
    target_dir = directory
    if target_dir is None:
        configured = os.environ.get("SYMBROWSE_DIAGNOSTIC_DIR", "")
        workspace = os.environ.get("GITHUB_WORKSPACE", "")
        if configured:
            target_dir = Path(configured)
        elif workspace:
            target_dir = Path(workspace) / "target" / "ci" / "pinned-daemon"
        else:
            target_dir = Path.cwd() / "target" / "ci" / "pinned-daemon"
    try:
        target_dir.mkdir(parents=True, exist_ok=True)
        target = target_dir / "startup.log"
        target.write_text(redacted_tail(log), encoding="utf-8")
        return target
    except OSError:
        return None


def startup_failure(log: Path, message: str) -> SystemExit:
    """Build a failure that includes diagnostics and preserves an artifact."""
    artifact = preserve_startup_diagnostic(log)
    detail = redacted_tail(log)
    suffix = f"; preserved diagnostic at {artifact}" if artifact else "; diagnostic artifact could not be preserved"
    return SystemExit(f"{message}: {detail}{suffix}")
