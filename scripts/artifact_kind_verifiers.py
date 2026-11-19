# SPDX-License-Identifier: MPL-2.0
"""Native per-kind payload verifiers for the runtime publication predicate.

This module exists so the publication lane NEVER qualifies an artifact from a
filename and a byte count alone. Every candidate of the declared kind is checked
by its real, already-existing verifier; if that verifier is unavailable in the
runner, the bundle stays REHEARSAL-ONLY and publication stops.

Reused verifiers (no new format parser is invented here):

* **qcow2** — the real QCOW2 header magic/version plus ``qemu-img info
  --output=json`` format/tool inspection. Digest and content length are bound
  by calling ``scripts.node_vm_verify_image.verify_image_bytes``.
* **lxc-rootfs** — ``packaging/node-lxc/node_lxc_artifact.py``'s
  ``_inspect_root_tarball`` / ``_inspect_lxd_metadata`` and
  ``ARCHIVE_ROOT_REQUIRED_DIRS`` perform the real xz tar / Ubuntu 24.04 layout /
  LXD metadata validation.
* **installer** — the checked-in ``internal/nodeinstall.VerifyArtifact``
  contract (``carbon-panel-node-artifact/v1`` manifest, exact per-file SHA-256,
  no unexpected files) via the existing ``cmd/carbon-node-installer verify``
  CLI; the installer payload digest is bound to the provenance pin as well.

No network, dispatch, registry, release or host provisioning happens here. All
I/O is read-only against an already-staged bundle; tool runners are injectable
so offline synthetic-record tests can prove refusals.
"""
from __future__ import annotations

import hashlib
import importlib.util
import json
import shutil
import struct
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
INSTALLER_PACKAGE = "cmd/carbon-node-installer"
INSTALLER_MANIFEST_SCHEMA = "carbon-panel-node-artifact/v1"
QCOW2_MAGIC = b"QFI\xfb"
QCOW2_VERSIONS = (2, 3)
LXD_ARCHITECTURES = frozenset({"x86_64", "aarch64"})

PAYLOAD_SUFFIXES = {
    "qcow2": (".qcow2",),
    "lxc-rootfs": (".tar.xz",),
    "installer": (".sh", ".bin"),
}

try:  # module import and direct CLI execution both work
    from scripts import node_vm_verify_image as image_verifier
except ImportError:  # pragma: no cover - direct `python3 scripts/runtime_artifacts.py`
    import node_vm_verify_image as image_verifier


def sha256_file(path):
    """Streaming raw SHA-256 of a file."""
    digest = hashlib.sha256()
    with Path(path).open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _which(runner, name):
    finder = runner or shutil.which
    return finder(name)


def _load_lxc_tool(root=None):
    base = Path(root) if root else ROOT
    tool = base / "packaging" / "node-lxc" / "node_lxc_artifact.py"
    if not tool.is_file():
        return None, "node-lxc inspector is unavailable: %s" % tool
    try:
        spec = importlib.util.spec_from_file_location("node_lxc_native_verifier", tool)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
    except Exception as error:  # a broken import is an unavailable verifier
        return None, "node-lxc inspector failed to load: %s: %s" % (type(error).__name__, error)
    return module, None


def verifier_availability(root=None, runner=None):
    """Report which native validators are ACTUALLY available in this runner.

    Returns ``{kind: (available: bool, reason: str)}``. A publication must stop
    when any required validator is unavailable; this is informational evidence,
    not an authorisation to proceed.
    """
    base = Path(root) if root else ROOT
    qemu = _which(runner, "qemu-img")
    go = _which(runner, "go")
    lxc_module, lxc_error = _load_lxc_tool(base)
    return {
        "qcow2": (bool(qemu), "qemu-img: %s" % qemu if qemu else "qemu-img not found on PATH"),
        "lxc-rootfs": (lxc_module is not None,
                       "node_lxc_artifact inspection functions" if lxc_module else lxc_error),
        "installer": (bool(go) and (ROOT / INSTALLER_PACKAGE).is_dir(),
                      "go + cmd/carbon-node-installer verify" if go else "go toolchain not found on PATH"),
    }


