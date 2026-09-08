#!/usr/bin/env python3
"""Fail-closed release and rollback gates for the Go/Rust symbrowse port.

The release workflow remains Go-owned.  This tool verifies a candidate artifact
set without changing the workflow or selecting Rust as the default.

Canonical GoReleaser layout inside each implementation directory::

    dual/{go,rust}/symbrowse_<version>_<os>_<arch>.{tar.gz,zip}
    dual/{go,rust}/checksums.txt
    dual/{go,rust}/<archive>.sbom
    dual/{go,rust}/<archive>.sig
    dual/{go,rust}/<archive>.pem

The archive names and companion names intentionally match the existing
.goreleaser.yml conventions.  The implementation directory is the only
namespace added to avoid Go/Rust filename collisions.
"""
from __future__ import annotations

import argparse
import base64
import hashlib
import io
import json
import os
import platform
import re
import shutil
import stat
import sys
import tarfile
import tempfile
import zipfile
from dataclasses import dataclass
from pathlib import Path
from typing import Mapping, Sequence

IMPLEMENTATIONS = ("go", "rust")
TARGETS = (
    ("darwin", "amd64"),
    ("darwin", "arm64"),
    ("linux", "amd64"),
    ("linux", "arm64"),
    ("windows", "amd64"),
    ("windows", "arm64"),
)
SPDX_VERSION = "SPDX-2.3"
PLATFORM_PROOFS_NAME = "platform-proofs.json"
SIGNATURE_INPUTS_NAME = "signature-inputs.json"
ARCHIVE_RE = re.compile(r"^symbrowse_(?P<version>[^_]+)_(?P<os>darwin|linux|windows)_(?P<arch>amd64|arm64)\.(?P<ext>tar\.gz|zip)$")
SHA256_RE = re.compile(r"^(?P<digest>[0-9a-fA-F]{64})\s+(?P<name>\S+)$")


class GateError(RuntimeError):
    """A release gate failed and must not be bypassed by fallback."""


@dataclass(frozen=True)
class ArchiveSpec:
    implementation: str
    version: str
    os_name: str
    arch: str
    path: Path

    @property
    def archive_name(self) -> str:
        return self.path.name

    @property
    def binary_name(self) -> str:
        return "symbrowse.exe" if self.os_name == "windows" else "symbrowse"

    @property
    def companion_names(self) -> tuple[str, str, str]:
        return (
            f"{self.archive_name}.sbom",
            f"{self.archive_name}.sig",
            f"{self.archive_name}.pem",
        )


def archive_name(version: str, os_name: str, arch: str) -> str:
    version = version.removeprefix("v")
    suffix = "zip" if os_name == "windows" else "tar.gz"
    return f"symbrowse_{version}_{os_name}_{arch}.{suffix}"


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _safe_member_name(name: str) -> bool:
    path = Path(name)
    return not path.is_absolute() and ".." not in path.parts


def _read_archive_members(path: Path) -> tuple[set[str], dict[str, int]]:
    """Return normalized member names and tar executable modes."""
    if path.name.endswith(".tar.gz"):
        try:
            with tarfile.open(path, "r:gz") as archive:
                members = archive.getmembers()
                if any(not _safe_member_name(member.name) for member in members):
                    raise GateError(f"archive path traversal in {path.name}")
                names = {Path(member.name).name for member in members if member.isfile()}
                modes = {Path(member.name).name: member.mode for member in members if member.isfile()}
                return names, modes
        except (tarfile.TarError, OSError) as error:
            raise GateError(f"cannot read {path.name}: {error}") from error
    if path.suffix == ".zip":
        try:
            with zipfile.ZipFile(path) as archive:
                members = archive.infolist()
                if any(not _safe_member_name(member.filename) for member in members):
                    raise GateError(f"archive path traversal in {path.name}")
                return {Path(member.filename).name for member in members if not member.is_dir()}, {}
        except (zipfile.BadZipFile, OSError) as error:
            raise GateError(f"cannot read {path.name}: {error}") from error
    raise GateError(f"unsupported archive extension: {path.name}")


