#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Recurring DISPOSABLE real-binary upgrade rehearsal (OSS-406).

This is the CI-ready version of the release-coordinator rehearsal: it uses the
REAL published v1.0.40 archive and a REAL candidate `carbon-panel` binary, and
runs both against seeded disposable DB/world copies. It asserts integrity,
additive table growth, row/world preservation and forced-failure rollback.

Every write stays under --workspace (a caller-owned temp directory). This script
never touches a live world, a host, a registry, a tag or a release. It fails
closed: a missing/mismatched fixture, a binary that does not reach the expected
schema, or any failed check produces a non-zero exit and an evidence report.

The checked-in MODEL rehearsal (`scripts/rehearse_upgrade.py`) and the staged
upgrade planner (`scripts/node_vm_upgrade.py`) are executed as additional
deterministic checks; neither is a substitute for the real binaries below.

Typical CI invocation (Linux runner)::

    python3 scripts/upgrade_rehearsal_ci.py \
      --workspace "$RUNNER_TEMP/upgrade-rehearsal" \
      --stable-version v1.0.40 --stable-commit f88748be6a216b187e675bbb1f110df07e544148 \
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
RELEASE_BASE = "https://github.com/athNdev/carbonpanel/releases/download/v1.0.40"

# Tables counted with the project's historical convention (every table,
# including sqlite_sequence and casbin_rule): v1.0.40 seeds 35 and the
# candidate adds 9 model tables for 44. Asserted as a superset relation plus
# these exact counts so drift is visible rather than silently accepted.
STABLE_TABLE_COUNT = 35
CANDIDATE_TABLE_COUNT = 44
NEW_MODEL_TABLES = {
    "player_identities", "player_aliases", "player_sessions",
    "player_access_states", "player_moderation_actions", "world_entries",
    "world_revisions", "world_bindings", "world_import_jobs",
}
# Core tables whose rows must be preserved across upgrade and both restores.
CORE_TABLES = (
    "servers", "server_configs", "users", "nodes", "roles", "user_roles",
    "server_blueprints", "backup_records", "server_snapshots",
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


def hash_tree(root, suffixes=None):
    root = Path(root)
    result = {}
    for path in sorted(root.rglob("*")):
        if not path.is_file():
            continue
        if suffixes is not None and path.suffix not in suffixes:
            continue
        result[str(path.relative_to(root)).replace("\\", "/")] = sha256_file(path)
    return result


def world_hashes(root):
    return hash_tree(root, {".dat", ".rgn", ".zst", ".json", ".txt"})


# --- Real binary execution ----------------------------------------------------

def write_config(directory, db_path, port):
    """Write a minimal, fully disposable config; all paths stay under workspace."""
    directory = Path(directory).resolve()
    directory.mkdir(parents=True, exist_ok=True)
    for child in ("data", "backups", "temp", "worlds/survival"):
        (directory / child).mkdir(parents=True, exist_ok=True)
    text = (
        'server:\n'
        '  host: "127.0.0.1"\n'
        '  port: "%d"\n'
        'database:\n'
        '  path: "%s"\n'
        '  panel_backup: "off"\n'
        'storage:\n'
        '  data_dir: "%s"\n'
        '  backup_dir: "%s"\n'
        '  temp_dir: "%s"\n'
        'auth:\n'
        '  local:\n'
        '    enabled: true\n'
    ) % (port,
         str(Path(db_path).resolve()).replace("\\", "/"),
         str(directory / "data").replace("\\", "/"),
         str(directory / "backups").replace("\\", "/"),
         str(directory / "temp").replace("\\", "/"))
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
    """Start a real binary, wait until it materialises the expected schema, stop it.

    The binary may exit on its own once migration is complete (for example when
    no Docker daemon is available). That is acceptable: the assertion is against
    the on-disk schema, not against a long-running service. A process that exits
    before the schema appears fails closed.
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
                # One final look: the last write may have completed at exit.
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
        tail = ""
        log_path = workdir / "service.log"
        if log_path.is_file():
            tail = log_path.read_text(encoding="utf-8", errors="replace")[-2000:]
        raise RehearsalError(
            "%s did not produce %d tables within %ss; log tail:\n%s" %
            (binary, expected_tables, timeout, tail))
    return db_path


def run_restore(binary, workdir, snapshot):
    """Offline app-level restore: must exit zero with the panel stopped."""
    result = subprocess.run(
        [str(binary), "-config", str(workdir), "-restore-panel-backup", str(snapshot)],
        cwd=str(workdir), capture_output=True, text=True, timeout=60)
    return result


# --- Fixture acquisition ------------------------------------------------------

def stable_archive_name(version, goos, goarch):
    suffix = ".zip" if goos == "windows" else ".tar.gz"
    return "carbon-panel_%s_%s_%s%s" % (version, goos, goarch, suffix)


def acquire_stable(workspace, version, commit, goos, goarch,
                   local_archive=None, local_checksums=None, local_manifest=None):
    """Download and verify the real stable archive against BOTH public records."""
    name = stable_archive_name(version, goos, goarch)
    fixtures = workspace / "stable-fixtures"
    fixtures.mkdir(parents=True, exist_ok=True)
    archive = Path(local_archive) if local_archive else fixtures / name
    if local_archive is None:
        download(RELEASE_BASE + "/" + name, archive)
    if not archive.is_file():
        raise RehearsalError("stable archive missing: %s" % archive)
    checksums_path = Path(local_checksums) if local_checksums else fixtures / "checksums.txt"
    manifest_path = Path(local_manifest) if local_manifest else fixtures / "release-manifest.json"
    if local_checksums is None:
        download(RELEASE_BASE + "/checksums.txt", checksums_path)
    if local_manifest is None:
        download(RELEASE_BASE + "/release-manifest.json", manifest_path)
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
    if artifacts[name]["sha256"].lower() != actual:
        raise RehearsalError("stable archive SHA mismatch vs release-manifest.json")
    if manifest.get("version") != version or manifest.get("commit") != commit:
        raise RehearsalError("stable manifest version/commit is not the pinned stable")
    metadata = {
        "version": version,
        "commit": commit,
        "archive": name,
        "archive_sha256": actual,
        "checksums_txt_sha256": checksums[name].lower(),
        "manifest_sha256": artifacts[name]["sha256"].lower(),
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
        metadata["build_info"] = json.loads(build_info.read_text(encoding="utf-8"))
        if metadata["build_info"].get("version") != version or \
                metadata["build_info"].get("commit") != commit:
            raise RehearsalError("stable BUILD-INFO.json disagrees with pinned stable")
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


# --- Checks -------------------------------------------------------------------

def seed_worlds(workdir):
    world = Path(workdir) / "worlds" / "survival"
    world.mkdir(parents=True, exist_ok=True)
    (world / "level.dat").write_bytes(b"synthetic-disposable-v140-level")
    (world / "region.rgn").write_bytes(b"synthetic-disposable-region" * 64)
    backup = Path(workdir) / "backups" / "weekly.tar.zst"
    backup.parent.mkdir(parents=True, exist_ok=True)
    backup.write_bytes(b"synthetic-disposable-backup" * 32)


def run_model_and_planner_checks(workspace):
    """Exercise the checked-in model rehearsal and staged planner as extra gates."""
    sys.path.insert(0, str(ROOT))
    from scripts import rehearse_upgrade, node_vm_upgrade  # noqa: WPS433
    model_workspace = workspace / "model-rehearsal"
    model = rehearse_upgrade.run(model_workspace)
    policy = json.loads((ROOT / "packaging/node-vm/config/upgrade-policy.json").read_text())
    old = {"carbon-node/agent": b"old-agent-bytes"}
    plan = node_vm_upgrade.plan_upgrade("v1.0.40", "candidate", old, policy, "rehearsal-batch")
    tampered = {"carbon-node/agent": b"unexpected-mutation"}
    return {
        "model_rehearsal_pass": bool(model["pass"]),
        "model_before_tables": model["before"]["tables"],
        "model_after_tables": model["after_upgrade"]["tables"],
        "model_rollback_tables": model["after_copy_back_rollback"]["tables"],
        "undo_manifest_written_before_promotion": (
            plan["steps"].index("undo: write and verify " + plan["undo_dir"] +
                                "/manifest.json before mutation") <
            next(i for i, s in enumerate(plan["steps"]) if s.startswith("promote:"))),
        "undo_verifies_clean_bytes": node_vm_upgrade.verify_undo(plan["undo_manifest"], old),
        "undo_refuses_unexpected_mutation": not node_vm_upgrade.verify_undo(
            plan["undo_manifest"], tampered),
    }


def execute(workspace, args):
    workspace = Path(workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    report = {
        "schema": "upgrade-rehearsal-ci-v1",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "workspace": str(workspace),
        "target_os": args.target_os,
        "target_arch": args.target_arch,
        "checks": {},
    }
    stable_binary, stable_meta = acquire_stable(
        workspace, args.stable_version, args.stable_commit, args.target_os, args.target_arch,
        args.stable_archive, args.stable_checksums, args.stable_manifest)
    candidate_binary, candidate_meta = build_candidate(
        workspace, args.candidate_version, args.candidate_commit,
        args.candidate_binary, args.go_binary)
    report["stable"] = stable_meta
    report["candidate"] = candidate_meta
    checks = report["checks"]

    # 1. Real stable binary seeds a 35-table DB on a disposable config.
    stable_ws = workspace / "stable-run"
    write_config(stable_ws, stable_ws / "panel.db", args.stable_port)
    stable_db = run_service(stable_binary, stable_ws, STABLE_TABLE_COUNT)
    seed_worlds(stable_ws)
    stable_tables = tables(stable_db)
    stable_counts = row_counts(stable_db, CORE_TABLES)
    checks["stable_schema_is_35_tables"] = len(stable_tables) == STABLE_TABLE_COUNT
    checks["stable_integrity_ok"] = integrity(stable_db) == "ok"

    # 2. Real online backup + quiesced world copy; hash every world file.
    backup_dir = workspace / "backup"
    backup_dir.mkdir(parents=True, exist_ok=True)
    back_up_database(stable_db, backup_dir / "panel.db")
    shutil.copytree(stable_ws / "worlds", backup_dir / "worlds", dirs_exist_ok=True)
    shutil.copytree(stable_ws / "backups", backup_dir / "backups", dirs_exist_ok=True)
    backup_hashes = world_hashes(backup_dir)
    checks["backup_integrity_ok"] = integrity(backup_dir / "panel.db") == "ok"
    checks["backup_schema_is_35_tables"] = len(tables(backup_dir / "panel.db")) == STABLE_TABLE_COUNT

    # 3. Real candidate binary upgrades a disposable COPY of the stable DB.
    candidate_ws = workspace / "candidate-run"
    write_config(candidate_ws, candidate_ws / "panel.db", args.candidate_port)
    shutil.copyfile(backup_dir / "panel.db", candidate_ws / "panel.db")
    for sidecar in ("-wal", "-shm"):
        sidecar_path = Path(str(backup_dir / "panel.db") + sidecar)
        if sidecar_path.is_file():
            shutil.copyfile(sidecar_path, str(candidate_ws / "panel.db") + sidecar)
    candidate_db = run_service(candidate_binary, candidate_ws, CANDIDATE_TABLE_COUNT)
    upgraded_tables = tables(candidate_db)
    upgraded_counts = row_counts(candidate_db, CORE_TABLES)
    checks["upgrade_is_additive_superset"] = stable_tables.issubset(upgraded_tables)
    checks["upgrade_adds_expected_models"] = NEW_MODEL_TABLES.issubset(upgraded_tables)
    checks["upgrade_schema_is_44_tables"] = len(upgraded_tables) == CANDIDATE_TABLE_COUNT
    checks["upgrade_integrity_ok"] = integrity(candidate_db) == "ok"
    checks["upgrade_preserves_rows"] = stable_counts == upgraded_counts

    # 4. Forced failure: a missing snapshot must be refused without mutation.
    before_failure = row_counts(candidate_db, CORE_TABLES)
    missing = run_restore(candidate_binary, candidate_ws, workspace / "does-not-exist.bak")
    checks["forced_failure_missing_snapshot_refused"] = missing.returncode != 0
    checks["forced_failure_missing_snapshot_no_mutation"] = (
        before_failure == row_counts(candidate_db, CORE_TABLES) and
        len(tables(candidate_db)) == CANDIDATE_TABLE_COUNT)

    # 5. Real app-level restore via the shipped -restore-panel-backup flag.
    premigrate = candidate_ws / "panel.db.pre-migrate.bak"
    if not premigrate.is_file():
        premigrate = candidate_ws / "data" / "panel.db.pre-migrate.bak"
    checks["premigrate_snapshot_exists"] = premigrate.is_file()
    if premigrate.is_file():
        restored = run_restore(candidate_binary, candidate_ws, premigrate)
        checks["app_level_restore_exit_zero"] = restored.returncode == 0
        checks["app_level_restore_returns_35_tables"] = len(tables(candidate_db)) == STABLE_TABLE_COUNT
        checks["app_level_restore_integrity_ok"] = integrity(candidate_db) == "ok"
        checks["app_level_restore_preserves_rows"] = (
            stable_counts == row_counts(candidate_db, CORE_TABLES))

    # 6. Real copy-back restore onto a SEPARATE disposable path.
    copyback_ws = workspace / "copyback-run"
    write_config(copyback_ws, copyback_ws / "panel.db", args.copyback_port)
    shutil.copyfile(backup_dir / "panel.db", copyback_ws / "panel.db")
    copyback_db = copyback_ws / "panel.db"
    shutil.copytree(backup_dir / "worlds", copyback_ws / "worlds", dirs_exist_ok=True)
    shutil.copytree(backup_dir / "backups", copyback_ws / "backups", dirs_exist_ok=True)
    checks["copyback_integrity_ok"] = integrity(copyback_db) == "ok"
    checks["copyback_returns_35_tables"] = len(tables(copyback_db)) == STABLE_TABLE_COUNT
    checks["copyback_removes_new_models"] = not (NEW_MODEL_TABLES & tables(copyback_db))
    checks["copyback_preserves_rows"] = stable_counts == row_counts(copyback_db, CORE_TABLES)
    restored_worlds = world_hashes(copyback_ws)
    checks["worlds_byte_identical_after_copyback"] = backup_hashes == restored_worlds
    checks["backup_untouched"] = backup_hashes == world_hashes(backup_dir)

    # 7. Deterministic model rehearsal + staged planner from the repo.
    report["model_and_planner"] = run_model_and_planner_checks(workspace)
    for key, value in report["model_and_planner"].items():
        checks[key] = bool(value)

    report["pass"] = all(checks.values())
    report["failed_checks"] = [name for name, ok in checks.items() if not ok]
    report["world_files_hashed"] = len(backup_hashes)
    report["limits"] = [
        "Disposable fixtures only; no live world, database, host, registry, tag or release is touched.",
        "The stable DB is seeded by the real v1.0.40 binary and the candidate DB by the real candidate binary.",
        "No Docker daemon, gameplay, browser, IdP or load balancer is exercised by this job.",
        "v1.0.40 -> 2.0.0 schema growth is asserted as additive; operator-breaking config changes remain documented go/no-go gates.",
    ]
    (workspace / "rehearsal-ci-report.json").write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True,
                        help="caller-owned disposable directory; all writes stay here")
    parser.add_argument("--stable-version", default="v1.0.40")
    parser.add_argument("--stable-commit", default="f88748be6a216b187e675bbb1f110df07e544148")
    parser.add_argument("--target-os", default=platform.system().lower())
    parser.add_argument("--target-arch", default=platform.machine().lower())
    parser.add_argument("--candidate-version", default="2.0.0-verify.ci")
    parser.add_argument("--candidate-commit", default="")
    parser.add_argument("--go-binary", default="go")
    parser.add_argument("--stable-archive", help="inject a local stable archive (offline fixture)")
    parser.add_argument("--stable-checksums", help="inject local checksums.txt")
    parser.add_argument("--stable-manifest", help="inject local release-manifest.json")
    parser.add_argument("--candidate-binary", help="inject a prebuilt candidate binary")
    parser.add_argument("--stable-port", type=int, default=18090)
    parser.add_argument("--candidate-port", type=int, default=18091)
    parser.add_argument("--copyback-port", type=int, default=18092)
    args = parser.parse_args(argv)
    if not args.candidate_commit:
        args.candidate_commit = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=str(ROOT), check=True,
            capture_output=True, text=True).stdout.strip()
    try:
        report = execute(args.workspace, args)
    except (RehearsalError, subprocess.CalledProcessError, OSError, ValueError,
            json.JSONDecodeError, sqlite3.Error) as error:
        # A failure still leaves an evidence artifact; it never looks green.
        failure = {
            "schema": "upgrade-rehearsal-ci-v1",
            "pass": False,
            "error": str(error),
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
        "stable": report["stable"],
        "candidate": report["candidate"],
        "report": str(Path(args.workspace) / "rehearsal-ci-report.json"),
    }, indent=2))
    return 0 if report["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
