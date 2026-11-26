#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Recurring DISPOSABLE real-binary upgrade rehearsal (OSS-406).

One job runs TWO fail-closed cases on caller-owned disposable paths:

* LEGACY baseline: the explicit v1.0.40 stable -> candidate migration
  (35 -> 45 -> 35 tables), using the verified release archive.
* CURRENT stable: the highest published product stable derived from the
  authoritative GitHub release record via the product version contract
  (`scripts/release.py`), -> candidate. No hardcoded table count: the candidate
  schema must be an additive superset of whatever the current stable binary
  produces. When a new stable is published, this case follows it.

Both cases run the REAL stable and candidate `carbon-panel` binaries against
seeded disposable DB/world copies and assert integrity, additive table growth,
row/world preservation, forced-failure refusal, app-level and copy-back
restores. Every write stays under --workspace (a caller-owned temp directory),
apart from toolchain build caches. Nothing here touches a live world, host,
registry, tag or release, and no check may be silently skipped.

The checked-in MODEL rehearsal (`scripts/rehearse_upgrade.py`) and the staged
planner (`scripts/node_vm_upgrade.py`) are executed as additional deterministic
checks; neither replaces the real binaries below.

Typical CI invocation (Linux runner)::

    python3 scripts/upgrade_rehearsal_ci.py \
      --workspace "$RUNNER_TEMP/upgrade-rehearsal" \
      --target-os linux --target-arch amd64 \
      --candidate-version 2.0.0-verify.ci \
      --candidate-commit "$(git rev-parse HEAD)" \
      --go-binary go
