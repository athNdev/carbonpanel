#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed checks for the OSS-246 user-path rehearsal helpers."""
import json
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from scripts import oss246_userpath_rehearsal as r


class SeededFixtureChecks(unittest.TestCase):
    def test_seeded_hashes_fail_closed_on_missing_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(r.RehearsalError):
                r.seeded_hashes(Path(tmp))

    def test_seeded_hashes_are_stable(self):
        with tempfile.TemporaryDirectory() as tmp:
            r.seed_world_files(tmp)
            first = r.seeded_hashes(tmp)
            second = r.seeded_hashes(tmp)
            self.assertEqual(first, second)
            self.assertEqual(len(first), 3)


class LegacySeedChecks(unittest.TestCase):
    def test_missing_table_is_refused(self):
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "panel.db"
            sqlite3.connect(str(db)).close()
            with self.assertRaises(r.RehearsalError):
                r.insert_legacy_rows(db)

    def test_rows_are_seeded_into_real_shape(self):
        with tempfile.TemporaryDirectory() as tmp:
            db = Path(tmp) / "panel.db"
            connection = sqlite3.connect(str(db))
            connection.executescript("""
                CREATE TABLE users (id text, username text NOT NULL, password_hash text,
                    auth_provider text NOT NULL DEFAULT 'local', is_active numeric,
                    created_at datetime, updated_at datetime);
                CREATE TABLE user_roles (id text, user_id text NOT NULL, role_name text NOT NULL,
                    source text NOT NULL DEFAULT 'local', created_at datetime);
                CREATE TABLE servers (id text, name text NOT NULL, mod_loader text NOT NULL,
                    mc_version text NOT NULL, status text NOT NULL, data_path text NOT NULL,
                    node_id text, port integer, created_at datetime, updated_at datetime);
                CREATE TABLE server_configs (id text, server_id text NOT NULL, level text,
                    updated_at datetime);
                CREATE TABLE server_subusers (server_id text, user_id text, permissions text,
                    created_at datetime);
                CREATE TABLE backup_records (id text, server_id text NOT NULL, name text NOT NULL,
                    path text NOT NULL, sha256 text, size_bytes integer, status text,
                    created_at datetime);
            """)
            connection.close()
            r.insert_legacy_rows(db)
            connection = sqlite3.connect(str(db))
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM users").fetchone()[0], 2)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM user_roles").fetchone()[0], 1)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM servers").fetchone()[0], 2)
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM server_configs").fetchone()[0], 2)
            perms = connection.execute(
                "SELECT permissions FROM server_subusers WHERE server_id=? AND user_id=?",
                (r.OWNED_SERVER, "usr-oss246-scoped")).fetchone()[0]
            self.assertEqual(json.loads(perms), ["servers.read", "backups.read"])
            connection.close()


class UserPathChecks(unittest.TestCase):
    def test_denied_login_returns_all_false(self):
        with mock.patch.object(r, "rpc", return_value=(403, {"code": "unauthenticated"})):
            result = r.user_paths("http://127.0.0.1:1")
        self.assertTrue(result)
        self.assertFalse(any(result.values()))

    def test_scoped_boundary_and_world_config(self):
        def fake_rpc(base, procedure, payload, token=None):
            if procedure == "AuthService/Login":
                return 200, {"token": "t-" + payload["username"]}
            if procedure == "AuthService/GetCurrentUser":
                return 200, {"user": {"username": r.LEGACY_USERNAME}}
            if procedure == "ServerService/ListServers":
                return 200, {"servers": [{"name": "OSS246 Owned"}, {"name": "OSS246 Other"}]}
            if procedure == "BackupService/ListBackups":
                return 200, {"backups": [{"name": r.BACKUP_NAME}]}
            if procedure == "ConfigService/GetServerConfig":
                return 200, {"categories": [{"properties": [
                    {"key": "level", "value": r.WORLD_NAME}]}]}
            if procedure == "ServerService/ListServerPlayers":
                return (200, {}) if payload["server_id"] == r.OWNED_SERVER else (403, {})
            return 200, {}
        with mock.patch.object(r, "rpc", side_effect=fake_rpc):
            result = r.user_paths("http://127.0.0.1:1")
        self.assertTrue(all(result.values()), result)


class CopyPairCheck(unittest.TestCase):
    def test_copy_pair_preserves_db_and_worlds(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "source"
            destination = Path(tmp) / "destination"
            r.seed_world_files(source)
            (source / "panel.db").write_bytes(b"disposable-db")
            r.copy_pair(source, destination)
            self.assertEqual((destination / "panel.db").read_bytes(), b"disposable-db")
            self.assertEqual(r.seeded_hashes(destination), r.seeded_hashes(source))


if __name__ == "__main__":
    unittest.main()
