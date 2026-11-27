# SPDX-License-Identifier: MPL-2.0
"""Run the arm64 disposable first-boot identity acceptance on Linux.

This driver is the checked-in entry point for the hosted arm64 qualification
path. It boots two fresh overlays of the pinned Ubuntu arm64 cloud image with
distinct NoCloud instance IDs on QEMU ``virt`` under UEFI/TCG, asserts that each
first boot reaches cloud-init completion and the reboot boundary and emits the
public node identity marker, and asserts that the two node IDs differ. The guest
uses QEMU user-mode networking with ``restrict=on`` and no host forward; the
driver asserts that effective argv before accepting a boot.

An optional ``--disk-size`` (for example ``20G``) exercises the expanded-disk
profile. Each boot disk is flattened from the pinned base, its GPT backup header
is relocated to the new end before the guest boots, and the instance is accepted
only when the helper reports that normalization, a matching virtual size, an
unchanged base image and no stale-GPT/label/emergency marker in the serial log.

It deliberately covers first-boot identity only. It does not mount the
production controller listener, does not enroll against a live controller, and
does not claim reboot persistence. The only uploaded output is a small,
allowlisted receipt (no serial logs, seed ISOs, keys, secrets or raw
subprocess/error output). Per-instance seed ISO digests, the seed-derived
instance ID, cloud-init completion and the reboot boundary are recorded there.
"""
import argparse
import hashlib
import json
import math
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
from scripts import node_vm_gpt as gpt  # noqa: E402

NODE_ID_RE = re.compile(r"^node-[0-9a-f]{32}$")
FINGERPRINT_RE = re.compile(r"^[0-9a-f]{64}$")
SEED_INSTANCE_RE = re.compile(r"(?m)^instance-id:\s*([^\s#]+)\s*$")
SECRET_RE = re.compile(
    r"BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY|"
    r'"(?:enrollment_secret|token|password|private_key)"\s*:',
    re.IGNORECASE,
)
RESTRICTED_NETDEV = "user,id=net0,restrict=on"
QEMU_VERSION_RE = re.compile(r"^QEMU emulator version (\d+\.\d+\.\d+)\b")
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
    "serial_console",
    "first_boot_identity_marker",
    "serial_secret_scan",
    "reboot_boundary",
)


class AcceptanceError(RuntimeError):
    pass


def sha256_file(path):
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def derived_node_id(instance_id):
    """Mirror the guest's deterministic node-ID derivation for a seed ID."""
    return "node-" + hashlib.sha256(instance_id.encode("utf-8")).hexdigest()[:32]


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


def collect_seed_evidence(work_dir, expected_instance_id):
    """Read the generated NoCloud seed back; never assume the requested ID.

    ``node_vm_boot_qual.py --generate-seed`` writes ``seed/nocloud/meta-data``
    and ``seed.iso`` into the attempt directory. The instance ID comes from that
    helper-produced seed, not from the argument vector, so the receipt can name
    the identity that was actually handed to the guest.
    """
    work_dir = Path(work_dir)
    meta = work_dir / "seed" / "nocloud" / "meta-data"
    if not meta.is_file():
        raise AcceptanceError("generated seed meta-data is missing")
    match = SEED_INSTANCE_RE.search(
        meta.read_text(encoding="utf-8", errors="replace"))
    if not match:
        raise AcceptanceError("generated seed has no instance-id")
    observed = match.group(1)
    if observed != expected_instance_id:
        raise AcceptanceError(
            "generated seed instance-id does not match the requested instance")
    iso = work_dir / "seed.iso"
    if not iso.is_file():
        raise AcceptanceError("generated seed ISO is missing")
    return {
        "instance_id": observed,
        "instance_id_sha256": hashlib.sha256(
            observed.encode("utf-8")).hexdigest(),
        "instance_id_source": "seed/nocloud/meta-data",
        "seed_iso": {"sha256": sha256_file(iso), "bytes": iso.stat().st_size},
    }


def assert_restricted_user_network(command, label):
    """Assert the effective argv has restricted user networking and no forward."""
    if not isinstance(command, list) or not command:
        raise AcceptanceError("%s: boot attempt did not record its argv" % label)
    joined = " ".join(str(value) for value in command)
    if "hostfwd" in joined or "guestfwd" in joined:
        raise AcceptanceError(
            "%s: boot attempt configured a host or guest forward" % label)
    netdevs = [command[index + 1] for index, value in enumerate(command[:-1])
               if value == "-netdev"]
    if netdevs != [RESTRICTED_NETDEV]:
        raise AcceptanceError(
            "%s: boot attempt did not use restricted user networking" % label)
    if any(value in ("-net", "-nic") for value in command):
        raise AcceptanceError(
            "%s: boot attempt used an unasserted network backend" % label)
    return True