def _regular_payloads(root):
    """Regular, non-symlink candidate files anywhere under the staged bundle."""
    return [path for path in Path(root).rglob("*")
            if path.is_file() and not path.is_symlink()]


def _pins(provenance_documents):
    """Non-synthetic artifact pins from the staged provenance records."""
    pins = []
    for document in provenance_documents:
        if not isinstance(document, dict) or document.get("synthetic") is not False:
            continue
        profile = str(document.get("profile") or document.get("provenance_profile") or "").lower()
        if profile != "artifact":
            continue
        images = document.get("images")
        if isinstance(images, list) and images:
            pins.extend(images)
        base_image = document.get("base_image")
        if isinstance(base_image, dict) and base_image:
            pins.append(base_image)
    return pins


def _bind_digest(payload, pin, problems):
    """Reuse node_vm_verify_image to bind the pin to the EXACT payload bytes."""
    if not isinstance(pin, dict) or not pin.get("expected_sha256"):
        problems.append("no provenance pin digest to bind the payload to")
        return None
    actual = sha256_file(payload)
    try:
        image_verifier.verify_image_bytes(actual, payload.stat().st_size, pin)
    except image_verifier.VerificationError as error:
        problems.append(str(error))
    except (KeyError, TypeError, ValueError) as error:
        problems.append("provenance pin cannot be used for digest binding: %s" % error)
    return actual


def _verify_qcow2(payload, *, runner=None, process_runner=None, problems):
    # Read only the 8-byte header, never the whole multi-GiB image.
    with payload.open("rb") as handle:
        header = handle.read(8)
    if header[:4] != QCOW2_MAGIC:
        problems.append("%s: payload is not a QCOW2 image (magic mismatch)" % payload.name)
    else:
        version = struct.unpack(">I", header[4:8])[0] if len(header) == 8 else 0
        if version not in QCOW2_VERSIONS:
            problems.append("%s: QCOW2 header version %s is not supported" % (payload.name, version))
    qemu = _which(runner, "qemu-img")
    if not qemu:
        problems.append("qemu-img native verifier unavailable; publication remains REHEARSAL-ONLY")
        return
    run = process_runner or subprocess.run
    try:
        proc = run([qemu, "info", "--output=json", str(payload)],
                   capture_output=True, text=True)
    except OSError as error:
        problems.append("qemu-img inspection failed: %s" % error)
        return
    if proc.returncode != 0:
        problems.append("qemu-img rejected %s: %s" % (payload.name, proc.stderr.strip()[:200]))
        return
    try:
        info = json.loads(proc.stdout)
    except (ValueError, TypeError):
        problems.append("qemu-img info output is not valid JSON")
        return
    if info.get("format") != "qcow2":
        problems.append("qemu-img reports %s format %r, not qcow2" % (payload.name, info.get("format")))


def _verify_lxc(root, *, root_path=None, problems):
    module, error = _load_lxc_tool(root_path)
    if module is None:
        problems.append(error + "; publication remains REHEARSAL-ONLY")
        return
    layout, layout_error = module._inspect_root_tarball(root)
    if layout_error:
        problems.append("rootfs archive validation failed: %s" % layout_error)
        return
    missing = [d for d in module.ARCHIVE_ROOT_REQUIRED_DIRS
               if d not in layout["top_level_entries"]]
    if missing:
        problems.append("rootfs archive lacks required directories: %s" % ", ".join(missing))
    os_release = layout.get("os_release") or {}
    if os_release.get("ID") != "ubuntu" or os_release.get("VERSION_ID") != "24.04":
        problems.append("rootfs os-release is not Ubuntu 24.04: %s" % os_release)
    lxd_files = [path for path in _regular_payloads(Path(root).parent)
                 if "-lxd.tar.xz" in path.name]
    if not lxd_files:
        problems.append("LXD metadata tarball is missing from the staged rootfs bundle")
        return
    metadata, metadata_error = module._inspect_lxd_metadata(lxd_files[0])
    if metadata_error:
        problems.append("LXD metadata validation failed: %s" % metadata_error)
        return
    if metadata.get("architecture") not in LXD_ARCHITECTURES:
        problems.append("LXD metadata architecture %r is unsupported" % metadata.get("architecture"))
    if metadata.get("os") != "ubuntu" or metadata.get("release") != "noble":
        problems.append("LXD metadata does not describe Ubuntu 24.04 (noble): %s" % metadata)


