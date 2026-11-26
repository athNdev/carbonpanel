# SPDX-License-Identifier: MPL-2.0
"""Run the arm64 disposable first-boot identity acceptance on Linux.

This driver is the checked-in entry point for the hosted arm64 qualification
path. It boots two fresh overlays of the pinned Ubuntu arm64 cloud image with
distinct NoCloud instance IDs on QEMU ``virt`` under UEFI/TCG, asserts that each
first boot emits the public node identity marker, and asserts that the two node
IDs differ.

It deliberately covers first-boot identity only. It does not mount the
production controller listener, does not enroll against a live controller, and
does not claim reboot persistence. The only uploaded output is a small,
allowlisted receipt (no serial logs, seed ISOs, private keys or tokens).
"""
import argparse
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
NODE_ID_RE = re.compile(r"^node-[0-9a-f]{32}$")
FINGERPRINT_RE = re.compile(r"^[0-9a-f]{64}$")
SECRET_RE = re.compile(
    r"BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY|"
    r'"(?:enrollment_secret|token|password|private_key)"\s*:',
    re.IGNORECASE,
)
DEFAULT_INSTANCE_IDS = (
    "oss377-arm64-accept-0001",
    "oss377-arm64-accept-0002",
)
REQUIRED_GATES = (
    "image_bytes_verified",
    "seed",
    "seed_iso",
    "firmware",
    "qemu_available",
    "boot_disk",
    "boot_attempt",
    "first_boot_identity_marker",
)


class AcceptanceError(RuntimeError):
    pass


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def parse_helper_output(stdout):
    """Return the helper's JSON report from its final non-empty stdout line."""
    for line in reversed(stdout.splitlines()):
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            report = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(report, dict) and "gates" in report:
            return report
    raise AcceptanceError("boot helper did not emit a JSON report")


def validate_instance_report(report, label):
    """Require the gate shapes this acceptance path depends on."""
    gates = report.get("gates") or {}
    missing = [name for name in REQUIRED_GATES if name not in gates]
    if missing:
        raise AcceptanceError("%s: missing gates: %s" % (label, ", ".join(missing)))
    for name in ("image_bytes_verified", "seed", "seed_iso", "firmware",
                 "qemu_available", "boot_disk"):
        if gates[name].get("status") != "PASS":
            raise AcceptanceError(
                "%s: gate %s is %s" % (label, name, gates[name].get("status")))
    attempt = gates["boot_attempt"]
    if attempt.get("status") != "COMPLETED" or attempt.get("returncode") != 0:
        raise AcceptanceError(
            "%s: boot attempt did not complete cleanly: status=%s returncode=%s"
            % (label, attempt.get("status"), attempt.get("returncode")))
    identity = gates["first_boot_identity_marker"]
    if identity.get("status") != "PASS":
        raise AcceptanceError(
            "%s: first-boot identity is %s" % (label, identity.get("status")))
    node_id = identity.get("node_id")
    fingerprint = identity.get("fingerprint")
    if not node_id or not NODE_ID_RE.match(node_id):
        raise AcceptanceError("%s: invalid public node ID %r" % (label, node_id))
    if not fingerprint or not FINGERPRINT_RE.match(fingerprint):
        raise AcceptanceError(
            "%s: invalid public key fingerprint %r" % (label, fingerprint))
    scan = gates.get("serial_secret_scan") or {}
    if scan.get("status") != "NO_HIGH_SIGNAL_MARKERS":
        raise AcceptanceError(
            "%s: serial secret scan is %s" % (label, scan.get("status")))
    return {
        "instance_id": report.get("instance_id"),
        "node_id": node_id,
        "approved_key_fingerprint": fingerprint,
        "boot_attempt": {
            "status": attempt.get("status"),
            "returncode": attempt.get("returncode"),
            "elapsed_seconds": attempt.get("elapsed_seconds"),
            "cpu": attempt.get("cpu"),
            "accel": attempt.get("accel"),
            "firmware": attempt.get("firmware"),
        },
        "serial_console": {
            "size": (gates.get("serial_console") or {}).get("size"),
            "sha256": (gates.get("serial_console") or {}).get("sha256"),
        },
        "serial_secret_scan": scan.get("status"),
    }


def build_receipt(head, arch, image, firmware, instances, toolchain=None, error=None):
    receipt = {
        "schema_version": 1,
        "purpose": "disposable arm64 UEFI/TCG first-boot identity acceptance",
        "head": head,
        "arch": arch,
        "image": image,
        "firmware": firmware,
        "toolchain": toolchain or {},
        "instances": instances,
        "checks": {
            "image_bytes_verified": error is None,
            "first_boot_identity": "PASS" if error is None else "FAIL",
            "distinct_node_ids": (
                len({item["node_id"] for item in instances}) == len(instances)
                if error is None else False),
            "reboot_persistence": "NOT_RUN",
            "live_controller_enrollment": "NOT_RUN",
        },
        "limits": [
            "base 3.5 GiB virtual disk; the expanded 20 GiB arm64 profile is not qualified",
            "first boots only; no clean second boot or reboot-persistence claim",
            "no production controller listener, live enrollment, mTLS or heartbeat",
            "QEMU user-mode networking restricted; no effective isolation claim",
            "serial logs, seed ISOs and overlays stay in the ephemeral runner",
        ],
    }
    if error:
        receipt["error"] = error
    text = json.dumps(receipt, indent=2, sort_keys=True) + "\n"
    if SECRET_RE.search(text):
        raise AcceptanceError("refusing to write a receipt containing secret markers")
    return text