"""
import argparse
import hashlib
import json
import os
import platform
import re
import shutil
import sqlite3
import subprocess
import sys
import tarfile
import time
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
RELEASE_BASE = "https://github.com/athNdev/carbonpanel/releases/download/{tag}"

# Legacy table counts use the project's historical convention (every table,
# including sqlite_sequence and casbin_rule): v1.0.40 seeds 35 and the candidate
# adds 10 model tables for 45 (including managed-node enrollment state). The
# CURRENT case derives counts from the binaries.
LEGACY_TABLE_COUNT = 35
CANDIDATE_TABLE_COUNT = 45
# These historical models must be present in the candidate; Stable may already have them.
EXPECTED_CANDIDATE_MODEL_TABLES = {
    "player_identities", "player_aliases", "player_sessions",
    "player_access_states", "player_moderation_actions", "world_entries",
    "world_revisions", "world_bindings", "world_import_jobs",
    "node_enrollment_authority_state",
}
CORE_TABLES = (
    "servers", "server_configs", "users", "nodes", "roles", "user_roles",
    "server_blueprints", "backup_records", "server_snapshots",
)
SEED_FILES = (
    "worlds/survival/level.dat", "worlds/survival/region.rgn",
    "backups/weekly.tar.zst",
)
SERVICE_TIMEOUT_SECONDS = 90


class RehearsalError(RuntimeError):
    """A fixture or invariant failure that must fail the job closed."""


def sha256_file(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def download(url, destination, timeout=120):
    destination = Path(destination)
    destination.parent.mkdir(parents=True, exist_ok=True)
    request = urllib.request.Request(url, headers={"User-Agent": "carbonpanel-upgrade-rehearsal/1.0"})
    with urllib.request.urlopen(request, timeout=timeout) as response, \
            destination.open("wb") as target:
        shutil.copyfileobj(response, target)
    return destination


def extract_archive(archive, destination):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    if str(archive).endswith(".zip"):
        with zipfile.ZipFile(archive) as bundle:
            bundle.extractall(destination)
    elif str(archive).endswith((".tar.gz", ".tgz")):
        with tarfile.open(archive, "r:gz") as bundle:
            bundle.extractall(destination)
    else:
        raise RehearsalError("unsupported stable archive format: %s" % archive)
    return destination


# --- Authoritative version contracts ------------------------------------------

def legacy_baseline():
    """The explicit v1.0.40 baseline, sourced from the release contract module."""
    sys.path.insert(0, str(ROOT))
    from scripts import release  # noqa: WPS433
    return {
        "tag": release.LEGACY_STABLE_TAG,
        "version": release.LEGACY_STABLE_TAG,
        "commit": release.LEGACY_STABLE_COMMIT,
        "source": "explicit-legacy-migration-baseline",
    }


def derive_current_stable():
    """Resolve the highest published stable from the product version contract.

    Fails closed when the release state cannot be established, so a network or
    gh failure can never silently downgrade the weekly rehearsal to the legacy
    path or skip the current case.
    """
    sys.path.insert(0, str(ROOT))
    from scripts import release  # noqa: WPS433
    versions = release.published_stable_versions()
    if versions is None or not versions:
        raise RehearsalError("published stable versions are unavailable; refusing to guess a baseline")
    internal = release.highest_stable(versions)
    tag = release.verified_previous_release_tag()
    if not internal or not tag:
        raise RehearsalError("authoritative current stable could not be derived")
    if release.resolve_panel_version(tag) != internal:
        raise RehearsalError("derived stable tag and version disagree: %s / %s" % (tag, internal))
    return {"tag": tag, "version": internal, "source": "derived-published-stable"}


# --- SQLite inspection helpers ------------------------------------------------

def tables(db_path):
    connection = sqlite3.connect(str(db_path))
    try:
        return {row[0] for row in connection.execute(
            "SELECT name FROM sqlite_master WHERE type='table'")}
    finally:
        connection.close()


def integrity(db_path):
    connection = sqlite3.connect(str(db_path))
    try:
        return connection.execute("PRAGMA integrity_check").fetchone()[0]
    finally:
        connection.close()


def row_counts(db_path, names):
    connection = sqlite3.connect(str(db_path))
    try:
        result = {}
        for name in names:
            try:
                result[name] = connection.execute(
                    "SELECT COUNT(*) FROM \"%s\"" % name).fetchone()[0]
            except sqlite3.Error:
                result[name] = None
        return result
    finally:
        connection.close()


def back_up_database(source, destination):
    """Real SQLite online backup API (safe against WAL), not a file copy."""
    source_connection = sqlite3.connect(str(source))
    target_connection = sqlite3.connect(str(destination))
    try:
        source_connection.backup(target_connection)
    finally:
        target_connection.close()
        source_connection.close()


def seeded_file_hashes(root):
    """Hash exactly the seeded disposable world/backup files, nothing else."""
    root = Path(root)
    result = {}
    for relative in SEED_FILES:
        path = root / relative
        if not path.is_file():
            raise RehearsalError("seeded disposable fixture missing: %s" % path)
        result[relative] = sha256_file(path)
    return result


# --- Real binary execution ----------------------------------------------------

def yaml_string(value):
    """JSON-quoted strings are valid, safely escaped YAML double-quoted scalars."""
    return json.dumps(str(value))


def write_config(directory, db_path, port):
    """Write a minimal, fully disposable config; all paths stay under workspace."""
    directory = Path(directory).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    for child in ("data", "backups", "temp", "worlds/survival"):
        (directory / child).mkdir(parents=True, exist_ok=True)
    text = (
        'server:\n'
        '  host: "127.0.0.1"\n'
        '  port: %s\n'
        'database:\n'
        '  path: %s\n'
        '  panel_backup: "off"\n'
        'storage:\n'
        '  data_dir: %s\n'
        '  backup_dir: %s\n'
        '  temp_dir: %s\n'
        'auth:\n'
        '  local:\n'
        '    enabled: true\n'
    ) % (yaml_string(port),
         yaml_string(Path(db_path).resolve()).replace("\\\\", "/"),
         yaml_string(directory / "data").replace("\\\\", "/"),
         yaml_string(directory / "backups").replace("\\\\", "/"),
         yaml_string(directory / "temp").replace("\\\\", "/"))
    (directory / "config.yaml").write_text(text, encoding="utf-8")
    return directory


def binary_version(binary):
    result = subprocess.run([str(binary), "--version"], cwd=str(Path(binary).parent),
                            capture_output=True, text=True, timeout=30)
    if result.returncode != 0 or not result.stdout.strip():
        raise RehearsalError("binary --version failed: %s (%s)" %
                             (binary, result.stderr.strip()))
    return result.stdout.strip()


def run_service(binary, workdir, expected_tables, timeout=SERVICE_TIMEOUT_SECONDS):
    """Start a real binary, wait for the expected schema, then stop it.

    The binary may exit on its own once migration is complete (for example when
    no Docker daemon is available). The assertion is the on-disk schema, not a
    long-running service. Exiting before the schema appears fails closed.
    """
    workdir = Path(workdir).resolve()
    db_path = workdir / "panel.db"
    log = (workdir / "service.log").open("ab")
    try:
        process = subprocess.Popen([str(binary), "-config", str(workdir)],
                                   cwd=str(workdir), stdout=log, stderr=subprocess.STDOUT)
    finally:
        log.close()
    deadline = time.monotonic() + timeout
    reached = False
    try:
        while time.monotonic() < deadline:
            if db_path.is_file():
                try:
                    if len(tables(db_path)) == expected_tables:
                        reached = True
                        break
                except sqlite3.Error:
                    pass
            if process.poll() is not None:
                if db_path.is_file():
                    try:
                        reached = len(tables(db_path)) == expected_tables
                    except sqlite3.Error:
                        reached = False
                break
            time.sleep(0.5)
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=15)
    if not reached:
        log_path = workdir / "service.log"
        tail = log_path.read_text(encoding="utf-8", errors="replace")[-2000:] \
            if log_path.is_file() else ""
        raise RehearsalError(
            "%s did not produce %d tables within %ss; log tail:\n%s" %
            (binary, expected_tables, timeout, tail))
    return db_path


def run_restore(binary, workdir, snapshot):
    """Offline app-level restore: must exit zero with the panel stopped."""
    return subprocess.run(
        [str(binary), "-config", str(workdir), "-restore-panel-backup", str(snapshot)],
        cwd=str(workdir), capture_output=True, text=True, timeout=60)


# --- Fixture acquisition ------------------------------------------------------

def stable_archive_name(version, goos, goarch):
    suffix = ".zip" if goos == "windows" else ".tar.gz"
    return "carbon-panel_%s_%s_%s%s" % (version, goos, goarch, suffix)


def acquire_stable(workspace, label, version, commit, goos, goarch, release_tag,
                   source="pinned", local_archive=None, local_checksums=None,
                   local_manifest=None):
    """Download and verify a real stable archive against BOTH public records."""
    name = stable_archive_name(version, goos, goarch)
    base = RELEASE_BASE.format(tag=release_tag)
    fixtures = workspace / ("stable-fixtures-" + label)
    fixtures.mkdir(parents=True, exist_ok=True)
    archive = Path(local_archive) if local_archive else fixtures / name
    if local_archive is None:
        download(base + "/" + name, archive)
    if not archive.is_file():
        raise RehearsalError("stable archive missing: %s" % archive)
    checksums_path = Path(local_checksums) if local_checksums else fixtures / "checksums.txt"
    manifest_path = Path(local_manifest) if local_manifest else fixtures / "release-manifest.json"
    if local_checksums is None:
        download(base + "/checksums.txt", checksums_path)
    if local_manifest is None:
        download(base + "/release-manifest.json", manifest_path)
    for required in (checksums_path, manifest_path):
        if not required.is_file():
            raise RehearsalError("stable verification fixture missing: %s" % required)

    actual = sha256_file(archive)
    checksums = {}
    for line in checksums_path.read_text(encoding="utf-8").splitlines():
        parts = line.split()
        if len(parts) == 2:
            checksums[parts[1]] = parts[0].lower()
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    artifacts = {item["name"]: item for item in manifest.get("artifacts", [])}
    if name not in checksums:
        raise RehearsalError("checksums.txt has no entry for %s" % name)
    if name not in artifacts:
        raise RehearsalError("release-manifest.json has no entry for %s" % name)
    if checksums[name].lower() != actual:
        raise RehearsalError("stable archive SHA mismatch vs checksums.txt")
    if str(artifacts[name].get("sha256", "")).lower() != actual:
        raise RehearsalError("stable archive SHA mismatch vs release-manifest.json")
    if manifest.get("version") != version:
        raise RehearsalError("stable manifest version is not the derived/pinned version")
    manifest_commit = str(manifest.get("commit", ""))
    if commit is not None:
        if manifest_commit != commit:
            raise RehearsalError("stable manifest commit is not the pinned baseline commit")
    elif not re.fullmatch(r"[0-9a-f]{40}", manifest_commit):
        raise RehearsalError("stable manifest has no valid commit for the current stable")
    else:
        commit = manifest_commit

    metadata = {
        "case": label,
        "source": source,
        "release_tag": release_tag,
        "version": version,
        "commit": commit,
        "archive": name,
        "archive_sha256": actual,
        "checksums_txt_sha256": checksums[name].lower(),
        "manifest_sha256": str(artifacts[name]["sha256"]).lower(),
        "checksums_file_sha256": sha256_file(checksums_path),
        "manifest_file_sha256": sha256_file(manifest_path),
    }
    binary_suffix = ".exe" if goos == "windows" else ""
    extracted = extract_archive(archive, fixtures / "extracted")
    binary = extracted / ("carbon-panel" + binary_suffix)
    if not binary.is_file():
        raise RehearsalError("stable archive lacks carbon-panel binary")
    metadata["binary_version"] = binary_version(binary)
    metadata["binary_sha256"] = sha256_file(binary)
    build_info = extracted / "BUILD-INFO.json"
    if build_info.is_file():
        info = json.loads(build_info.read_text(encoding="utf-8"))
        metadata["build_info"] = info
        if info.get("version") != version or info.get("commit") != commit:
            raise RehearsalError("stable BUILD-INFO.json disagrees with the verified record")
    return binary, metadata


def build_candidate(workspace, version, commit, local_binary=None, go_binary="go"):
    """Build the candidate from the checked-out tree, or use an injected binary."""
    suffix = ".exe" if platform.system() == "Windows" else ""
    if local_binary:
        binary = Path(local_binary).resolve()
    else:
        binary = workspace / ("carbon-panel-candidate" + suffix)
        env = {**os.environ, "CGO_ENABLED": "0"}
        ldflags = (
            "-s -w "
            "-X github.com/athNdev/carbon-panel/internal/buildinfo.Version=%s "
            "-X github.com/athNdev/carbon-panel/internal/buildinfo.Commit=%s "
            "-X github.com/athNdev/carbon-panel/internal/buildinfo.Date=disposable-rehearsal"
        ) % (version, commit)
        subprocess.run([go_binary, "build", "-trimpath", "-buildvcs=false",
                        "-ldflags", ldflags, "-o", str(binary), "./cmd/carbon-panel"],
                       cwd=str(ROOT), env=env, check=True, timeout=600)
    if not binary.is_file():
        raise RehearsalError("candidate binary missing: %s" % binary)
    reported = binary_version(binary)
    if version not in reported or commit not in reported:
        raise RehearsalError("candidate --version does not carry version/commit: %s" % reported)
    return binary, {
        "version": version,
        "commit": commit,
        "binary_sha256": sha256_file(binary),
        "binary_version": reported,
    }


def seed_worlds(workdir):
    world = Path(workdir) / "worlds" / "survival"
    world.mkdir(parents=True, exist_ok=True)
    (world / "level.dat").write_bytes(b"synthetic-disposable-v140-level")
    (world / "region.rgn").write_bytes(b"synthetic-disposable-region" * 64)
    backup = Path(workdir) / "backups" / "weekly.tar.zst"
    backup.parent.mkdir(parents=True, exist_ok=True)
    backup.write_bytes(b"synthetic-disposable-backup" * 32)


def copy_seeded_worlds(source, destination):
    """Give the candidate/copy-back runs the SAME seeded worlds the stable run saw."""
    for child in ("worlds", "backups"):
        shutil.copytree(Path(source) / child, Path(destination) / child, dirs_exist_ok=True)


# --- Per-case rehearsal -------------------------------------------------------

def run_case(workspace, label, stable_binary, candidate_binary,
             expected_stable_tables, expected_candidate_tables,
             stable_port, candidate_port, copyback_port):
    """Run one full real-binary rehearsal and return its checks and metadata."""
    checks = {}

    def check(name, value):
        checks[label + "_" + name] = bool(value)

    stable_ws = workspace / (label + "-stable-run")
    write_config(stable_ws, stable_ws / "panel.db", stable_port)
    # Seed BEFORE starting the real binary so it actually operates on these files.
    seed_worlds(stable_ws)
    seeded = seeded_file_hashes(stable_ws)
    explicit_stable_count = expected_stable_tables is not None
    if not explicit_stable_count:
        # CURRENT case: the real binary defines its own baseline; do not guess.
        stable_db = run_service_any(stable_binary, stable_ws)
        expected_stable_tables = len(tables(stable_db))
    else:
        stable_db = run_service(stable_binary, stable_ws, expected_stable_tables)
    stable_tables = tables(stable_db)
    stable_counts = row_counts(stable_db, CORE_TABLES)
    check("stable_integrity_ok", integrity(stable_db) == "ok")
    check("stable_schema_nonempty", len(stable_tables) > 0)
    if explicit_stable_count:
        check("stable_table_count_expected", len(stable_tables) == expected_stable_tables)
    check("seeded_worlds_present_after_stable_run", seeded_file_hashes(stable_ws) == seeded)

    backup_dir = workspace / (label + "-backup")
    backup_dir.mkdir(parents=True, exist_ok=True)
    back_up_database(stable_db, backup_dir / "panel.db")
    copy_seeded_worlds(stable_ws, backup_dir)
    check("backup_integrity_ok", integrity(backup_dir / "panel.db") == "ok")
    check("backup_schema_matches_stable", tables(backup_dir / "panel.db") == stable_tables)

    candidate_ws = workspace / (label + "-candidate-run")
    write_config(candidate_ws, candidate_ws / "panel.db", candidate_port)
    shutil.copyfile(backup_dir / "panel.db", candidate_ws / "panel.db")
    for sidecar in ("-wal", "-shm"):
        sidecar_path = Path(str(backup_dir / "panel.db") + sidecar)
        if sidecar_path.is_file():
            shutil.copyfile(sidecar_path, str(candidate_ws / "panel.db") + sidecar)
    copy_seeded_worlds(backup_dir, candidate_ws)
    explicit_candidate_count = expected_candidate_tables is not None
    if not explicit_candidate_count:
        candidate_db = run_service_any(candidate_binary, candidate_ws)
        expected_candidate_tables = len(tables(candidate_db))
    else:
        candidate_db = run_service(candidate_binary, candidate_ws, expected_candidate_tables)
    upgraded_tables = tables(candidate_db)
    candidate_only_tables = upgraded_tables - stable_tables
    upgraded_counts = row_counts(candidate_db, CORE_TABLES)
    check("upgrade_is_additive_superset", stable_tables.issubset(upgraded_tables))
    check("upgrade_adds_expected_models", EXPECTED_CANDIDATE_MODEL_TABLES.issubset(upgraded_tables))
    check("upgrade_schema_grows_or_matches", len(upgraded_tables) >= len(stable_tables))
    if explicit_candidate_count:
        check("upgrade_table_count_expected", len(upgraded_tables) == expected_candidate_tables)
    check("upgrade_integrity_ok", integrity(candidate_db) == "ok")
    check("upgrade_preserves_rows", stable_counts == upgraded_counts)
    check("worlds_byte_identical_after_upgrade", seeded_file_hashes(candidate_ws) == seeded)

    # Forced failure: a missing snapshot must be refused without mutation.
    before_failure = row_counts(candidate_db, CORE_TABLES)
    missing = run_restore(candidate_binary, candidate_ws, workspace / "does-not-exist.bak")
    check("forced_failure_missing_snapshot_refused", missing.returncode != 0)
    check("forced_failure_missing_snapshot_no_mutation",
          before_failure == row_counts(candidate_db, CORE_TABLES) and
          tables(candidate_db) == upgraded_tables)

    # Real app-level restore via the shipped -restore-panel-backup flag.
    premigrate = candidate_ws / "panel.db.pre-migrate.bak"
    if not premigrate.is_file():
        premigrate = candidate_ws / "data" / "panel.db.pre-migrate.bak"
    check("premigrate_snapshot_exists", premigrate.is_file())
    if premigrate.is_file():
        restored = run_restore(candidate_binary, candidate_ws, premigrate)
        check("app_level_restore_exit_zero", restored.returncode == 0)
        check("app_level_restore_returns_stable_tables",
              tables(candidate_db) == stable_tables)
        check("app_level_restore_integrity_ok", integrity(candidate_db) == "ok")
        check("app_level_restore_preserves_rows",
              stable_counts == row_counts(candidate_db, CORE_TABLES))
        check("worlds_byte_identical_after_app_restore",
              seeded_file_hashes(candidate_ws) == seeded)

    # Real copy-back restore onto a SEPARATE disposable path.
    copyback_ws = workspace / (label + "-copyback-run")
    write_config(copyback_ws, copyback_ws / "panel.db", copyback_port)
    shutil.copyfile(backup_dir / "panel.db", copyback_ws / "panel.db")
    copy_seeded_worlds(backup_dir, copyback_ws)
    copyback_db = copyback_ws / "panel.db"
    check("copyback_integrity_ok", integrity(copyback_db) == "ok")
    restored_tables = tables(copyback_db)
    check("copyback_returns_stable_tables", restored_tables == stable_tables)
    check("copyback_removes_new_models", candidate_only_tables.isdisjoint(restored_tables))
    check("copyback_preserves_rows", stable_counts == row_counts(copyback_db, CORE_TABLES))
    check("worlds_byte_identical_after_copyback", seeded_file_hashes(copyback_ws) == seeded)

    return checks, {
        "stable_tables": len(stable_tables),
        "candidate_tables": len(upgraded_tables),
        "candidate_only_tables": sorted(candidate_only_tables),
        "expected_stable_tables": expected_stable_tables,
        "expected_candidate_tables": expected_candidate_tables,
        "core_rows_before": stable_counts,
        "core_rows_after_upgrade": upgraded_counts,
        "world_files_verified": len(seeded),
    }


def run_model_and_planner_checks(workspace):
    """Exercise the checked-in model rehearsal and staged planner as extra gates."""
    sys.path.insert(0, str(ROOT))
    from scripts import rehearse_upgrade, node_vm_upgrade  # noqa: WPS433
    model = rehearse_upgrade.run(workspace / "model-rehearsal")
    policy = json.loads((ROOT / "packaging/node-vm/config/upgrade-policy.json").read_text())
    old = {"carbon-node/agent": b"old-agent-bytes"}
    plan = node_vm_upgrade.plan_upgrade("v1.0.40", "candidate", old, policy, "rehearsal-batch")
    tampered = {"carbon-node/agent": b"unexpected-mutation"}
    details = {
        "before_tables": model["before"]["tables"],
        "after_tables": model["after_upgrade"]["tables"],
        "rollback_tables": model["after_copy_back_rollback"]["tables"],
    }
    checks = {
        "model_rehearsal_pass": model["pass"],
        "model_before_tables": details["before_tables"] == LEGACY_TABLE_COUNT,
        "model_after_tables": details["after_tables"] == CANDIDATE_TABLE_COUNT,
        "model_rollback_tables": details["rollback_tables"] == LEGACY_TABLE_COUNT,
        "undo_manifest_written_before_promotion": (
            plan["steps"].index("undo: write and verify " + plan["undo_dir"] +
                                "/manifest.json before mutation") <
            next(i for i, s in enumerate(plan["steps"]) if s.startswith("promote:"))),
        "undo_verifies_clean_bytes": node_vm_upgrade.verify_undo(plan["undo_manifest"], old),
        "undo_refuses_unexpected_mutation": not node_vm_upgrade.verify_undo(
            plan["undo_manifest"], tampered),
    }
    return {("model_" + key if not key.startswith("model_") and not key.startswith("undo_")
             else key): bool(value) for key, value in checks.items()}, details


def execute(workspace, args):
    workspace = Path(workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    report = {
        "schema": "upgrade-rehearsal-ci-v2",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "workspace": str(workspace),
        "target_os": args.target_os,
        "target_arch": args.target_arch,
        "checks": {},
        "cases": {},
    }
    checks = report["checks"]

    legacy = legacy_baseline()
    current = derive_current_stable()
    report["baselines"] = {"legacy": legacy, "current": current}

    if args.current_stable_tag:
        current["tag"] = args.current_stable_tag
    if args.current_stable_version:
        current["version"] = args.current_stable_version

    legacy_binary, legacy_meta = acquire_stable(
        workspace, "legacy", legacy["version"], legacy["commit"],
        args.target_os, args.target_arch, legacy["tag"], source="pinned",
        local_archive=args.stable_archive, local_checksums=args.stable_checksums,
        local_manifest=args.stable_manifest)
    report["stable_legacy"] = legacy_meta

    if current["tag"] == legacy["tag"] and current["version"] == legacy["version"]:
        current_binary, current_meta = legacy_binary, dict(legacy_meta)
        current_meta["case"] = "current"
        current_meta["source"] = "derived-published-stable"
    else:
        current_binary, current_meta = acquire_stable(
            workspace, "current", current["version"], None,
            args.target_os, args.target_arch, current["tag"], source="derived")
    report["stable_current"] = current_meta

    candidate_binary, candidate_meta = build_candidate(
        workspace, args.candidate_version, args.candidate_commit,
        args.candidate_binary, args.go_binary)
    report["candidate"] = candidate_meta

    legacy_checks, legacy_detail = run_case(
        workspace, "legacy", legacy_binary, candidate_binary,
        LEGACY_TABLE_COUNT, CANDIDATE_TABLE_COUNT,
        args.stable_port, args.candidate_port, args.copyback_port)
    current_checks, current_detail = run_case(
        workspace, "current", current_binary, candidate_binary,
        None, None,
        args.current_stable_port, args.current_candidate_port, args.current_copyback_port)
    checks.update(legacy_checks)
    checks.update(current_checks)
    report["cases"]["legacy"] = legacy_detail
    report["cases"]["current"] = current_detail

    model_checks, model_details = run_model_and_planner_checks(workspace)
    checks.update(model_checks)
    report["model_and_planner"] = model_details

    report["pass"] = all(checks.values())
    report["failed_checks"] = [name for name, ok in checks.items() if not ok]
    report["limits"] = [
        "Disposable fixtures only; no live world, database, host, registry, tag or release is touched.",
        "Both stable DBs are seeded by real stable binaries and candidate DBs by the real candidate binary.",
        "No Docker daemon, gameplay, browser, IdP or load balancer is exercised by this job.",
        "The legacy case pins the 35 -> 45 baseline; the current case derives its baseline from the authoritative release record and asserts additive growth without a hardcoded count.",
        "Operator-breaking OIDC/PROXY config changes remain documented go/no-go gates.",
    ]
    (workspace / "rehearsal-ci-report.json").write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def run_service_any(binary, workdir, timeout=SERVICE_TIMEOUT_SECONDS):
    """Start a real binary and stop it once its DB has stopped changing."""
    workdir = Path(workdir).resolve()
    db_path = workdir / "panel.db"
    log = (workdir / "service.log").open("ab")
    try:
        process = subprocess.Popen([str(binary), "-config", str(workdir)],
                                   cwd=str(workdir), stdout=log, stderr=subprocess.STDOUT)
    finally:
        log.close()
    deadline = time.monotonic() + timeout
    last_count = -1
    stable_rounds = 0
    try:
        while time.monotonic() < deadline:
            if db_path.is_file():
                try:
                    count = len(tables(db_path))
                    if count > 0 and count == last_count:
                        stable_rounds += 1
                        if stable_rounds >= 6:
                            break
                    else:
                        stable_rounds = 0
                    last_count = count
                except sqlite3.Error:
                    pass
            if process.poll() is not None:
                break
            time.sleep(0.5)
    finally:
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=15)
    if last_count <= 0:
        raise RehearsalError("%s did not produce a readable schema: %s" % (binary, workdir))
    return db_path


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True,
                        help="caller-owned disposable directory")
    parser.add_argument("--target-os", default=platform.system().lower())
    parser.add_argument("--target-arch", default=platform.machine().lower())
    parser.add_argument("--candidate-version", default="2.0.0-verify.ci")
    parser.add_argument("--candidate-commit", default="")
    parser.add_argument("--go-binary", default="go")
    parser.add_argument("--stable-archive", help="inject a local legacy archive (offline fixture)")
    parser.add_argument("--stable-checksums", help="inject local checksums.txt")
    parser.add_argument("--stable-manifest", help="inject local release-manifest.json")
    parser.add_argument("--current-stable-tag", help="override derived current stable tag (offline fixture)")
    parser.add_argument("--current-stable-version", help="override derived current stable version (offline fixture)")
    parser.add_argument("--candidate-binary", help="inject a prebuilt candidate binary")
    parser.add_argument("--stable-port", type=int, default=18090)
    parser.add_argument("--candidate-port", type=int, default=18091)
    parser.add_argument("--copyback-port", type=int, default=18092)
    parser.add_argument("--current-stable-port", type=int, default=18093)
    parser.add_argument("--current-candidate-port", type=int, default=18094)
    parser.add_argument("--current-copyback-port", type=int, default=18095)
    args = parser.parse_args(argv)
    if not args.candidate_commit:
        args.candidate_commit = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=str(ROOT), check=True,
            capture_output=True, text=True).stdout.strip()
    try:
        report = execute(args.workspace, args)
    except Exception as error:  # noqa: BLE001 - fail closed AND leave evidence
        failure = {
            "schema": "upgrade-rehearsal-ci-v2",
            "pass": False,
            "error": "%s: %s" % (type(error).__name__, error),
            "workspace": str(Path(args.workspace).resolve()),
        }
        try:
            (Path(args.workspace) / "rehearsal-ci-report.json").write_text(
                json.dumps(failure, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        except OSError:
            pass
        print(json.dumps(failure, indent=2), file=sys.stderr)
        return 1
    print(json.dumps({
        "pass": report["pass"],
        "failed_checks": report["failed_checks"],
        "legacy": report["stable_legacy"],
        "current": report["stable_current"],
        "candidate": report["candidate"],
        "report": str(Path(args.workspace) / "rehearsal-ci-report.json"),
    }, indent=2))
    return 0 if report["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