def _read_checksum_manifest(path: Path) -> dict[str, str]:
    if not path.is_file():
        raise GateError(f"missing checksum manifest: {path}")
    result: dict[str, str] = {}
    for line_number, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line:
            continue
        match = SHA256_RE.fullmatch(line)
        if not match:
            raise GateError(f"invalid checksum line {path}:{line_number}")
        name = match.group("name")
        if name in result:
            raise GateError(f"duplicate checksum entry for {name} in {path}")
        result[name] = match.group("digest").lower()
    return result


def _verify_spdx(path: Path) -> None:
    if not path.is_file() or path.stat().st_size == 0:
        raise GateError(f"missing or empty SPDX SBOM: {path}")
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise GateError(f"invalid SPDX SBOM {path}: {error}") from error
    if not isinstance(document, dict) or document.get("spdxVersion") != SPDX_VERSION:
        raise GateError(f"{path.name} is not an SPDX 2.3 document")
    if not document.get("SPDXID") or not document.get("creationInfo"):
        raise GateError(f"{path.name} is missing SPDX identity or creationInfo")
    packages = document.get("packages")
    if not isinstance(packages, list) or not packages:
        raise GateError(f"{path.name} has no SPDX package entries")


def _verify_signature_names(directory: Path, archive: ArchiveSpec) -> None:
    sbom, signature, certificate = (directory / name for name in archive.companion_names)
    _verify_spdx(sbom)
    if not signature.is_file() or not signature.read_bytes().strip():
        raise GateError(f"missing or empty signature: {signature}")
    if b"BLOCKED" in signature.read_bytes():
        raise GateError(f"placeholder signature is not accepted: {signature}")
    if not certificate.is_file() or certificate.stat().st_size == 0:
        raise GateError(f"missing or empty certificate: {certificate}")
    certificate_text = certificate.read_text(encoding="utf-8", errors="replace").strip()
    if "-----BEGIN CERTIFICATE-----" in certificate_text and "-----END CERTIFICATE-----" in certificate_text:
        return
    try:
        decoded = base64.b64decode(certificate_text, validate=True).decode("ascii")
    except (ValueError, UnicodeDecodeError):
        decoded = ""
    if "-----BEGIN CERTIFICATE-----" not in decoded or "-----END CERTIFICATE-----" not in decoded:
        raise GateError(f"certificate is not PEM or base64-encoded PEM material: {certificate}")




def _read_json(path: Path, label: str) -> Mapping[str, object]:
    if not path.is_file() or path.stat().st_size == 0:
        raise GateError(f"missing or empty {label}: {path}")
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise GateError(f"invalid {label} {path}: {error}") from error
    if not isinstance(value, dict):
        raise GateError(f"{label} is not a JSON object: {path}")
    return value


def _verify_signature_inputs(directory: Path, implementation: str, archives: Sequence[ArchiveSpec]) -> None:
    payload = _read_json(directory / SIGNATURE_INPUTS_NAME, "signature-input manifest")
    if payload.get("schema_version") != 1 or payload.get("implementation") != implementation:
        raise GateError(f"invalid signature-input manifest for {implementation}")
    if payload.get("signing") != "required" or payload.get("signed") is not True:
        raise GateError(f"{implementation} signature-input manifest does not attest signed artifacts")
    entries = payload.get("artifacts")
    if not isinstance(entries, list):
        raise GateError(f"{implementation} signature-input manifest has no artifacts")
    expected = {
        archive.archive_name: (archive, _sha256(archive.path))
        for archive in archives
    }
    seen: set[str] = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise GateError(f"invalid signature-input entry for {implementation}")
        name = entry.get("artifact")
        if not isinstance(name, str) or name in seen or name not in expected:
            raise GateError(f"signature-input manifest has unexpected or duplicate artifact: {name!r}")
        archive, digest = expected[name]
        if entry.get("sha256") != digest:
            raise GateError(f"signature-input digest mismatch for {name}")
        if entry.get("signature") != f"{name}.sig" or entry.get("certificate") != f"{name}.pem":
            raise GateError(f"signature-input companions mismatch for {name}")
        seen.add(name)
    if seen != set(expected):
        raise GateError(f"signature-input manifest is incomplete for {implementation}")


