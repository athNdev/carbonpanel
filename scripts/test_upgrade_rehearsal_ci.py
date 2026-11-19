# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed tests for the CI upgrade rehearsal helpers."""
import sqlite3
import tempfile
import unittest
from pathlib import Path

from scripts import upgrade_rehearsal_ci as rehearsal


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

    def test_acquire_stable_fails_closed_on_checksum_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            archive = root / name
            archive.write_bytes(b"not-the-real-archive")
            checksums = root / "checksums.txt"
            checksums.write_text("0" * 64 + "  " + name + "\n", encoding="utf-8")
            manifest = root / "release-manifest.json"
            manifest.write_text('{"version":"v1.0.40","commit":"f88748be",'
                                '"artifacts":[{"name":"%s","sha256":"%s"}]}' %
                                (name, "0" * 64), encoding="utf-8")
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "v1.0.40", "f88748be", "linux", "amd64",
                    archive, checksums, manifest)

    def test_acquire_stable_fails_closed_on_manifest_identity_mismatch(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            name = rehearsal.stable_archive_name("v1.0.40", "linux", "amd64")
            archive = root / name
            archive.write_bytes(b"fixture")
            digest = rehearsal.sha256_file(archive)
            checksums = root / "checksums.txt"
            checksums.write_text(digest + "  " + name + "\n", encoding="utf-8")
            manifest = root / "release-manifest.json"
            manifest.write_text('{"version":"v2.0.0","commit":"deadbeef",'
                                '"artifacts":[{"name":"%s","sha256":"%s"}]}' %
                                (name, digest), encoding="utf-8")
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.acquire_stable(
                    root / "workspace", "v1.0.40", "f88748be", "linux", "amd64",
                    archive, checksums, manifest)

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

    def test_world_hashes_detect_unexpected_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            world = root / "worlds" / "survival"
            world.mkdir(parents=True)
            (world / "level.dat").write_bytes(b"level")
            before = rehearsal.world_hashes(root)
            self.assertIn("worlds/survival/level.dat", before)
            (world / "level.dat").write_bytes(b"level-tampered")
            self.assertNotEqual(before, rehearsal.world_hashes(root))

    def test_config_paths_stay_inside_workspace(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config = rehearsal.write_config(root / "run", root / "run" / "panel.db", 18090)
            text = (config / "config.yaml").read_text(encoding="utf-8")
            self.assertIn(str(root.resolve()).replace("\\", "/"), text)
            self.assertIn('panel_backup: "off"', text)
            self.assertTrue((config / "worlds" / "survival").is_dir())


if __name__ == "__main__":
    unittest.main()