def expanded_disk_evidence(gates, label, requested_size):
    """Require the pre-boot GPT normalization and guest observation gates."""
    expected = gpt.parse_disk_size(requested_size)
    disk = gates.get("boot_disk") or {}
    if disk.get("status") != "PASS":
        raise AcceptanceError(
            "%s: boot disk gate is %s" % (label, disk.get("status")))
    if disk.get("mode") != "expanded-gpt":
        raise AcceptanceError("%s: boot disk was not GPT-normalized" % label)
    if disk.get("base_image_unchanged") is not True:
        raise AcceptanceError("%s: base image was not proven unchanged" % label)
    if disk.get("virtual_size") != expected:
        raise AcceptanceError(
            "%s: expanded disk virtual size is not the requested size" % label)
    expansion = disk.get("gpt") or {}
    if expansion.get("status") not in ("EXPANDED", "ALREADY_EXPANDED"):
        raise AcceptanceError(
            "%s: GPT normalization is %s" % (label, expansion.get("status")))
    expected_last = expected // gpt.SECTOR_SIZE - 1
    if expansion.get("new_last_lba") != expected_last:
        raise AcceptanceError(
            "%s: GPT backup header is not at the expanded disk end" % label)
    guest = gates.get("expanded_disk_guest") or {}
    if guest.get("status") != "PASS":
        raise AcceptanceError(
            "%s: expanded-disk guest observation is %s"
            % (label, guest.get("status")))
    if guest.get("failure_markers"):
        raise AcceptanceError(
            "%s: expanded-disk guest reported failure markers" % label)
    return {
        "mode": "expanded-gpt",
        "requested_size": requested_size,
        "virtual_size": expected,
        "base_virtual_size": disk.get("base_virtual_size"),
        "base_image_unchanged": True,
        "gpt_status": expansion.get("status"),
        "old_last_lba": expansion.get("old_last_lba"),
        "new_last_lba": expansion.get("new_last_lba"),
        "partition_count": expansion.get("partition_count"),
        "guest_observation": guest.get("status"),
    }