def _verify_platform_proofs(
    dual: Path, version: str, implementations: Mapping[str, Sequence[ArchiveSpec]]
) -> None:
    payload = _read_json(dual / PLATFORM_PROOFS_NAME, "platform proof manifest")
    if payload.get("schema_version") != 1 or payload.get("version") != version.removeprefix("v"):
        raise GateError("platform proof manifest version/schema mismatch")
    entries = payload.get("proofs")
    if not isinstance(entries, list):
        raise GateError("platform proof manifest has no proofs")
    expected = {
        (implementation, f"{spec.os_name}-{spec.arch}"): spec
        for implementation, specs in implementations.items()
        for spec in specs
    }
    seen: set[tuple[str, str]] = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise GateError("invalid platform proof entry")
        implementation = entry.get("implementation")
        target = entry.get("target")
        if not isinstance(implementation, str) or not isinstance(target, str):
            raise GateError("platform proof implementation/target must be strings")
        key = (implementation, target)
        if key not in expected or key in seen:
            raise GateError(f"unexpected or duplicate platform proof: {key!r}")
        spec = expected[key]
        if entry.get("archive") != spec.archive_name or entry.get("archive_sha256") != _sha256(spec.path):
            raise GateError(f"platform proof does not bind to {spec.archive_name}")
        if entry.get("status") not in {"native_verified", "cross_built"}:
            raise GateError(f"platform proof is not verified for {implementation}/{target}")
        if entry.get("mode") not in {"native", "cross"}:
            raise GateError(f"platform proof has invalid mode for {implementation}/{target}")
        seen.add(key)
    if seen != set(expected):
        raise GateError("platform proof manifest is incomplete")


def _discover_archive(directory: Path, expected: str) -> Path:
    matches = [path for path in directory.iterdir() if path.is_file() and path.name == expected]
    if len(matches) != 1:
        raise GateError(f"expected exactly one archive {expected} in {directory}")
    return matches[0]


def _validate_impl(directory: Path, implementation: str, version: str) -> list[ArchiveSpec]:
    if not directory.is_dir():
        raise GateError(f"missing implementation directory: {directory}")
    expected_names = {archive_name(version, os_name, arch) for os_name, arch in TARGETS}
    archives = [path for path in directory.iterdir() if path.is_file() and (path.name.endswith(".tar.gz") or path.name.endswith(".zip"))]
    actual_names = {path.name for path in archives}
    if actual_names != expected_names:
        missing = sorted(expected_names - actual_names)
        extra = sorted(actual_names - expected_names)
        detail = []
        if missing:
            detail.append(f"missing={missing}")
        if extra:
            detail.append(f"unexpected={extra}")
        raise GateError(f"{implementation} archive matrix mismatch: {', '.join(detail)}")

    manifest = _read_checksum_manifest(directory / "checksums.txt")
    expected_checksum_names = expected_names | {f"{name}.sbom" for name in expected_names}
    if set(manifest) != expected_checksum_names:
        raise GateError(
            f"{implementation} checksum matrix mismatch: expected {sorted(expected_checksum_names)}, got {sorted(manifest)}"
        )

    validated: list[ArchiveSpec] = []
    for os_name, arch in TARGETS:
        path = _discover_archive(directory, archive_name(version, os_name, arch))
        spec = ArchiveSpec(implementation, version.removeprefix("v"), os_name, arch, path)
        members, modes = _read_archive_members(path)
        if spec.binary_name not in members:
            raise GateError(f"{path.name} does not contain the required binary {spec.binary_name}")
        if os_name != "windows" and not (modes.get(spec.binary_name, 0) & stat.S_IXUSR):
            raise GateError(f"{path.name} contains a non-executable {spec.binary_name}")
        if manifest[path.name] != _sha256(path):
            raise GateError(f"checksum mismatch for {path.name}")
        sbom = directory / f"{path.name}.sbom"
        if not sbom.is_file():
            raise GateError(f"missing SPDX SBOM: {sbom}")
        if manifest[sbom.name] != _sha256(sbom):
            raise GateError(f"checksum mismatch for {sbom.name}")
        _verify_signature_names(directory, spec)
        validated.append(spec)
    _verify_signature_inputs(directory, implementation, validated)
    return validated


