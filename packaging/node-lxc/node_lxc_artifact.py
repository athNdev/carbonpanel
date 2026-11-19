#!/usr/bin/env python3
"""Unprivileged LXC / Proxmox rootfs artifact lane for managed Carbon Panel nodes.

This tool builds and qualifies a *documentation and offline-fixture* artifact for
an unprivileged LXC container that hosts the shared managed node agent and its
captive Docker daemon. It intentionally does NOT provision a live host, change
global host configuration or publish anything.

The enrollment/control contract is SHARED (ADR 0019, PR 516/517/518). This tool
only produces the node-side, digest-only pre-registration material defined there;
it never forks, redefines or mounts that contract.

All commands print a single JSON result to stdout and use exit code 0 for
``status: ok`` and 1 for ``status: refused``. That uniform shape is what the
offline fixture tests assert against.
"""
from __future__ import annotations

import argparse
import base64
import binascii
import datetime as _dt
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

TOOL_VERSION = "1.0.0"
PROVENANCE_SCHEMA = "carbonpanel.node-lxc.provenance/v1"
CAPABILITY_SCHEMA = "carbonpanel.node-lxc.capability/v1"
ARTIFACT_SCHEMA = "carbonpanel.node-lxc.artifact/v1"
SBOM_SPEC = "1.5"
VERSION_RE = re.compile(r"^runtime-v\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$")
SHA256_RE = re.compile(r"^[0-9a-f]{64}$")
REQUIRED_CONTROLLERS = ("cpu", "cpuacct", "memory", "pids", "io", "cpuset")
SECRET_CONTENT_RE = re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----")
SECRET_FIELD_RE = re.compile(r'(?i)"(?:secret|token|password|private_key|plaintext_secret|enrollment_secret)"\s*:')
VOLATILE_SUFFIXES = (".partial", ".tmp", "~")


# --------------------------------------------------------------------------- #
# result / refusal helpers
# --------------------------------------------------------------------------- #
def emit(obj: dict[str, Any]) -> None:
    print(json.dumps(obj, indent=2, sort_keys=True))


def ok(**kwargs: Any) -> int:
    emit({"status": "ok", **kwargs})
    return 0


def refuse(reason_code: str, message: str, **kwargs: Any) -> int:
    emit({"status": "refused", "reason_code": reason_code, "message": message, **kwargs})
    return 1


def now_utc() -> str:
    return _dt.datetime.now(_dt.timezone.utc).replace(microsecond=0).isoformat()


# --------------------------------------------------------------------------- #
# hashing / canonical manifest
# --------------------------------------------------------------------------- #
def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def canonical_bytes(path: Path) -> bytes:
    """Return LF-normalized bytes of a file (see canonical_file_digest)."""
    data = path.read_bytes()
    return data.replace(b"\r\n", b"\n")


def canonical_file_info(path: Path) -> tuple[str, int]:
    """Line-ending-independent (sha256, size): normalize CRLF to LF across chunks.

    Both the digest and the recorded size must be platform independent. A
    fixture authored with LF on Linux and checked out with CRLF on Windows must
    produce an identical canonical map; raw st_size would otherwise differ.
    """
    h = hashlib.sha256()
    size = 0
    pending_cr = False
    with path.open("rb") as fh:
        for chunk in iter(lambda: fh.read(1024 * 1024), b""):
            if pending_cr:
                chunk = b"\r" + chunk
                pending_cr = False
            if chunk.endswith(b"\r"):
                pending_cr = True
                chunk = chunk[:-1]
            normalized = chunk.replace(b"\r\n", b"\n")
            h.update(normalized)
            size += len(normalized)
    if pending_cr:
        h.update(b"\r")
        size += 1
    return h.hexdigest(), size


def canonical_file_digest(path: Path) -> str:
    return canonical_file_info(path)[0]


def iter_files(root: Path) -> list[Path]:
    """Deterministic regular-file list. Refuse symlinks (no path escape in an artifact)."""
    found: list[Path] = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames.sort()
        for name in sorted(filenames):
            p = Path(dirpath) / name
            if p.is_symlink():
                raise ValueError(f"symlink not allowed in artifact tree: {p.relative_to(root).as_posix()}")
            if p.name.endswith(VOLATILE_SUFFIXES):
                continue
            found.append(p)
    return sorted(found, key=lambda p: p.relative_to(root).as_posix())


def canonical_files(root: Path) -> dict[str, dict[str, str]]:
    """Relative-path -> {sha256, mode, size}; the reproducible rootfs digest input."""
    files: dict[str, dict[str, str]] = {}
    for p in iter_files(root):
        rel = p.relative_to(root).as_posix()
        st = p.stat()
        digest, size = canonical_file_info(p)
        files[rel] = {
            "sha256": digest,
            "mode": format(st.st_mode & 0o7777, "04o"),
            "size": str(size),
        }
    return files


