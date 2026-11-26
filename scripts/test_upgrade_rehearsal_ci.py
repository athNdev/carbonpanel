# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed tests for the CI upgrade rehearsal helpers."""
import json
import shutil
import sqlite3
import tempfile
import unittest
from contextlib import closing
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

from scripts import upgrade_rehearsal_ci as rehearsal


def fixture_release(root, name, archive_bytes, checksum=None, version=None, commit=None):
    """Write a local archive + verification records for a synthetic stable."""
    archive = root / name
    archive.write_bytes(archive_bytes)
    digest = rehearsal.sha256_file(archive)
    checksums = root / "checksums.txt"
    checksums.write_text((checksum or digest) + "  " + name + "\n", encoding="utf-8")
    manifest = root / "release-manifest.json"
    manifest.write_text(json.dumps({
        "version": version or "v1.0.40",
        "commit": commit or "f88748be" + "0" * 32,
        "artifacts": [{"name": name, "sha256": digest}],
    }), encoding="utf-8")
    return archive, checksums, manifest, digest


def legacy_stable_tables():
    """The synthetic v1.0.40 shape: 35 tables including the core surface."""
    base = set(rehearsal.CORE_TABLES) | {rehearsal.ARCHIVE_RECEIPT_TABLE}
    filler = {"historical_%02d" % index
              for index in range(rehearsal.LEGACY_TABLE_COUNT - len(base))}
    return base | filler


def write_schema(path, names, receipt_columns=()):
    """Create a disposable SQLite schema without any real panel data."""
    with closing(sqlite3.connect(str(path))) as connection, connection:
        for name in sorted(names):
            connection.execute(
                'CREATE TABLE "%s" (id INTEGER PRIMARY KEY, note TEXT)' % name)
        if "servers" in names:
            connection.execute("INSERT INTO servers (note) VALUES ('seeded')")
        if rehearsal.ARCHIVE_RECEIPT_TABLE in names:
            for column in sorted(receipt_columns):
                connection.execute(
                    'ALTER TABLE "%s" ADD COLUMN "%s" TEXT' %
                    (rehearsal.ARCHIVE_RECEIPT_TABLE, column))


class LegacyCase:
    """Fake real binaries that produce stable/candidate disposable schemas."""

    def __init__(self, candidate_tables=None, receipt_columns=None, drop_tables=None):
        self.stable_tables = legacy_stable_tables()
        if candidate_tables is None:
            candidate_tables = (self.stable_tables |
                                rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES |
                                rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES)
        self.candidate_tables = set(candidate_tables)
        self.receipt_columns = (set(rehearsal.ARCHIVE_RECEIPT_COLUMNS)
                                if receipt_columns is None else set(receipt_columns))
        self.drop_tables = set(drop_tables or ())

    def run(self, root):
        stable_tables = self.stable_tables
        candidate_tables = self.candidate_tables
        receipt_columns = self.receipt_columns
        drop_tables = self.drop_tables

        def run_service(binary, workdir, expected_tables, timeout=None):
            db = Path(workdir) / "panel.db"
            write_schema(db, stable_tables)
            return db

        def run_service_any(binary, workdir):
            db = Path(workdir) / "panel.db"
            rehearsal.back_up_database(db, Path(workdir) / "panel.db.pre-migrate.bak")
            with closing(sqlite3.connect(str(db))) as connection, connection:
                for name in sorted(candidate_tables - stable_tables):
                    connection.execute(
                        'CREATE TABLE "%s" (id INTEGER PRIMARY KEY, note TEXT)' % name)
                for column in sorted(receipt_columns):
                    connection.execute(
                        'ALTER TABLE "%s" ADD COLUMN "%s" TEXT' %
                        (rehearsal.ARCHIVE_RECEIPT_TABLE, column))
                for name in sorted(drop_tables):
                    connection.execute('DROP TABLE "%s"' % name)
            return db

        def run_restore(binary, workdir, snapshot):
            if not Path(snapshot).is_file():
                return SimpleNamespace(returncode=1)
            shutil.copyfile(snapshot, Path(workdir) / "panel.db")
            return SimpleNamespace(returncode=0)

        with patch.object(rehearsal, "run_service", side_effect=run_service), \
             patch.object(rehearsal, "run_service_any", side_effect=run_service_any), \
             patch.object(rehearsal, "run_restore", side_effect=run_restore):
            return rehearsal.run_case(
                Path(root), "legacy", "stable", "candidate",
                rehearsal.LEGACY_TABLE_COUNT, None, 18090, 18091, 18092)