def run_instance(args, instance_id, index):
    label = "instance-%d" % index
    work_dir = Path(args.workspace) / label
    cmd = [
        sys.executable, str(ROOT / "scripts" / "node_vm_boot_qual.py"),
        "--image", str(args.image),
        "--work-dir", str(work_dir),
        "--arch", "aarch64",
        "--machine", "virt",
        "--accel", "tcg",
        "--firmware", "uefi",
        "--generate-seed",
        "--instance-id", instance_id,
        "--node-label", "oss377arm",
        "--verify-ref", "ubuntu-24.04-arm64",
        "--sha256sums", str(args.sha256sums),
        "--timeout", str(args.boot_timeout),
    ]
    proc = subprocess.run(
        cmd, cwd=str(ROOT), capture_output=True, text=True,
        timeout=args.boot_timeout + 300, check=False)
    report = None
    if proc.stdout.strip():
        try:
            report = parse_helper_output(proc.stdout)
        except AcceptanceError:
            report = None
    if proc.returncode != 0:
        detail = (report or {}).get("gates", {}).get("boot_attempt", {})
        raise AcceptanceError(
            "%s: helper exited %d (%s); stderr sha256=%s"
            % (label, proc.returncode, detail.get("status", "no report"),
               hashlib.sha256(proc.stderr.encode("utf-8", "replace")).hexdigest()))
    if report is None:
        raise AcceptanceError("%s: helper emitted no report" % label)
    report["instance_id"] = instance_id
    result = validate_instance_report(report, label)
    serial = work_dir / "serial-console.log"
    if serial.is_file():
        result["serial_console"] = {
            "size": serial.stat().st_size,
            "sha256": sha256_file(serial),
        }
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True, type=Path)
    parser.add_argument("--sha256sums", required=True, type=Path)
    parser.add_argument("--workspace", required=True, type=Path)
    parser.add_argument("--head", required=True)
    parser.add_argument("--receipt", required=True, type=Path)
    parser.add_argument("--instance-id", action="append", dest="instance_ids")
    parser.add_argument("--boot-timeout", type=int, default=3000)
    args = parser.parse_args(argv)

    instance_ids = tuple(args.instance_ids or DEFAULT_INSTANCE_IDS)
    if len(instance_ids) != 2:
        parser.error("pass exactly two --instance-id values")
    if len(set(instance_ids)) != 2 or any(not item.strip() for item in instance_ids):
        parser.error("instance IDs must be non-empty and distinct")
    if not re.fullmatch(r"[a-f0-9]{40}", args.head):
        parser.error("--head must be a full lowercase commit SHA")

    args.workspace.mkdir(parents=True, exist_ok=True)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    image_sha256 = sha256_file(args.image)
    image = {
        "ref": "ubuntu-24.04-arm64",
        "sha256": image_sha256,
        "bytes": args.image.stat().st_size,
        "verified": False,
    }
    try:
        qemu_version = subprocess.run(
            ["qemu-system-aarch64", "--version"], capture_output=True,
            text=True, timeout=30, check=False).stdout.splitlines()[0]
    except Exception:
        qemu_version = None
    toolchain = {"qemu": qemu_version, "python": sys.version.split()[0]}
    firmware = {"code_sha256": None, "vars_sha256": None}
    instances = []
    error = None
    try:
        from scripts import node_vm_verify_image as verify_image
        sums = args.sha256sums.read_text(encoding="utf-8")
        verified = verify_image.verify_pinned_image(
            ROOT, str(args.image), "ubuntu-24.04-arm64",
            profile="artifact", checksums_text=sums)
        image.update({
            "sha256": verified["sha256_actual"],
            "bytes": verified["bytes"],
            "verified": verified["bytes_verified"],
        })
        if not image["verified"]:
            raise AcceptanceError("pinned arm64 image bytes were not verified")

        from scripts import node_vm_boot_qual as boot_qual
        code, variables = boot_qual.find_uefi_firmware("aarch64")
        firmware = {
            "code_sha256": sha256_file(code),
            "vars_sha256": sha256_file(variables),
        }
        for index, instance_id in enumerate(instance_ids, start=1):
            instances.append(run_instance(args, instance_id, index))
        if len({item["node_id"] for item in instances}) != len(instances):
            raise AcceptanceError("distinct instance IDs produced the same node ID")
    except Exception as exc:  # keep failure evidence, but fail closed
        error = str(exc)
        print("node_vm_arm64_acceptance: %s" % error, file=sys.stderr)

    receipt = build_receipt(
        args.head, "aarch64", image, firmware, instances,
        toolchain=toolchain, error=error)
    args.receipt.write_text(receipt, encoding="utf-8")
    print(receipt, end="")
    return 1 if error else 0


if __name__ == "__main__":
    raise SystemExit(main())
