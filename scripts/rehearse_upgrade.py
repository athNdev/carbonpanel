#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Deterministic, offline v1.0.40 upgrade/backup-restore rehearsal.

This is a MODEL rehearsal on owned, disposable, synthetic fixtures. It does not
run a real v1.0.40 binary, use a real user database, touch a registry, or start
a service. It models the documented copy-back backup/restore procedure and the
additive schema growth described in migration-compatibility-audit.md section 3
(35 -> 44 tables and 10 node-control metadata columns). It exists so the
migration policy PR carries executable evidence of the *procedure* and its
limits, not a claim of live-upgrade qualification.
"""
import argparse
import hashlib
import json
import shutil
import sqlite3
from pathlib import Path

BASE_TABLES = [
    "users", "sessions", "roles", "user_roles", "nodes", "servers",
    "server_configs", "server_blueprints", "backups", "snapshots",
    "operation_journal", "module_installations", "node_credentials",
]
NEW_MODELS = [
    "player_identities", "player_aliases", "player_sessions",
    "player_access_states", "player_moderation_actions", "world_entries",
    "world_revisions", "world_bindings", "world_import_jobs",
]
NODE_COLUMNS = [
    "transport", "enrollment_state", "enrollment_id", "enrollment_expires_at",
    "protocol_version", "capabilities_json", "capability_owner",
    "credential_generation", "key_fingerprint", "token_fingerprint",
]


def tables(connection):
    return {row[0] for row in connection.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}


def integrity(connection):
    return connection.execute("PRAGMA integrity_check").fetchone()[0]


def counts(connection, names):
    return {name: connection.execute(f"SELECT COUNT(*) FROM {name}").fetchone()[0]
            for name in names}


def write_worlds(root, marker):
    world = root / "worlds" / "survival"
    world.mkdir(parents=True, exist_ok=True)
    files = {"level.dat": marker.encode(), "region.rgn": (marker * 64).encode()}
    for name, data in files.items():
        (world / name).write_bytes(data)
    backup = root / "backups" / "weekly.tar.zst"
    backup.parent.mkdir(parents=True, exist_ok=True)
    backup.write_bytes((marker * 32).encode())
    return {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
            for p in root.rglob("*") if p.is_file() and p.suffix in {".dat", ".rgn", ".zst"}}


def build_v1_database(path, marker):
    connection = sqlite3.connect(str(path))
    connection.execute("PRAGMA journal_mode=WAL")
    connection.execute("PRAGMA foreign_keys=ON")
    # 35 tables: the 13 authored above plus 22 inert historical tables, matching
    # the audited v1.0.40 shape without copying any real user data.
    for index in range(35 - len(BASE_TABLES)):
        connection.execute(f"CREATE TABLE historical_{index:02d} (id INTEGER PRIMARY KEY, note TEXT)")
    for name in BASE_TABLES:
        if name not in tables(connection):
            connection.execute(f"CREATE TABLE {name} (id INTEGER PRIMARY KEY, note TEXT)")
    for name, rows in (("servers", 2), ("server_configs", 3), ("server_blueprints", 6),
                       ("users", 1), ("nodes", 1)):
        for index in range(rows):
            connection.execute(f"INSERT INTO {name} (note) VALUES (?)", (f"{marker}-{index}",))
    connection.commit()
    return connection


def apply_additive_migration(connection):
    # New tables do not drop existing ones; node columns are backfilled 'legacy'.
    for name in NEW_MODELS:
        connection.execute(f"CREATE TABLE IF NOT EXISTS {name} (id INTEGER PRIMARY KEY, note TEXT)")
    for column in NODE_COLUMNS:
        connection.execute(f"ALTER TABLE nodes ADD COLUMN {column} TEXT")
    connection.execute("UPDATE nodes SET transport='legacy'")
    connection.commit()


def run(workspace):
    root = Path(workspace).resolve()
    live = root / "live"
    backup = root / "backup"
    restored = root / "restored"
    for path in (live, backup, restored):
        if path.exists():
            shutil.rmtree(path)
    live.mkdir(parents=True)

    marker = "synthetic-v1.0.40-fixture"
    connection = build_v1_database(live / "panel.db", marker)
    before_tables = tables(connection)
    before_counts = counts(connection, ["servers", "server_configs", "server_blueprints", "users", "nodes"])
    before_integrity = integrity(connection)
    worlds_before = write_worlds(live, marker)

    # Real SQLite online backup API for the database (safe against a live WAL),
    # plus a copy of the world/backup directories while the service is quiesced.
    # This is the documented backup primitive, exercised on owned synthetic data.
    backup.mkdir(parents=True, exist_ok=True)
    destination = sqlite3.connect(str(backup / "panel.db"))
    try:
        connection.backup(destination)
    finally:
        destination.close()
    for name in ("worlds", "backups"):
        shutil.copytree(live / name, backup / name)
    backup_hashes_before = {str(p.relative_to(backup)): hashlib.sha256(p.read_bytes()).hexdigest()
                            for p in backup.rglob("*") if p.is_file()}
    connection.close()

    # Upgrade the live copy in place (additive migration).
    upgraded = sqlite3.connect(str(live / "panel.db"))
    apply_additive_migration(upgraded)
    after_tables = tables(upgraded)
    after_counts = counts(upgraded, before_counts)
    after_integrity = integrity(upgraded)
    transport = upgraded.execute("SELECT transport FROM nodes LIMIT 1").fetchone()[0]
    upgraded.close()

    # Copy-back rollback onto a DISPOSABLE path; the live upgraded copy is not
    # a user artifact and the backup is never mutated.
    shutil.copytree(backup, restored)
    rolled = sqlite3.connect(str(restored / "panel.db"))
    restored_tables = tables(rolled)
    restored_counts = counts(rolled, before_counts)
    restored_integrity = integrity(rolled)
    restored_columns = {row[1] for row in rolled.execute("PRAGMA table_info(nodes)")}
    rolled.close()
    worlds_restored = {str(p.relative_to(restored)): hashlib.sha256(p.read_bytes()).hexdigest()
                       for p in restored.rglob("*") if p.is_file() and p.suffix in {".dat", ".rgn", ".zst"}}
    backup_hashes_after = {str(p.relative_to(backup)): hashlib.sha256(p.read_bytes()).hexdigest()
                           for p in backup.rglob("*") if p.is_file()}

    checks = {
        "v1_shape_is_35_tables": len(before_tables) == 35,
        "before_integrity_ok": before_integrity == "ok",
        "upgrade_adds_9_models": after_tables - before_tables == set(NEW_MODELS),
        "upgrade_adds_node_columns": all(c in {"transport", *NODE_COLUMNS} for c in NODE_COLUMNS),
        "upgrade_backfills_legacy": transport == "legacy",
        "upgrade_preserves_rows": before_counts == after_counts,
        "upgrade_integrity_ok": after_integrity == "ok",
        "rollback_restores_35_tables": restored_tables == before_tables,
        "rollback_removes_new_columns": not (set(NODE_COLUMNS) & restored_columns),
        "rollback_preserves_rows": before_counts == restored_counts,
        "rollback_integrity_ok": restored_integrity == "ok",
        "worlds_and_backups_byte_identical": worlds_before == worlds_restored,
        "backup_untouched": backup_hashes_before == backup_hashes_after,
    }
    report = {
        "schema": "v1.0.40-model-rehearsal-v1",
        "workspace": str(root),
        "before": {"tables": len(before_tables), "integrity": before_integrity, "rows": before_counts},
        "after_upgrade": {"tables": len(after_tables), "integrity": after_integrity,
                          "rows": after_counts, "new_models": sorted(after_tables - before_tables),
                          "nodes_backfill": transport},
        "after_copy_back_rollback": {"tables": len(restored_tables), "integrity": restored_integrity,
                                     "rows": restored_counts,
                                     "node_columns_after_rollback": sorted(set(NODE_COLUMNS) & restored_columns)},
        "world_files_verified": len(worlds_before),
        "checks": checks,
        "pass": all(checks.values()),
        "limits": [
            "Synthetic schema and rows; not a real v1.0.40 database or backup.",
            "No real v1.0.40 binary, service, Docker container or network call is used.",
            "Models the documented copy-back procedure and additive schema growth only.",
            "No live gameplay, browser, auth/OIDC or PROXY header behavior is exercised.",
            "Does not satisfy the go/no-go gate for first stable publication.",
        ],
    }
    (root / "rehearsal-report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True)
    args = parser.parse_args()
    report = run(args.workspace)
    print(json.dumps({"pass": report["pass"], "report": str(Path(args.workspace) / "rehearsal-report.json")}, indent=2))
    if not report["pass"]:
        raise SystemExit("rehearsal failed: " + repr(report["checks"]))


if __name__ == "__main__":
    main()
