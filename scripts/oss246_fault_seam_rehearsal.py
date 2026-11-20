#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Bounded, reversible fault seams for OSS-246 upgrade/rollback script policy.

Runs the REAL shipped ``scripts/compose-upgrade.sh`` and
``scripts/compose-upgrade-rollback.sh`` against caller-owned DISPOSABLE copied
DB/world fixtures. Faults are injected only through a temporary PATH shim in
the workspace (no filled disk, no real Docker, no live host):

  * snapshot failure (sqlite3/python3 refuse) must stop upgrade BEFORE
    ``docker compose up`` and leave the copied DB/world bytes unchanged;
  * a missing snapshot must be refused by rollback without mutating the DB;
  * a snapshot that is the DB itself must be refused.

The shim is removed in a finally block. ``docker`` is shimmed to record any
attempted invocation so the "stop before up" claim is checked, not assumed.
This does not exercise the packaged binary's migration internals: no supported
fault seam exists there (see report). Nothing is published or merged.
"""
import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent


def now():
    return datetime.now(timezone.utc).isoformat()


def sha256_file(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def run(cmd, env=None, cwd=None):
    result = subprocess.run(cmd, env=env, cwd=str(cwd or ROOT),
                            capture_output=True, text=True, timeout=120)
    return result.returncode, result.stdout + result.stderr


def make_shim(workspace, label):
    """A PATH shim that deterministically fails sqlite3/python3/docker."""
    shim = workspace / ("shim-" + label)
    shim.mkdir(parents=True, exist_ok=True)
    marker = workspace / ("shim-" + label + "-invoked.log")
    for name in ("sqlite3", "python3", "docker"):
        path = shim / name
        path.write_text(
            f"#!/bin/sh\nprintf '%s %s\\n' '{name}' \"$*\" >> '{marker}'\nexit 1\n",
            encoding="utf-8")
        path.chmod(0o755)
    env = dict(os.environ)
    env["PATH"] = str(shim) + os.pathsep + env.get("PATH", "")
    return env, marker


def copied_fixture(workspace, original):
    """Copy an already-seeded disposable fixture; never use the original."""
    target = workspace / "fault-fixture"
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(original, target)
    return target


def fixture_hashes(fixture):
    out = {}
    for path in sorted(Path(fixture).rglob("*")):
        if path.is_file() and path.name != "service.log":
            out[str(path.relative_to(fixture))] = sha256_file(path)
    return out


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--seed-fixture", required=True,
                        help="a previously seeded disposable workspace (stable-legacy)")
    parser.add_argument("--report", default=None)
    args = parser.parse_args()

    workspace = Path(args.workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    fixture = copied_fixture(workspace, Path(args.seed_fixture))
    before = fixture_hashes(fixture)
    report = {"started_at": now(), "seed_fixture": str(args.seed_fixture),
              "fault_fixture": str(fixture), "checks": {}, "failed_checks": [],
              "tools": {"python": sys.version.split()[0],
                        "platform": os.uname().sysname + " " + os.uname().release if hasattr(os, "uname") else os.name}}

    def check(name, condition):
        report["checks"][name] = bool(condition)
        if not condition:
            report["failed_checks"].append(name)

    upgrade = str(ROOT / "scripts" / "compose-upgrade.sh")
    rollback = str(ROOT / "scripts" / "compose-upgrade-rollback.sh")

    # Positive control: default dry-run must not touch copied data.
    code, out = run(["bash", upgrade, "--data-dir", str(fixture)])
    check("dry_run_exit_zero", code == 0)
    check("dry_run_prints_only", "Dry-run only" in out)
    check("dry_run_leaves_data", fixture_hashes(fixture) == before)

    # Fault A: snapshot fails -> upgrade must stop before compose up.
    env, marker = make_shim(workspace, "snapshot")
    try:
        code, out = run(["bash", upgrade, "--data-dir", str(fixture), "--apply"], env=env)
    finally:
        shutil.rmtree(workspace / "shim-snapshot", ignore_errors=True)
    check("snapshot_fault_exit_nonzero", code != 0)
    check("snapshot_fault_stops_before_up", "not running compose up" in out)
    invoked = marker.read_text(encoding="utf-8") if marker.exists() else ""
    check("snapshot_fault_docker_never_invoked",
          not any(line.startswith("docker ") for line in invoked.splitlines()))
    check("snapshot_fault_db_worlds_unchanged", fixture_hashes(fixture) == before)

    # Fault B: rollback refuses a missing snapshot without mutation.
    missing = workspace / "does-not-exist.sqlite"
    code, out = run(["bash", rollback, "--data-dir", str(fixture),
                     "--snapshot", str(missing), "--apply"])
    check("missing_snapshot_refused", code != 0)
    check("missing_snapshot_no_mutation", fixture_hashes(fixture) == before)

    # Fault C: rollback refuses to copy a snapshot onto itself.
    db = fixture / "carbon-panel.db"
    code, out = run(["bash", rollback, "--data-dir", str(fixture),
                     "--snapshot", str(db), "--apply"])
    check("self_copy_refused", code != 0)
    check("self_copy_no_mutation", fixture_hashes(fixture) == before)

    # Fault D: rollback copy-back on a SEPARATE copy restores DB bytes exactly.
    copy = workspace / "rollback-copy"
    if copy.exists():
        shutil.rmtree(copy)
    shutil.copytree(fixture, copy)
    snapshot = workspace / "operator-snapshot.sqlite"
    shutil.copyfile(db, snapshot)
    target_db = copy / "carbon-panel.db"
    target_db.write_bytes(b"damaged-by-interrupted-upgrade")
    code, out = run(["bash", rollback, "--data-dir", str(copy),
                     "--snapshot", str(snapshot), "--apply"])
    check("copyback_exit_zero", code == 0)
    check("copyback_restores_db_bytes", sha256_file(target_db) == sha256_file(snapshot))
    check("copyback_drops_wal_shm", not (copy / "carbon-panel.db-wal").exists()
          and not (copy / "carbon-panel.db-shm").exists())
    # Every copied file except the deliberately damaged DB must remain byte-identical.
    copy_hashes = fixture_hashes(copy)
    expected = {k: v for k, v in before.items() if k != "carbon-panel.db"}
    actual = {k: v for k, v in copy_hashes.items() if k != "carbon-panel.db"}
    check("copyback_leaves_worlds_and_backups", actual == expected)

    report["pass"] = not report["failed_checks"]
    report["finished_at"] = now()
    destination = Path(args.report) if args.report else workspace / "oss246-fault-report.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"pass": report["pass"], "failed_checks": report["failed_checks"],
                      "report": str(destination)}, indent=2))
    if not report["pass"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