def verify_candidate(
    candidate: Path, version: str, implementation_names: Sequence[str] = IMPLEMENTATIONS
) -> dict[str, object]:
    """Verify implementation namespaces and return machine-readable evidence."""
    candidate = candidate.resolve()
    dual = candidate / "dual" if (candidate / "dual").is_dir() else candidate
    implementations: dict[str, list[ArchiveSpec]] = {}
    for implementation in implementation_names:
        implementations[implementation] = _validate_impl(dual / implementation, implementation, version)
    selection_contract: object = None
    manifest = dual / "dual-release-manifest.json"
    if len(implementation_names) == len(IMPLEMENTATIONS):
        if not manifest.is_file():
            raise GateError(f"missing dual release manifest: {manifest}")
        try:
            payload = json.loads(manifest.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise GateError(f"invalid dual release manifest: {error}") from error
        validate_selection_contract(payload)
        selection_contract = payload["selection_contract"]
    _verify_platform_proofs(dual, version, implementations)
    return {
        "schema_version": 1,
        "candidate": str(candidate),
        "version": version.removeprefix("v"),
        "implementations": {name: len(items) for name, items in implementations.items()},
        "targets": [f"{os_name}-{arch}" for os_name, arch in TARGETS],
        "signed_companions": True,
        "selection_contract": selection_contract,
    }


def validate_selection_contract(payload: Mapping[str, object]) -> None:
    if payload.get("cutover_enabled", False) is not False:
        raise GateError("Rust cutover must remain disabled in the dual release manifest")
    required = payload.get("required_manifests")
    if required is not None and required != ["checksums.txt", "signature-inputs.json", "platform-proofs.json"]:
        raise GateError("dual release manifest has an incomplete required-manifest contract")
    contract = payload.get("selection_contract")
    if not isinstance(contract, dict):
        raise GateError("dual release manifest is missing selection_contract")
    if contract.get("default") != "go":
        raise GateError("fallback contract must keep Go as the default implementation")
    if contract.get("opt_in") != "rust":
        raise GateError("fallback contract must require explicit Rust opt-in")
    if contract.get("forced_go") != "go":
        raise GateError("forced Go selection must resolve to Go")
    if contract.get("integrity_failure") != "block_no_fallback":
        raise GateError("integrity failures must block without fallback")
    if contract.get("availability_failure") != "fallback_go":
        raise GateError("only classified availability failures may fall back to Go")


def select_implementation(
    requested: str | None,
    *,
    go_available: bool,
    rust_available: bool,
    rust_integrity_ok: bool = True,
) -> str:
    """Resolve the explicit launcher contract; integrity failures never downgrade."""
    requested = requested or "go"
    if requested not in {"go", "rust"}:
        raise GateError("SYMBROWSE_IMPL must be exactly go or rust; implicit auto-selection is forbidden")
    if requested == "go":
        if not go_available:
            raise GateError("forced Go implementation is unavailable")
        return "go"
    if rust_available and rust_integrity_ok:
        return "rust"
    if rust_available and not rust_integrity_ok:
        raise GateError("Rust integrity/signature failure; refusing Go fallback")
    if go_available:
        return "go"
    raise GateError("Rust is unavailable and Go rollback is unavailable")


def _spdx(binary_name: str, archive_name_value: str, implementation: str) -> bytes:
    document = {
        "spdxVersion": SPDX_VERSION,
        "dataLicense": "CC0-1.0",
        "SPDXID": "SPDXRef-DOCUMENT",
        "name": f"symbrowse-{implementation}-{archive_name_value}",
        "documentNamespace": f"https://example.invalid/symbrowse/{implementation}/{archive_name_value}",
        "creationInfo": {"created": "1970-01-01T00:00:00Z", "creators": ["Tool: rust016-dry-run"]},
        "packages": [{"SPDXID": "SPDXRef-Package-symbrowse", "name": binary_name, "versionInfo": "dry-run"}],
    }
    return (json.dumps(document, sort_keys=True, indent=2) + "\n").encode()


def _package_archive(binary: Path, output: Path, implementation: str, version: str) -> Path:
    name = archive_name(version, "darwin", "arm64")
    archive = output / implementation / name
    archive.parent.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "w:gz") as stream:
        info = tarfile.TarInfo("symbrowse")
        data = binary.read_bytes()
        info.size = len(data)
        info.mode = 0o755
        info.mtime = 0
        stream.addfile(info, io.BytesIO(data))
        for filename in ("LICENSE", "README.md", "AGENTS.md"):
            source = Path(filename)
            if source.is_file():
                content = source.read_bytes()
                entry = tarfile.TarInfo(filename)
                entry.size = len(content)
                entry.mode = 0o644
                entry.mtime = 0
                stream.addfile(entry, io.BytesIO(content))
    return archive


