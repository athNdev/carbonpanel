#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""OSS-415: build and verify an importable, unprivileged amd64 system CT rootfs
for the Carbon Panel CONTROLLER.

Offline by default. Real upstream bytes (pinned Ubuntu rootfs, signed Docker
key and package receipt) are *inputs* checked here; downloads and chroot work
belong to the owned disposable runner in the workflow. Nothing in this module
publishes, provisions a host, or touches a host Docker socket.
"""
import argparse
import ctypes
import hashlib
import io
import json
import os
import posixpath
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
LANE = ROOT / "packaging" / "proxmox-controller-ct"
MANIFEST = LANE / "manifest.json"
PACKAGES = LANE / "docker-packages.json"
FIRSTBOOT = LANE / "firstboot"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
VERSION = re.compile(r"^controller-ct-v\d+\.\d+\.\d+$")
PLACEHOLDER = re.compile(r"^(changeme|todo|placeholder|xxx+|0{8,})$", re.I)
# Exact path/basename patterns only. Substring matching wrongly rejected
# legitimate shipped assets such as systemd.environment-generator.7.gz.
FORBIDDEN_SOCKETS = frozenset({"./var/run/docker.sock", "./run/docker.sock"})
FORBIDDEN_PATHS = frozenset({
    "./etc/machine-id", "./var/lib/dbus/machine-id",
    "./var/lib/carbon-node/enrollment/enrollment-secret.bin",
    "./var/lib/carbon-panel/enrollment/enrollment-secret.bin",
})
FORBIDDEN_BASENAMES = frozenset({
    "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
    "enrollment-secret.bin", "enrollment_secret.bin",
    "seed-manifest.json", "level.dat",
})
SSH_HOST_KEY = re.compile(r"^ssh_host_(rsa|dsa|ecdsa|ed25519)_key$")
REQUIRED = (
    "./usr/lib/os-release",
    "./etc/systemd/system/carbon-panel-ct-firstboot.service",
    "./usr/local/sbin/carbon-panel-ct-firstboot",
    "./usr/local/share/carbon-panel/install-carbon-panel.sh",
    "./usr/local/share/carbon-panel/controller-image.txt",
    "./usr/share/carbon-panel/metadata.json",
    "./usr/share/carbon-panel/docker-package-receipt.json",
)
METADATA = "./usr/share/carbon-panel/metadata.json"
RECEIPT = "./usr/share/carbon-panel/docker-package-receipt.json"
IMAGE_PIN = "./usr/local/share/carbon-panel/controller-image.txt"
UNIT = "./etc/systemd/system/carbon-panel-ct-firstboot.service"
WANTS_LINK = "./etc/systemd/system/multi-user.target.wants/carbon-panel-ct-firstboot.service"


class CtError(RuntimeError):
    pass


def sha256_file(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def load_manifest(path=None):
    m = json.loads(Path(path or MANIFEST).read_text(encoding="utf-8"))
    if m.get("schema") != "carbonpanel.proxmox-controller-ct.manifest/v1":
        raise CtError("unexpected manifest schema")
    if m.get("version_domain") != "controller-ct-v":
        raise CtError("artifact must use the independent controller-ct-v domain")
    if m["base"].get("distro") != "debian":
        raise CtError("unsupported base profile")
    image = m["controller"].get("image", "")
    if "@sha256:" not in image or ":latest" in image:
        raise CtError("controller image must be name@sha256 digest, never a moving tag")
    digest = image.split("@sha256:", 1)[1]
    if SHA256.fullmatch(digest) is None:
        raise CtError("controller image digest is not 64-hex")
    if digest != m["controller"]["ghcr_digest"].removeprefix("sha256:"):
        raise CtError("Docker Hub and GHCR digests disagree")
    if m["runtime"]["apt_key_fingerprint"].replace(" ", "").upper() != "9DC858229FC7DD38854AE2D88D81803C0EBFCD88":
        raise CtError("unexpected Docker signing key fingerprint")
    return m


def verify_manifest_against_repo(manifest=None):
    """Fail closed unless the manifest agrees with checked-in trusted sources."""
    m = manifest or load_manifest()
    versions = json.loads((ROOT / "docs" / "version-index.json").read_text(encoding="utf-8"))
    stable = versions["stable"]
    c = m["controller"]
    for key in ("version", "commit"):
        if c[key] != stable[key]:
            raise CtError("controller %s disagrees with version-index.json" % key)
    if stable["digest"].removeprefix("sha256:") not in c["image"]:
        raise CtError("controller digest disagrees with version-index.json")
    if c["ghcr_digest"] != stable["digest"]:
        raise CtError("GHCR digest disagrees with canonical Stable contract")
    if SHA256.fullmatch(c["amd64_manifest_digest"].removeprefix("sha256:")) is None:
        raise CtError("amd64 manifest digest is not a full 64-hex digest")
    base = m["base"]
    if base["distro"] != "debian" or base["suite"] != "trixie" or base["arch"] != "amd64":
        raise CtError("base must be the signed Debian 13 amd64 trixie archive")
    if base["archive_url"] != "https://deb.debian.org/debian" or "--force-check-gpg" not in base["sig_verification"]:
        raise CtError("base archive must be signature-verified by debootstrap")
    if m["runtime"]["apt_repo"] != "https://download.docker.com/linux/debian" or m["runtime"]["apt_suite"] != "trixie":
        raise CtError("Docker upstream repo must match the Debian base suite")
    return {
        "status": "PASS",
        "controller_version": c["version"],
        "controller_commit": c["commit"],
        "controller_digest": stable["digest"],
        "base_suite": base["suite"],
        "base_sig_verification": base["sig_verification"],
    }


# --- zstd via libzstd (no installs, no external tool dependency) -------------
_zstd = None


def _zlib():
    global _zstd
    if _zstd is None:
        lib = ctypes.CDLL("libzstd.so.1")
        lib.ZSTD_compressBound.argtypes = [ctypes.c_size_t]
        lib.ZSTD_compressBound.restype = ctypes.c_size_t
        lib.ZSTD_compress.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p, ctypes.c_size_t, ctypes.c_int]
        lib.ZSTD_compress.restype = ctypes.c_size_t
        lib.ZSTD_isError.argtypes = [ctypes.c_size_t]
        lib.ZSTD_isError.restype = ctypes.c_uint
        lib.ZSTD_getFrameContentSize.argtypes = [ctypes.c_void_p, ctypes.c_size_t]
        lib.ZSTD_getFrameContentSize.restype = ctypes.c_ulonglong
        lib.ZSTD_decompress.argtypes = [ctypes.c_void_p, ctypes.c_size_t, ctypes.c_void_p, ctypes.c_size_t]
        lib.ZSTD_decompress.restype = ctypes.c_size_t
        _zstd = lib
    return _zstd


def zstd_compress(data, level=3):
    lib = _zlib()
    bound = lib.ZSTD_compressBound(len(data))
    out = ctypes.create_string_buffer(bound)
    src = ctypes.create_string_buffer(data, len(data))
    n = lib.ZSTD_compress(out, bound, src, len(data), level)
    if lib.ZSTD_isError(n):
        raise CtError("zstd compression failed")
    return out.raw[:n]


def zstd_decompress(data, max_bytes=4 * 1024**3):
    lib = _zlib()
    src = ctypes.create_string_buffer(data, len(data))
    size = lib.ZSTD_getFrameContentSize(src, len(data))
    if size in (0xFFFFFFFFFFFFFFFF, 0xFFFFFFFFFFFFFFFE) or size > max_bytes:
        raise CtError("zstd frame size missing or too large")
    out = ctypes.create_string_buffer(size)
    n = lib.ZSTD_decompress(out, size, src, len(data))
    if lib.ZSTD_isError(n):
        raise CtError("zstd decompression failed")
    return out.raw[:n]


class _InBuffer(ctypes.Structure):
    _fields_ = [("src", ctypes.c_void_p), ("size", ctypes.c_size_t), ("pos", ctypes.c_size_t)]


class _OutBuffer(ctypes.Structure):
    _fields_ = [("dst", ctypes.c_void_p), ("size", ctypes.c_size_t), ("pos", ctypes.c_size_t)]


def _stream_lib():
    lib = _zlib()
    lib.ZSTD_createCStream.restype = ctypes.c_void_p
    lib.ZSTD_CStreamInSize.restype = ctypes.c_size_t
    lib.ZSTD_CStreamOutSize.restype = ctypes.c_size_t
    lib.ZSTD_initCStream.argtypes = [ctypes.c_void_p, ctypes.c_int]
    lib.ZSTD_initCStream.restype = ctypes.c_size_t
    lib.ZSTD_compressStream.argtypes = [ctypes.c_void_p, ctypes.POINTER(_OutBuffer), ctypes.POINTER(_InBuffer)]
    lib.ZSTD_compressStream.restype = ctypes.c_size_t
    lib.ZSTD_endStream.argtypes = [ctypes.c_void_p, ctypes.POINTER(_OutBuffer)]
    lib.ZSTD_endStream.restype = ctypes.c_size_t
    lib.ZSTD_freeCStream.argtypes = [ctypes.c_void_p]
    lib.ZSTD_freeCStream.restype = ctypes.c_size_t
    lib.ZSTD_createDStream.restype = ctypes.c_void_p
    lib.ZSTD_DStreamInSize.restype = ctypes.c_size_t
    lib.ZSTD_DStreamOutSize.restype = ctypes.c_size_t
    lib.ZSTD_initDStream.argtypes = [ctypes.c_void_p]
    lib.ZSTD_initDStream.restype = ctypes.c_size_t
    lib.ZSTD_decompressStream.argtypes = [ctypes.c_void_p, ctypes.POINTER(_OutBuffer), ctypes.POINTER(_InBuffer)]
    lib.ZSTD_decompressStream.restype = ctypes.c_size_t
    lib.ZSTD_freeDStream.argtypes = [ctypes.c_void_p]
    lib.ZSTD_freeDStream.restype = ctypes.c_size_t
    return lib


def _check_zstd(lib, value):
    if lib.ZSTD_isError(value):
        raise CtError("zstd stream error: " + lib.ZSTD_getErrorName(value).decode(errors="replace"))


def zstd_compress_file(src, dst, level=3):
    """Stream a file through libzstd (standard zstd frames), bounded memory."""
    lib = _stream_lib()
    cstream = lib.ZSTD_createCStream()
    if not cstream:
        raise CtError("cannot create zstd stream")
    in_size = lib.ZSTD_CStreamInSize()
    out_size = lib.ZSTD_CStreamOutSize()
    try:
        _check_zstd(lib, lib.ZSTD_initCStream(cstream, level))
        with Path(src).open("rb") as fi, Path(dst).open("wb") as fo:
            while True:
                chunk = fi.read(in_size)
                if not chunk:
                    break
                buf = ctypes.create_string_buffer(chunk, len(chunk))
                ib = _InBuffer(ctypes.cast(buf, ctypes.c_void_p), len(chunk), 0)
                while ib.pos < ib.size:
                    out = ctypes.create_string_buffer(out_size)
                    ob = _OutBuffer(ctypes.cast(out, ctypes.c_void_p), out_size, 0)
                    _check_zstd(lib, lib.ZSTD_compressStream(cstream, ctypes.byref(ob), ctypes.byref(ib)))
                    if ob.pos:
                        fo.write(out.raw[:ob.pos])
            while True:
                out = ctypes.create_string_buffer(out_size)
                ob = _OutBuffer(ctypes.cast(out, ctypes.c_void_p), out_size, 0)
                remaining = lib.ZSTD_endStream(cstream, ctypes.byref(ob))
                _check_zstd(lib, remaining)
                if ob.pos:
                    fo.write(out.raw[:ob.pos])
                if remaining == 0:
                    break
    finally:
        lib.ZSTD_freeCStream(cstream)


def write_tar_zst(root, out_path, level=3):
    """Produce a Proxmox-importable ./etc ./usr ./sbin rootfs tar.zst.

    GNU tar preserves numeric ownership, modes, symlinks, hard links, xattrs
    and ACLs; the tar stream is compressed with libzstd. No Python re-encoding
    of metadata and no top-level wrapper directory.
    """
    out_path = Path(out_path)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        tar_path = Path(tmp) / "rootfs.tar"
        cmd = [
            "tar", "--numeric-owner", "--acls", "--xattrs", "--format=posix",
            "--sort=name",
            "--exclude=./dev/*", "--exclude=./proc/*", "--exclude=./sys/*", "--exclude=./run/*",
            "--exclude=./work", "--exclude=./prepare.sh", "--exclude=./run-prepare.sh",
            "-cf", str(tar_path), "-C", str(root), ".",
        ]
        subprocess.run(cmd, check=True)
        zstd_compress_file(tar_path, out_path, level=level)
    return out_path


def zstd_decompress_file(src, dst):
    lib = _stream_lib()
    dstream = lib.ZSTD_createDStream()
    if not dstream:
        raise CtError("cannot create zstd stream")
    in_size = lib.ZSTD_DStreamInSize()
    out_size = lib.ZSTD_DStreamOutSize()
    try:
        _check_zstd(lib, lib.ZSTD_initDStream(dstream))
        with Path(src).open("rb") as fi, Path(dst).open("wb") as fo:
            while True:
                chunk = fi.read(in_size)
                if not chunk:
                    break
                buf = ctypes.create_string_buffer(chunk, len(chunk))
                ib = _InBuffer(ctypes.cast(buf, ctypes.c_void_p), len(chunk), 0)
                while ib.pos < ib.size:
                    out = ctypes.create_string_buffer(out_size)
                    ob = _OutBuffer(ctypes.cast(out, ctypes.c_void_p), out_size, 0)
                    _check_zstd(lib, lib.ZSTD_decompressStream(dstream, ctypes.byref(ob), ctypes.byref(ib)))
                    if ob.pos:
                        fo.write(out.raw[:ob.pos])
    finally:
        lib.ZSTD_freeDStream(dstream)


def archive_members(archive_path, checksum_path=None, read_content=True):
    """Stream-check the archive checksum, then read only small metadata files.
    Ownership metadata is returned for every member without extracting files."""
    archive = Path(archive_path)
    if checksum_path:
        expected = Path(checksum_path).read_text(encoding="utf-8").split()[0]
        if not SHA256.fullmatch(expected) or expected != sha256_file(archive):
            raise CtError("archive checksum mismatch")
    with tempfile.TemporaryDirectory() as tmp:
        tar_path = Path(tmp) / "archive.tar"
        zstd_decompress_file(archive, tar_path)
        out, ownership, members_seen, symlinks, links = {}, {}, [], set(), {}
        wanted = set(REQUIRED)
        with tarfile.open(tar_path, mode="r|", format=tarfile.PAX_FORMAT) as tf:
            for member in tf:
                check_member_name(member.name)
                if member.issym():
                    check_link_target(member, is_symlink=True)
                    symlinks.add(member.name)
                    links[member.name] = member.linkname
                elif member.islnk():
                    check_link_target(member, is_symlink=False)
                    links[member.name] = member.linkname
                ownership[member.name] = (member.uid, member.gid, member.mode, member.uname, member.gname)
                members_seen.append(member.name)
                if member.isdir() or member.issym() or member.islnk():
                    continue
                if not member.isfile():
                    raise CtError("unsupported archive member: " + member.name)
                if read_content and member.name in wanted:
                    f = tf.extractfile(member)
                    out[member.name] = f.read() if f else b""
        check_symlink_ancestors(members_seen, symlinks)
        return out, ownership, members_seen, links


def check_link_target(member, is_symlink):
    """Validate a link while preserving normal Debian link conventions.

    Absolute symlink targets are guest-root-relative (/usr/sbin/... and
    /dev/null are legitimate). Hardlink targets are archive-root-relative and
    must never be absolute or traverse out of the root.
    """
    target = member.linkname
    if is_symlink:
        if target.startswith("/"):
            resolved = posixpath.normpath(target.lstrip("/"))
        else:
            resolved = posixpath.normpath(
                posixpath.join(posixpath.dirname(member.name), target))
    else:
        if target.startswith("/"):
            raise CtError("absolute hardlink target: " + member.name)
        resolved = posixpath.normpath(target)
    if resolved in ("", ".", "..") or resolved.startswith("../"):
        raise CtError("link escapes archive root: %s -> %s" % (member.name, target))
    return resolved


def check_symlink_ancestors(members, symlinks):
    for name in members:
        parts = name.split("/")
        for i in range(1, len(parts)):
            if "/".join(parts[:i]) in symlinks:
                raise CtError("archive member under symlink ancestor: " + name)


def check_member_name(name):
    """Single exact-pattern validator shared by every archive check."""
    if name.startswith("/") or ".." in name.split("/"):
        raise CtError("unsafe archive path: " + name)
    if name.startswith("rootfs/"):
        raise CtError("rootfs wrapper is not importable: " + name)
    base = posixpath.basename(name.lower())
    if base == ".env" or (base.startswith(".env.") and len(base) > len(".env.")):
        raise CtError("forbidden path in CT archive: " + name)
    if (name in FORBIDDEN_SOCKETS or name in FORBIDDEN_PATHS
            or base in FORBIDDEN_BASENAMES or SSH_HOST_KEY.fullmatch(base)):
        raise CtError("forbidden path in CT archive: " + name)


def check_socket_path(name):
    """Compatibility wrapper; exact socket paths are covered by check_member_name."""
    check_member_name(name)


def check_no_forbidden(members):
    for name in members:
        check_member_name(name)


def verify_unit(unit_text):
    for required in (
        "Type=oneshot",
        "Requires=docker.service",
        "After=network-online.target docker.service",
        "Wants=network-online.target",
        "Restart=on-failure",
        "TimeoutStartSec=900",
    ):
        if required not in unit_text:
            raise CtError("first-boot unit missing: " + required)
    for bad in ("AppArmorProfile=unconfined", "Privileged=true", "PrivateDevices=false"):
        if bad in unit_text:
            raise CtError("first-boot unit contains unsafe setting: " + bad)


def verify_archive(archive_path, checksum_path, manifest=None, require_real=False):
    m = manifest or load_manifest()
    members, ownership, seen, links = archive_members(archive_path, checksum_path)
    # Proxmox imports the tar root as the container rootfs: ./etc ./usr ./sbin.
    for root in ("./etc/", "./usr/", "./var/"):
        if not any(name.startswith(root) for name in seen):
            raise CtError("archive root shape is not a system rootfs: " + root)
    for name in REQUIRED:
        if name not in members:
            raise CtError("missing archive member: " + name)
    if "./etc/os-release" not in seen:
        raise CtError("guest /etc/os-release is missing")
    os_release = members["./usr/lib/os-release"].decode(errors="replace")
    if "debian" not in os_release.lower() or "13" not in os_release or "trixie" not in os_release.lower():
        raise CtError("guest OS is not the pinned Debian 13 trixie rootfs")
    if not any("/usr/share/doc/" in name and "copyright" in name for name in seen):
        raise CtError("upstream package copyright metadata was removed")
    if not any(n in ("./lib/systemd/systemd", "./usr/lib/systemd/systemd") for n in seen):
        raise CtError("guest init (systemd) is missing")
    if not any(n.endswith("/dockerd") for n in seen):
        raise CtError("guest dockerd is missing")
    if not any("docker-compose" in n for n in seen):
        raise CtError("guest Compose v2 plugin is missing")
    metadata = json.loads(members[METADATA])
    if metadata.get("schema") != "carbonpanel.proxmox-controller-ct.archive/v1":
        raise CtError("unexpected archive metadata schema")
    if metadata.get("version_domain") != "controller-ct-v":
        raise CtError("archive version domain mismatch")
    image = members[IMAGE_PIN].decode().strip()
    if image != m["controller"]["image"]:
        raise CtError("baked controller image is not the verified digest pin")
    verify_unit(members[UNIT].decode())
    if WANTS_LINK not in links:
        raise CtError("firstboot unit is not enabled (multi-user.target.wants symlink missing)")
    if links[WANTS_LINK].strip() not in (
        "../carbon-panel-ct-firstboot.service",
        "/etc/systemd/system/carbon-panel-ct-firstboot.service",
    ):
        raise CtError("unexpected firstboot enable symlink target: " + links[WANTS_LINK])
    receipt = json.loads(members[RECEIPT])
    packages = json.loads(PACKAGES.read_text(encoding="utf-8"))
    if require_real:
        if receipt.get("fixture") is not False or receipt.get("publishable") is not True:
            raise CtError("write path requires a non-fixture, publishable receipt")
        if receipt.get("base_suite") != m["base"]["suite"]:
            raise CtError("receipt base suite does not match the pinned Debian base")
        if SHA256.fullmatch(str(receipt.get("base_release_sha256", ""))) is None:
            raise CtError("signed Debian InRelease digest missing from receipt")
    if not receipt.get("signed_repo_required"):
        raise CtError("Docker packages were not sourced from a signed repo")
    if receipt.get("key_fingerprint") != packages["key_fingerprint"]:
        raise CtError("Docker key fingerprint mismatch in receipt")
    expected = set(packages["packages"])
    if set(receipt.get("packages", [])) != expected:
        raise CtError("Docker package set mismatch in receipt")
    if not receipt.get("fixture"):
        for field in ("package_sha256", "package_versions"):
            details = receipt.get(field) or {}
            if set(details) != expected or any(not str(v).strip() for v in details.values()):
                raise CtError("real receipt has incomplete " + field)
        if any(SHA256.fullmatch(str(v)) is None
               for v in receipt["package_sha256"].values()):
            raise CtError("real receipt needs exact per-package SHA-256 values")
    return {
        "status": "PASS",
        "members": len(members),
        "owned_entries": len(ownership),
        "image": image,
        "fixture": bool(receipt.get("fixture")),
    }


def build_rootfs(staging, manifest=None, fixture=False, base_archive=None,
                 docker_key=None, package_receipt=None, prepared_rootfs=None):
    """Assemble a rootfs staging tree from verified inputs. No downloads here."""
    m = manifest or load_manifest()
    if prepared_rootfs:
        # Single owned rootfs: never copytree (it follows links and drops
        # ownership/specials). The runner chowns this tree before this step.
        staging = Path(prepared_rootfs)
    else:
        staging = Path(staging)
        if staging.exists():
            shutil.rmtree(staging)
        staging.mkdir(parents=True)
    # ReadWritePaths in the shipped unit must exist in the rootfs.
    (staging / "opt" / "carbon-panel").mkdir(parents=True, exist_ok=True)
    (staging / "var" / "lib" / "carbon-panel-ct").mkdir(parents=True, exist_ok=True)
    (staging / "var" / "log").mkdir(parents=True, exist_ok=True)
    if fixture:
        (staging / "etc" / "systemd" / "system").mkdir(parents=True)
        (staging / "usr" / "local" / "sbin").mkdir(parents=True)
        (staging / "usr" / "local" / "share" / "carbon-panel").mkdir(parents=True)
        (staging / "etc" / "apt" / "keyrings").mkdir(parents=True)
        (staging / "usr" / "bin").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "lib" / "systemd").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "libexec" / "docker" / "cli-plugins").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "lib").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "lib" / "os-release").write_text(
            'ID=debian\nVERSION_ID="13"\nVERSION_CODENAME=trixie\nPRETTY_NAME="Debian GNU/Linux 13 (trixie)"\n',
            encoding="utf-8")
        if not (staging / "etc" / "os-release").exists():
            (staging / "etc" / "os-release").symlink_to("../usr/lib/os-release")
        (staging / "usr" / "share" / "doc" / "base-files").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "share" / "doc" / "base-files" / "copyright").write_text(
            "Upstream copyright metadata retained\n", encoding="utf-8")
        (staging / "etc" / "apt" / "keyrings" / "docker.gpg").write_bytes(b"fixture-key")
        (staging / "usr" / "lib" / "systemd" / "systemd").write_bytes(b"fixture-init")
        (staging / "usr" / "bin" / "dockerd").write_bytes(b"fixture-dockerd")
        (staging / "usr" / "libexec" / "docker" / "cli-plugins" / "docker-compose").write_bytes(b"fixture-compose")
        man7 = staging / "usr" / "share" / "man" / "man7"
        man7.mkdir(parents=True, exist_ok=True)
        (man7 / "systemd.environment-generator.7.gz").write_bytes(b"fixture-manpage")
        # Normal Debian rootfs links must survive packaging: relative,
        # guest-root-absolute (/usr/sbin/...), and systemd enable links.
        (staging / "bin").symlink_to("usr/bin")
        (staging / "usr" / "sbin").mkdir(parents=True, exist_ok=True)
        (staging / "usr" / "sbin" / "arptables").symlink_to("/usr/sbin/xtables-nft-multi")
        wants = staging / "etc" / "systemd" / "system" / "multi-user.target.wants"
        wants.mkdir(parents=True, exist_ok=True)
        (wants / "carbon-panel-ct-firstboot.service").symlink_to(
            "../carbon-panel-ct-firstboot.service")
        receipt = {
            "schema": "carbonpanel.proxmox-controller-ct.docker-receipt/v1",
            "signed_repo_required": True,
            "key_fingerprint": m["runtime"]["apt_key_fingerprint"],
            "packages": json.loads(PACKAGES.read_text(encoding="utf-8"))["packages"],
            "fixture": True,
            "publishable": False,
        }
    else:
        if prepared_rootfs:
            if not Path(prepared_rootfs).is_dir():
                raise CtError("prepared rootfs directory is missing")
        else:
            if not base_archive or sha256_file(base_archive) != m["base"]["sha256"]:
                raise CtError("base rootfs archive missing or hash mismatch")
            subprocess.run(["tar", "-xJf", str(base_archive), "-C", str(staging)], check=True)
        if not docker_key or not docker_key.is_file():
            raise CtError("verified Docker signing key is required")
        if not package_receipt:
            raise CtError("signed package receipt is required")
        receipt = json.loads(Path(package_receipt).read_text(encoding="utf-8"))
        if receipt.get("fixture") or receipt.get("publishable") is False:
            raise CtError("fixture receipt cannot build a publishable CT")
        # Remove build-only and host-derived state; Proxmox repopulates /dev
        # and guest networking regenerates resolv.conf and host keys.
        for rel in ("work", "prepare.sh", "run-prepare.sh", "etc/resolv.conf",
                    "etc/machine-id", "var/lib/dbus/machine-id", "var/lib/apt/lists",
                    "var/cache/apt/archives"):
            p = staging / rel
            if p.is_symlink() or p.is_file():
                p.unlink(missing_ok=True)
            elif p.is_dir():
                shutil.rmtree(p, ignore_errors=True)
        for host_key in (staging / "etc" / "ssh").glob("ssh_host_*"):
            host_key.unlink(missing_ok=True)
        dev = staging / "dev"
        if dev.is_dir():
            for node in dev.iterdir():
                if node.is_char_device() or node.is_block_device():
                    node.unlink(missing_ok=True)
        (staging / "etc" / "apt" / "keyrings").mkdir(parents=True, exist_ok=True)
        shutil.copy2(docker_key, staging / "etc" / "apt" / "keyrings" / "docker.gpg")
    (staging / "etc" / "systemd" / "system").mkdir(parents=True, exist_ok=True)
    (staging / "usr" / "local" / "sbin").mkdir(parents=True, exist_ok=True)
    (staging / "usr" / "local" / "share" / "carbon-panel").mkdir(parents=True, exist_ok=True)
    shutil.copy2(FIRSTBOOT / "carbon-panel-ct-firstboot.sh", staging / "usr" / "local" / "sbin" / "carbon-panel-ct-firstboot")
    (staging / "usr" / "local" / "sbin" / "carbon-panel-ct-firstboot").chmod(0o755)
    shutil.copy2(ROOT / "scripts" / "proxmox" / "install-carbon-panel.sh",
                 staging / "usr" / "local" / "share" / "carbon-panel" / "install-carbon-panel.sh")
    (staging / "usr" / "local" / "share" / "carbon-panel" / "controller-image.txt").write_text(
        m["controller"]["image"] + "\n", encoding="utf-8")
    shutil.copy2(FIRSTBOOT / "carbon-panel-ct-firstboot.service",
                 staging / "etc" / "systemd" / "system" / "carbon-panel-ct-firstboot.service")
    receipt_path = staging.parent / "docker-package-receipt.json"
    receipt_path.write_text(json.dumps(receipt, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {"staging": str(staging), "fixture": fixture, "receipt": str(receipt_path)}


def make_archive(staging, out_path, version, manifest=None):
    m = manifest or load_manifest()
    if not VERSION.fullmatch(version):
        raise CtError("version must use the controller-ct-vMAJOR.MINOR.PATCH domain")
    out_path = Path(out_path)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    receipt_dir = Path(staging).parent
    receipt_path = receipt_dir / "docker-package-receipt.json"
    if not receipt_path.is_file():
        raise CtError("build receipt is missing; run build first")
    # Build metadata lives inside the rootfs, never as a top-level wrapper.
    meta_dir = Path(staging) / "usr" / "share" / "carbon-panel"
    meta_dir.mkdir(parents=True, exist_ok=True)
    (meta_dir / "metadata.json").write_text(json.dumps({
        "schema": "carbonpanel.proxmox-controller-ct.archive/v1",
        "version_domain": "controller-ct-v",
        "version": version,
        "artifact_family": m["artifact_family"],
        "controller_image": m["controller"]["image"],
    }, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    shutil.copy2(receipt_path, meta_dir / "docker-package-receipt.json")
    write_tar_zst(staging, out_path)
    checksum = Path(str(out_path) + ".sha256")
    checksum.write_text("%s  %s\n" % (sha256_file(out_path), out_path.name), encoding="utf-8")
    return {"archive": str(out_path), "checksum": str(checksum), "sha256": sha256_file(out_path)}


def run_firstboot_fixture(firstboot_source, installer_source, image):
    """Inert first-boot contract test with stub docker/installer in temp dirs."""
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        share = root / "share"
        state = root / "state"
        install = root / "install"
        share.mkdir(); state.mkdir(); install.mkdir()
        script = (firstboot_source).read_text(encoding="utf-8")
        script = (script.replace("/usr/local/share/carbon-panel", str(share))
                        .replace("/opt/carbon-panel", str(install))
                        .replace("/var/lib/carbon-panel-ct", str(state)))
        dest = root / "firstboot.sh"
        dest.write_text(script, encoding="utf-8")
        dest.chmod(0o755)
        (share / "controller-image.txt").write_text(image + "\n", encoding="utf-8")
        # Stateful stub mirrors the real installer: compose + marker are written
        # before pull; a failed pull leaves the marker, and the next invocation
        # resumes pull + up before clearing it.
        service_state = root / "service.state"
        (share / "install-carbon-panel.sh").write_text(
            '#!/usr/bin/env bash\n'
            'set -euo pipefail\n'
            'dir="$3"; image="$5"\n'
            'mkdir -p "$dir/data" "$dir/backups" "$dir/tmp"\n'
            'if [ -f "$dir/.carbon-panel-install-owned" ]; then\n'
            '  echo "pull carbon-panel" >>"$DOCKER_STUB_LOG"\n'
            '  echo "up -d carbon-panel" >>"$DOCKER_STUB_LOG"\n'
            '  rm -f "$dir/.carbon-panel-install-owned"\n'
            '  touch "$SERVICE_STATE"\n'
            '  exit 0\n'
            'fi\n'
            'if [ -f "$SERVICE_STATE" ]; then exit 0; fi\n'
            'printf "image: %s\\n" "$image" > "$dir/compose.yml"\n'
            'touch "$dir/.carbon-panel-install-owned"\n'
            'echo "pull carbon-panel" >>"$DOCKER_STUB_LOG"\n'
            'exit 1\n', encoding="utf-8")
        (share / "install-carbon-panel.sh").chmod(0o755)
        bindir = root / "bin"
        bindir.mkdir()
        docker = bindir / "docker"
        docker.write_text(
            '#!/usr/bin/env bash\n'
            'if [ "${1:-}" = "info" ]; then exit 0; fi\n'
            'if [ "${1:-}" = "context" ]; then\n'
            '  if [ "${2:-}" = "show" ]; then printf "default\\n"; exit 0; fi\n'
            '  if [ "${2:-}" = "inspect" ]; then printf "unix:///var/run/docker.sock\\n"; exit 0; fi\n'
            'fi\n'
            'if [ "${1:-}" = "compose" ] && [ "${2:-}" = "version" ]; then exit 0; fi\n'
            'if [ "${1:-}" = "compose" ] && [ "${2:-}" = "ps" ]; then\n'
            '  [ -f "$SERVICE_STATE" ] && printf "cid\\n"\n'
            '  exit 0\n'
            'fi\n'
            'echo "$@" >>"$DOCKER_STUB_LOG"\nexit 0\n', encoding="utf-8")
        docker.chmod(0o755)
        log = root / "docker.log"
        env = dict(os.environ, PATH=str(bindir) + os.pathsep + os.environ["PATH"],
                   DOCKER_STUB_LOG=str(log), SERVICE_STATE=str(service_state))
        # First invocation: compose + marker are created, pull fails. The unit
        # must NOT mark success or hide the incomplete install.
        first = subprocess.run([str(dest)], env=env, capture_output=True, text=True)
        if first.returncode == 0:
            raise CtError("failed pull was reported as success")
        if not (install / "compose.yml").is_file() or not (install / ".carbon-panel-install-owned").is_file():
            raise CtError("failed pull did not leave resume state")
        if (state / "firstboot.done").exists():
            raise CtError("failed pull wrote the success marker")
        # Retry: installer resumes pull + up and clears its marker.
        second = subprocess.run([str(dest)], env=env, capture_output=True, text=True)
        if second.returncode != 0:
            raise CtError("retry after failed pull failed")
        if not (state / "firstboot.done").is_file() or (install / ".carbon-panel-install-owned").exists():
            raise CtError("retry did not finish cleanly")
        if image not in (install / "compose.yml").read_text(encoding="utf-8"):
            raise CtError("compose does not carry the digest-pinned image")
        ops = log.read_text(encoding="utf-8")
        if "pull carbon-panel" not in ops or "up -d carbon-panel" not in ops:
            raise CtError("retry did not pull and start the service")
        before = (install / "compose.yml").read_bytes()
        (install / "data").mkdir(exist_ok=True)
        (install / "data" / "keep.txt").write_text("operator-data", encoding="utf-8")
        log.write_text("", encoding="utf-8")
        # Sticky success: no further operations, no state replacement.
        third = subprocess.run([str(dest)], env=env, capture_output=True, text=True)
        if third.returncode != 0:
            raise CtError("sticky rerun failed")
        if (install / "compose.yml").read_bytes() != before:
            raise CtError("sticky rerun replaced compose")
        if (install / "data" / "keep.txt").read_text(encoding="utf-8") != "operator-data":
            raise CtError("sticky rerun changed operator data")
        if log.read_text(encoding="utf-8").strip():
            raise CtError("sticky rerun performed docker operations")
        # refusal: no docker CLI
        bad_env = dict(env, PATH="/nonexistent")
        refused = subprocess.run([str(dest)], env=bad_env, capture_output=True, text=True)
        if refused.returncode == 0:
            raise CtError("missing docker CLI was accepted")
        # refusal: moving image tag
        (share / "controller-image.txt").write_text("atharvnaphade/carbonpanel:latest\n", encoding="utf-8")
        (state / "firstboot.done").unlink(missing_ok=True)
        shutil.rmtree(install)
        install.mkdir()
        refused_tag = subprocess.run([str(dest)], env=env, capture_output=True, text=True)
        if refused_tag.returncode == 0:
            raise CtError("moving image tag was accepted")
        return {"status": "PASS", "checks": ["failed-pull", "retry", "sticky-success", "no-docker-refusal", "moving-tag-refusal"]}


def publication_gate(event, ref, trusted_sha, version, artifact_sha256, identity,
                     publish, main_sha=None, ci_success=None):
    reasons = []
    if event != "workflow_dispatch":
        reasons.append("only explicit workflow_dispatch can publish")
    if ref != "refs/heads/main":
        reasons.append("publication ref must be refs/heads/main")
    if re.fullmatch(r"[0-9a-f]{40}", trusted_sha or "") is None:
        reasons.append("trusted_sha must be a full 40-hex commit")
    if main_sha is not None and main_sha != trusted_sha:
        reasons.append("trusted_sha is not current main")
    if ci_success is not True:
        reasons.append("exact-commit main CI is not verified successful")
    if VERSION.fullmatch(version or "") is None:
        reasons.append("version must be in the controller-ct-v domain")
    if SHA256.fullmatch(artifact_sha256 or "") is None:
        reasons.append("artifact_sha256 must be a full 64-hex digest")
    identity = identity or ""
    for token in (version or "", (trusted_sha or "")[:12], (artifact_sha256 or "")[:12], "oss-runtime"):
        if token not in identity:
            reasons.append("identity assertion is not bound to " + (token[:12] or "a required field"))
    allowed = not reasons and publish is True
    return {
        "allowed": allowed,
        "reason": "" if allowed else ("publication opt-in false" if not reasons else "; ".join(reasons)),
        "stage_only": not reasons and publish is not True,
    }


def build_parser():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="command", required=True)
    sub.add_parser("verify-manifest", help="check manifest against checked-in trusted sources")
    b = sub.add_parser("build", help="assemble rootfs staging from verified inputs")
    b.add_argument("--staging", required=True)
    b.add_argument("--fixture", action="store_true")
    b.add_argument("--base-archive")
    b.add_argument("--docker-key")
    b.add_argument("--package-receipt")
    b.add_argument("--prepared-rootfs", help="already-prepared rootfs tree from the owned build runner")
    a = sub.add_parser("make-archive", help="package staging as checksum-bound tar.zst")
    a.add_argument("--staging", required=True)
    a.add_argument("--out", required=True)
    a.add_argument("--version", required=True)
    v = sub.add_parser("verify-archive", help="verify layout, pinning and safety gates")
    v.add_argument("--archive", required=True)
    v.add_argument("--checksum", required=True)
    v.add_argument("--require-real", action="store_true", help="refuse fixture receipts (write/publication path)")
    f = sub.add_parser("check-firstboot", help="inert first-boot success/refusal/idempotence fixture")
    g = sub.add_parser("publish-gate", help="guarded publication decision (never publishes)")
    g.add_argument("--event", required=True)
    g.add_argument("--ref", required=True)
    g.add_argument("--trusted-sha", required=True)
    g.add_argument("--version", required=True)
    g.add_argument("--artifact-sha256", required=True)
    g.add_argument("--identity", default="")
    g.add_argument("--publish", action="store_true")
    g.add_argument("--main-sha")
    g.add_argument("--ci-success", action="store_true")
    return p


def main(argv=None):
    args = build_parser().parse_args(argv)
    try:
        m = load_manifest()
        if args.command == "verify-manifest":
            print(json.dumps(verify_manifest_against_repo(m), sort_keys=True))
        elif args.command == "build":
            print(json.dumps(build_rootfs(args.staging, m, fixture=args.fixture,
                                          base_archive=Path(args.base_archive) if args.base_archive else None,
                                          docker_key=Path(args.docker_key) if args.docker_key else None,
                                          package_receipt=args.package_receipt,
                                          prepared_rootfs=args.prepared_rootfs), sort_keys=True, default=str))
        elif args.command == "make-archive":
            print(json.dumps(make_archive(args.staging, args.out, args.version, m), sort_keys=True))
        elif args.command == "verify-archive":
            print(json.dumps(verify_archive(args.archive, args.checksum, m,
                                            require_real=args.require_real), sort_keys=True))
        elif args.command == "check-firstboot":
            print(json.dumps(run_firstboot_fixture(
                FIRSTBOOT / "carbon-panel-ct-firstboot.sh",
                ROOT / "scripts" / "proxmox" / "install-carbon-panel.sh",
                m["controller"]["image"]), sort_keys=True))
        elif args.command == "publish-gate":
            print(json.dumps(publication_gate(
                args.event, args.ref, args.trusted_sha, args.version,
                args.artifact_sha256, args.identity, args.publish,
                main_sha=args.main_sha, ci_success=args.ci_success), sort_keys=True))
    except (CtError, ValueError, KeyError, OSError) as exc:
        print("error: " + str(exc), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
