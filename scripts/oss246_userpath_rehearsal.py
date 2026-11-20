#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Disposable v1.0.40 -> candidate USER-PATH upgrade/rollback rehearsal (OSS-246).

Complements ``scripts/upgrade_rehearsal_ci.py`` (schema/row/world preservation).
It seeds a *representative persisted legacy* SQLite DB and world/backup files,
then starts the REAL v1.0.40 binary and the REAL candidate binary and drives the
normal HTTP/Connect entry points to prove:

  * old local login still works (same username/password hash),
  * server inventory, backup list and world/config reads survive the upgrade,
  * server-scoped access is preserved (a subuser can read only its own server),
  * a copy-back rollback of the OLD DB + world/backup pair restores the same
    old login / server / backup / world / scoped journeys under the old binary.

Every write stays under --workspace (a caller-owned disposable directory). No
live world, DB, host, registry, tag or release is read or written. The candidate
binary is injected (e.g. the verified archive from a build-only release run) or
built from the checkout with pure-Go settings; nothing is published.

Typical Linux/WSL invocation::

    python3 scripts/oss246_userpath_rehearsal.py \
      --workspace "$TMP/oss246" \
      --stable-binary "$TMP/v140/carbon-panel" \
      --candidate-binary "$TMP/candidate/carbon-panel" \
      --stable-version 1.0.40 --candidate-version 2.0.1 \
      --candidate-commit e8245faa... --report "$TMP/oss246/report.json"