def package_dry_run(go_binary: Path, rust_binary: Path, output: Path, version: str) -> dict[str, object]:
    if platform.system() != "Darwin" or platform.machine() not in {"arm64", "aarch64"}:
        raise GateError("darwin-arm64 dry-run packaging requires a darwin/arm64 host")
    for implementation, binary in (("go", go_binary), ("rust", rust_binary)):
        if not binary.is_file() or not os.access(binary, os.X_OK):
            raise GateError(f"{implementation} binary is missing or not executable: {binary}")
    output = output.resolve()
    if output.exists():
        shutil.rmtree(output)
    (output / "dual").mkdir(parents=True)
    archives = []
    signature_inputs: dict[str, list[dict[str, str]]] = {name: [] for name in IMPLEMENTATIONS}
    platform_proofs: list[dict[str, object]] = []
    for implementation, binary in (("go", go_binary), ("rust", rust_binary)):
        archive = _package_archive(binary, output / "dual", implementation, version)
        implementation_dir = output / "dual" / implementation
        sbom_path = implementation_dir / f"{archive.name}.sbom"
        sbom_path.write_bytes(_spdx("symbrowse", archive.name, implementation))
        (implementation_dir / "checksums.txt").write_text(
            f"{_sha256(archive)}  {archive.name}\n{_sha256(sbom_path)}  {sbom_path.name}\n",
            encoding="utf-8",
        )
        signature_inputs[implementation].append(
            {
                "artifact": archive.name,
                "sha256": _sha256(archive),
                "signature": f"{archive.name}.sig",
                "certificate": f"{archive.name}.pem",
            }
        )
        platform_proofs.append(
            {
                "implementation": implementation,
                "target": "darwin-arm64",
                "archive": archive.name,
                "archive_sha256": _sha256(archive),
                "mode": "native",
                "status": "native_verified",
            }
        )
        archives.append({
            "implementation": implementation,
            "archive": str(archive),
            "archive_sha256": _sha256(archive),
            "binary": str(binary),
            "signature": "BLOCKED: signing not performed by local dry-run",
            "certificate": "BLOCKED: certificate not present in local dry-run",
        })
    (output / "dual" / "dual-release-manifest.json").write_text(
        json.dumps(
            {
                "schema_version": 1,
                "selection_contract": selection_manifest(),
                "release_state": "go-default; rust-opt-in",
                "dry_run": True,
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    dual_root = output / "dual"
    for implementation in IMPLEMENTATIONS:
        (dual_root / implementation / SIGNATURE_INPUTS_NAME).write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "implementation": implementation,
                    "signing": "required",
                    "signed": False,
                    "artifacts": signature_inputs[implementation],
                },
                indent=2,
            )
            + "\n",
            encoding="utf-8",
        )
    (dual_root / PLATFORM_PROOFS_NAME).write_text(
        json.dumps(
            {
                "schema_version": 1,
                "version": version.removeprefix("v"),
                "proofs": platform_proofs,
                "runtime": "host-only; cross-target and Windows runtime proof remains external",
            },
            indent=2,
        )
        + "\n",
        encoding="utf-8",
    )
    evidence = {
        "schema_version": 1,
        "host": "darwin-arm64",
        "version": version.removeprefix("v"),
        "layout": "dual/{go,rust}/<goreleaser-archive>",
        "archives": archives,
        "verification": "BLOCKED: local dry-run intentionally omits signatures and certificates",
    }
    (output / "package-evidence.json").write_text(json.dumps(evidence, indent=2) + "\n", encoding="utf-8")
    return evidence


