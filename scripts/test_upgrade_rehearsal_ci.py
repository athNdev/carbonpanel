# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed tests for the CI upgrade rehearsal helpers."""
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path

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