def validate_instance_report(report, label, seed_evidence, disk_size=None):
    """Require the gate shapes this acceptance path depends on."""
    gates = report.get("gates") or {}
    required = list(REQUIRED_GATES)
    if disk_size:
        required.append("expanded_disk_guest")
    missing = [name for name in required if name not in gates]
    if missing:
        raise AcceptanceError("%s: missing gates: %s" % (label, ", ".join(missing)))
    for name in ("image_bytes_verified", "seed", "seed_iso", "firmware",
                 "qemu_available", "boot_disk"):
        if gates[name].get("status") != "PASS":
            raise AcceptanceError(
                "%s: gate %s is %s" % (label, name, gates[name].get("status")))
    expanded = expanded_disk_evidence(gates, label, disk_size) if disk_size else None
    attempt = gates["boot_attempt"]
    if attempt.get("status") != "COMPLETED" or attempt.get("returncode") != 0:
        raise AcceptanceError(
            "%s: boot attempt did not complete cleanly: status=%s returncode=%s"
            % (label, attempt.get("status"), attempt.get("returncode")))
    assert_restricted_user_network(attempt.get("command"), label)
    if (attempt.get("cpu"), attempt.get("accel"), attempt.get("firmware")) != ("max", "tcg", "uefi"):
        raise AcceptanceError("%s: unexpected QEMU target" % label)
    elapsed = attempt.get("elapsed_seconds")
    if type(elapsed) not in (int, float) or not math.isfinite(elapsed) or elapsed < 0:
        raise AcceptanceError("%s: invalid elapsed time" % label)
    serial = gates["serial_console"]
    if serial.get("status") != "SCANNED":
        raise AcceptanceError("%s: serial console was not scanned" % label)
    if serial.get("cloud_init_finished") is not True:
        raise AcceptanceError("%s: first boot did not complete cloud-init" % label)
    boundary = gates["reboot_boundary"]
    if boundary.get("status") != "OBSERVED":
        raise AcceptanceError("%s: first-boot reboot boundary was not observed" % label)
    identity = gates["first_boot_identity_marker"]
    if identity.get("status") != "PASS":
        raise AcceptanceError(
            "%s: first-boot identity is %s" % (label, identity.get("status")))
    node_id = identity.get("node_id")
    fingerprint = identity.get("fingerprint")
    if not isinstance(node_id, str) or not NODE_ID_RE.fullmatch(node_id):
        raise AcceptanceError("%s: invalid public node ID %r" % (label, node_id))
    if not isinstance(fingerprint, str) or not FINGERPRINT_RE.fullmatch(fingerprint):
        raise AcceptanceError(
            "%s: invalid public key fingerprint %r" % (label, fingerprint))
    expected_node_id = derived_node_id(seed_evidence["instance_id"])
    if node_id != expected_node_id:
        raise AcceptanceError(
            "%s: guest node ID does not derive from the seed instance ID" % label)
    scan = gates["serial_secret_scan"]
    if scan.get("status") != "NO_HIGH_SIGNAL_MARKERS":
        raise AcceptanceError(
            "%s: serial secret scan is %s" % (label, scan.get("status")))
    return {
        "instance_id": seed_evidence["instance_id"],
        "instance_id_sha256": seed_evidence["instance_id_sha256"],
        "instance_id_source": seed_evidence["instance_id_source"],
        "node_id": node_id,
        "approved_key_fingerprint": fingerprint,
        "seed_iso": seed_evidence["seed_iso"],
        "first_boot": {
            "status": "PASS",
            "cloud_init_finished": True,
            "reboot_boundary": boundary.get("status"),
        },
        "boot_attempt": {
            "status": attempt.get("status"),
            "returncode": attempt.get("returncode"),
            "elapsed_seconds": elapsed,
            "cpu": attempt.get("cpu"),
            "accel": attempt.get("accel"),
            "firmware": attempt.get("firmware"),
            "network_restrict": True,
            "network_host_forward": False,
        },
        "serial_secret_scan": scan.get("status"),
        "expanded_disk": expanded,
    }