def selection_manifest() -> dict[str, str]:
    return {
        "default": "go",
        "opt_in": "rust",
        "forced_go": "go",
        "availability_failure": "fallback_go",
        "integrity_failure": "block_no_fallback",
        "selector": "SYMBROWSE_IMPL=go|rust (unset is go; auto is invalid)",
    }


def _write_signed_fixture(root: Path, *, missing_signature: bool = False, wrong_binary: bool = False) -> None:
    proofs: list[dict[str, object]] = []
    for implementation in IMPLEMENTATIONS:
        directory = root / "dual" / implementation
        directory.mkdir(parents=True)
        checksums = []
        signature_inputs = []
        for os_name, arch in TARGETS:
            name = archive_name("0.8.0", os_name, arch)
            archive = directory / name
            binary_name = "not-symbrowse" if wrong_binary and implementation == "rust" else ("symbrowse.exe" if os_name == "windows" else "symbrowse")
            if os_name == "windows":
                with zipfile.ZipFile(archive, "w") as stream:
                    stream.writestr(binary_name, b"binary")
            else:
                with tarfile.open(archive, "w:gz") as stream:
                    info = tarfile.TarInfo(binary_name)
                    info.size = len(b"binary")
                    info.mode = 0o755
                    stream.addfile(info, io.BytesIO(b"binary"))
            checksums.append(f"{_sha256(archive)}  {name}")
            sbom_path = directory / f"{name}.sbom"
            sbom_path.write_bytes(_spdx("symbrowse.exe" if os_name == "windows" else "symbrowse", name, implementation))
            checksums.append(f"{_sha256(sbom_path)}  {sbom_path.name}")
            if not (missing_signature and implementation == "rust"):
                (directory / f"{name}.sig").write_text("signature\n", encoding="utf-8")
                (directory / f"{name}.pem").write_text("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n", encoding="utf-8")
            signature_inputs.append(
                {
                    "artifact": name,
                    "sha256": _sha256(archive),
                    "signature": f"{name}.sig",
                    "certificate": f"{name}.pem",
                }
            )
            proofs.append(
                {
                    "implementation": implementation,
                    "target": f"{os_name}-{arch}",
                    "archive": name,
                    "archive_sha256": _sha256(archive),
                    "mode": "cross",
                    "status": "cross_built",
                }
            )
        (directory / "checksums.txt").write_text("\n".join(checksums) + "\n", encoding="utf-8")
        (directory / SIGNATURE_INPUTS_NAME).write_text(
            json.dumps(
                {
                    "schema_version": 1,
                    "implementation": implementation,
                    "signing": "required",
                    "signed": True,
                    "artifacts": signature_inputs,
                }
            )
            + "\n",
            encoding="utf-8",
        )
    (root / "dual" / PLATFORM_PROOFS_NAME).write_text(
        json.dumps({"schema_version": 1, "version": "0.8.0", "proofs": proofs}) + "\n",
        encoding="utf-8",
    )
    (root / "dual" / "dual-release-manifest.json").write_text(
        json.dumps({"schema_version": 1, "selection_contract": selection_manifest()}) + "\n", encoding="utf-8"
    )


