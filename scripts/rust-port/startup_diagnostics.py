"""Bounded, redacted diagnostics for pinned daemon startup failures."""
from __future__ import annotations

import os
import re
from pathlib import Path

MAX_STARTUP_DIAGNOSTIC_BYTES = 16 << 10
_SECRET = re.compile(
    r"(?i)(?P<prefix>\b(?:authorization\s*:\s*bearer|(?:token|password|secret|api[_-]?key)\s*[=:])\s*)(?P<value>[^\s,;]+)"
)


def redacted_tail(path: Path, limit: int = MAX_STARTUP_DIAGNOSTIC_BYTES) -> str:
    """Return at most limit bytes of the log tail, with secret values masked."""
    if limit <= 0:
        return ""
    try:
        with path.open("rb") as stream:
            stream.seek(0, 2)
            size = stream.tell()
            stream.seek(max(0, size - limit))
            data = stream.read(limit)
    except OSError as error:
        return f"<unable to read daemon log: {error}>"
    text = data.decode("utf-8", errors="replace")
    if size > limit:
        text = f"<tail truncated to {limit} bytes>\n" + text
    return _SECRET.sub(r"\g<prefix><redacted>", text)


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