class LegacyCaseRehearsalTests(unittest.TestCase):
    def test_legacy_case_derives_candidate_baseline_and_requires_archive_import(self):
        with tempfile.TemporaryDirectory() as directory:
            checks, evidence = LegacyCase().run(directory)
        expected_candidate = (rehearsal.LEGACY_TABLE_COUNT +
                              len(rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES) +
                              len(rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES))
        self.assertEqual(evidence["stable_tables"], rehearsal.LEGACY_TABLE_COUNT)
        self.assertEqual(evidence["expected_stable_tables"], rehearsal.LEGACY_TABLE_COUNT)
        # The candidate count follows the built binary (46 with archive_imports),
        # not the historical 45-table model.
        self.assertEqual(evidence["candidate_tables"], expected_candidate)
        self.assertEqual(evidence["expected_candidate_tables"], expected_candidate)
        self.assertNotEqual(evidence["expected_candidate_tables"],
                            rehearsal.MODEL_CANDIDATE_TABLE_COUNT)
        self.assertEqual(evidence["archive_import_tables"], ["archive_imports"])
        self.assertEqual(set(evidence["archive_receipt_columns"]),
                         rehearsal.ARCHIVE_RECEIPT_COLUMNS)
        self.assertTrue(checks["legacy_upgrade_adds_archive_import_tables"])
        self.assertTrue(checks["legacy_upgrade_has_archive_receipt_schema"])
        self.assertEqual({name for name, ok in checks.items() if not ok}, set())

    def test_legacy_case_fails_closed_when_a_required_model_is_missing(self):
        candidate = (legacy_stable_tables() |
                     rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES |
                     rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES)
        candidate = (candidate - {"world_revisions"}) | {"unexpected_extra_model"}
        with tempfile.TemporaryDirectory() as directory:
            checks, evidence = LegacyCase(candidate_tables=candidate).run(directory)
        # Same table count as a complete candidate, so only the required model fails.
        self.assertEqual(evidence["candidate_tables"],
                         len(legacy_stable_tables()) +
                         len(rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES) +
                         len(rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES))
        self.assertEqual({name for name, ok in checks.items() if not ok},
                         {"legacy_upgrade_adds_expected_models"})

    def test_legacy_case_fails_closed_when_candidate_schema_does_not_grow(self):
        with tempfile.TemporaryDirectory() as directory:
            checks, evidence = LegacyCase(
                candidate_tables=legacy_stable_tables(), receipt_columns=set(),
                drop_tables={"historical_00", "historical_01", "historical_02"}).run(directory)
        self.assertEqual(evidence["candidate_tables"],
                         rehearsal.LEGACY_TABLE_COUNT - 3)
        failures = {name for name, ok in checks.items() if not ok}
        self.assertIn("legacy_upgrade_schema_grows_or_matches", failures)
        self.assertIn("legacy_upgrade_is_additive_superset", failures)
        self.assertEqual(failures, {
            "legacy_upgrade_is_additive_superset",
            "legacy_upgrade_adds_expected_models",
            "legacy_upgrade_adds_archive_import_tables",
            "legacy_upgrade_has_archive_receipt_schema",
            "legacy_upgrade_schema_grows_or_matches",
        })

    def test_legacy_case_requires_archive_table_and_receipt_columns(self):
        complete = (legacy_stable_tables() |
                    rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES |
                    rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES)
        cases = (
            ("archive table", complete - {"archive_imports"} | {"unexpected_extra_model"},
             rehearsal.ARCHIVE_RECEIPT_COLUMNS,
             {"legacy_upgrade_adds_archive_import_tables"}),
            ("receipt column", complete,
             rehearsal.ARCHIVE_RECEIPT_COLUMNS - {"archive_completion_upload_id"},
             {"legacy_upgrade_has_archive_receipt_schema"}),
        )
        for label, candidate, receipt_columns, expected in cases:
            with self.subTest(label):
                with tempfile.TemporaryDirectory() as directory:
                    checks, _ = LegacyCase(candidate_tables=candidate,
                                           receipt_columns=receipt_columns).run(directory)
                self.assertEqual({name for name, ok in checks.items() if not ok},
                                 expected)