def digest_of_files(files: dict[str, dict[str, str]]) -> str:
    canonical = json.dumps(files, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(canonical).hexdigest()


def write_json(path: Path, obj: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(json.dumps(obj, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    os.replace(tmp, path)


def read_json(path: Path) -> Any:
    return json.loads(path.read_text(encoding="utf-8"))


def write_text(path: Path, text: str, mode: int = 0o644) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(path.name + ".tmp")
    tmp.write_text(text, encoding="utf-8")
    os.chmod(tmp, mode)
    os.replace(tmp, path)


def check_sha256(value: Any, label: str) -> int | None:
    if not isinstance(value, str) or not SHA256_RE.match(value):
        return refuse("INVALID_PROVENANCE", f"{label} must be a 64-character lowercase sha256")
    return None


# --------------------------------------------------------------------------- #
# secret-material guard (no secrets in template / provisioning metadata)
# --------------------------------------------------------------------------- #
def scan_secrets(root: Path) -> list[str]:
    findings: list[str] = []
    for p in iter_files(root):
        data = p.read_bytes()
        rel = p.relative_to(root).as_posix()
        if SECRET_CONTENT_RE.search(data):
            findings.append(f"{rel}: private-key PEM marker")
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            continue
        if SECRET_FIELD_RE.search(text):
            findings.append(f"{rel}: secret/token/password field")
    return findings


# --------------------------------------------------------------------------- #
# signing (RSA-2048/SHA-256 via openssl; keys are supplied by the operator and
# never stored in the artifact or this repository)
# --------------------------------------------------------------------------- #
def openssl_sign(private_key: Path, payload: Path, out_sig: Path) -> str | None:
    if not private_key.is_file():
        return f"signing key not found: {private_key}"
    proc = subprocess.run(
        ["openssl", "dgst", "-sha256", "-sign", str(private_key), "-out", str(out_sig), str(payload)],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return proc.stderr.strip() or "openssl sign failed"
    return None


def openssl_verify(public_key: Path, payload: Path, sig: Path) -> bool:
    if not public_key.is_file() or not sig.is_file():
        return False
    proc = subprocess.run(
        ["openssl", "dgst", "-sha256", "-verify", str(public_key), "-signature", str(sig), str(payload)],
        capture_output=True,
        text=True,
    )
    return proc.returncode == 0


def generate_keypair(dirpath: Path, stem: str = "node") -> tuple[Path, Path]:
    """ADR0019-conformant node key pair: Ed25519, generated ephemerally at boot."""
    private = dirpath / f"{stem}.key.pem"
    public = dirpath / f"{stem}.pub.pem"
    subprocess.run(
        ["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private)],
        check=True, capture_output=True,
    )
    subprocess.run(
        ["openssl", "pkey", "-in", str(private), "-pubout", "-out", str(public)],
        check=True, capture_output=True,
    )
    return private, public


def openssl_sign_raw(private_key: Path, payload: Path, out_sig: Path) -> str | None:
    """Ed25519 raw signature (no digest); used only for the node's CSR_PRE proof."""
    proc = subprocess.run(
        ["openssl", "pkeyutl", "-sign", "-inkey", str(private_key), "-rawin",
         "-in", str(payload), "-out", str(out_sig)],
        capture_output=True, text=True,
    )
    if proc.returncode != 0:
        return proc.stderr.strip() or "openssl pkeyutl sign failed"
    return None


# --------------------------------------------------------------------------- #
# checksum/signature/BOM writer and verifier (shared by build, backup, volume)
# --------------------------------------------------------------------------- #
def write_integrity_bundle(out: Path, payload_root: Path, version: str, sign_key: Path | None,
                           extra: dict[str, Any] | None = None) -> dict[str, Any]:
    files = canonical_files(payload_root)
    digest = digest_of_files(files)
    manifest = {
        "schema": ARTIFACT_SCHEMA,
        "artifact_version": version,
        "payload_digest_sha256": digest,
        "files": files,
    }
    if extra:
        manifest.update(extra)
    write_json(out / "manifest.json", manifest)
    sums = []
    for rel in sorted(files):
        sums.append(f"{files[rel]['sha256']}  {rel}")
    sums.append(f"{canonical_file_digest(out / 'manifest.json')}  manifest.json")
    write_text(out / "SHA256SUMS", "\n".join(sums) + "\n")
    signed = False
    if sign_key is not None:
        # Sign the canonical (LF-normalized) manifest bytes so a Windows build
        # verifies identically on Linux and vice versa.
        canonical_manifest = out / "manifest.canonical.tmp"
        canonical_manifest.write_bytes(canonical_bytes(out / "manifest.json"))
        sig = out / "signature.bin"
        err = openssl_sign(sign_key, canonical_manifest, sig)
        canonical_manifest.unlink(missing_ok=True)
        if err:
            raise RuntimeError(f"signing failed: {err}")
        write_json(out / "signature.json", {
            "algorithm": "RSA-SHA256",
            "signature_b64": base64.b64encode(sig.read_bytes()).decode(),
            "signed_manifest_sha256": canonical_file_digest(out / "manifest.json"),
            "key_ref": "operator-supplied; not embedded in artifact",
        })
        sig.unlink()
        signed = True
    else:
        write_json(out / "unsigned.json", {
            "reason": "no operator signing key supplied; verification must refuse this artifact",
        })
    return {"digest": digest, "files": manifest, "signed": signed}


def check_sha256sums(root: Path) -> str | None:
    sums_path = root / "SHA256SUMS"
    if not sums_path.is_file():
        return "SHA256SUMS"
    expected: dict[str, str] = {}
    for line in sums_path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        digest, rel = line.split("  ", 1)
        expected[rel] = digest
    if not expected:
        return "SHA256SUMS.empty"
    for rel, digest in expected.items():
        # File lines are payload-relative (rootfs/...); manifest.json is at the bundle root.
        # Use the same line-ending-independent digest that produced SHA256SUMS so
        # verification is reproducible across Windows and Linux checkouts.
        p = root / rel if rel == "manifest.json" else root / "rootfs" / rel
        if not p.is_file():
            return f"missing:{rel}"
        if canonical_file_digest(p) != digest:
            return f"checksum-mismatch:{rel}"
    return None


def verify_bundle(root: Path, public_key: Path | None, require_signature: bool = True,
                  require_sbom: bool = True, allow_secrets: bool = False) -> tuple[str | None, dict[str, Any]]:
    """Return (refusal_code, info). Verifies checksums, manifest and signature."""
    manifest_path = root / "manifest.json"
    if not manifest_path.is_file():
        return "MISSING_MANIFEST", {}
    try:
        manifest = read_json(manifest_path)
    except (ValueError, OSError):
        return "INVALID_MANIFEST", {}
    if manifest.get("schema") != ARTIFACT_SCHEMA:
        return "INVALID_MANIFEST", {}

    bad = check_sha256sums(root)
    if bad:
        return ("MISSING_CHECKSUMS" if bad == "SHA256SUMS" else "CHECKSUM_MISMATCH"), {"detail": bad}

    payload_root = root / "rootfs"
    if not payload_root.is_dir():
        return "MISSING_PAYLOAD", {}
    files = canonical_files(payload_root)
    if files != manifest.get("files"):
        return "MANIFEST_MISMATCH", {}
    digest = digest_of_files(files)
    if digest != manifest.get("payload_digest_sha256"):
        return "MANIFEST_DIGEST_MISMATCH", {}

    sbom: dict[str, Any] = {}
    if require_sbom:
        sbom_path = root / "sbom.cdx.json"
        if not sbom_path.is_file():
            return "MISSING_SBOM", {}
        try:
            sbom = read_json(sbom_path)
        except (ValueError, OSError):
            return "INVALID_SBOM", {}
        if sbom.get("bomFormat") != "CycloneDX" or sbom.get("specVersion") != SBOM_SPEC:
            return "INVALID_SBOM", {}
        hashes = sbom.get("metadata", {}).get("component", {}).get("hashes", [])
        if not any(h.get("alg") == "SHA-256" and h.get("content") == digest for h in hashes):
            return "SBOM_DIGEST_MISMATCH", {}
        if not sbom.get("components"):
            return "SBOM_EMPTY", {}

    if not allow_secrets:
        findings = scan_secrets(root)
        if findings:
            return "SECRET_MATERIAL_IN_ARTIFACT", {"findings": findings}

    if require_signature:
        if not (root / "signature.json").is_file():
            return "UNSIGNED_ARTIFACT", {"detail": "signature.json missing"}
        if public_key is None or not public_key.is_file():
            return "MISSING_PUBLIC_KEY", {}
        sig = read_json(root / "signature.json")
        if canonical_file_digest(manifest_path) != sig.get("signed_manifest_sha256"):
            return "SIGNATURE_STALE", {}
        canonical_manifest = root / "manifest.verify.tmp"
        canonical_manifest.write_bytes(canonical_bytes(manifest_path))
        binary = root / "signature.verify.tmp"
        binary.write_bytes(base64.b64decode(sig.get("signature_b64", ""), validate=True))
        valid = openssl_verify(public_key, canonical_manifest, binary)
        binary.unlink(missing_ok=True)
        canonical_manifest.unlink(missing_ok=True)
        if not valid:
            return "SIGNATURE_INVALID", {}
        if sig.get("algorithm") != "RSA-SHA256":
            return "UNSUPPORTED_SIGNATURE_ALGORITHM", {}

    return None, {"digest": digest, "manifest": manifest, "sbom": sbom}


def verification_refusal(code: str, info: dict[str, Any]) -> int:
    messages = {
        "MISSING_MANIFEST": "artifact manifest.json is missing",
        "INVALID_MANIFEST": "artifact manifest is not valid JSON of the expected schema",
        "MISSING_CHECKSUMS": "SHA256SUMS is missing or empty",
        "CHECKSUM_MISMATCH": "a checksum in SHA256SUMS does not match the file",
        "MISSING_PAYLOAD": "artifact rootfs payload directory is missing",
        "MANIFEST_MISMATCH": "rootfs files do not match the manifest",
        "MANIFEST_DIGEST_MISMATCH": "computed payload digest does not match the manifest",
        "MISSING_SBOM": "sbom.cdx.json is missing",
        "INVALID_SBOM": "SBOM is not valid CycloneDX 1.5",
        "SBOM_DIGEST_MISMATCH": "SBOM component hash does not bind the artifact digest",
        "SBOM_EMPTY": "SBOM has no components",
        "SECRET_MATERIAL_IN_ARTIFACT": "secret material found in artifact",
        "UNSIGNED_ARTIFACT": "artifact is unsigned; fail closed",
        "MISSING_PUBLIC_KEY": "a public key is required to verify the signature",
        "SIGNATURE_STALE": "signature does not cover the current manifest",
        "SIGNATURE_INVALID": "signature verification failed",
        "UNSUPPORTED_SIGNATURE_ALGORITHM": "unsupported signature algorithm",
    }
    return refuse(code, messages.get(code, code), **info)


# --------------------------------------------------------------------------- #
# provenance profiles: real artifact pins vs explicitly synthetic fixtures
# --------------------------------------------------------------------------- #
ARTIFACT_PROFILE = "artifact"
FIXTURE_PROFILE = "fixture"
IMMUTABLE_URL_RE = re.compile(r"^https://cloud-images\.ubuntu\.com/(?:[^/]+/)+release-(\d{8})/")
OPENPGP_FPR_RE = re.compile(r"^[0-9A-Fa-f]{40}$")
MOVING_URL_MARKERS = ("/release/", "fixture://", "example.com", "latest", "placeholder")


def is_placeholder_digest(value: Any) -> bool:
    if not isinstance(value, str) or not SHA256_RE.match(value):
        return True
    if len(set(value)) == 1:  # all zeros / all repeated characters
        return True
    lowered = value.lower()
    return any(token in lowered for token in ("deadbeef", "cafebabe", "0123456789abcdef")) and len(set(value)) < 8


def validate_artifact_provenance(prov: dict[str, Any]) -> int | None:
    """Fail closed unless every upstream pin is real, immutable, dated and signed."""
    if prov.get("profile") != ARTIFACT_PROFILE:
        return refuse("INVALID_PROVENANCE_PROFILE", "artifact build requires profile=artifact")
    if prov.get("synthetic") is not False:
        return refuse("SYNTHETIC_PROVENANCE", "artifact build refuses a synthetic provenance profile")
    if prov.get("schema_version") != 2:
        return refuse("UNSUPPORTED_PROVENANCE_VERSION", "artifact provenance must be schema_version 2")
    images = prov.get("images")
    if not isinstance(images, list) or not images:
        return refuse("NO_PINNED_IMAGES", "artifact provenance has no pinned images")
    evidence = prov.get("retrieval_evidence", {})
    method = str(evidence.get("method", "")).lower()
    if not evidence or "direct" not in method or "gpg" not in method:
        return refuse("MISSING_RETRIEVAL_EVIDENCE", "direct retrieval and gpg verification evidence are required")

    for image in images:
        if not isinstance(image, dict):
            return refuse("INVALID_IMAGE_PIN", "image pin must be an object")
        ref = str(image.get("ref", "unknown"))
        if image.get("digest_status") != "verified":
            return refuse("UNVERIFIED_DIGEST", f"{ref}: digest_status must be 'verified'", image=ref)
        if is_placeholder_digest(image.get("expected_sha256")):
            return refuse("PLACEHOLDER_DIGEST", f"{ref}: expected_sha256 is a placeholder", image=ref)
        if image.get("immutable") is not True:
            return refuse("MUTABLE_IMAGE_URL", f"{ref}: pin must be immutable", image=ref)
        url = str(image.get("image_url", ""))
        match = IMMUTABLE_URL_RE.match(url)
        if not match or any(marker in url.lower() for marker in MOVING_URL_MARKERS):
            return refuse("MOVING_URL", f"{ref}: image_url must be an immutable dated primary-source URL", image=ref, url=url)
        dated = match.group(1)
        if str(image.get("release_date", "")).replace("-", "") != dated:
            return refuse("DATE_MISMATCH", f"{ref}: release_date does not match the dated URL", image=ref)
        checksum_url = str(image.get("checksum_url", ""))
        signature_url = str(image.get("signature_url", ""))
        prefix = url.rsplit("/", 1)[0]
        if ((checksum_url and not checksum_url.startswith(prefix + "/")) or
                (signature_url and not signature_url.startswith(prefix + "/"))):
            return refuse("URL_PROVENANCE_MISMATCH", f"{ref}: checksum/signature must come from the same dated release", image=ref)
        if any(marker in checksum_url.lower() or marker in signature_url.lower() for marker in MOVING_URL_MARKERS):
            return refuse("MOVING_URL", f"{ref}: checksum/signature URL is moving or synthetic", image=ref)
        for field in ("image_url", "checksum_url", "signature_url",
                      "checksum_file_sha256", "signature_file_sha256", "signing_key_fingerprint"):
            if not image.get(field):
                return refuse("MISSING_SIGNING_EVIDENCE", f"{ref}: {field} is required", image=ref)
        for field in ("checksum_file_sha256", "signature_file_sha256"):
            err = check_sha256(image.get(field), f"{ref}.{field}")
            if err is not None:
                return err
        if not OPENPGP_FPR_RE.match(str(image.get("signing_key_fingerprint", ""))):
            return refuse("MISSING_SIGNING_EVIDENCE", f"{ref}: signing_key_fingerprint is invalid", image=ref)
        if not image.get("signature_made") or not image.get("signing_key_name") or not image.get("signing_key_source"):
            return refuse("MISSING_SIGNING_EVIDENCE", f"{ref}: signature metadata is incomplete", image=ref)
    return None


def validate_fixture_provenance(prov: dict[str, Any]) -> int | None:
    if prov.get("profile") != FIXTURE_PROFILE or prov.get("synthetic") is not True:
        return refuse("INVALID_FIXTURE_PROFILE", "fixture build requires profile=fixture and synthetic=true")
    err = check_sha256(prov.get("fixture", {}).get("manifest_sha256"), "fixture.manifest_sha256")
    return err


def provenance_pin_summary(prov: dict[str, Any]) -> dict[str, Any]:
    return {
        "profile": prov.get("profile"),
        "synthetic": prov.get("synthetic"),
        "verified_on": prov.get("verified_on"),
        "pins": [
            {
                "ref": i.get("ref"),
                "arch": i.get("arch"),
                "image_url": i.get("image_url"),
                "expected_sha256": i.get("expected_sha256"),
                "checksum_url": i.get("checksum_url"),
                "signature_url": i.get("signature_url"),
                "signing_key_fingerprint": i.get("signing_key_fingerprint"),
                "digest_status": i.get("digest_status"),
            }
            for i in prov.get("images", [])
        ],
    }


def cmd_validate_provenance(args: argparse.Namespace) -> int:
    path = Path(args.provenance)
    if not path.is_file():
        return refuse("MISSING_PROVENANCE", f"provenance file not found: {path}")
    try:
        prov = read_json(path)
    except (ValueError, OSError):
        return refuse("INVALID_PROVENANCE", "provenance is not valid JSON")
    if prov.get("schema") != PROVENANCE_SCHEMA:
        return refuse("INVALID_PROVENANCE", "unsupported provenance schema")
    expected = args.expect_profile
    if expected == ARTIFACT_PROFILE:
        rc = validate_artifact_provenance(prov)
        if rc is not None:
            return rc
        return ok(pin_count=len(prov["images"]), **provenance_pin_summary(prov))
    if expected == FIXTURE_PROFILE:
        rc = validate_fixture_provenance(prov)
        if rc is not None:
            return rc
        return ok(profile=FIXTURE_PROFILE, synthetic=True,
                  manifest_sha256=prov["fixture"]["manifest_sha256"],
                  note="synthetic fixture profile; not an artifact claim")
    return refuse("UNKNOWN_PROFILE", f"unknown expected profile: {expected}")


# --------------------------------------------------------------------------- #
# end-to-end signed upstream archive verification (real dated release bytes)
# --------------------------------------------------------------------------- #
ARCHIVE_ROOT_REQUIRED_DIRS = ("bin", "etc", "lib", "sbin", "usr", "var")
LXD_ARCH_ALIASES = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}


def _yaml_value(text: str, key: str) -> str | None:
    match = re.search(r"(?m)^\s*" + re.escape(key) + r":\s*[\"']?([^\n\"']+)[\"']?\s*$", text)
    return match.group(1).strip() if match else None


def _url_basename(url: str) -> str:
    return Path(urllib.parse.urlparse(url).path).name


def _fetch_url(url: str, dest: Path) -> int:
    """Streaming direct HTTPS GET into an owned disposable path (no search/proxy)."""
    req = urllib.request.Request(url, headers={"User-Agent": "carbonpanel-node-lxc-qualification/1.0"})
    total = 0
    with urllib.request.urlopen(req, timeout=180) as resp, dest.open("wb") as fh:
        while True:
            chunk = resp.read(1024 * 1024)
            if not chunk:
                break
            fh.write(chunk)
            total += len(chunk)
    return total


def _expected_key_sha256(prov: dict[str, Any]) -> str | None:
    text = str(prov.get("retrieval_evidence", {}).get("key_get", ""))
    match = re.search(r"sha256\s+([0-9a-f]{64})", text)
    return match.group(1) if match else None


def _gpg_fingerprint(homedir: Path) -> str | None:
    # GnuPG for Windows does not accept a drive-letter --homedir; run it with
    # the work dir as cwd and use the homedir's relative name instead.
    proc = subprocess.run(
        ["gpg", "--homedir", homedir.name, "--batch", "--with-colons", "--fingerprint"],
        cwd=str(homedir.parent), capture_output=True, text=True,
    )
    for line in proc.stdout.splitlines():
        if line.startswith("fpr:"):
            parts = line.split(":")
            if len(parts) > 9:
                return parts[9].strip().upper()
    return None


def _gpg_verify_detached(homedir: Path, key_path: Path, sig: Path, data: Path,
                         expected_fpr: str) -> tuple[bool, str]:
    imported = subprocess.run(
        ["gpg", "--homedir", homedir.name, "--batch", "--import", key_path.name],
        cwd=str(homedir.parent), capture_output=True, text=True,
    )
    if imported.returncode != 0:
        return False, f"key import failed: {(imported.stderr or imported.stdout).strip()[:200]}"
    fpr = _gpg_fingerprint(homedir)
    if fpr != expected_fpr.upper():
        return False, f"key fingerprint mismatch: imported={fpr} expected={expected_fpr}"
    verified = subprocess.run(
        ["gpg", "--homedir", homedir.name, "--batch", "--status-fd", "2",
         "--verify", sig.name, data.name],
        cwd=str(homedir.parent), capture_output=True, text=True,
    )
    status = (verified.stderr or "") + (verified.stdout or "")
    good = verified.returncode == 0 and ("VALIDSIG " + expected_fpr.upper()) in status
    detail = next((line.strip() for line in status.splitlines()
                   if "VALIDSIG" in line or "Good signature" in line or "BADSIG" in line), "")
    return good, detail[:300]


def _parse_sha256sums(text: str) -> dict[str, str]:
    entries: dict[str, str] = {}
    for line in text.splitlines():
        line = line.strip()
        if not line:
            continue
        parts = line.split(None, 1)
        if len(parts) != 2:
            continue
        digest, name = parts[0], parts[1].lstrip("*").strip()
        if SHA256_RE.match(digest):
            entries[name] = digest
    return entries


def _parse_os_release(data: bytes) -> dict[str, str]:
    out: dict[str, str] = {}
    for line in data.decode("utf-8", "replace").splitlines():
        if "=" in line:
            key, value = line.split("=", 1)
            out[key.strip()] = value.strip().strip('"').strip("'")
    return out


def _inspect_root_tarball(path: Path) -> tuple[dict[str, Any], str | None]:
    top: set[str] = set()
    os_release: dict[str, str] = {}
    member_count = 0
    try:
        with tarfile.open(path, "r:xz") as tf:
            for member in tf:
                member_count += 1
                if member_count > 2_000_000:
                    return {}, "LAYOUT_TOO_LARGE"
                name = member.name[2:] if member.name.startswith("./") else member.name
                parts = name.split("/")
                if name.startswith("/") or ".." in parts:
                    return {}, "UNSAFE_TARBALL_PATH"
                top.add(parts[0])
                # Ubuntu 24.04 is usrmerged: etc/os-release is a symlink to
                # ../usr/lib/os-release. Read the regular file at either path.
                if (not os_release and name in ("etc/os-release", "usr/lib/os-release")
                        and member.isfile()):
                    handle = tf.extractfile(member)
                    if handle is not None:
                        os_release = _parse_os_release(handle.read())
    except (tarfile.TarError, OSError, EOFError) as exc:
        return {}, f"INVALID_ROOT_TARBALL:{type(exc).__name__}"
    return {
        "member_count": member_count,
        "top_level_entries": sorted(top),
        "os_release": {k: os_release.get(k) for k in ("ID", "VERSION_ID", "VERSION_CODENAME", "NAME")},
    }, None


def _inspect_lxd_metadata(path: Path) -> tuple[dict[str, Any], str | None]:
    try:
        with tarfile.open(path, "r:xz") as tf:
            handle = tf.extractfile("metadata.yaml")
            if handle is None:
                return {}, "LXD_METADATA_MISSING"
            text = handle.read().decode("utf-8", "replace")
    except (tarfile.TarError, OSError, EOFError, KeyError) as exc:
        return {}, f"INVALID_LXD_METADATA:{type(exc).__name__}"
    return {
        "architecture": _yaml_value(text, "architecture"),
        "os": _yaml_value(text, "os"),
        "release": _yaml_value(text, "release"),
        "description": _yaml_value(text, "description"),
    }, None


def cmd_verify_signed_archive(args: argparse.Namespace) -> int:
    """Verify the signed upstream archive end-to-end in an owned disposable path."""
    path = Path(args.provenance)
    if not path.is_file():
        return refuse("MISSING_PROVENANCE", f"provenance file not found: {path}")
    try:
        prov = read_json(path)
    except (ValueError, OSError):
        return refuse("INVALID_PROVENANCE", "provenance is not valid JSON")
    if prov.get("schema") != PROVENANCE_SCHEMA:
        return refuse("INVALID_PROVENANCE", "unsupported provenance schema")

    # Fail closed on synthetic / placeholder / moving URL pins before any I/O.
    rc = validate_artifact_provenance(prov)
    if rc is not None:
        return rc

    pin = next((i for i in prov.get("images", []) if i.get("arch") == args.arch), None)
    if pin is None:
        return refuse("UNSUPPORTED_PLATFORM", f"no pinned upstream image for arch {args.arch}")

    work = Path(args.work_dir)
    work.mkdir(parents=True, exist_ok=True)
    paths = {
        "checksum": work / "SHA256SUMS",
        "signature": work / "SHA256SUMS.gpg",
        "key": work / "signing-key.asc",
        "root": work / _url_basename(pin["image_url"]),
        "lxd": work / _url_basename(pin["lxd_metadata_url"]),
    }

    fetched: list[dict[str, Any]] = []
    if args.fetch:
        sources = {
            "checksum": pin["checksum_url"],
            "signature": pin["signature_url"],
            "key": pin["signing_key_source"],
            "root": pin["image_url"],
            "lxd": pin["lxd_metadata_url"],
        }
        for label, url in sources.items():
            try:
                count = _fetch_url(url, paths[label])
            except Exception as exc:  # direct fetch failure is a refusal, never a fallback
                return refuse("FETCH_FAILED", f"{label}: {type(exc).__name__}: {exc}"[:300], artifact=label)
            fetched.append({"artifact": label, "url": url, "bytes": count})
    else:
        missing = [label for label, item in paths.items() if not item.is_file()]
        if missing:
            return refuse("MISSING_ARCHIVE_FILE", "required local archive file(s) missing", missing=missing)

    control_checks = {
        "checksum": pin.get("checksum_file_sha256"),
        "signature": pin.get("signature_file_sha256"),
    }
    key_sha = _expected_key_sha256(prov)
    if key_sha:
        control_checks["key"] = key_sha
    for label, expected in control_checks.items():
        if expected and sha256_file(paths[label]) != expected:
            return refuse("CONTROL_FILE_DIGEST_MISMATCH", f"{label} does not match the recorded pin", artifact=label)

    gpg_home = Path(tempfile.mkdtemp(prefix="cp-lxc-gpg-", dir=str(work)))
    try:
        os.chmod(gpg_home, 0o700)
    except OSError:
        pass
    good, detail = _gpg_verify_detached(
        gpg_home, paths["key"], paths["signature"], paths["checksum"],
        str(pin["signing_key_fingerprint"]),
    )
    shutil.rmtree(gpg_home, ignore_errors=True)
    if not good:
        return refuse("SIGNATURE_INVALID", f"GPG verification of signed SHA256SUMS failed: {detail}",
                      image=pin["ref"])

    sums = _parse_sha256sums(paths["checksum"].read_text(encoding="utf-8", errors="replace"))
    root_name = _url_basename(pin["image_url"])
    lxd_name = _url_basename(pin["lxd_metadata_url"])
    signed_root = sums.get(root_name)
    signed_lxd = sums.get(lxd_name)
    if not signed_root:
        return refuse("CHECKSUM_ENTRY_MISSING", f"signed SHA256SUMS has no entry for {root_name}")
    if signed_root != pin["expected_sha256"]:
        return refuse("SIGNED_SUM_MISMATCH", "pin expected_sha256 differs from the signed SHA256SUMS entry",
                      image=pin["ref"])
    if not signed_lxd:
        return refuse("CHECKSUM_ENTRY_MISSING", f"signed SHA256SUMS has no entry for {lxd_name}")
    if signed_lxd != pin.get("lxd_metadata_sha256"):
        return refuse("SIGNED_SUM_MISMATCH", "LXD metadata pin differs from the signed SHA256SUMS entry",
                      image=pin["ref"])

    bytes_checked: list[dict[str, Any]] = []
    for label, expected, name in (("root", pin["expected_sha256"], root_name),
                                  ("lxd", pin["lxd_metadata_sha256"], lxd_name)):
        actual = sha256_file(paths[label])
        size = paths[label].stat().st_size
        bytes_checked.append({"artifact": name, "bytes": size, "sha256": actual})
        if actual != expected:
            return refuse("UPSTREAM_DIGEST_MISMATCH",
                          f"{name} bytes do not match the signed upstream digest",
                          image=pin["ref"], expected=expected, actual=actual)
    root_size = paths["root"].stat().st_size
    if pin.get("image_content_length") and root_size != int(pin["image_content_length"]):
        return refuse("CONTENT_LENGTH_MISMATCH", "root tarball byte count differs from the pinned content length",
                      image=pin["ref"], expected=pin["image_content_length"], actual=root_size)

    layout, layout_err = _inspect_root_tarball(paths["root"])
    if layout_err:
        return refuse(layout_err.split(":", 1)[0], f"root tarball layout inspection failed: {layout_err}",
                      image=pin["ref"])
    missing_dirs = [d for d in ARCHIVE_ROOT_REQUIRED_DIRS if d not in layout["top_level_entries"]]
    if missing_dirs:
        return refuse("LAYOUT_INVALID", "root tarball lacks canonical Ubuntu layout directories",
                      missing_dirs=missing_dirs)
    osr = layout["os_release"]
    if osr.get("ID") != "ubuntu" or osr.get("VERSION_ID") != "24.04":
        return refuse("METADATA_VERSION_MISMATCH", "root tarball is not Ubuntu 24.04 (Noble)",
                      os_release=osr, image=pin["ref"])

    lxd_meta, lxd_err = _inspect_lxd_metadata(paths["lxd"])
    if lxd_err:
        return refuse(lxd_err.split(":", 1)[0], f"LXD metadata inspection failed: {lxd_err}", image=pin["ref"])
    arch_name = LXD_ARCH_ALIASES.get(str(lxd_meta.get("architecture", "")).lower())
    if arch_name != args.arch:
        return refuse("METADATA_ARCH_MISMATCH", "LXD metadata architecture does not match the pin",
                      metadata=lxd_meta)
    if (lxd_meta.get("os") != "ubuntu" or lxd_meta.get("release") != "noble"
            or "24.04" not in str(lxd_meta.get("description"))):
        return refuse("METADATA_VERSION_MISMATCH", "LXD metadata does not describe Ubuntu 24.04 (Noble)",
                      metadata=lxd_meta)

    return ok(
        image=pin["ref"], arch=args.arch, fetch_mode=bool(args.fetch),
        gpg_signature="GOOD", signing_key_fingerprint=str(pin["signing_key_fingerprint"]).upper(),
        gpg_status=detail, signed_checksum_entry=signed_root,
        bytes_checked=bytes_checked, root_tarball_bytes=root_size,
        layout=layout, lxd_metadata=lxd_meta, fetched=fetched,
        evidence_scope=("actual tarball bytes rehashed and matched to the GPG-signed SHA256SUMS and pinned "
                        "digests; canonical layout/metadata/version inspected; NOT live import/boot evidence"),
        note="no live host, LXC import or guest boot; external blocker preserved",
    )


# --------------------------------------------------------------------------- #
# build
# --------------------------------------------------------------------------- #
def cmd_build(args: argparse.Namespace) -> int:
    version = args.version
    if not VERSION_RE.match(version or ""):
        return refuse("INVALID_VERSION", "version must be runtime-vMAJOR.MINOR.PATCH")
    prov_path = Path(args.provenance)
    if not prov_path.is_file():
        return refuse("MISSING_PROVENANCE", f"provenance file not found: {prov_path}")
    try:
        prov = read_json(prov_path)
    except (ValueError, OSError):
        return refuse("INVALID_PROVENANCE", "provenance is not valid JSON")
    if prov.get("schema") != PROVENANCE_SCHEMA:
        return refuse("INVALID_PROVENANCE", "unsupported provenance schema")
    if args.fixture:
        rc = validate_fixture_provenance(prov)
        if rc is not None:
            return rc
    else:
        rc = validate_artifact_provenance(prov)
        if rc is not None:
            return rc
    if args.platform not in prov.get("platform_matrix", {}):
        return refuse("UNSUPPORTED_PLATFORM", f"{args.platform} is not in the supported platform matrix")

    source = Path(args.source_dir)
    out = Path(args.out_dir)
    if not source.is_dir():
        return refuse("MISSING_SOURCE", f"source directory not found: {source}")
    if out.exists() and any(out.iterdir()) and not args.force:
        return refuse("OUTPUT_NOT_EMPTY", "output directory is not empty; pass --force to replace")

    try:
        files = canonical_files(source)
    except ValueError as exc:
        return refuse("UNSAFE_SOURCE", str(exc))
    computed = digest_of_files(files)

    if args.fixture:
        expected = prov.get("fixture", {}).get("manifest_sha256")
        if computed != expected:
            return refuse("PROVENANCE_DIGEST_MISMATCH", "fixture rootfs digest does not match pinned provenance",
                          expected=expected, computed=computed)
        mode = "offline-fixture"
        base_ref = prov["fixture"].get("description", "synthetic fixture rootfs")
    else:
        pinned = next((i for i in prov["images"] if i.get("arch") == args.platform), None)
        if pinned is None:
            return refuse("UNSUPPORTED_PLATFORM", f"no real pinned image for platform {args.platform}")
        tarball = Path(args.base_tarball) if args.base_tarball else None
        if tarball is None or not tarball.is_file():
            return refuse("MISSING_BASE_TARBALL", "production build requires --base-tarball")
        # The pin is the RAW SHA-256 of the immutable upstream tarball bytes,
        # exactly as recorded in the GPG-signed SHA256SUMS and checked by
        # verify-signed-archive (sha256_file) and the control-file digests.
        # A CRLF-normalized digest here would disagree with those checks for a
        # tarball whose compressed bytes happen to contain a CRLF sequence, so
        # build and verify would accept/reject different bytes (OSS-402).
        tarball_digest = sha256_file(tarball)
        if tarball_digest != pinned["expected_sha256"]:
            return refuse("UPSTREAM_DIGEST_MISMATCH", "base tarball does not match the signed upstream pin",
                          expected=pinned["expected_sha256"], actual=tarball_digest)
        mode = "pinned-upstream"
        base_ref = pinned["image_url"]

    source_findings = scan_secrets(source)
    if source_findings:
        return refuse("SECRET_MATERIAL_IN_SOURCE", "source tree contains secret material", findings=source_findings)

    if out.exists():
        shutil.rmtree(out)
    (out / "rootfs").mkdir(parents=True)
    shutil.copytree(source, out / "rootfs", dirs_exist_ok=True)
    sign_key = Path(args.sign_key) if args.sign_key else None
    try:
        integrity = write_integrity_bundle(
            out, out / "rootfs", version, sign_key,
            extra={
                "base_ref": base_ref,
                "build_mode": mode,
                "platform": args.platform,
                "provenance_schema": PROVENANCE_SCHEMA,
            },
        )
    except RuntimeError as exc:
        return refuse("SIGNING_FAILED", str(exc))

    components = []
    for c in prov.get("components", []):
        components.append({
            "type": c.get("type", "library"),
            "bom-ref": c["purl"],
            "name": c["name"],
            "version": c["version"],
            "purl": c["purl"],
            "licenses": [{"license": {"id": c.get("license", "NOASSERTION")}}],
            "hashes": [{"alg": "SHA-256", "content": c["sha256"]}] if c.get("sha256") else [],
        })
    if not components:
        return refuse("SBOM_NO_COMPONENTS", "provenance has no pinned components for the SBOM")
    write_json(out / "sbom.cdx.json", {
        "bomFormat": "CycloneDX",
        "specVersion": SBOM_SPEC,
        "serialNumber": f"urn:uuid:{hashlib.sha256((version + integrity['digest']).encode()).hexdigest()[:32]}",
        "version": 1,
        "metadata": {
            "timestamp": now_utc(),
            "component": {
                "type": "operating-system",
                "bom-ref": f"pkg:generic/carbonpanel-node-lxc@{version}",
                "name": "carbonpanel-node-lxc-rootfs",
                "version": version,
                "hashes": [{"alg": "SHA-256", "content": integrity["digest"]}],
            },
        },
        "components": components,
    })
    shutil.copy2(prov_path, out / "provenance.json")
    write_json(out / "BUILD_INFO.json", {
        "tool_version": TOOL_VERSION,
        "build_mode": mode,
        "created_utc": now_utc(),
        "version": version,
        "platform": args.platform,
        "artifact_digest_sha256": integrity["digest"],
        "signed": integrity["signed"],
        "qualification": "offline fixture only; no live import/boot evidence",
    })

    findings = scan_secrets(out)
    if findings:
        shutil.rmtree(out)
        return refuse("SECRET_MATERIAL_IN_ARTIFACT", "built artifact contains secret material", findings=findings)
    return ok(artifact_digest_sha256=integrity["digest"], version=version,
              platform=args.platform, file_count=len(files), signed=integrity["signed"], mode=mode)


# --------------------------------------------------------------------------- #
# verify
# --------------------------------------------------------------------------- #
def cmd_verify(args: argparse.Namespace) -> int:
    root = Path(args.artifact_dir)
    if not root.is_dir():
        return refuse("MISSING_ARTIFACT", f"artifact directory not found: {root}")
    pub = Path(args.public_key) if args.public_key else None
    code, info = verify_bundle(root, pub, require_signature=not args.allow_unsigned)
    if code:
        return verification_refusal(code, info)
    manifest = info["manifest"]
    if args.expect_version and manifest.get("artifact_version") != args.expect_version:
        return refuse("VERSION_MISMATCH", "artifact version does not match --expect-version",
                      expected=args.expect_version, actual=manifest.get("artifact_version"))
    return ok(status_detail="VERIFY_OK", artifact_digest_sha256=info["digest"],
              version=manifest.get("artifact_version"), platform=manifest.get("platform"),
              signed=True)


# --------------------------------------------------------------------------- #
# nested-runtime capability gate (read-only; refuses unsupported hosts)
# --------------------------------------------------------------------------- #
REQUIRED_CAPABILITY_FIELDS = (
    "kernel_version", "user_namespaces_enabled", "userns_clone_allowed", "cgroup_version",
    "cgroup_controllers", "seccomp_available", "apparmor_available", "selinux_available",
    "nesting_attested", "privileged_container", "lxc_unprivileged", "device_nodes",
    "host_docker_socket_visible", "host_docker_data_root_visible",
)


def kernel_at_least(version: str, minimum: tuple[int, ...]) -> bool:
    nums = []
    for part in version.split("-", 1)[0].split(".")[:3]:
        try:
            nums.append(int(part))
        except ValueError:
            return False
    while len(nums) < 3:
        nums.append(0)
    return tuple(nums) >= minimum


def evaluate_capability(cap: dict[str, Any]) -> tuple[str | None, str, list[str]]:
    checked: list[str] = []
    if cap.get("privileged_container") is True:
        return "PRIVILEGED_LXC_REFUSED", "privileged LXC nesting is refused; use an unprivileged container", checked
    checked.append("privileged_container")
    if cap.get("lxc_unprivileged") is not True:
        return "NOT_UNPRIVILEGED_LXC", "host is not an unprivileged LXC container", checked
    checked.append("lxc_unprivileged")
    if not kernel_at_least(str(cap.get("kernel_version", "")), (5, 11)):
        return "KERNEL_TOO_OLD", "kernel must be >= 5.11 for overlay2/fuse-overlayfs nested storage", checked
    checked.append("kernel_version")
    if cap.get("user_namespaces_enabled") is not True or cap.get("userns_clone_allowed") is not True:
        return "USERNS_DISABLED", "unprivileged user namespaces are required for rootless nested Docker", checked
    checked.append("user_namespaces")
    if cap.get("cgroup_version") != 2:
        return "CGROUP_V2_REQUIRED", "cgroup v2 with delegation is required for nested resource limits", checked
    checked.append("cgroup_version")
    missing = [c for c in REQUIRED_CONTROLLERS if c not in cap.get("cgroup_controllers", [])]
    if missing:
        return "CGROUP_CONTROLLERS_MISSING", "required cgroup controllers not delegated", checked + [f"controllers:{c}" for c in missing]
    checked.append("cgroup_controllers")
    if cap.get("seccomp_available") is not True:
        return "SECCOMP_UNAVAILABLE", "seccomp is required for nested container confinement", checked
    checked.append("seccomp")
    if not (cap.get("apparmor_available") is True or cap.get("selinux_available") is True):
        return "MAC_PROFILE_MISSING", "an AppArmor or SELinux profile is required for the nested runtime", checked
    checked.append("mac_profile")
    if cap.get("nesting_attested") is not True:
        return "NESTING_NOT_ATTESTED", "nested runtime capability is not attested by the host", checked
    checked.append("nesting_attested")
    devices = cap.get("device_nodes", {})
    if devices.get("/dev/fuse") is not True:
        return "FUSE_DEVICE_MISSING", "/dev/fuse is required for fuse-overlayfs fallback", checked
    if devices.get("/dev/net/tun") is not True:
        return "TUN_DEVICE_MISSING", "/dev/net/tun is required for the rootless user-mode network stack", checked
    checked.append("device_nodes")
    if cap.get("host_docker_socket_visible") is True:
        return "HOST_DOCKER_SOCKET_VISIBLE", "a host Docker socket is visible; host socket sharing is refused", checked
    if cap.get("host_docker_data_root_visible") is True:
        return "HOST_DOCKER_DATA_VISIBLE", "a host Docker data root is visible; shared engine state is refused", checked
    checked.append("host_engine_isolation")
    return None, "supported nested-runtime capabilities verified; use the portable guest installer if the gate refuses", checked


def cmd_capability_gate(args: argparse.Namespace) -> int:
    if not args.capability_file:
        return refuse("AUTODETECT_UNAVAILABLE", "an explicit capability file is required; this lane never probes or mutates a live host",
                      hint="pass --capability-file with a host-provided snapshot")
    path = Path(args.capability_file)
    if not path.is_file():
        return refuse("MISSING_CAPABILITY_FILE", f"capability file not found: {path}")
    try:
        cap = read_json(path)
    except (ValueError, OSError):
        return refuse("INVALID_CAPABILITY_FILE", "capability file is not valid JSON")
    if cap.get("schema") != CAPABILITY_SCHEMA:
        return refuse("INVALID_CAPABILITY_FILE", "unsupported capability schema")
    missing = [f for f in REQUIRED_CAPABILITY_FIELDS if f not in cap]
    if missing:
        return refuse("INCOMPLETE_CAPABILITY_FILE", "capability file is missing required fields", missing=missing)
    code, message, checked = evaluate_capability(cap)
    if code:
        return refuse(code, message, checked=checked)
    return ok(decision="SUPPORTED", message=message, checked=checked,
              evidence_scope="host-provided capability snapshot only; not live import/boot evidence")


# --------------------------------------------------------------------------- #
# first-boot identity + scoped enrollment bundle (ADR 0019, digest-only)
# --------------------------------------------------------------------------- #
def cmd_first_boot_identity(args: argparse.Namespace) -> int:
    volume = Path(args.volume)
    volume.mkdir(parents=True, exist_ok=True)
    identity_path = volume / "identity" / "identity.json"
    if identity_path.exists() and not args.force:
        return refuse("IDENTITY_ALREADY_EXISTS", "volume already has an identity; refusing to churn identity",
                      hint="pass --force only for an intentional re-enrollment")
    if not args.node_label:
        return refuse("MISSING_NODE_LABEL", "--node-label is required")

    with tempfile.TemporaryDirectory(prefix="cp-lxc-identity-") as td:
        keypair_dir = Path(td)
        private, public = generate_keypair(keypair_dir)
        der = keypair_dir / "public.spki.der"
        subprocess.run(["openssl", "pkey", "-pubin", "-in", str(public), "-pubout", "-outform", "DER", "-out", str(der)],
                       check=True, capture_output=True)
        spki_der = der.read_bytes()
        spki_b64 = base64.b64encode(spki_der).decode()
        fingerprint = hashlib.sha256(spki_der).hexdigest()
        node_id = fingerprint[:32]

        salt = os.urandom(32)
        secret_s = os.urandom(32)
        digest = hashlib.sha256(salt + secret_s).hexdigest()

        statement = {
            "schema": "carbonpanel.node-lxc.csr-pre/v1",
            "type": "CSR_PRE",
            "node_label": args.node_label,
            "salt_b64": base64.b64encode(salt).decode(),
            "enrollment_secret_digest": digest,
            "approved_public_key_spki_b64": spki_b64,
            "approved_key_fingerprint": fingerprint,
            "node_id": node_id,
        }
        statement_bytes = (json.dumps(statement, sort_keys=True, separators=(",", ":")) + "\n").encode()
        stmt_file = keypair_dir / "csr_pre.json"
        stmt_file.write_bytes(statement_bytes)
        sig_file = keypair_dir / "csr_pre.sig.bin"
        err = openssl_sign_raw(private, stmt_file, sig_file)
        if err:
            return refuse("IDENTITY_SIGNING_FAILED", err)

        write_text(volume / "secrets" / "node.key.pem", private.read_text(), mode=0o600)
        write_text(volume / "secrets" / "enrollment-secret.hex", secret_s.hex() + "\n", mode=0o600)
        write_text(volume / "identity" / "public_key.spki.der.b64", spki_b64 + "\n", mode=0o644)
        write_json(identity_path, {
            "schema": "carbonpanel.node-lxc.identity/v1",
            "node_id": node_id,
            "public_key_fingerprint": fingerprint,
            "created_utc": now_utc(),
            "note": "unique first-boot identity; private key lives only in volume/secrets (mode 0600)",
        })
        bundle = {
            "schema": "carbonpanel.node-lxc.enrollment-bundle/v1",
            "contract": "ADR 0019 shared node-control contract (do not fork)",
            "node_id": node_id,
            "salt_b64": statement["salt_b64"],
            "enrollment_secret_digest": digest,
            "csr_pre": statement,
            "csr_pre_signature_algorithm": "Ed25519-raw",
            "csr_pre_signature_b64": base64.b64encode(sig_file.read_bytes()).decode(),
            "approved_public_key_spki_b64": spki_b64,
            "approved_key_fingerprint": fingerprint,
            "required_capabilities": ["managed_agent", "captive_docker", "nested_runtime"],
            "protocol_version": "v1",
            "note": "digest-only registration material; no plaintext secret or private key appears in this bundle",
        }
        bundle_path = volume / "enrollment" / "pre-registration.json"
        write_json(bundle_path, bundle)

    findings = scan_secrets(volume / "enrollment")
    findings += scan_secrets(volume / "identity")
    if findings:
        return refuse("SECRET_MATERIAL_IN_METADATA", "enrollment/identity metadata contains secret material", findings=findings)
    return ok(node_id=node_id, fingerprint=fingerprint, bundle_path=str(bundle_path),
              identity_path=str(identity_path), note="scoped ADR0019 digest-only enrollment; secret stays on volume")


# --------------------------------------------------------------------------- #
# persistent volume: import / backup / restore
# --------------------------------------------------------------------------- #
def safe_rel(path: Path, root: Path) -> str | None:
    try:
        rel = path.relative_to(root).as_posix()
    except ValueError:
        return None
    if rel.startswith("/") or ".." in rel.split("/"):
        return None
    return rel


def copy_payload(src: Path, dst: Path) -> str | None:
    for p in iter_files(src):
        rel = safe_rel(p, src)
        if rel is None:
            return f"unsafe path: {p}"
        target = dst / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(p, target)
        os.chmod(target, p.stat().st_mode & 0o7777)
    return None


def cmd_volume(args: argparse.Namespace) -> int:
    action = args.action
    pub = Path(args.public_key) if args.public_key else None
    if action == "import":
        source = Path(args.source)
        volume = Path(args.volume)
        code, info = verify_bundle(source, pub, require_signature=True)
        if code:
            return verification_refusal(code, info)
        receipt = volume / ".carbon-panel" / "import-receipt.json"
        if receipt.exists() and not args.force:
            return refuse("IMPORT_ALREADY_RECORDED", "volume already has an import receipt; pass --force to replace")
        err = copy_payload(source / "rootfs", volume)
        if err:
            return refuse("UNSAFE_PAYLOAD_PATH", err)
        write_json(receipt, {"schema": "carbonpanel.node-lxc.import/v1", "source_digest_sha256": info["digest"],
                             "imported_utc": now_utc()})
        return ok(imported_digest_sha256=info["digest"], volume=str(volume))

    if action == "backup":
        volume = Path(args.volume)
        out = Path(args.out)
        if not volume.is_dir():
            return refuse("MISSING_VOLUME", f"volume not found: {volume}")
        if out.exists() and any(out.iterdir()) and not args.force:
            return refuse("OUTPUT_NOT_EMPTY", "backup output is not empty; pass --force to replace")
        payload = out / "rootfs"
        if out.exists():
            shutil.rmtree(out)
        payload.mkdir(parents=True)
        for p in iter_files(volume):
            rel = safe_rel(p, volume)
            if rel is None:
                return refuse("UNSAFE_VOLUME_PATH", f"refusing to back up unsafe path: {p}")
            if rel.startswith(".carbon-panel/tmp") or p.name.endswith(VOLATILE_SUFFIXES):
                continue
            target = payload / rel
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(p, target)
        sign_key = Path(args.sign_key) if args.sign_key else None
        integrity = write_integrity_bundle(
            out, payload, args.backup_version or "backup-v1", sign_key,
            extra={"schema_note": "volume backup; includes identity private key, store encrypted at rest",
                   "includes_secrets": True},
        )
        write_json(out / "backup.json", {
            "schema": "carbonpanel.node-lxc.backup/v1",
            "created_utc": now_utc(),
            "volume_digest_sha256": integrity["digest"],
            "signed": integrity["signed"],
            "storage_note": "controller-owned encrypted storage only; never in public artifact channels",
        })
        with tarfile.open(out / "backup.tar.gz", "w:gz") as tf:
            for p in sorted(out.rglob("*")):
                if p.name == "backup.tar.gz" or not p.is_file():
                    continue
                tf.add(p, arcname=p.relative_to(out).as_posix())
        return ok(backup_digest_sha256=integrity["digest"], signed=integrity["signed"], out=str(out))

    if action == "restore":
        backup = Path(args.backup)
        volume = Path(args.volume)
        # Backups are controller-owned encrypted storage and legitimately carry the
        # volume identity private key; unlike a rootfs template they are not a
        # provisioning artifact. Checksums and signature are still mandatory.
        code, info = verify_bundle(backup, pub, require_signature=True,
                                   require_sbom=False, allow_secrets=True)
        if code:
            return verification_refusal(code, info)
        existing = [p for p in volume.rglob("*") if p.is_file()] if volume.exists() else []
        if existing and not args.force:
            return refuse("VOLUME_NOT_EMPTY", "refusing to overwrite existing volume data; pass --force after verifying recovery intent",
                          file_count=len(existing))
        err = copy_payload(backup / "rootfs", volume)
        if err:
            return refuse("UNSAFE_PAYLOAD_PATH", err)
        write_json(volume / ".carbon-panel" / "restore-receipt.json", {
            "schema": "carbonpanel.node-lxc.restore/v1",
            "backup_digest_sha256": info["digest"],
            "restored_utc": now_utc(),
        })
        return ok(restored_digest_sha256=info["digest"], volume=str(volume))

    return refuse("UNKNOWN_VOLUME_ACTION", f"unknown volume action: {action}")


# --------------------------------------------------------------------------- #
# repeatable artifact upgrade / rollback with staged slots
# --------------------------------------------------------------------------- #
def upgrade_state_path(volume: Path) -> Path:
    return volume / ".carbon-panel" / "upgrade-state.json"


def cmd_upgrade(args: argparse.Namespace) -> int:
    volume = Path(args.volume)
    state_path = upgrade_state_path(volume)
    pub = Path(args.public_key) if args.public_key else None
    action = args.action

    if action == "status":
        if not state_path.is_file():
            return refuse("NO_UPGRADE_STATE", "no upgrade state recorded for this volume")
        state = read_json(state_path)
        return ok(current=state.get("current"), previous=state.get("previous"),
                  current_version=(state.get("slots", {}).get(state.get("current"), {}).get("version")),
                  slots=list(state.get("slots", {}).keys()))

    if action == "apply":
        artifact = Path(args.artifact)
        code, info = verify_bundle(artifact, pub, require_signature=True)
        if code:
            return verification_refusal(code, info)
        state = read_json(state_path) if state_path.is_file() else {
            "schema": "carbonpanel.node-lxc.upgrade/v1", "current": None, "previous": None, "slots": {},
        }
        target_slot = "b" if state.get("current") == "a" else "a"
        slot_dir = volume / ".carbon-panel" / "slots" / target_slot
        if slot_dir.exists():
            shutil.rmtree(slot_dir)
        slot_dir.mkdir(parents=True)
        err = copy_payload(artifact, slot_dir)
        if err:
            return refuse("UNSAFE_PAYLOAD_PATH", err)
        manifest = info["manifest"]
        new_state = dict(state)
        new_state["slots"] = dict(state.get("slots", {}))
        new_state["slots"][target_slot] = {
            "version": manifest.get("artifact_version"),
            "digest_sha256": info["digest"],
            "applied_utc": now_utc(),
        }
        new_state["previous"] = state.get("current")
        new_state["current"] = target_slot
        write_json(state_path, new_state)
        return ok(current=target_slot, previous=new_state["previous"],
                  version=manifest.get("artifact_version"), digest_sha256=info["digest"])

    if action == "rollback":
        if not state_path.is_file():
            return refuse("NO_UPGRADE_STATE", "no upgrade state recorded for this volume")
        state = read_json(state_path)
        prev = state.get("previous")
        if not prev:
            return refuse("NO_PREVIOUS_SLOT", "no previous slot is available for rollback")
        slot_dir = volume / ".carbon-panel" / "slots" / prev
        if not slot_dir.is_dir():
            return refuse("PREVIOUS_SLOT_MISSING", "the previous slot payload is missing")
        code, info = verify_bundle(slot_dir, pub, require_signature=True)
        if code:
            return verification_refusal(code, info)
        new_state = dict(state)
        new_state["previous"] = state.get("current")
        new_state["current"] = prev
        write_json(state_path, new_state)
        return ok(current=prev, version=info["manifest"].get("artifact_version"),
                  digest_sha256=info["digest"], note="rollback switched to the verified previous slot")

    return refuse("UNKNOWN_UPGRADE_ACTION", f"unknown upgrade action: {action}")


# --------------------------------------------------------------------------- #
# entrypoint
# --------------------------------------------------------------------------- #
def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    b = sub.add_parser("build")
    b.add_argument("--source-dir", required=True)
    b.add_argument("--out-dir", required=True)
    b.add_argument("--provenance", default=str(Path(__file__).parent / "config" / "provenance.json"))
    b.add_argument("--version", required=True)
    b.add_argument("--platform", required=True)
    b.add_argument("--sign-key")
    b.add_argument("--base-tarball")
    b.add_argument("--fixture", action="store_true")
    b.add_argument("--force", action="store_true")
    b.set_defaults(func=cmd_build)

    vp = sub.add_parser("validate-provenance")
    vp.add_argument("--provenance", default=str(Path(__file__).parent / "config" / "provenance.json"))
    vp.add_argument("--expect-profile", choices=(ARTIFACT_PROFILE, FIXTURE_PROFILE), default=ARTIFACT_PROFILE)
    vp.set_defaults(func=cmd_validate_provenance)

    v = sub.add_parser("verify")
    v.add_argument("--artifact-dir", required=True)
    v.add_argument("--public-key")
    v.add_argument("--expect-version")
    v.add_argument("--allow-unsigned", action="store_true")
    v.set_defaults(func=cmd_verify)

    c = sub.add_parser("capability-gate")
    c.add_argument("--capability-file")
    c.set_defaults(func=cmd_capability_gate)

    i = sub.add_parser("first-boot-identity")
    i.add_argument("--volume", required=True)
    i.add_argument("--node-label", required=True)
    i.add_argument("--force", action="store_true")
    i.set_defaults(func=cmd_first_boot_identity)

    vol = sub.add_parser("volume")
    vol.add_argument("action", choices=("import", "backup", "restore"))
    vol.add_argument("--source")
    vol.add_argument("--volume", required=True)
    vol.add_argument("--out")
    vol.add_argument("--backup")
    vol.add_argument("--public-key")
    vol.add_argument("--sign-key")
    vol.add_argument("--backup-version")
    vol.add_argument("--force", action="store_true")
    vol.set_defaults(func=cmd_volume)

    up = sub.add_parser("upgrade")
    up.add_argument("action", choices=("status", "apply", "rollback"))
    up.add_argument("--volume", required=True)
    up.add_argument("--artifact")
    up.add_argument("--public-key")
    up.set_defaults(func=cmd_upgrade)

    sa = sub.add_parser("verify-signed-archive")
    sa.add_argument("--provenance", default=str(Path(__file__).parent / "config" / "provenance.json"))
    sa.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    sa.add_argument("--work-dir", required=True)
    sa.add_argument("--fetch", action="store_true",
                    help="direct-GET the pinned artifacts into --work-dir; otherwise verify files already staged there")
    sa.set_defaults(func=cmd_verify_signed_archive)

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return int(args.func(args))
    except Exception as exc:  # pragma: no cover - hardened top-level guard
        return refuse("INTERNAL_ERROR", f"{type(exc).__name__}: {exc}")


if __name__ == "__main__":
    sys.exit(main())