def _verify_installer(root, *, runner=None, process_runner=None, problems):
    """Reuse nodeinstall.VerifyArtifact via the existing installer verify CLI."""
    base = Path(root)
    go = _which(runner, "go")
    if not go or not (ROOT / INSTALLER_PACKAGE).is_dir():
        problems.append("go installer verifier unavailable; publication remains REHEARSAL-ONLY")
        return False
    packages = [path for path in base.rglob("manifest.json")
                if path.is_file() and not path.is_symlink()]
    if not packages:
        problems.append("no installer package manifest; candidate-only payload")
        return False
    run = process_runner or subprocess.run
    for manifest in packages:
        package_dir = manifest.parent
        try:
            proc = run([go, "run", INSTALLER_PACKAGE, "verify",
                        "--artifact", str(package_dir),
                        "--target-os", "linux", "--target-arch", "amd64"],
                       cwd=str(ROOT), capture_output=True, text=True)
        except OSError as error:
            problems.append("installer verifier could not run: %s" % error)
            continue
        if proc.returncode != 0:
            problems.append("nodeinstall.VerifyArtifact refused %s: %s"
                            % (package_dir.name, proc.stderr.strip()[:200]))
            continue
        try:
            document = json.loads(proc.stdout)
        except (ValueError, TypeError):
            problems.append("installer verifier output is not valid JSON")
            continue
        if document.get("schema") != INSTALLER_MANIFEST_SCHEMA or not document.get("files"):
            problems.append("installer manifest structure is not the expected package")
            continue
        return True
    problems.append("no installer package passed nodeinstall.VerifyArtifact")
    return False


def verify_kind_payload(directory, artifact, provenance_documents, *,
                        root=None, runner=None, process_runner=None):
    """Verify EVERY candidate payload with its native verifier.

    Returns (ok, problems, digests). Rejects symlinks/non-regular candidates,
    binds every same-kind candidate to the provenance pin, and fails closed when
    a native verifier is unavailable. A single unverified extra file of the
    declared kind therefore cannot be published alongside a valid one.
    """
    if artifact not in PAYLOAD_SUFFIXES:
        return False, ["unverified artifact %r has no kind verifier" % artifact], []
    problems = []
    candidates = [path for path in _regular_payloads(directory)
                  if path.name.lower().endswith(PAYLOAD_SUFFIXES[artifact])
                  and not (artifact == "lxc-rootfs" and "-lxd.tar.xz" in path.name)]
    if not candidates:
        return False, ["no regular payload of kind %s in staged bundle" % artifact], []
    pins = _pins(provenance_documents)
    pin = pins[0] if pins else None

    if artifact == "installer":
        if not _verify_installer(directory, runner=runner,
                                 process_runner=process_runner, problems=problems):
            return False, problems, []
        digests = []
        for payload in candidates:  # bind every installer payload, not just one
            digests.append(_bind_digest(payload, pin, problems))
        return (not problems), problems, digests

    digests = []
    for payload in candidates:
        digests.append(_bind_digest(payload, pin, problems))
        if artifact == "qcow2":
            _verify_qcow2(payload, runner=runner,
                          process_runner=process_runner, problems=problems)
        elif artifact == "lxc-rootfs":
            _verify_lxc(payload, root_path=root, problems=problems)
    return (not problems), problems, digests
