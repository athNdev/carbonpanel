# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed tests for the server-archive acceptance harness.

These tests never start Docker, a controller or Minecraft. They build synthetic
ZIP and tar.gz archives with the real schema and exercise the same independent
verification, lifecycle gates and workspace/remote-node validators the hosted
rehearsal uses. Docker-dependent orchestration remains a CI-only check.
"""
import json
import base64
import tarfile
import tempfile
import threading
import unittest
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import BytesIO
from pathlib import Path

from scripts import server_archive_rehearsal_ci as rehearsal


def properties_bytes():
    return (
        "# operator notes are not archived\n"
        "difficulty=hard\n"
        "gamemode=creative\n"
        "level-name=world\n"
        "sync-chunk-writes=false\n"
        "pause-when-empty-seconds=0\n"
    ).encode("utf-8")


def fixture_files():
    return {
        "server.properties": properties_bytes(),
        rehearsal.FIXTURE_PATH: rehearsal.FIXTURE_BYTES,
        "world/level.dat": b"synthetic-disposable-level" * 8,
    }


def manifest_for(files, exporter="2.0.1-rehearsal", runtime=None):
    return {
        "schema_version": rehearsal.ARCHIVE_SCHEMA,
        "exporter_version": exporter,
        "runtime": runtime or {
            "minecraft_version": "1.21.1",
            "mod_loader": "vanilla",
            "memory_mb": 1024,
            "max_players": 5,
        },
        "files": [
            {"path": path, "size": len(body),
             "sha256": rehearsal.sha256_bytes(body)}
            for path, body in sorted(files.items())
        ],
    }


def write_archive(path, archive_format, files=None, manifest=None, extra=None):
    files = files if files is not None else fixture_files()
    manifest = manifest if manifest is not None else manifest_for(files)
    extra = extra or {}
    path = Path(path)
    if archive_format == "zip":
        with zipfile.ZipFile(path, "w", zipfile.ZIP_DEFLATED) as bundle:
            for name, body in files.items():
                bundle.writestr(rehearsal.DATA_PREFIX + name, body)
            for name, body in extra.items():
                bundle.writestr(name, body)
            bundle.writestr(rehearsal.MANIFEST_NAME,
                            json.dumps(manifest, sort_keys=True).encode("utf-8"))
    else:
        with tarfile.open(path, "w:gz") as bundle:
            for name, body in list(files.items()) + list(extra.items()):
                info = tarfile.TarInfo(rehearsal.DATA_PREFIX + name
                                       if name in files else name)
                info.size = len(body)
                info.mode = 0o644
                bundle.addfile(info, BytesIO(body))
            body = json.dumps(manifest, sort_keys=True).encode("utf-8")
            info = tarfile.TarInfo(rehearsal.MANIFEST_NAME)
            info.size = len(body)
            info.mode = 0o644
            bundle.addfile(info, BytesIO(body))
    return path


class ArchiveVerificationTests(unittest.TestCase):
    def test_zip_and_tar_round_trip_with_identical_manifest(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            reports = {}
            for archive_format, suffix in (("zip", ".zip"), ("tar.gz", ".tar.gz")):
                path = write_archive(root / ("source" + suffix), archive_format)
                reports[archive_format] = rehearsal.verify_server_archive(
                    path, archive_format, "2.0.1-rehearsal", "1.21.1", 1024, 5)
            self.assertEqual(reports["zip"]["files"], reports["tar.gz"]["files"])
            self.assertEqual(reports["zip"]["canonical_manifest_sha256"],
                             reports["tar.gz"]["canonical_manifest_sha256"])
            self.assertEqual(reports["zip"]["fixture_sha256"],
                             rehearsal.sha256_bytes(rehearsal.FIXTURE_BYTES))
            self.assertEqual(reports["tar.gz"]["expanded_bytes"],
                             sum(record["size"] for record in reports["zip"]["files"].values()))

    def test_file_digest_mismatch_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            files = fixture_files()
            manifest = manifest_for(files)
            manifest["files"][0]["sha256"] = "0" * 64
            path = write_archive(Path(directory) / "tampered.zip", "zip",
                                 files=files, manifest=manifest)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "SHA-256 mismatch"):
                rehearsal.verify_server_archive(
                    path, "zip", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_unmanifested_member_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = write_archive(Path(directory) / "extra.zip", "zip",
                                 extra={"data/plugins/backdoor.jar": b"unexpected"})
            with self.assertRaisesRegex(rehearsal.RehearsalError, "unmanifested"):
                rehearsal.verify_server_archive(
                    path, "zip", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_traversal_path_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            files = fixture_files()
            manifest = manifest_for(files)
            manifest["files"].append({
                "path": "../outside", "size": 0, "sha256": "0" * 64,
            })
            path = write_archive(Path(directory) / "traversal.zip", "zip",
                                 files=files, manifest=manifest)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "non-portable"):
                rehearsal.verify_server_archive(
                    path, "zip", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_exporter_and_runtime_provenance_are_required(self):
        with tempfile.TemporaryDirectory() as directory:
            path = write_archive(Path(directory) / "wrong.zip", "zip")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "exporter_version"):
                rehearsal.verify_server_archive(
                    path, "zip", "different-version", "1.21.1", 1024, 5)
            runtime = manifest_for(fixture_files())["runtime"]
            runtime["requires_custom_runtime"] = True
            path = write_archive(Path(directory) / "custom.tar.gz", "tar.gz",
                                 manifest=manifest_for(fixture_files(), runtime=runtime))
            with self.assertRaisesRegex(rehearsal.RehearsalError, "custom runtime"):
                rehearsal.verify_server_archive(
                    path, "tar.gz", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_credential_fixture_must_not_be_exported(self):
        with tempfile.TemporaryDirectory() as directory:
            files = fixture_files()
            files[rehearsal.EXCLUDED_PATH] = rehearsal.EXCLUDED_BYTES
            path = write_archive(Path(directory) / "leak.zip", "zip", files=files)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "credential"):
                rehearsal.verify_server_archive(
                    path, "zip", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_archive_rejects_credential_properties(self):
        with tempfile.TemporaryDirectory() as directory:
            files = fixture_files()
            files["server.properties"] += b"rcon.password=do-not-export\n"
            path = write_archive(Path(directory) / "properties-leak.zip", "zip", files=files)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "credential or endpoint"):
                rehearsal.verify_server_archive(
                    path, "zip", "2.0.1-rehearsal", "1.21.1", 1024, 5)

    def test_archive_rejects_directory_entries(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "directory-entry.zip"
            with zipfile.ZipFile(path, "w") as bundle:
                bundle.writestr("data/world/", b"")
                bundle.writestr("manifest.json", "{}")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "directory entry"):
                rehearsal.read_archive_entries(path, "zip")


class DestinationAndLifecycleTests(unittest.TestCase):
    def test_destination_requires_exact_verified_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            files = fixture_files()
            expected = {
                path: {"size": len(body), "sha256": rehearsal.sha256_bytes(body)}
                for path, body in files.items()
            }
            for path, body in files.items():
                target = root / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(body)
            observed = rehearsal.verify_destination(root, expected)
            self.assertEqual(observed[rehearsal.FIXTURE_PATH],
                             rehearsal.sha256_bytes(rehearsal.FIXTURE_BYTES))
            (root / "unexpected.txt").write_text("extra")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "unverified files"):
                rehearsal.verify_destination(root, expected)

    def test_destination_digest_mismatch_is_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / rehearsal.FIXTURE_PATH).parent.mkdir(parents=True)
            (root / rehearsal.FIXTURE_PATH).write_bytes(b"changed")
            expected = {rehearsal.FIXTURE_PATH: {
                "size": len(rehearsal.FIXTURE_BYTES),
                "sha256": rehearsal.sha256_bytes(rehearsal.FIXTURE_BYTES),
            }}
            with self.assertRaisesRegex(rehearsal.RehearsalError, "SHA-256 mismatch"):
                rehearsal.verify_destination(root, expected)

    def test_source_hashes_skip_archive_exclusions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "world").mkdir()
            (root / "world/level.dat").write_bytes(b"level")
            (root / Path(rehearsal.FIXTURE_PATH).parent).mkdir()
            (root / rehearsal.FIXTURE_PATH).write_bytes(rehearsal.FIXTURE_BYTES)
            (root / Path(rehearsal.EXCLUDED_PATH).parent / ".env").write_bytes(b"secret")
            (root / "logs").mkdir()
            (root / "logs/latest.log").write_bytes(b"log")
            (root / "session.lock").write_bytes(b"lock")
            hashes = rehearsal.source_file_hashes(root)
            self.assertEqual(set(hashes), {"world/level.dat",
                                           rehearsal.FIXTURE_PATH})
            self.assertEqual(hashes[rehearsal.FIXTURE_PATH]["sha256"],
                             rehearsal.sha256_bytes(rehearsal.FIXTURE_BYTES))

    def test_lifecycle_gates_fail_closed(self):
        stopped = {"status": "SERVER_STATUS_STOPPED", "containerId": "container"}
        rehearsal.assert_not_started(stopped, "import")
        rehearsal.assert_export_ready(stopped)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "not stopped"):
            rehearsal.assert_not_started(
                {"status": "SERVER_STATUS_RUNNING", "containerId": "container"}, "import")
        with self.assertRaisesRegex(rehearsal.RehearsalError, "stopped server"):
            rehearsal.assert_export_ready({"status": "SERVER_STATUS_RUNNING"})
        with self.assertRaisesRegex(rehearsal.RehearsalError, "Docker overrides"):
            rehearsal.assert_no_docker_overrides(
                {"dockerOverrides": {"environment": {"ONLINE_MODE": "FALSE"}}}, "source")

    def test_protojson_int64_strings_are_accepted(self):
        self.assertEqual(rehearsal.as_int("12345", "total"), 12345)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "missing"):
            rehearsal.as_int(None, "total")

    def test_config_property_lookup(self):
        payload = {"categories": [{"properties": [
            {"key": "difficulty", "value": "hard"},
            {"key": "onlineMode", "value": "false"},
        ]}]}
        self.assertEqual(rehearsal.config_value(payload, "onlineMode"), "false")
        self.assertIsNone(rehearsal.config_value(payload, "missing"))


class SafetyValidationTests(unittest.TestCase):
    def test_loopback_remote_node_is_refused(self):
        for host in ("tcp://127.0.0.1:2375", "tcp://localhost:2375", "unix:///var/run/docker.sock"):
            with self.assertRaises(rehearsal.RehearsalError):
                rehearsal.validate_remote_node(host, "/storage", "/tmp/remote")
        rehearsal.validate_remote_node("tcp://192.0.2.10:2375", "/storage", "/tmp/remote")

    def test_workspace_must_own_both_storage_trees(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            rehearsal.validate_workspace(root, root / "data", root / "remote")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "escapes"):
                rehearsal.validate_workspace(root, root / "data", Path(directory).parent / "elsewhere")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "must not be"):
                rehearsal.validate_workspace(root, root / "data", root / "data")

    def test_properties_update_preserves_unrelated_records(self):
        raw = properties_bytes() + b"rcon.password=do-not-export\n"
        updated = rehearsal.apply_properties(raw, {
            "difficulty": "hard",
            "view-distance": "8",
        })
        parsed = rehearsal.parse_properties(updated.decode("utf-8"))
        self.assertEqual(parsed["difficulty"], "hard")
        self.assertEqual(parsed["view-distance"], "8")
        self.assertEqual(parsed["rcon.password"], "do-not-export")

    def test_provenance_record_must_match_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "provenance.json"
            path.write_text(json.dumps({
                "commit": "a" * 40, "version": "2.0.1", "image_revision": "a" * 40,
            }))
            record = rehearsal.verify_provenance("a" * 40, "2.0.1", path)
            self.assertEqual(record["source"], "explicit-provenance")
            with self.assertRaisesRegex(rehearsal.RehearsalError, "commit"):
                rehearsal.verify_provenance("b" * 40, "2.0.1", path)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "version"):
                rehearsal.verify_provenance("a" * 40, "9.9.9", path)


class ControllerTransportTests(unittest.TestCase):
    """A fake controller, not Docker: proves Connect JSON and upload wiring."""

    def test_upload_and_download_round_trip(self):
        state = {"chunks": [], "requests": []}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def _json(self, code, body):
                payload = json.dumps(body).encode("utf-8")
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

            def do_POST(self):
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(length) or b"{}")
                state["requests"].append((self.path, body))
                if self.path.endswith("/InitUpload"):
                    self._json(200, {"sessionId": "upload-1", "totalChunks": 1})
                elif self.path.endswith("/UploadChunk"):
                    state["chunks"].append(body)
                    self._json(200, {"sessionId": "upload-1", "completed": True})
                elif self.path.endswith("/GetUploadStatus"):
                    self._json(200, {"sessionId": "upload-1", "completed": True})
                elif self.path.endswith("/Fail"):
                    self._json(400, {"code": "invalid_argument", "message": "refused"})
                else:
                    self._json(404, {"code": "not_found", "message": "unknown"})

            def do_GET(self):
                if self.path.startswith("/containers/"):
                    self._json(200, {"State": {"Running": False, "Status": "created"}})
                    return
                payload = b"downloaded-archive-bytes"
                self.send_response(200)
                self.send_header("Content-Length", str(len(payload)))
                self.end_headers()
                self.wfile.write(payload)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            controller = rehearsal.Controller("http://127.0.0.1:%d" % server.server_port)
            controller.token = "test-token"
            with tempfile.TemporaryDirectory() as directory:
                source = Path(directory) / "source.zip"
                source.write_bytes(b"archive-contents")
                session_id = controller.upload_file(source, "source.zip")
                self.assertEqual(session_id, "upload-1")
                chunk = state["chunks"][0]
                self.assertEqual(chunk["sessionId"], "upload-1")
                self.assertEqual(chunk["chunkIndex"], 0)
                self.assertEqual(base64.b64decode(chunk["data"]), b"archive-contents")
                target = Path(directory) / "download.zip"
                self.assertEqual(controller.download_to(session_id, target),
                                 len(b"downloaded-archive-bytes"))
            with self.assertRaisesRegex(rehearsal.ControllerError, "refused"):
                controller.call("FakeService", "Fail")
            state_before = rehearsal.remote_container_state(
                "tcp://127.0.0.1:%d" % server.server_port, "container-id")
            self.assertFalse(state_before["running"])
            self.assertEqual(state_before["status"], "created")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