"""
import argparse
import hashlib
import json
import os
import shutil
import socket
import sqlite3
import subprocess
import sys
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from scripts import upgrade_rehearsal_ci as ci  # noqa: E402

# Fixed test credential; bcrypt hash (cost 10) is generated once and embedded
# so the fixture needs no Python bcrypt dependency on the runner.
LEGACY_USERNAME = "oss246-admin"
LEGACY_PASSWORD = "RehearsalPass123!"
LEGACY_HASH = "$2b$10$Jf86zpvdRS0LLemvb9FvG.zOlnGHTnVYNduCvSMvsFT991jk4k5ae"
SCOPED_USERNAME = "oss246-scoped"
SCOPED_PASSWORD = LEGACY_PASSWORD  # same hash; different identity/grants
OWNED_SERVER = "srv-oss246-owned"
OTHER_SERVER = "srv-oss246-other"
WORLD_NAME = "survival"
BACKUP_NAME = "oss246-weekly"
SERVICE_TIMEOUT = 90


class RehearsalError(RuntimeError):
    pass


def now():
    return datetime.now(timezone.utc).isoformat()


def sha256_file(path):
    return ci.sha256_file(path)


def table_columns(db_path, table):
    connection = sqlite3.connect(str(db_path))
    try:
        return [row[1] for row in connection.execute("PRAGMA table_info('%s')" % table)]
    finally:
        connection.close()


def insert_legacy_rows(db_path):
    """Insert representative legacy rows into the stable-created 35-table DB.

    Column names are discovered from the real schema so a table-shape change
    fails closed instead of silently seeding the wrong thing.
    """
    required = {
        "users": ["id", "username", "password_hash", "auth_provider", "is_active"],
        "user_roles": ["id", "user_id", "role_name", "source"],
        "servers": ["id", "name", "mod_loader", "mc_version", "status"],
        "server_configs": ["id", "server_id", "level"],
        "server_subusers": ["server_id", "user_id", "permissions"],
        "backup_records": ["id", "server_id", "name", "path", "sha256"],
    }
    for table, columns in required.items():
        actual = table_columns(db_path, table)
        if not actual:
            raise RehearsalError("legacy table missing: %s" % table)
        missing = set(columns) - set(actual)
        if missing:
            raise RehearsalError("legacy table %s missing columns %s" % (table, sorted(missing)))

    def insert(table, values):
        cols = table_columns(db_path, table)
        keys = [key for key in values if key in cols]
        sql = "INSERT INTO '%s' (%s) VALUES (%s)" % (
            table, ",".join('"%s"' % key for key in keys), ",".join("?" for _ in keys))
        connection = sqlite3.connect(str(db_path))
        try:
            connection.execute(sql, [values[key] for key in keys])
            connection.commit()
        finally:
            connection.close()

    stamp = now()
    insert("users", dict(id="usr-oss246-admin", username=LEGACY_USERNAME,
                         password_hash=LEGACY_HASH, auth_provider="local", is_active=1,
                         created_at=stamp, updated_at=stamp))
    insert("users", dict(id="usr-oss246-scoped", username=SCOPED_USERNAME,
                         password_hash=LEGACY_HASH, auth_provider="local", is_active=1,
                         created_at=stamp, updated_at=stamp))
    insert("user_roles", dict(id="ur-oss246-admin", user_id="usr-oss246-admin",
                              role_name="admin", source="local", created_at=stamp))
    # The scoped identity has NO role: its only access is the per-server grant
    # below. A "user" role would grant global servers.read and hide scoping.
    for server_id, name in ((OWNED_SERVER, "OSS246 Owned"), (OTHER_SERVER, "OSS246 Other")):
        insert("servers", dict(id=server_id, name=name, mod_loader="VANILLA",
                               mc_version="1.21.1", status="STOPPED", node_id="default",
                               data_path="/app/data/servers/" + server_id, port=25565,
                               created_at=stamp, updated_at=stamp))
        insert("server_configs", dict(id="cfg-" + server_id, server_id=server_id,
                                      level=WORLD_NAME, updated_at=stamp))
    insert("server_subusers", dict(server_id=OWNED_SERVER, user_id="usr-oss246-scoped",
                                   permissions=json.dumps(["servers.read", "backups.read"]),
                                   created_at=stamp))
    insert("backup_records", dict(id="bk-oss246", server_id=OWNED_SERVER, name=BACKUP_NAME,
                                  path="backups/oss246-weekly.tar.zst", sha256="",
                                  size_bytes=1024, status="complete", created_at=stamp))


def seed_world_files(workdir):
    root = Path(workdir)
    world = root / "worlds" / OWNED_SERVER / WORLD_NAME
    world.mkdir(parents=True, exist_ok=True)
    (world / "level.dat").write_bytes(b"oss246-legacy-level-v1.0.40" * 8)
    region = world / "region"
    region.mkdir(parents=True, exist_ok=True)
    (region / "r.0.0.mca").write_bytes(b"oss246-legacy-region-chunk" * 64)
    backups = root / "backups"
    backups.mkdir(parents=True, exist_ok=True)
    (backups / "oss246-weekly.tar.zst").write_bytes(b"oss246-legacy-backup-archive" * 32)


def seeded_hashes(workdir):
    root = Path(workdir)
    files = (
        "worlds/%s/%s/level.dat" % (OWNED_SERVER, WORLD_NAME),
        "worlds/%s/%s/region/r.0.0.mca" % (OWNED_SERVER, WORLD_NAME),
        "backups/oss246-weekly.tar.zst",
    )
    out = {}
    for relative in files:
        path = root / relative
        if not path.is_file():
            raise RehearsalError("seeded fixture missing: %s" % path)
        out[relative] = sha256_file(path)
    return out


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def start_service(binary, workdir, port):
    workdir = Path(workdir).resolve()
    log = (workdir / "service.log").open("ab")
    process = subprocess.Popen([str(binary), "-config", str(workdir)],
                               cwd=str(workdir), stdout=log, stderr=subprocess.STDOUT)
    deadline = time.monotonic() + SERVICE_TIMEOUT
    ready = False
    base = "http://127.0.0.1:%d" % port
    try:
        while time.monotonic() < deadline:
            if process.poll() is not None:
                break
            try:
                with urllib.request.urlopen(base + "/readyz", timeout=2) as response:
                    if response.status == 200:
                        ready = True
                        break
            except (urllib.error.URLError, OSError):
                time.sleep(0.5)
    finally:
        log.close()
    if not ready:
        stop_service(process)
        tail = (workdir / "service.log").read_text(encoding="utf-8", errors="replace")[-2000:]
        raise RehearsalError("service did not become ready: %s\n%s" % (binary, tail))
    return process


def stop_service(process):
    if process.poll() is None:
        process.terminate()
        try:
            process.wait(timeout=15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=15)


def rpc(base, procedure, payload, token=None):
    url = base + "/carbonpanel.v1." + procedure
    data = json.dumps(payload).encode("utf-8")
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            return response.status, json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as error:
        body = error.read().decode("utf-8", errors="replace")
        try:
            return error.code, json.loads(body)
        except json.JSONDecodeError:
            return error.code, {"raw": body}


def login(base, username, password):
    status, body = rpc(base, "AuthService/Login", {"username": username, "password": password})
    token = body.get("token") if status == 200 else None
    return status, token, body


def user_paths(base):
    """Drive the real HTTP/Connect entry points; return per-check booleans."""
    checks = {}
    status, admin_token, body = login(base, LEGACY_USERNAME, LEGACY_PASSWORD)
    checks["old_login_admin"] = status == 200 and bool(admin_token)
    checks["old_login_current_user"] = False
    checks["server_inventory"] = False
    checks["backup_list"] = False
    checks["world_config"] = False
    checks["scoped_read_own"] = False
    checks["scoped_read_other_denied"] = False
    checks["scoped_backup_own"] = False
    if not admin_token:
        return checks

    status, current = rpc(base, "AuthService/GetCurrentUser", {}, admin_token)
    checks["old_login_current_user"] = status == 200 and current.get("user", {}).get("username") == LEGACY_USERNAME

    status, servers = rpc(base, "ServerService/ListServers", {}, admin_token)
    names = {item.get("name") for item in servers.get("servers", [])} if status == 200 else set()
    checks["server_inventory"] = "OSS246 Owned" in names and "OSS246 Other" in names

    status, backups = rpc(base, "BackupService/ListBackups",
                          {"server_id": OWNED_SERVER}, admin_token)
    backup_names = {item.get("name") for item in backups.get("backups", [])} if status == 200 else set()
    checks["backup_list"] = BACKUP_NAME in backup_names

    status, config = rpc(base, "ConfigService/GetServerConfig",
                         {"server_id": OWNED_SERVER}, admin_token)
    level_ok = False
    if status == 200:
        for category in config.get("categories", []):
            for prop in category.get("properties", []):
                if prop.get("key") == "level" and prop.get("value") == WORLD_NAME:
                    level_ok = True
    checks["world_config"] = level_ok

    status, scoped_token, _ = login(base, SCOPED_USERNAME, SCOPED_PASSWORD)
    if status != 200 or not scoped_token:
        return checks
    # ListServerPlayers is a server-scoped read whose RBAC object field is
    # "server_id", so the legacy subuser-grant fallback applies to it (GetServer
    # uses "id" and legitimately relies on role grants only).
    status, own = rpc(base, "ServerService/ListServerPlayers",
                      {"server_id": OWNED_SERVER}, scoped_token)
    checks["scoped_read_own"] = status == 200
    status, other = rpc(base, "ServerService/ListServerPlayers",
                        {"server_id": OTHER_SERVER}, scoped_token)
    checks["scoped_read_other_denied"] = status in (403, 401)
    status, scoped_backups = rpc(base, "BackupService/ListBackups",
                                 {"server_id": OWNED_SERVER}, scoped_token)
    checks["scoped_backup_own"] = status == 200
    return checks


def run_checks(report, name, condition):
    report["checks"][name] = bool(condition)
    if not condition:
        report["failed_checks"].append(name)


def copy_pair(source, destination):
    source, destination = Path(source), Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source / "panel.db", destination / "panel.db")
    for sidecar in ("-wal", "-shm"):
        path = Path(str(source / "panel.db") + sidecar)
        if path.is_file():
            shutil.copyfile(path, str(destination / "panel.db") + sidecar)
    for child in ("worlds", "backups"):
        if (source / child).is_dir():
            shutil.copytree(source / child, destination / child, dirs_exist_ok=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    parser.add_argument("--stable-binary", required=True)
    parser.add_argument("--candidate-binary", required=True)
    parser.add_argument("--stable-version", default="1.0.40")
    parser.add_argument("--candidate-version", required=True)
    parser.add_argument("--candidate-commit", required=True)
    parser.add_argument("--report", default=None)
    args = parser.parse_args()

    workspace = Path(args.workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    report = {
        "started_at": now(),
        "stable": {"version": args.stable_version, "binary": str(args.stable_binary),
                   "sha256": sha256_file(args.stable_binary)},
        "candidate": {"version": args.candidate_version, "commit": args.candidate_commit,
                      "binary": str(args.candidate_binary), "sha256": sha256_file(args.candidate_binary)},
        "checks": {}, "failed_checks": [], "phases": {},
    }

    # --- Phase 1: real stable binary on a seeded representative legacy DB ----
    stable_ws = workspace / "stable-legacy"
    stable_port = free_port()
    ci.write_config(stable_ws, stable_ws / "panel.db", stable_port)
    seed_world_files(stable_ws)
    seeded = seeded_hashes(stable_ws)
    # First real run creates the legacy schema and then stops; seed the
    # representative persisted rows while the binary is stopped.
    ci.run_service(args.stable_binary, stable_ws, ci.LEGACY_TABLE_COUNT)
    insert_legacy_rows(stable_ws / "panel.db")
    process = start_service(args.stable_binary, stable_ws, stable_port)
    try:
        stable_paths = user_paths("http://127.0.0.1:%d" % stable_port)
        for key, value in stable_paths.items():
            report["checks"]["stable_seeded_" + key] = bool(value)
            if not value:
                report["failed_checks"].append("stable_seeded_" + key)
    finally:
        stop_service(process)
    stable_tables = ci.tables(stable_ws / "panel.db")
    run_checks(report, "stable_legacy_table_shape", len(stable_tables) == ci.LEGACY_TABLE_COUNT)
    run_checks(report, "stable_worlds_unchanged", seeded_hashes(stable_ws) == seeded)
    report["phases"]["stable_tables"] = len(stable_tables)

    # --- Snapshot: real SQLite online backup + world/backup pair --------------
    snapshot = workspace / "legacy-snapshot"
    snapshot.mkdir(parents=True, exist_ok=True)
    ci.back_up_database(stable_ws / "panel.db", snapshot / "panel.db")
    for child in ("worlds", "backups"):
        shutil.copytree(stable_ws / child, snapshot / child, dirs_exist_ok=True)
    run_checks(report, "snapshot_integrity_ok", ci.integrity(snapshot / "panel.db") == "ok")

    # --- Phase 2: real candidate binary on a disposable copy ------------------
    candidate_ws = workspace / "candidate-upgraded"
    candidate_port = free_port()
    ci.write_config(candidate_ws, candidate_ws / "panel.db", candidate_port)
    copy_pair(snapshot, candidate_ws)
    process = start_service(args.candidate_binary, candidate_ws, candidate_port)
    try:
        candidate_paths = user_paths("http://127.0.0.1:%d" % candidate_port)
        for key, value in candidate_paths.items():
            report["checks"]["candidate_" + key] = bool(value)
            if not value:
                report["failed_checks"].append("candidate_" + key)
    finally:
        stop_service(process)
    candidate_tables = ci.tables(candidate_ws / "panel.db")
    run_checks(report, "upgrade_additive_superset", stable_tables.issubset(candidate_tables))
    run_checks(report, "upgrade_worlds_unchanged", seeded_hashes(candidate_ws) == seeded)
    run_checks(report, "upgrade_integrity_ok", ci.integrity(candidate_ws / "panel.db") == "ok")
    report["phases"]["candidate_tables"] = len(candidate_tables)

    # --- Phase 3: copy-back rollback of the OLD pair under the OLD binary -----
    rollback_ws = workspace / "rollback-old-pair"
    rollback_port = free_port()
    ci.write_config(rollback_ws, rollback_ws / "panel.db", rollback_port)
    copy_pair(snapshot, rollback_ws)
    process = start_service(args.stable_binary, rollback_ws, rollback_port)
    try:
        rollback_paths = user_paths("http://127.0.0.1:%d" % rollback_port)
        for key, value in rollback_paths.items():
            report["checks"]["rollback_" + key] = bool(value)
            if not value:
                report["failed_checks"].append("rollback_" + key)
    finally:
        stop_service(process)
    rollback_tables = ci.tables(rollback_ws / "panel.db")
    run_checks(report, "rollback_old_schema", rollback_tables == stable_tables)
    run_checks(report, "rollback_worlds_unchanged", seeded_hashes(rollback_ws) == seeded)
    run_checks(report, "rollback_integrity_ok", ci.integrity(rollback_ws / "panel.db") == "ok")

    report["pass"] = not report["failed_checks"]
    report["finished_at"] = now()
    destination = Path(args.report) if args.report else workspace / "oss246-userpath-report.json"
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"pass": report["pass"], "failed_checks": report["failed_checks"],
                      "report": str(destination)}, indent=2))
    if not report["pass"]:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