def build_receipt(head, arch, image, firmware, instances, toolchain=None,
                  failed_phase=None, disk_size=None):
    both_booted = len(instances) == 2
    first_boot_identity = "PASS" if both_booted else "NOT_RUN"
    if failed_phase in ("first_boot_1", "first_boot_2"):
        first_boot_identity = "FAIL"
    expanded_instances = [item.get("expanded_disk") for item in instances]
    expanded_requested = disk_size is not None
    expanded_pass = bool(
        expanded_requested and both_booted
        and all(isinstance(item, dict) and item.get("guest_observation") == "PASS"
                and item.get("gpt_status") in ("EXPANDED", "ALREADY_EXPANDED")
                for item in expanded_instances))
    if expanded_requested:
        limits = [
            "expanded %s virtual disk; the backup GPT is relocated before boot, "
            "but the guest's growpart/resize2fs result is not independently "
            "inspected" % disk_size,
        ]
    else:
        limits = [
            "base 3.5 GiB virtual disk; the expanded 20 GiB arm64 profile is not qualified",
        ]
    limits.extend([
        "first boots only; the guest may restart at the first-boot boundary, so no second boot or reboot-persistence claim",
        "no production controller listener, live enrollment, mTLS or heartbeat",
        "QEMU user-mode netdev used restrict=on with no host forward; effective network isolation is not proven",
        "serial logs, seed ISOs, overlays, keys and secrets stay in the ephemeral runner",
    ])
    receipt = {
        "schema_version": 3,
        "purpose": "disposable arm64 UEFI/TCG first-boot identity acceptance",
        "head": head,
        "arch": arch,
        "image": image,
        "firmware": firmware,
        "disk": {
            "profile": "expanded" if expanded_requested else "base",
            "requested_size": disk_size,
        },
        "toolchain": toolchain or {},
        "instances": instances,
        "checks": {
            "image_bytes_verified": image.get("verified") is True,
            "first_boot_identity": first_boot_identity,
            "first_boot_completed": both_booted and all(
                item.get("first_boot", {}).get("cloud_init_finished") is True for item in instances),
            "reboot_boundary_observed": both_booted and all(
                item.get("first_boot", {}).get("reboot_boundary") == "OBSERVED" for item in instances),
            "restricted_user_network": both_booted and all(
                item.get("boot_attempt", {}).get("network_restrict") is True
                and item.get("boot_attempt", {}).get("network_host_forward") is False
                for item in instances),
            "seed_iso_sha256_recorded": both_booted and all(
                FINGERPRINT_RE.fullmatch(item.get("seed_iso", {}).get("sha256", "")) for item in instances),
            "distinct_node_ids": both_booted and len({item.get("node_id") for item in instances}) == 2,
            "distinct_instance_ids": both_booted and len({item.get("instance_id") for item in instances}) == 2,
            "expanded_disk_gpt": ("PASS" if expanded_pass else "FAIL")
            if expanded_requested else "NOT_RUN",
            "reboot_persistence": "NOT_RUN",
            "live_controller_enrollment": "NOT_RUN",
        },
        "limits": limits,
    }
    if failed_phase:
        receipt["failed_phase"] = failed_phase
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
        "--restrict-user-network",
    ]
    if args.disk_size:
        cmd.extend(["--disk-size", args.disk_size])
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
    seed_evidence = collect_seed_evidence(work_dir, instance_id)
    result = validate_instance_report(
        report, label, seed_evidence, disk_size=args.disk_size)
    serial = work_dir / "serial-console.log"
    if not serial.is_file():
        raise AcceptanceError("%s: serial log is missing" % label)
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
    parser.add_argument(
        "--disk-size",
        help="optional expanded overlay virtual size (for example 20G); the "
             "default keeps the base 3.5 GiB disk, and an empty value is ignored")
    parser.add_argument("--instance-id", action="append", dest="instance_ids")
    parser.add_argument("--boot-timeout", type=int, default=3000)
    args = parser.parse_args(argv)

    disk_size = (args.disk_size or "").strip() or None
    if disk_size:
        try:
            gpt.parse_disk_size(disk_size)
        except gpt.GptError as exc:
            parser.error(str(exc))
    args.disk_size = disk_size

    instance_ids = tuple(args.instance_ids or DEFAULT_INSTANCE_IDS)
    if len(instance_ids) != 2:
        parser.error("pass exactly two --instance-id values")
    if len(set(instance_ids)) != 2 or any(not item.strip() for item in instance_ids):
        parser.error("instance IDs must be non-empty and distinct")
    if not re.fullmatch(r"[a-f0-9]{40}", args.head):
        parser.error("--head must be a full lowercase commit SHA")

    args.workspace.mkdir(parents=True, exist_ok=True)
    args.receipt.parent.mkdir(parents=True, exist_ok=True)
    image = {
        "ref": "ubuntu-24.04-arm64",
        "sha256": None,
        "bytes": None,
        "verified": False,
    }
    try:
        version = subprocess.run(
            ["qemu-system-aarch64", "--version"], capture_output=True,
            text=True, timeout=30, check=True).stdout
        match = QEMU_VERSION_RE.match(version)
        qemu_version = match.group(1) if match else None
    except Exception:
        qemu_version = None
    toolchain = {"qemu": qemu_version, "python": sys.version.split()[0]}
    firmware = {"code_sha256": None, "vars_sha256": None}
    instances = []
    failed_phase = None
    phase = "image_verification"
    try:
        image.update({
            "sha256": sha256_file(args.image),
            "bytes": args.image.stat().st_size,
        })
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

        phase = "firmware_verification"
        from scripts import node_vm_boot_qual as boot_qual
        code, variables = boot_qual.find_uefi_firmware("aarch64")
        firmware = {
            "code_sha256": sha256_file(code),
            "vars_sha256": sha256_file(variables),
        }
        for index, instance_id in enumerate(instance_ids, start=1):
            phase = "first_boot_%d" % index
            instances.append(run_instance(args, instance_id, index))
        phase = "identity_comparison"
        if len({item["node_id"] for item in instances}) != len(instances):
            raise AcceptanceError("distinct instance IDs produced the same node ID")
    except Exception:
        # Guest logs, helper data and exception text can hold arbitrary values.
        failed_phase = phase
        print("node_vm_arm64_acceptance: failed during %s" % failed_phase, file=sys.stderr)

    receipt = build_receipt(
        args.head, "aarch64", image, firmware, instances,
        toolchain=toolchain, failed_phase=failed_phase, disk_size=disk_size)
    args.receipt.write_text(receipt, encoding="utf-8")
    print(receipt, end="")
    return 1 if failed_phase else 0


if __name__ == "__main__":
    raise SystemExit(main())
