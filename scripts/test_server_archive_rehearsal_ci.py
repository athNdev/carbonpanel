# SPDX-License-Identifier: MPL-2.0
"""Offline fail-closed tests for the server-archive acceptance harness.

These tests never start Docker, a controller or Minecraft. They build synthetic
ZIP and tar.gz archives with the real schema and exercise the same independent
verification, lifecycle gates and workspace/remote-node validators the hosted
rehearsal uses. Docker-dependent orchestration remains a CI-only check.
"""
import json
import base64
import os
import subprocess
import tarfile
import tempfile
import threading
import unittest
import zipfile
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from io import BytesIO
from pathlib import Path
from unittest import mock

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


def _write_private_json(path, record):
    path = Path(path)
    fd = os.open(str(path), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(record, handle, sort_keys=True)
    return path


def build_hosted_fixture(runner_temp, commit="a" * 40, version="2.0.1"):
    workspace = Path(runner_temp) / "server-archive-rehearsal"
    workspace.mkdir()
    config = workspace / "config.yaml"
    config.write_text(
        'server:\n  host: "127.0.0.1"\n  port: "18080"\n', encoding="utf-8")
    run_path = _write_private_json(workspace / "run.json", {
        "schema": rehearsal.RUN_SCHEMA,
        "run_id": "1234-1",
    })
    remote_storage = (workspace / "remote-storage").resolve()
    info = workspace.stat()
    fixture = {
        "schema": rehearsal.FIXTURE_SCHEMA,
        "workspace": str(workspace.resolve()),
        "workspace_device": info.st_dev,
        "workspace_inode": info.st_ino,
        "runner_temp": str(Path(runner_temp).resolve()),
        "run_file": str(run_path.resolve()),
        "run_sha256": rehearsal.sha256_file(run_path),
        "run_id": "1234-1",
        "commit": commit,
        "version": version,
        "image": "sha256:image",
        "image_reference": "server-archive-rehearsal:" + commit,
        "image_revision": commit,
        "source_binary_sha256": "b" * 64,
        "controller_name": "server-archive-controller-1",
        "controller_id": "1" * 64,
        "controller_port": 18080,
        "controller_image_id": "sha256:image",
        "controller_config": str(config.resolve()),
        "controller_config_sha256": rehearsal.sha256_file(config),
        "base_url": "http://127.0.0.1:18080",
        "remote_engine_name": "server-archive-node-1",
        "remote_engine_id": "2" * 64,
        "remote_engine_image_id": "sha256:remote-image",
        "remote_network": "archive-private",
        "remote_network_id": "3" * 64,
        "remote_docker": "tcp://192.0.2.10:2375",
        "remote_address": "192.0.2.10",
        "remote_management_port": 2375,
        "destination_probe_host": "192.0.2.10",
        "remote_storage_root": "/storage",
        "remote_controller_storage": str(remote_storage),
        "dind_image": "docker:dind@sha256:" + "c" * 64,
        "dind_digest": "sha256:" + "c" * 64,
        "dind_registry_source": "https://registry-1.docker.io/v2/library/docker/manifests/dind",
        "local_docker_server_version": "27.0.0",
        "remote_docker_server_version": "27.0.0",
    }
    fixture_path = _write_private_json(workspace / "fixture.json", fixture)
    return fixture_path, fixture, workspace


def fixture_inspector(fixture, commit):
    def inspect(container_id, local_docker):
        assert local_docker == rehearsal.LOCAL_DOCKER
        run_labels = {"com.carbon-panel.rehearsal.run": fixture["run_id"]}
        if container_id == fixture["remote_engine_id"]:
            return {
                "Id": container_id,
                "Name": "/" + fixture["remote_engine_name"],
                "Image": fixture["remote_engine_image_id"],
                "State": {"Running": True},
                "Config": {"Labels": run_labels},
                "HostConfig": {"NetworkMode": fixture["remote_network"]},
                "NetworkSettings": {"Networks": {fixture["remote_network"]: {
                    "NetworkID": fixture["remote_network_id"],
                    "IPAddress": fixture["remote_address"],
                }}},
                "Mounts": [{
                    "Type": "bind", "RW": True,
                    "Source": fixture["remote_controller_storage"],
                    "Destination": fixture["remote_storage_root"],
                }],
            }
        assert container_id == fixture["controller_id"]
        return {
            "Id": container_id,
            "Name": "/" + fixture["controller_name"],
            "Image": fixture["controller_image_id"],
            "State": {"Running": True},
            "HostConfig": {"NetworkMode": "host"},
            "Config": {
                "Labels": {**run_labels, "org.opencontainers.image.revision": commit},
                "Cmd": ["./carbon-panel", "-config", fixture["controller_config"]],
            },
            "Mounts": [{
                "Type": "bind", "Source": fixture["workspace"],
                "Destination": fixture["workspace"],
            }],
        }
    return inspect


class RunningExportRefusalTests(unittest.TestCase):
    def refusal(self, code, message):
        def attempt():
            raise rehearsal.ControllerError(
                "ServerService", "ExportServerArchive", 400, code, message)
        return attempt

    def test_requires_both_code_and_specific_message(self):
        rehearsal.assert_running_export_refused(self.refusal(
            "failed_precondition", rehearsal.RUNNING_EXPORT_REFUSAL))
        for code, message in (
                ("invalid_argument", rehearsal.RUNNING_EXPORT_REFUSAL),
                ("failed_precondition", "server is busy")):
            with self.assertRaisesRegex(rehearsal.RehearsalError, "wrong reason"):
                rehearsal.assert_running_export_refused(self.refusal(code, message))

    def test_acceptance_is_refused(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "accepted"):
            rehearsal.assert_running_export_refused(lambda: None)


class HostedFixtureBindingTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.runner_temp = Path(self.temp.name)
        self.commit = "a" * 40
        self.version = "2.0.1"
        self.fixture_path, self.fixture, self.workspace = build_hosted_fixture(
            self.runner_temp, self.commit, self.version)

    def tearDown(self):
        self.temp.cleanup()

    def verify(self, **overrides):
        base = overrides.get("base", self.fixture["base_url"])
        remote = overrides.get("remote", self.fixture["remote_docker"])
        fixture_path = overrides.get("fixture_path", self.fixture_path)
        commit = overrides.get("commit", self.commit)
        inspector = overrides.get("inspector", fixture_inspector(self.fixture, self.commit))
        return rehearsal.verify_fixture(
            fixture_path, self.workspace, self.runner_temp, commit, self.version,
            base, remote, self.fixture["remote_storage_root"],
            self.fixture["remote_controller_storage"],
            overrides.get("local_docker", rehearsal.LOCAL_DOCKER),
            inspector=inspector, checkout=lambda wanted: wanted)

    def test_fixture_binds_checkout_controller_and_remote_endpoint(self):
        record = self.verify()
        self.assertEqual(record["source"], "hosted-fixture")
        self.assertEqual(record["controller_id"], self.fixture["controller_id"])
        self.assertEqual(record["remote_engine_id"], self.fixture["remote_engine_id"])
        self.assertEqual(record["remote_address"], "192.0.2.10")
        self.assertEqual(record["destination_probe_host"], "192.0.2.10")

    def test_missing_fixture_is_refused(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "missing"):
            self.verify(fixture_path=self.workspace / "absent.json")

    def test_arbitrary_base_is_refused(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "literal loopback"):
            self.verify(base="http://192.0.2.10:18080")
        with self.assertRaisesRegex(rehearsal.RehearsalError, "port"):
            self.verify(base="http://127.0.0.1:18081")
        with self.assertRaisesRegex(rehearsal.RehearsalError, "path"):
            self.verify(base="http://127.0.0.1:18080/other")

    def test_loopback_remote_endpoint_is_refused(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "loopback"):
            self.verify(remote="tcp://127.0.0.1:2375")

    def test_wrong_commit_and_storage_are_refused(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "candidate checkout"):
            self.verify(commit="b" * 40)
        with self.assertRaisesRegex(rehearsal.RehearsalError, "remote storage root"):
            rehearsal.verify_fixture(
                self.fixture_path, self.workspace, self.runner_temp, self.commit,
                self.version, self.fixture["base_url"], self.fixture["remote_docker"],
                "/other", self.fixture["remote_controller_storage"],
                "unix:///var/run/docker.sock",
                inspector=fixture_inspector(self.fixture, self.commit),
                checkout=lambda wanted: wanted)

    def test_controller_identity_is_required(self):
        def wrong_revision(container_id, local_docker):
            payload = fixture_inspector(self.fixture, "b" * 40)(
                container_id, local_docker)
            return payload
        with self.assertRaisesRegex(rehearsal.RehearsalError, "revision"):
            self.verify(inspector=wrong_revision)

    def test_requires_local_runner_docker_socket(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "runner socket"):
            self.verify(local_docker="tcp://192.0.2.30:2375")

    def test_destination_engine_requires_owned_bridge_address_and_storage(self):
        base_inspector = fixture_inspector(self.fixture, self.commit)
        def inspect_changed_engine(mutation):
            def inspect(container_id, local_docker):
                observation = base_inspector(container_id, local_docker)
                if container_id == self.fixture["remote_engine_id"]:
                    mutation(observation)
                return observation
            return inspect

        cases = (
            ("ownership", "belong", lambda o: o["Config"]["Labels"].clear()),
            ("image", "pinned image", lambda o: o.update(Image="sha256:another-image")),
            ("port", "unpublished bridge", lambda o: o["HostConfig"].update(
                PortBindings={"2375/tcp": [{"HostPort": "2375"}]})),
            ("address", "bridge or address", lambda o: o["NetworkSettings"]["Networks"][
                self.fixture["remote_network"]].update(IPAddress="192.0.2.11")),
            ("mount", "storage mount", lambda o: o["Mounts"].clear()),
        )
        for name, expected, mutation in cases:
            with self.subTest(name=name):
                with self.assertRaisesRegex(rehearsal.RehearsalError, expected):
                    self.verify(inspector=inspect_changed_engine(mutation))

    def test_setup_refusal_preserves_existing_report(self):
        report = self.workspace / rehearsal.REPORT_NAME
        report.write_text("existing content", encoding="utf-8")
        args = [
            "--workspace", str(self.workspace), "--fixture", str(self.fixture_path),
            "--base", self.fixture["base_url"], "--candidate-version", self.version,
            "--candidate-commit", self.commit, "--remote-docker", self.fixture["remote_docker"],
            "--remote-storage-root", self.fixture["remote_storage_root"],
            "--remote-controller-storage", self.fixture["remote_controller_storage"],
        ]
        with mock.patch.dict(os.environ, {"GITHUB_ACTIONS": "false"}):
            self.assertEqual(rehearsal.main(args), 1)
        self.assertEqual(report.read_text(encoding="utf-8"), "existing content")

    def test_changed_run_record_is_refused(self):
        (self.workspace / "run.json").write_text(json.dumps({
            "schema": rehearsal.RUN_SCHEMA, "run_id": "changed"}), encoding="utf-8")
        with self.assertRaisesRegex(rehearsal.RehearsalError, "run record changed"):
            self.verify()

    def test_hosted_environment_and_workspace_are_required(self):
        with self.assertRaisesRegex(rehearsal.RehearsalError, "GitHub Actions"):
            rehearsal.require_hosted_environment({})
        with self.assertRaisesRegex(rehearsal.RehearsalError, "github-hosted"):
            rehearsal.require_hosted_environment({
                "GITHUB_ACTIONS": "true",
                "RUNNER_ENVIRONMENT": "self-hosted",
                "RUNNER_TEMP": str(self.runner_temp),
            })
        nested = self.runner_temp / "fresh"
        nested.mkdir()
        self.assertEqual(rehearsal.validate_hosted_workspace(nested, self.runner_temp),
                         nested.resolve())
        with self.assertRaisesRegex(rehearsal.RehearsalError, "subdirectory"):
            rehearsal.validate_hosted_workspace(self.runner_temp, self.runner_temp)
        with tempfile.TemporaryDirectory() as elsewhere:
            with self.assertRaisesRegex(rehearsal.RehearsalError, "inside RUNNER_TEMP"):
                rehearsal.validate_hosted_workspace(Path(elsewhere), self.runner_temp)


@unittest.skipUnless(os.name == "posix", "hosted cleanup uses Bash on Linux")
class ShellCleanupTests(unittest.TestCase):
    def test_wrong_owner_is_untouched_and_failed_removal_fails_the_job(self):
        script = (rehearsal.ROOT / "scripts" / "qualify-server-archive-hosted.sh").read_text()
        cleanup_functions = script.split("owned_container() {", 1)[1].split(
            "trap cleanup EXIT", 1)[0]
        fixture = r"""
set -eu
work="$PWD"
evidence="$work/evidence"
mkdir "$evidence"
run_label=1234-1
controller=fixture-controller
remote_engine=fixture-node
source_network=source-net
network=destination-net
source_network_id=
network_id=
STAGED_SHA=fixture
CANDIDATE_VERSION=fixture
stage=rehearsal
printf '%064d\n' 1 > "$work/controller.cid"
timeout() { shift; "$@"; }
docker() {
  case "$1" in
    inspect) printf '/%s %s\n' "$controller" "$observed_label";;
    logs) echo logs >> "$work/calls";;
    rm) echo rm >> "$work/calls"; return 1;;
    *) return 77;;
  esac
}
observed_label=other-run
if (true; cleanup); then exit 1; fi
test ! -e "$work/calls"
observed_label=$run_label
if (true; cleanup); then exit 1; fi
grep -qx logs "$work/calls"
grep -qx rm "$work/calls"
grep -qx status=1 "$evidence/run-status.txt"
grep -qx cleanup_failed=1 "$evidence/cleanup.txt"
"""
        with tempfile.TemporaryDirectory() as workspace:
            result = subprocess.run(
                ["bash", "-s"], input="owned_container() {" + cleanup_functions + fixture,
                text=True, capture_output=True, cwd=workspace, timeout=15)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


class CleanupTests(unittest.TestCase):
    class FakeController:
        def __init__(self, surviving=()):
            self.surviving = set(surviving)
            self.calls = []

        def call(self, service, method, body=None, timeout=120, retries=3):
            self.calls.append((method, body))
            if method == "GetServer":
                if body["id"] in self.surviving:
                    return {"server": {"id": body["id"]}}
                raise rehearsal.ControllerError(
                    service, method, 404, "not_found", "server not found")
            if method == "DeleteServer" and body["id"] in self.surviving:
                raise rehearsal.ControllerError(
                    service, method, 500, "internal", "delete failed")
            return {}

    def test_owned_servers_are_cleaned_and_deduplicated(self):
        controller = self.FakeController()
        result = rehearsal.cleanup_owned_servers(controller, ["s1", "s2", "s1"])
        self.assertTrue(result["pass"])
        self.assertEqual(result["requested"], 2)
        self.assertEqual(result["cleaned"], ["s1", "s2"])
        self.assertEqual(result["failures"], [])

    def test_surviving_owned_server_fails_cleanup(self):
        controller = self.FakeController(surviving={"s1"})
        result = rehearsal.cleanup_owned_servers(controller, ["s1"])
        self.assertFalse(result["pass"])
        self.assertIn("still exists", result["failures"][0]["reason"])

    def test_invalid_owned_id_is_reported(self):
        result = rehearsal.cleanup_owned_servers(self.FakeController(), [""])
        self.assertFalse(result["pass"])
        self.assertIn("invalid", result["failures"][0]["reason"])


class RedirectTests(unittest.TestCase):
    def test_controller_refuses_redirects(self):
        hits = []

        class Target(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                hits.append(self.path)
                self.send_response(200)
                self.send_header("Content-Length", "2")
                self.end_headers()
                self.wfile.write(b"{}")

        target = ThreadingHTTPServer(("127.0.0.1", 0), Target)
        target_url = "http://127.0.0.1:%d/elsewhere" % target.server_port

        class Redirect(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_POST(self):
                self.rfile.read(int(self.headers.get("Content-Length", "0")))
                self.send_response(302)
                self.send_header("Location", target_url)
                self.send_header("Content-Length", "0")
                self.end_headers()

        redirect = ThreadingHTTPServer(("127.0.0.1", 0), Redirect)
        threads = [
            threading.Thread(target=server.serve_forever, daemon=True)
            for server in (target, redirect)
        ]
        for thread in threads:
            thread.start()
        try:
            controller = rehearsal.Controller(
                "http://127.0.0.1:%d" % redirect.server_port)
            with self.assertRaisesRegex(rehearsal.RehearsalError, "redirect"):
                controller.call("FakeService", "Ping")
            self.assertEqual(hits, [])
        finally:
            for server in (target, redirect):
                server.shutdown()
                server.server_close()
            for thread in threads:
                thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