class RehearsalHelperTests(unittest.TestCase):
    def test_sha256_file_is_stable_and_content_sensitive(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "artifact.bin"
            path.write_bytes(b"disposable-artifact")
            first = rehearsal.sha256_file(path)
            self.assertEqual(first, rehearsal.sha256_file(path))
            path.write_bytes(b"disposable-artifact-tampered")
            self.assertNotEqual(first, rehearsal.sha256_file(path))

    def test_stable_archive_name_matches_release_convention(self):
        self.assertEqual(rehearsal.stable_archive_name("v1.0.40", "linux", "amd64"),
                         "carbon-panel_v1.0.40_linux_amd64.tar.gz")
        self.assertEqual(rehearsal.stable_archive_name("v1.0.40", "windows", "amd64"),
                         "carbon-panel_v1.0.40_windows_amd64.zip")
        self.assertEqual(rehearsal.stable_archive_name("v2.0.0", "linux", "arm64"),
                         "carbon-panel_v2.0.0_linux_arm64.tar.gz")

    def test_legacy_baseline_comes_from_release_contract(self):
        baseline = rehearsal.legacy_baseline()
        self.assertEqual(baseline["tag"], "v1.0.40")
        self.assertEqual(baseline["commit"], "f88748be6a216b187e675bbb1f110df07e544148")
        self.assertIn("legacy", baseline["source"])

    def test_acquire_stable_fails_closed_on_checksum_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            archive, checksums, manifest, _ = fixture_release(
                root, name, b"not-the-real-archive", checksum="0" * 64)
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "legacy", "v1.0.40", "f88748be" + "0" * 32,
                    "linux", "amd64", "v1.0.40",
                    local_archive=archive, local_checksums=checksums,
                    local_manifest=manifest)

    def test_acquire_stable_fails_closed_on_manifest_sha_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            archive, checksums, manifest, digest = fixture_release(root, name, b"fixture")
            # Valid checksums.txt, but the manifest claims a different SHA.
            manifest.write_text(json.dumps({
                "version": "v1.0.40",
                "commit": "f88748be" + "0" * 32,
                "artifacts": [{"name": name, "sha256": "1" * 64}],
            }), encoding="utf-8")
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "legacy", "v1.0.40", "f88748be" + "0" * 32,
                    "linux", "amd64", "v1.0.40",
                    local_archive=archive, local_checksums=checksums,
                    local_manifest=manifest)

    def test_acquire_stable_fails_closed_on_missing_artifact_entry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            archive, checksums, manifest, _ = fixture_release(root, name, b"fixture")
            manifest.write_text(json.dumps({
                "version": "v1.0.40", "commit": "f88748be" + "0" * 32,
                "artifacts": [],
            }), encoding="utf-8")
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "legacy", "v1.0.40", None,
                    "linux", "amd64", "v1.0.40",
                    local_archive=archive, local_checksums=checksums,
                    local_manifest=manifest)

    def test_acquire_stable_fails_closed_on_missing_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "legacy", "v1.0.40", None,
                    "linux", "amd64", "v1.0.40",
                    local_archive=root / name,
                    local_checksums=root / "checksums.txt",
                    local_manifest=root / "release-manifest.json")

    def test_online_backup_preserves_integrity_and_rows(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, destination = root / "live.db", root / "backup.db"
            connection = sqlite3.connect(str(source))
            connection.execute("CREATE TABLE servers (id INTEGER PRIMARY KEY, note TEXT)")
            connection.execute("INSERT INTO servers (note) VALUES ('seeded')")
            connection.commit()
            rehearsal.back_up_database(source, destination)
            connection.close()
            self.assertEqual(rehearsal.integrity(destination), "ok")
            self.assertEqual(rehearsal.row_counts(destination, ["servers"])["servers"], 1)

    def test_current_rollback_uses_observed_table_delta(self):
        # Current Stable already includes the historical migration and archive surfaces.
        retained = "world_entries"
        candidate_only = "future_model"
        stable_tables = (rehearsal.EXPECTED_CANDIDATE_MODEL_TABLES |
                         rehearsal.EXPECTED_CANDIDATE_ARCHIVE_TABLES |
                         {rehearsal.ARCHIVE_RECEIPT_TABLE, "servers"})
        copyfile = shutil.copyfile

        cases = ((False, None), (True, None), (True, "app"), (True, "copyback"))
        for grow, corrupt in cases:
            with self.subTest(grow=grow, corrupt=corrupt):
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)

                    def run_service(binary, workdir):
                        db = workdir / "panel.db"
                        if binary == "stable":
                            with closing(sqlite3.connect(db)) as conn, conn:
                                for name in stable_tables:
                                    conn.execute(f"CREATE TABLE {name} (id INTEGER)")
                                conn.execute("INSERT INTO servers VALUES (1)")
                                for column in rehearsal.ARCHIVE_RECEIPT_COLUMNS:
                                    conn.execute(
                                        f"ALTER TABLE {rehearsal.ARCHIVE_RECEIPT_TABLE} "
                                        f"ADD COLUMN {column} TEXT")
                        else:
                            rehearsal.back_up_database(db, workdir / "panel.db.pre-migrate.bak")
                            if grow:
                                with closing(sqlite3.connect(db)) as conn, conn:
                                    conn.execute(f"CREATE TABLE {candidate_only} (id INTEGER)")
                        return db

                    def run_restore(binary, workdir, snapshot):
                        if not snapshot.is_file():
                            return SimpleNamespace(returncode=1)
                        db = workdir / "panel.db"
                        copyfile(snapshot, db)
                        if corrupt == "app":
                            with closing(sqlite3.connect(db)) as conn, conn:
                                conn.execute(f"ALTER TABLE {retained} RENAME TO {candidate_only}")
                        return SimpleNamespace(returncode=0)

                    def copy_snapshot(source, destination, *args, **kwargs):
                        result = copyfile(source, destination, *args, **kwargs)
                        if corrupt == "copyback" and Path(destination).parent.name == "current-copyback-run":
                            with closing(sqlite3.connect(destination)) as conn, conn:
                                # Keep the count unchanged while leaking a candidate table.
                                conn.execute(f"ALTER TABLE {retained} RENAME TO {candidate_only}")
                        return result

                    with patch.object(rehearsal, "run_service_any", side_effect=run_service), \
                         patch.object(rehearsal, "run_restore", side_effect=run_restore), \
                         patch.object(rehearsal.shutil, "copyfile", side_effect=copy_snapshot):
                        checks, evidence = rehearsal.run_case(
                            root, "current", "stable", "candidate", None, None,
                            18090, 18091, 18092)

                    self.assertEqual(evidence["candidate_tables"], len(stable_tables) + grow)
                    self.assertEqual(evidence["candidate_only_tables"], [candidate_only] if grow else [])
                    failures = {
                        "app": {"current_app_level_restore_returns_stable_tables"},
                        "copyback": {
                            "current_copyback_returns_stable_tables",
                            "current_copyback_removes_new_models",
                        },
                    }
                    self.assertEqual({name for name, ok in checks.items() if not ok},
                                     failures.get(corrupt, set()))

    def test_seeded_file_hashes_are_content_sensitive_and_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rehearsal.seed_worlds(root)
            before = rehearsal.seeded_file_hashes(root)
            self.assertEqual(len(before), len(rehearsal.SEED_FILES))
            (root / "worlds" / "survival" / "level.dat").write_bytes(b"tampered")
            self.assertNotEqual(before, rehearsal.seeded_file_hashes(root))
            (root / "worlds" / "survival" / "level.dat").unlink()
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.seeded_file_hashes(root)

    def test_config_paths_stay_inside_workspace_and_escape_safely(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = rehearsal.write_config(root / "run", root / "run" / "panel.db", 18090)
            text = (config / "config.yaml").read_text(encoding="utf-8")
            self.assertIn(str(root.resolve()).replace("\\", "/"), text)
            self.assertIn('panel_backup: "off"', text)
            self.assertTrue((config / "worlds" / "survival").is_dir())
            quoted = rehearsal.yaml_string('C:\\a "quoted" path')
            self.assertTrue(quoted.startswith('"') and quoted.endswith('"'))
            self.assertIn('\\\\', quoted)


if __name__ == "__main__":
    unittest.main()
