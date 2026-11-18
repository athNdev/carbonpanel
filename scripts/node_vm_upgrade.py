# SPDX-License-Identifier: MPL-2.0
"""Staged, reproducible artifact upgrade/rollback and persistent-volume ops.

All functions are pure planners: they return the ordered steps and manifests an
owned disposable VM would execute. Nothing here mounts, formats, deletes or
mutates a host volume. World, backup, panel DB and daemon data-root paths are
preserved and are explicitly excluded from any delete step.
"""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


class UpgradeError(ValueError):
    pass


def _sha(data):
    return hashlib.sha256(data).hexdigest()


def _undo_manifest(files, batch_id, from_revision):
    return {
        "schema_version": 1,
        "batch_id": batch_id,
        "from_revision": from_revision,
        "entries": [
            {"path": name, "sha256": _sha(files[name]), "size": len(files[name])}
            for name in sorted(files)
        ],
    }


def plan_upgrade(from_revision, to_revision, artifact_files, policy,
                 batch_id, actor="system"):
    """Build a staged upgrade plan with a checksummed undo manifest.

    artifact_files maps relative staged-file name -> bytes of the OLD live
    artifact that must be restorable. The new artifact is verified by its own
    checksum before any promotion; the undo manifest is written before mutation.
    """
    if from_revision == to_revision:
        raise UpgradeError("upgrade target equals current revision")
    if not artifact_files:
        raise UpgradeError("no artifacts to protect with an undo manifest")
    if not policy.get("require_undo_manifest_before_promotion"):
        raise UpgradeError("upgrade policy must require an undo manifest")
    upgrade_dir = policy["upgrade_dir_template"].replace("<artifact_revision>", to_revision)
    undo_dir = policy["undo_dir_template"].replace("<batch_id>", batch_id)
    manifest = _undo_manifest(artifact_files, batch_id, from_revision)
    return {
        "schema_version": 1,
        "batch_id": batch_id,
        "from_revision": from_revision,
        "to_revision": to_revision,
        "upgrade_dir": upgrade_dir,
        "undo_dir": undo_dir,
        "undo_manifest": manifest,
        "preserve_paths": list(policy["preserve_paths"]),
        "never_delete_paths": list(policy["never_delete_paths"]),
        "steps": [
            "preflight: verify target arch/version and free space; refuse on missing capability",
            "stage: write new artifact into " + upgrade_dir,
            "verify: check new artifact SHA256SUMS before promotion",
            "undo: write and verify " + undo_dir + "/manifest.json before mutation",
            "promote: atomic rename of staged files into live paths",
            "health: recheck node health/capability before marking upgrade complete",
            "audit: record batch_id, revisions, actor and file hashes (no secrets)",
        ],
        "on_uncertain": "mark UNCERTAIN and block retry; require explicit reconciliation"
                        if policy.get("refuse_retry_on_uncertain") else "report",
        "audit": _audit(batch_id, from_revision, to_revision, actor, manifest, "planned"),
    }


def _audit(batch_id, from_revision, to_revision, actor, manifest, outcome):
    return {
        "batch_id": batch_id,
        "from_revision": from_revision,
        "to_revision": to_revision,
        "actor": actor,
        "timestamp_source": "owned-runtime-clock",
        "file_sha256": {e["path"]: e["sha256"] for e in manifest["entries"]},
        "outcome": outcome,
        "no_secret_values": True,
    }


def verify_undo(undo_manifest, files):
    """Return True only when every undo entry still matches the live bytes."""
    entries = undo_manifest.get("entries", [])
    if not entries:
        return False
    for entry in entries:
        name = entry["path"]
        if name not in files or _sha(files[name]) != entry["sha256"]:
            return False
        if len(files[name]) != entry["size"]:
            return False
    return True


def plan_rollback(undo_manifest, live_files, policy, actor="system"):
    """Rollback restores old bytes only after the undo manifest verifies."""
    if not policy.get("rollback", {}).get("restore_only_if_undo_verified"):
        raise UpgradeError("rollback policy must require a verified undo manifest")
    if not verify_undo(undo_manifest, live_files):
        raise UpgradeError("undo manifest failed verification; rollback refused")
    return {
        "schema_version": 1,
        "batch_id": undo_manifest["batch_id"],
        "from_revision": undo_manifest["from_revision"],
        "restore_entries": undo_manifest["entries"],
        "keep_current_artifact_for_forensics": bool(
            policy["rollback"].get("preserve_new_artifact_for_forensics")),
        "no_volume_delete": bool(policy["rollback"].get("no_volume_delete")),
        "preserve_paths": list(policy["preserve_paths"]),
        "steps": [
            "verify: undo manifest SHA256 matches the staged old artifact",
            "stop: quiesce owned agent with a bounded context",
            "restore: copy verified old artifact over live paths",
            "health: recheck node health/capability",
            "audit: record rollback outcome (no secrets)",
        ],
    }


def plan_volume_import(source, target, volume_sha256, policy):
    """Import a persistent volume by copy; refuse to overwrite without opt-in."""
    if not source or not target:
        raise UpgradeError("import requires source and target")
    allowed = tuple(policy["preserve_paths"])
    if not target.startswith(allowed):
        raise UpgradeError("import target is outside preserved data paths")
    return {
        "operation": "import",
        "source": source,
        "target": target,
        "verify_source_sha256": volume_sha256,
        "copy_only": True,
        "overwrite": False,
        "steps": [
            "verify source SHA256", "copy to " + target,
            "re-verify target SHA256", "never delete the source",
        ],
    }


def plan_volume_backup(volume, backup_dir, volume_bytes, policy):
    """Hash and describe a copy-only backup of a persistent volume."""
    return {
        "operation": "backup",
        "source": volume,
        "backup_dir": backup_dir,
        "sha256": _sha(volume_bytes),
        "size": len(volume_bytes),
        "copy_only": True,
        "preserve_source": True,
    }


def plan_volume_recover(backup_record, volume_bytes, target, policy):
    """Recover only when the backup hash matches; never mutate the source."""
    if _sha(volume_bytes) != backup_record["sha256"]:
        raise UpgradeError("backup hash mismatch; recovery refused")
    allowed = tuple(policy["preserve_paths"])
    if not target.startswith(allowed):
        raise UpgradeError("recovery target is outside preserved data paths")
    return {
        "operation": "recover",
        "source_sha256": backup_record["sha256"],
        "target": target,
        "copy_only": True,
        "no_volume_delete": True,
        "steps": ["verify backup SHA256", "copy to " + target, "re-verify target"],
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--from-revision", required=True)
    parser.add_argument("--to-revision", required=True)
    parser.add_argument("--batch-id", required=True)
    args = parser.parse_args(argv)
    policy = json.loads(
        (Path(args.root) / "packaging/node-vm/config/upgrade-policy.json").read_text()
    )
    plan = plan_upgrade(args.from_revision, args.to_revision, {
        "carbon-node/agent": b"fixture-old-agent-bytes",
    }, policy, args.batch_id)
    print(json.dumps({"batch_id": plan["batch_id"], "undo_entries":
                      len(plan["undo_manifest"]["entries"])}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
