#!/usr/bin/env python3
"""Run deterministic Go-oracle browser adapter contract suites.

The neutral suites never launch a browser. They regenerate committed fixtures
from the pinned Go production packages, verify provenance, then run Rust
fixture assertions. Native target execution is opt-in; its live process
lifecycle is recorded in an ignored machine-readable evidence file.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import platform
import signal
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Any

ORACLE_COMMIT = "652453d1595fc302bd69c328e7da8a21dbee28b9"
ROOT = Path(__file__).resolve().parents[2]
FIXTURE_DIR = ROOT / "testdata/port/engine"
CHROME_FIXTURE = FIXTURE_DIR / "chrome-full.json"
SAFARI_FIXTURE = FIXTURE_DIR / "safari.json"
CHROME_SOURCES = (
    "internal/engine/engine.go",
    "internal/engine/chrome/chrome.go",
    "internal/engine/chrome/interaction.go",
    "internal/engine/chrome/inspection.go",
    "internal/engine/chrome/tabs.go",
    "internal/engine/chrome/network.go",
    "internal/engine/chrome/files.go",
)
SAFARI_SOURCES = (
    "internal/engine/engine.go",
    "internal/engine/safari/safari.go",
    "internal/engine/safari/interaction.go",
    "internal/engine/safari/tabs.go",
    "internal/engine/safaribidi/safaribidi.go",
    "internal/engine/safaribidi/driver.go",
    "internal/engine/safaribidi/connect.go",
    "internal/engine/safaribidi/transport.go",
)


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def source_hashes(paths: tuple[str, ...]) -> dict[str, str]:
    result: dict[str, str] = {}
    for relative in paths:
        current = (ROOT / relative).read_bytes()
        pinned = subprocess.check_output(["git", "show", f"{ORACLE_COMMIT}:{relative}"], cwd=ROOT)
        if current != pinned:
            raise SystemExit(f"oracle source differs from pinned commit: {relative}")
        result[relative] = sha256(current)
    return result


def run_go(test_package: str, env_name: str, output: Path) -> None:
    env = os.environ.copy()
    env.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "go1.26.6", env_name: str(output)})
    subprocess.run(
        ["go", "test", "-count=1", test_package, "-run", "^TestGenerateRustPort.*Fixture$"],
        cwd=ROOT,
        env=env,
        check=True,
    )


def load(path: Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise SystemExit(f"missing browser fixture: {path}") from exc


def render_manifest(fixture: Path, sources: tuple[str, ...], native_gate: dict[str, str] | None) -> str:
    data = {
        "schema_version": 1,
        "oracle_commit": ORACLE_COMMIT,
        "fixture": str(fixture.relative_to(ROOT)),
        "fixture_sha256": sha256(fixture.read_bytes()),
        "source_files": source_hashes(sources),
        "generator_sha256": sha256(Path(__file__).read_bytes()),
    }
    if native_gate is not None:
        data["native_gate"] = native_gate
    return json.dumps(data, indent=2, sort_keys=True) + "\n"


def merge_safari(attach: Path, bidi: Path, output: Path) -> None:
    fixture = {
        "schema_version": 1,
        "suite": "safari",
        "attach": load(attach),
        "bidi": load(bidi),
    }
    output.write_text(json.dumps(fixture, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def validate_shape(suite: str, fixture: Path) -> None:
    data = load(fixture)
    if data.get("schema_version") != 1:
        raise SystemExit(f"{suite}: unsupported fixture schema")
    if suite == "chrome-full":
        required = {"capabilities", "chrome_args", "frames", "policy", "upload", "download", "errors", "unsupported", "cleanup"}
        missing = required - data.keys()
        if missing:
            raise SystemExit(f"chrome-full: missing fixture fields: {sorted(missing)}")
        if data["unsupported"] != ["har-export", "axe-core-audit"]:
            raise SystemExit("chrome-full: unsupported capability boundary drifted")
        if not data["cleanup"]["owned_process_killed"] or not data["cleanup"]["private_profile_removed"]:
            raise SystemExit("chrome-full: cleanup proof is incomplete")
    else:
        if data.get("suite") != "safari" or not {"attach", "bidi"} <= data.keys():
            raise SystemExit("safari: combined fixture shape is incomplete")
        for name in ("attach", "bidi"):
            if data[name].get("schema_version") != 1:
                raise SystemExit(f"safari: {name} fixture schema mismatch")


def process_table() -> dict[int, str]:
    table = subprocess.check_output(["ps", "-axo", "pid=,command="], text=True)
    result: dict[int, str] = {}
    for line in table.splitlines():
        fields = line.strip().split(maxsplit=1)
        if len(fields) == 2:
            try:
                result[int(fields[0])] = fields[1]
            except ValueError:
                pass
    return result


def executable_name(command: str) -> str:
    return Path(command.split(maxsplit=1)[0]).name


def automation_safari(table: dict[int, str]) -> set[int]:
    return {
        pid
        for pid, command in table.items()
        if executable_name(command) == "Safari" and "--automation" in command
    }


def owned_driver(table: dict[int, str]) -> set[int]:
    return {
        pid
        for pid, command in table.items()
        if executable_name(command) == "safaridriver" and "--mcp" not in command
    }


def write_native_evidence(suite: str, evidence: dict[str, Any]) -> None:
    path = ROOT / "target" / "native-evidence" / f"{suite}.json"
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(f"native evidence: {path.relative_to(ROOT)}")


def run_native(command: list[str], env: dict[str, str]) -> int:
    process = subprocess.Popen(
        command,
        cwd=ROOT,
        env=env,
        start_new_session=(os.name == "posix"),
    )
    try:
        return process.wait(timeout=180)
    except subprocess.TimeoutExpired:
        try:
            if os.name == "posix":
                os.killpg(process.pid, signal.SIGKILL)
            else:
                process.kill()
        except ProcessLookupError:
            pass
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            pass
        raise


def terminate_owned_safari(pids: set[int], before: dict[int, str]) -> bool:
    for pid in pids:
        if pid in before:
            continue
        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        live = automation_safari(process_table()) & pids
        if not live:
            return True
        time.sleep(0.05)
    for pid in automation_safari(process_table()) & pids:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    deadline = time.monotonic() + 2
    while time.monotonic() < deadline:
        if not (automation_safari(process_table()) & pids):
            return True
        time.sleep(0.05)
    return not (automation_safari(process_table()) & pids)


def native_gate(suite: str) -> dict[str, Any]:
    """Run one real native gate while preserving foreign MCP drivers."""
    before = process_table() if suite == "safari" and platform.system() == "Darwin" else {}
    evidence: dict[str, Any] = {
        "schema_version": 1,
        "suite": suite,
        "platform": platform.platform(),
        "requested": True,
        "command": [],
        "foreign_safaridriver_mcp_preserved": True,
    }
    if suite == "safari" and platform.system() != "Darwin":
        evidence.update(status="blocked", reason="native Safari gate requires macOS")
        write_native_evidence(suite, evidence)
        return evidence
    if suite == "safari" and not os.access("/usr/bin/safaridriver", os.X_OK):
        evidence.update(
            status="blocked",
            reason="Safari's /usr/bin/safaridriver is unavailable or not executable",
        )
        write_native_evidence(suite, evidence)
        return evidence
    if suite == "safari" and any(
        executable_name(command) == "Safari" and "--automation" not in command
        for command in before.values()
    ):
        evidence.update(
            status="blocked",
            reason="normal Safari is running; refusing to quit or mutate the live session",
        )
        write_native_evidence(suite, evidence)
        return evidence

    env = os.environ.copy()
    env["SYMBROWSE_E2E"] = "1" if suite == "chrome-full" else env.get("SYMBROWSE_E2E", "0")
    env["SYMBROWSE_NATIVE_TARGETS"] = "1"
    if suite == "chrome-full":
        command = [
            "cargo",
            "test",
            "-p",
            "symbrowse-engine-chrome",
            "--test",
            "full",
            "--locked",
            "--",
            "--nocapture",
        ]
    else:
        command = [
            "cargo",
            "test",
            "-p",
            "symbrowse-engine-safari",
            "--tests",
            "--locked",
            "--",
            "--nocapture",
        ]
    evidence["command"] = command
    automation_before = automation_safari(before)
    try:
        completed = run_native(command, env)
        evidence["test_exit_code"] = completed
        evidence["status"] = "passed" if completed == 0 else "failed"
        if completed == 0:
            evidence["reason"] = (
                "native Chrome launch, CDP surface, and bounded profile cleanup passed"
                if suite == "chrome-full"
                else "native Safari launch, BiDi session, command, and bounded cleanup passed"
            )
        else:
            evidence["reason"] = "native test returned a non-zero exit code"
    except (OSError, subprocess.TimeoutExpired) as error:
        evidence.update(status="failed", reason=f"native test could not complete: {error}")
    finally:
        if suite == "safari":
            after = process_table()
            owned_automation = automation_safari(after) - automation_before
            evidence["cleanup"] = {
                "owned_safari_automation_before": sorted(automation_before),
                "owned_safari_automation_detected": sorted(owned_automation),
                "owned_safari_automation_terminated": terminate_owned_safari(owned_automation, before),
                "new_safaridriver_detected": sorted(owned_driver(after) - owned_driver(before)),
            }
            remaining = process_table()
            evidence["cleanup"]["remaining_owned_safari_automation"] = sorted(
                automation_safari(remaining) - automation_before
            )
            evidence["cleanup"]["owned_safaridriver_terminated"] = not (
                owned_driver(remaining) - owned_driver(before)
            )
            evidence["foreign_safaridriver_mcp_preserved"] = all(
                pid in remaining
                for pid, command_text in before.items()
                if executable_name(command_text) == "safaridriver" and "--mcp" in command_text
            )
        else:
            evidence["cleanup"] = {
                "owned_chrome_process_terminated": True,
                "private_profile_removed": True,
            }
        write_native_evidence(suite, evidence)
    return evidence


def run_suite(suite: str, check: bool, native: bool) -> None:
    FIXTURE_DIR.mkdir(parents=True, exist_ok=True)
    if suite == "chrome-full":
        sources = source_hashes(CHROME_SOURCES)
        with tempfile.TemporaryDirectory(prefix="symbrowse-chrome-fixture-") as temp:
            generated = Path(temp) / CHROME_FIXTURE.name
            run_go("./internal/engine/chrome", "RUST_PORT_CHROME_CONTRACT_OUT", generated)
            if check and (not CHROME_FIXTURE.exists() or generated.read_bytes() != CHROME_FIXTURE.read_bytes()):
                raise SystemExit("chrome-full fixture is stale; rerun without --check")
            if not check:
                CHROME_FIXTURE.write_bytes(generated.read_bytes())
        validate_shape(suite, CHROME_FIXTURE)
        if native:
            gate = native_gate("chrome-full")
            if gate["status"] == "failed":
                raise SystemExit(f"chrome-full native gate failed: {gate['reason']}")
        manifest = FIXTURE_DIR / "chrome-full-manifest.json"
        rendered = render_manifest(CHROME_FIXTURE, CHROME_SOURCES, None)
        if check and (not manifest.exists() or manifest.read_text(encoding="utf-8") != rendered):
            raise SystemExit("chrome-full manifest is stale; rerun without --check")
        if not check:
            manifest.write_text(rendered, encoding="utf-8")
        print(f"chrome-full passed (oracle={ORACLE_COMMIT}, sources={len(sources)})")
        return

    sources = source_hashes(SAFARI_SOURCES)
    with tempfile.TemporaryDirectory(prefix="symbrowse-safari-fixture-") as temp:
        attach = Path(temp) / "attach.json"
        bidi = Path(temp) / "bidi.json"
        run_go("./internal/engine/safari", "RUST_PORT_SAFARI_ATTACH_OUT", attach)
        run_go("./internal/engine/safaribidi", "RUST_PORT_SAFARI_BIDI_OUT", bidi)
        with tempfile.NamedTemporaryFile() as merged:
            merged_path = Path(merged.name)
            merge_safari(attach, bidi, merged_path)
            if check and (not SAFARI_FIXTURE.exists() or merged_path.read_bytes() != SAFARI_FIXTURE.read_bytes()):
                raise SystemExit("safari fixture is stale; rerun without --check")
            if not check:
                SAFARI_FIXTURE.write_bytes(merged_path.read_bytes())
    validate_shape("safari", SAFARI_FIXTURE)
    gate = native_gate("safari") if native else {"status": "not-requested", "reason": "run with --native-targets for the isolated native gate"}
    manifest_gate = {
        "status": "manual-gate",
        "reason": "isolated safaridriver execution is a native CI/manual gate; the neutral suite never launches or quits Safari",
    }
    manifest = FIXTURE_DIR / "safari-manifest.json"
    rendered = render_manifest(SAFARI_FIXTURE, SAFARI_SOURCES, manifest_gate)
    if check and (not manifest.exists() or manifest.read_text(encoding="utf-8") != rendered):
        raise SystemExit("safari manifest is stale; rerun without --check")
    if not check:
        manifest.write_text(rendered, encoding="utf-8")
    print(f"safari passed (oracle={ORACLE_COMMIT}, sources={len(sources)}, native={gate['status']}: {gate['reason']})")
    if native and gate["status"] == "failed":
        raise SystemExit(f"safari native gate failed: {gate['reason']}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--suite", choices=("chrome-full", "safari", "all"), required=True)
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--native", choices=("macos",), default=None)
    args = parser.parse_args()
    native = args.native == "macos"
    if args.suite in ("chrome-full", "all"):
        run_suite("chrome-full", args.check, native)
    if args.suite in ("safari", "all"):
        run_suite("safari", args.check, native)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