def self_test() -> None:
    with tempfile.TemporaryDirectory(prefix="rust016-verify-") as raw:
        root = Path(raw)
        _write_signed_fixture(root)
        report = verify_candidate(root, "v0.8.0")
        assert report["implementations"] == {"go": 6, "rust": 6}
        assert select_implementation("go", go_available=True, rust_available=False) == "go"
        assert select_implementation(None, go_available=True, rust_available=False) == "go"
        assert select_implementation("rust", go_available=True, rust_available=False) == "go"
        assert select_implementation("rust", go_available=True, rust_available=True) == "rust"
        try:
            select_implementation("rust", go_available=True, rust_available=True, rust_integrity_ok=False)
        except GateError:
            pass
        else:
            raise AssertionError("integrity failure incorrectly fell back")
        for requested, go_available, rust_available, integrity, expected in (
            ("go", False, True, True, "block"),
            ("rust", False, False, True, "block"),
            ("auto", True, True, True, "block"),
        ):
            try:
                result = select_implementation(
                    requested,
                    go_available=go_available,
                    rust_available=rust_available,
                    rust_integrity_ok=integrity,
                )
            except GateError:
                result = "block"
            assert result == expected, (requested, result)
        for kwargs, needle in (
            ({"missing_signature": True}, "signature"),
            ({"wrong_binary": True}, "required binary"),
        ):
            broken = root / ("broken-" + next(iter(kwargs)))
            _write_signed_fixture(broken, **kwargs)
            try:
                verify_candidate(broken, "v0.8.0")
            except GateError as error:
                assert needle in str(error), error
            else:
                raise AssertionError(f"{kwargs} did not fail closed")
        missing_sbom = root / "broken-sbom"
        _write_signed_fixture(missing_sbom)
        (missing_sbom / "dual" / "go" / f"{archive_name('0.8.0', *TARGETS[0])}.sbom").unlink()
        try:
            verify_candidate(missing_sbom, "v0.8.0")
        except GateError as error:
            assert "SBOM" in str(error), error
        else:
            raise AssertionError("missing SBOM did not fail closed")
        missing_proof = root / "broken-platform-proof"
        _write_signed_fixture(missing_proof)
        (missing_proof / "dual" / PLATFORM_PROOFS_NAME).unlink()
        try:
            verify_candidate(missing_proof, "v0.8.0")
        except GateError as error:
            assert "platform proof" in str(error), error
        else:
            raise AssertionError("missing platform proof did not fail closed")
    print("PASS RUST-016 verifier self-tests")


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--oracle-tag", default="v0.8.0")
    parser.add_argument("--candidate", type=Path)
    parser.add_argument("--implementation", choices=("both", "go", "rust"), default="both")
    parser.add_argument("--self-test", action="store_true")
    parser.add_argument("--package-dry-run", action="store_true")
    parser.add_argument("--go-binary", type=Path)
    parser.add_argument("--rust-binary", type=Path)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args(argv)
    try:
        if args.self_test:
            self_test()
            return 0
        if args.package_dry_run:
            if not args.go_binary or not args.rust_binary or not args.output:
                parser.error("--package-dry-run requires --go-binary, --rust-binary and --output")
            evidence = package_dry_run(args.go_binary.resolve(), args.rust_binary.resolve(), args.output, args.oracle_tag)
            print(json.dumps(evidence, indent=2))
            return 0
        if not args.candidate:
            parser.error("--candidate is required unless --self-test or --package-dry-run is used")
        names = IMPLEMENTATIONS if args.implementation == "both" else (args.implementation,)
        report = verify_candidate(args.candidate, args.oracle_tag, names)
        print(json.dumps(report, indent=2))
        return 0
    except GateError as error:
        print(f"BLOCK: {error}", file=sys.stderr)
        return 1
    except OSError as error:
        print(f"BLOCK: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
