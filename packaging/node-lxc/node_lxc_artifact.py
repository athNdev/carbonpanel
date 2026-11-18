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
# build
# --------------------------------------------------------------------------- #
def cmd_build(args: argparse.Namespace) -> int:
    version = args.version
    if not VERSION_RE.match(version or ""):
        return refuse("INVALID_VERSION", "version must be runtime-vMAJOR.MINOR.PATCH")
    prov_path = Path(args.provenance)
    if not prov_path.is_file():
        return refuse("MISSING_PROVENANCE", f"provenance file not found: {prov_path}")
    prov = read_json(prov_path)
    if prov.get("schema") != PROVENANCE_SCHEMA:
        return refuse("INVALID_PROVENANCE", "unsupported provenance schema")
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
        err = check_sha256(expected, "fixture.manifest_sha256")
        if err is not None:
            return err
        if computed != expected:
            return refuse("PROVENANCE_DIGEST_MISMATCH", "fixture rootfs digest does not match pinned provenance",
                          expected=expected, computed=computed)
        mode = "offline-fixture"
        base_ref = prov["fixture"].get("description", "synthetic fixture rootfs")
    else:
        tarball = Path(args.base_tarball) if args.base_tarball else None
        if tarball is None or not tarball.is_file():
            return refuse("MISSING_BASE_TARBALL", "production build requires --base-tarball")
        err = check_sha256(prov["base"].get("upstream_digest_sha256"), "base.upstream_digest_sha256")
        if err is not None:
            return err
        if sha256_file(tarball) != prov["base"]["upstream_digest_sha256"]:
            return refuse("UPSTREAM_DIGEST_MISMATCH", "base tarball does not match pinned upstream digest")
        mode = "pinned-upstream"
        base_ref = prov["base"]["upstream_ref"]

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

    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return int(args.func(args))
    except Exception as exc:  # pragma: no cover - hardened top-level guard
        return refuse("INTERNAL_ERROR", f"{type(exc).__name__}: {exc}")


if __name__ == "__main__":
    sys.exit(main())
