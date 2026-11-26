#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Disposable server-portability acceptance rehearsal (OSS-426/427, OSS-428).

This harness drives a real Carbon Panel controller through one complete native
archive round trip. It never creates, changes or publishes a release, image tag,
world, host service or repository:

* create a managed Vanilla source server without Docker overrides and prove the
  panel refuses an export while it is running;
* populate a deterministic fixture through the panel file API, save the world,
  stop the server and export both native formats (ZIP and tar.gz);
* verify each downloaded archive independently: schema, runtime, per-file
  SHA-256, expanded size and the absence of credential-bearing fixture files;
* upload each archive, preview it against a separate registered Docker node,
  create the imported server on that node, and verify the destination file
  hashes and stopped lifecycle *before* starting it;
* start both imports and confirm Minecraft readiness with the pinned
  minecraft-protocol client plus a live console roster request.

The job that calls this script owns the disposable controller and Docker
engines. All paths stay under ``--workspace``; the JSON evidence report is
written there. A failure always writes a failing report and exits non-zero.
"""
import argparse
import base64
import hashlib
import json
import os
import re
import socket
import subprocess
import sys
import tarfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
ARCHIVE_SCHEMA = 1
MANIFEST_NAME = "manifest.json"
DATA_PREFIX = "data/"
FIXTURE_PATH = "archive-rehearsal/fixture.txt"
EXCLUDED_PATH = "archive-rehearsal/.env"
FIXTURE_BYTES = (
    b"carbon-panel server archive acceptance fixture\n"
    b"format=zip-and-tar.gz\n"
    b"owner=disposable-ci\n"
)
EXCLUDED_BYTES = b"CARBON_ARCHIVE_ACCEPTANCE_SECRET=must-not-be-exported\n"
GAMEPLAY_PROPERTIES = {
    "difficulty": "hard",
    "gamemode": "creative",
    "level-name": "world",
    "sync-chunk-writes": "false",
    "pause-when-empty-seconds": "0",
}
READ_SAFE_RPCS = {
    "GetServer", "ListServers", "ListNodes", "GetNextAvailablePort",
    "GetUploadStatus", "GetImportJob", "Login", "PingNode", "GetFile",
    "ListFiles", "ListServerPlayers",
}
ARCHIVE_TIMEOUT_SECONDS = 3600
REPORT_NAME = "server-archive-rehearsal.json"
MAX_ARCHIVE_FILES = 50000
MAX_ARCHIVE_BYTES = 32 << 30


class RehearsalError(RuntimeError):
    """A fixture or acceptance invariant failure that must fail closed."""


class ControllerError(RehearsalError):
    def __init__(self, service, method, status, code, message):
        self.service = service
        self.method = method
        self.status = status
        self.code = code
        self.message = message
        super().__init__("%s/%s: HTTP %s %s: %s" % (service, method, status, code, message))


def as_int(value, label):
    """protojson encodes int64 fields as strings; accept either encoding."""
    if isinstance(value, bool) or value is None:
        raise RehearsalError("%s is missing" % label)
    try:
        return int(value)
    except (TypeError, ValueError):
        raise RehearsalError("%s is not an integer: %r" % (label, value))


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def portable_relative_path(rel):
    """The archive contract: slash-separated, relative, no drive or traversal."""
    if not isinstance(rel, str) or not rel or rel.startswith("/") or "\\" in rel:
        return False
    if "\x00" in rel or re.match(r"^[A-Za-z]:", rel):
        return False
    if rel.endswith("/") or "//" in rel:
        return False
    parts = rel.split("/")
    return all(part not in ("", ".", "..") for part in parts)


def _read_zip(path):
    entries = {}
    with zipfile.ZipFile(path) as bundle:
        for info in bundle.infolist():
            name = info.filename
            if info.is_dir():
                raise RehearsalError("archive contains a directory entry: %s" % name)
            if name in entries:
                raise RehearsalError("archive contains a duplicate entry: %s" % name)
            if info.file_size < 0 or info.file_size > MAX_ARCHIVE_BYTES:
                raise RehearsalError("archive entry has an invalid size: %s" % name)
            entries[name] = bundle.read(info)
    return entries


def _read_tar(path):
    entries = {}
    with tarfile.open(path, "r:gz") as bundle:
        for member in bundle:
            if member.isdir():
                raise RehearsalError("archive contains a directory entry: %s" % member.name)
            if not member.isreg():
                raise RehearsalError("archive contains a non-regular entry: %s" % member.name)
            if member.name in entries:
                raise RehearsalError("archive contains a duplicate entry: %s" % member.name)
            if member.size < 0 or member.size > MAX_ARCHIVE_BYTES:
                raise RehearsalError("archive entry has an invalid size: %s" % member.name)
            source = bundle.extractfile(member)
            if source is None:
                raise RehearsalError("archive entry is unreadable: %s" % member.name)
            entries[member.name] = source.read()
    return entries


def read_archive_entries(path, archive_format):
    path = Path(path)
    if archive_format == "zip":
        if not zipfile.is_zipfile(path):
            raise RehearsalError("download is not a ZIP archive")
        return _read_zip(path)
    if archive_format == "tar.gz":
        try:
            return _read_tar(path)
        except tarfile.TarError as error:
            raise RehearsalError("download is not a tar.gz archive: %s" % error)
    raise RehearsalError("unsupported archive format: %s" % archive_format)


def _expect_keys(value, allowed, label):
    if not isinstance(value, dict):
        raise RehearsalError("%s is not an object" % label)
    extra = sorted(set(value) - set(allowed))
    if extra:
        raise RehearsalError("%s has unknown fields: %s" % (label, ", ".join(extra)))


def verify_server_archive(path, archive_format, expected_exporter, expected_mc_version,
                          expected_memory, expected_max_players):
    """Independently validate one downloaded native archive and its manifest."""
    path = Path(path)
    entries = read_archive_entries(path, archive_format)
    if MANIFEST_NAME not in entries:
        raise RehearsalError("archive has no root %s" % MANIFEST_NAME)
    if list(entries).count(MANIFEST_NAME) != 1:
        raise RehearsalError("archive has more than one manifest")
    try:
        manifest = json.loads(entries[MANIFEST_NAME].decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RehearsalError("manifest.json is not valid UTF-8 JSON: %s" % error)
    _expect_keys(manifest, {"schema_version", "exporter_version", "runtime", "files"}, "manifest")
    if manifest.get("schema_version") != ARCHIVE_SCHEMA:
        raise RehearsalError("manifest schema_version is not %d" % ARCHIVE_SCHEMA)
    if not isinstance(manifest.get("exporter_version"), str) or not manifest["exporter_version"]:
        raise RehearsalError("manifest exporter_version is empty")
    if manifest["exporter_version"] != expected_exporter:
        raise RehearsalError("manifest exporter_version does not match the candidate")
    runtime = manifest.get("runtime")
    _expect_keys(runtime, {
        "minecraft_version", "mod_loader", "loader_version", "memory_mb",
        "max_players", "requires_custom_runtime", "requires_provider_setup",
    }, "manifest runtime")
    if runtime.get("minecraft_version") != expected_mc_version:
        raise RehearsalError("archive Minecraft version is not the fixed input")
    if runtime.get("mod_loader") != "vanilla":
        raise RehearsalError("archive is not a managed Vanilla source")
    if runtime.get("loader_version", "") != "":
        raise RehearsalError("Vanilla archive unexpectedly pins a loader version")
    if runtime.get("memory_mb") != expected_memory or runtime.get("max_players") != expected_max_players:
        raise RehearsalError("archive runtime does not match the source server")
    if runtime.get("requires_custom_runtime") or runtime.get("requires_provider_setup"):
        raise RehearsalError("archive depends on a custom runtime or provider setup")

    files = manifest.get("files")
    if not isinstance(files, list) or not files:
        raise RehearsalError("manifest has no files")
    if len(files) > MAX_ARCHIVE_FILES:
        raise RehearsalError("manifest exceeds the file count limit")
    observed = {}
    expanded = 0
    for record in files:
        _expect_keys(record, {"path", "size", "sha256"}, "manifest file record")
        rel = record.get("path")
        if not portable_relative_path(rel):
            raise RehearsalError("manifest contains a non-portable path: %r" % rel)
        if rel in observed:
            raise RehearsalError("manifest contains a duplicate path: %s" % rel)
        size = record.get("size")
        digest = record.get("sha256")
        if not isinstance(size, int) or size < 0 or size > MAX_ARCHIVE_BYTES:
            raise RehearsalError("manifest size is invalid for %s" % rel)
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise RehearsalError("manifest sha256 is invalid for %s" % rel)
        member = DATA_PREFIX + rel
        if member not in entries:
            raise RehearsalError("archive is missing data member for %s" % rel)
        body = entries[member]
        if len(body) != size:
            raise RehearsalError("archive size mismatch for %s" % rel)
        if sha256_bytes(body) != digest:
            raise RehearsalError("archive SHA-256 mismatch for %s" % rel)
        observed[rel] = {"size": size, "sha256": digest}
        expanded += size
    expected_members = {DATA_PREFIX + rel for rel in observed} | {MANIFEST_NAME}
    unexpected = sorted(set(entries) - expected_members)
    if unexpected:
        raise RehearsalError("archive has unmanifested members: %s" % ", ".join(unexpected))
    if expanded > MAX_ARCHIVE_BYTES:
        raise RehearsalError("archive exceeds the expanded size limit")

    if FIXTURE_PATH not in observed:
        raise RehearsalError("archive is missing the deterministic fixture file")
    if entries[DATA_PREFIX + FIXTURE_PATH] != FIXTURE_BYTES:
        raise RehearsalError("fixture bytes changed in the archive")
    if any(path == EXCLUDED_PATH or "/.env" in "/" + path or path.startswith(".env")
           for path in observed):
        raise RehearsalError("archive exported a credential-bearing fixture file")
    properties_rel = "server.properties"
    if properties_rel not in observed:
        raise RehearsalError("archive is missing the managed server.properties")
    properties = entries[DATA_PREFIX + properties_rel].decode("utf-8", errors="replace")
    parsed = parse_properties(properties)
    for key, value in GAMEPLAY_PROPERTIES.items():
        if parsed.get(key) != value:
            raise RehearsalError("archived gameplay setting %s is not %r" % (key, value))
    for forbidden in ("rcon.password", "rcon.port", "online-mode"):
        if forbidden in parsed:
            raise RehearsalError("archive exported credential or endpoint property %s"
                                 % forbidden)

    return {
        "format": archive_format,
        "archive_sha256": sha256_file(path),
        "archive_bytes": path.stat().st_size,
        "manifest_sha256": sha256_bytes(entries[MANIFEST_NAME]),
        "canonical_manifest_sha256": sha256_bytes(json.dumps({
            "schema_version": manifest["schema_version"],
            "exporter_version": manifest["exporter_version"],
            "runtime": runtime,
            "files": sorted(files, key=lambda item: item["path"]),
        }, sort_keys=True, separators=(",", ":")).encode("utf-8")),
        "file_count": len(observed),
        "expanded_bytes": expanded,
        "runtime": runtime,
        "files": observed,
        "fixture_sha256": observed[FIXTURE_PATH]["sha256"],
        "properties_sha256": observed[properties_rel]["sha256"],
    }


def parse_properties(text):
    result = {}
    continued = False
    for raw in text.splitlines():
        line = raw.rstrip("\r")
        slashes = len(line) - len(line.rstrip("\\"))
        was_continued = continued
        continued = slashes % 2 != 0
        if was_continued or continued:
            continue
        stripped = line.lstrip(" \t\f")
        if not stripped or stripped[0] in "#!":
            continue
        key, separator, value = stripped.partition("=")
        if separator:
            result[key] = value
    return result


def apply_properties(data, updates):
    """Update simple literal properties while preserving every other record."""
    remaining = dict(updates)
    output = []
    for raw in data.decode("utf-8", errors="replace").splitlines():
        stripped = raw.lstrip(" \t\f")
        key = stripped.partition("=")[0] if "=" in stripped and stripped[0] not in "#!" else None
        if key in remaining:
            output.append("%s=%s" % (key, remaining.pop(key)))
        else:
            output.append(raw)
    for key, value in remaining.items():
        output.append("%s=%s" % (key, value))
    return ("\n".join(output).rstrip("\n") + "\n").encode("utf-8")


def verify_destination(root, manifest_files):
    """Require the imported tree to be exactly the verified manifest bytes."""
    root = Path(root)
    if not root.is_dir():
        raise RehearsalError("destination directory is missing: %s" % root)
    observed = {}
    for current, dirs, names in os.walk(root, followlinks=False):
        for name in list(dirs):
            path = Path(current) / name
            if path.is_symlink():
                raise RehearsalError("destination contains a symlinked directory: %s" % path)
        for name in names:
            path = Path(current) / name
            if path.is_symlink() or not path.is_file():
                raise RehearsalError("destination contains a non-regular file: %s" % path)
            rel = path.relative_to(root).as_posix()
            if not portable_relative_path(rel):
                raise RehearsalError("destination contains a non-portable path: %s" % rel)
            observed[rel] = path
    expected = set(manifest_files)
    missing = sorted(expected - set(observed))
    extra = sorted(set(observed) - expected)
    if missing:
        raise RehearsalError("destination is missing files: %s" % ", ".join(missing[:5]))
    if extra:
        raise RehearsalError("destination has unverified files: %s" % ", ".join(extra[:5]))
    verified = {}
    for rel, record in manifest_files.items():
        actual = sha256_file(observed[rel])
        if actual != record["sha256"]:
            raise RehearsalError("destination SHA-256 mismatch for %s" % rel)
        verified[rel] = actual
    return verified


def archive_excluded(rel):
    """Mirror internal/serverarchive.excluded for source-to-archive comparison."""
    for component in rel.lower().split("/"):
        if component in (".git", ".ssh", ".env", "logs", "crash-reports",
                         "session.lock", "forwarding.secret", "secrets", "credentials"):
            return True
        if component.startswith(".env.") or component.startswith(".carbon-"):
            return True
        if Path(component).suffix in (".key", ".pem", ".p12", ".pfx", ".jks", ".secret"):
            return True
    return False


def source_file_hashes(root):
    """Hash every regular source file the archive format can carry verbatim."""
    root = Path(root)
    observed = {}
    for current, dirs, names in os.walk(root, followlinks=False):
        dirs[:] = [name for name in dirs
                   if not archive_excluded((Path(current) / name)
                                           .relative_to(root).as_posix())]
        for name in names:
            path = Path(current) / name
            rel = path.relative_to(root).as_posix()
            if archive_excluded(rel):
                continue
            if path.is_symlink() or not path.is_file():
                raise RehearsalError("source contains a non-regular file: %s" % rel)
            observed[rel] = {"size": path.stat().st_size, "sha256": sha256_file(path)}
    return observed


def _host_is_loopback(host):
    if not host:
        return True
    if host.lower() == "localhost":
        return True
    try:
        addresses = socket.getaddrinfo(host, None)
    except socket.gaierror:
        return False
    import ipaddress
    return any(ipaddress.ip_address(item[4][0]).is_loopback for item in addresses)


def validate_remote_node(host, storage_root, controller_storage_root):
    """A rehearsal node must be a genuinely remote engine with mapped storage."""
    parsed = urllib.parse.urlparse(host or "")
    if parsed.scheme not in ("tcp", "http", "https"):
        raise RehearsalError("remote Docker endpoint must be a TCP URL")
    if not parsed.hostname:
        raise RehearsalError("remote Docker endpoint has no host")
    if _host_is_loopback(parsed.hostname):
        raise RehearsalError(
            "remote Docker endpoint is loopback; the controller would classify it as local")
    if not storage_root.startswith("/"):
        raise RehearsalError("remote storage root must be an absolute node-side path")
    if not controller_storage_root:
        raise RehearsalError("controller-side remote storage mount is required")
    if Path(storage_root).as_posix() == Path(controller_storage_root).as_posix():
        raise RehearsalError("remote node and controller storage paths must not be identical")


def remote_container_state(host, container_id, timeout=30):
    """Read the engine's own state so the rehearsal does not trust only the DB."""
    parsed = urllib.parse.urlparse(host)
    if parsed.scheme not in ("tcp", "http", "https") or not parsed.hostname:
        raise RehearsalError("remote Docker endpoint cannot be inspected")
    scheme = "https" if parsed.scheme == "https" else "http"
    port = parsed.port or (443 if scheme == "https" else 2375)
    url = "%s://%s:%d/containers/%s/json" % (
        scheme, parsed.hostname, port, urllib.parse.quote(container_id, safe=""))
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response:
            payload = json.loads(response.read().decode("utf-8"))
    except (urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError) as error:
        raise RehearsalError("could not inspect imported container on the remote engine: %s"
                             % error)
    state = payload.get("State") or {}
    return {"running": bool(state.get("Running")), "status": state.get("Status", "")}


def validate_workspace(workspace, data_dir, remote_controller_storage):
    workspace = Path(workspace).resolve()
    if workspace == Path(workspace.anchor) or workspace == Path.home().resolve():
        raise RehearsalError("workspace must be a caller-owned disposable directory")
    data_dir = Path(data_dir).resolve()
    remote = Path(remote_controller_storage).resolve()
    for label, path in (("data directory", data_dir), ("remote storage", remote)):
        try:
            path.relative_to(workspace)
        except ValueError:
            raise RehearsalError("%s escapes the disposable workspace" % label)
    if data_dir == remote:
        raise RehearsalError("remote storage must not be the controller data directory")
    if data_dir in remote.parents or remote in data_dir.parents:
        raise RehearsalError("remote storage and controller data must be separate trees")


def assert_not_started(server, label):
    if server.get("status") != "SERVER_STATUS_STOPPED":
        raise RehearsalError("%s was not stopped before start: %s" % (label, server.get("status")))
    if not server.get("containerId"):
        raise RehearsalError("%s has no created container to verify before start" % label)


def assert_export_ready(server):
    if server.get("status") != "SERVER_STATUS_STOPPED":
        raise RehearsalError("export requires a stopped server")
    if server.get("detached"):
        raise RehearsalError("export requires a managed server")


def assert_no_docker_overrides(server, label):
    overrides = server.get("dockerOverrides")
    if overrides not in (None, {}, []):
        raise RehearsalError("%s unexpectedly carries Docker overrides" % label)


def remote_destination(controller_storage_root, data_path):
    base = Path(data_path).name
    if not base or not portable_relative_path(base):
        raise RehearsalError("server data path has no portable directory name")
    return Path(controller_storage_root) / base


def port_is_free(port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        try:
            probe.bind(("0.0.0.0", port))
        except OSError:
            return False
    return True


def next_free_port(start, reserved=()):
    for candidate in range(start, min(start + 200, 65536)):
        if candidate in reserved:
            continue
        if port_is_free(candidate):
            return candidate
    raise RehearsalError("no free game port near %d" % start)


class Controller:
    def __init__(self, base):
        self.base = base.rstrip("/")
        self.token = ""

    def call(self, service, method, body=None, timeout=120):
        body = body or {}
        headers = {
            "Content-Type": "application/json",
            "Connect-Protocol-Version": "1",
            "Accept": "application/json",
        }
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        request = urllib.request.Request(
            self.base + "/carbonpanel.v1." + service + "/" + method,
            data=json.dumps(body).encode("utf-8"),
            headers=headers,
            method="POST",
        )
        for attempt in range(3):
            try:
                with urllib.request.urlopen(request, timeout=timeout) as response:
                    payload = response.read()
                break
            except urllib.error.HTTPError as error:
                raw = error.read().decode("utf-8", errors="replace")
                try:
                    parsed = json.loads(raw)
                except json.JSONDecodeError:
                    parsed = {}
                raise ControllerError(service, method, error.code,
                                      parsed.get("code", ""), parsed.get("message", raw))
            except (urllib.error.URLError, TimeoutError) as error:
                retryable = (method in READ_SAFE_RPCS or body.get("requestId") or
                             (service == "ServerService" and method == "SendCommand"))
                if attempt == 2 or not retryable:
                    raise RehearsalError("%s/%s transport failed: %s"
                                         % (service, method, error))
                time.sleep(1)
        if not payload:
            return {}
        return json.loads(payload.decode("utf-8"))

    def health(self):
        with urllib.request.urlopen(self.base + "/healthz", timeout=5) as response:
            if response.status != 200:
                raise RehearsalError("controller health is not ok: HTTP %s" % response.status)
            return json.loads(response.read().decode("utf-8"))

    def download_to(self, session_id, destination):
        request = urllib.request.Request(
            self.base + "/api/v1/download/" + urllib.parse.quote(session_id),
            headers={"Authorization": "Bearer " + self.token},
        )
        with urllib.request.urlopen(request, timeout=ARCHIVE_TIMEOUT_SECONDS) as response, \
                Path(destination).open("wb") as target:
            while True:
                chunk = response.read(1 << 20)
                if not chunk:
                    break
                target.write(chunk)
        return Path(destination).stat().st_size

    def upload_file(self, path, filename):
        path = Path(path)
        total = path.stat().st_size
        session = self.call("UploadService", "InitUpload", {
            "filename": filename,
            "totalSize": total,
            "chunkSize": 4 * 1024 * 1024,
        })
        session_id = session.get("sessionId")
        if not session_id:
            raise RehearsalError("upload session was not created")
        chunk_size = 4 * 1024 * 1024
        with path.open("rb") as source:
            index = 0
            completed = False
            while True:
                chunk = source.read(chunk_size)
                if not chunk:
                    break
                result = self.call("UploadService", "UploadChunk", {
                    "sessionId": session_id,
                    "chunkIndex": index,
                    "data": base64.b64encode(chunk).decode("ascii"),
                })
                completed = bool(result.get("completed"))
                index += 1
        status = self.call("UploadService", "GetUploadStatus", {"sessionId": session_id})
        if not status.get("completed") and not completed:
            raise RehearsalError("upload did not complete")
        return session_id


def wait_for(label, seconds, attempt, interval=2):
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        try:
            return attempt()
        except (RehearsalError, ControllerError) as error:
            last = error
        time.sleep(interval)
    raise RehearsalError("%s did not converge: %s" % (label, last))


def verify_provenance(commit, version, provenance_path):
    record = {}
    if provenance_path:
        try:
            record = json.loads(Path(provenance_path).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as error:
            raise RehearsalError("provenance record is unreadable: %s" % error)
        if record.get("commit") != commit:
            raise RehearsalError("provenance commit does not match --candidate-commit")
        if record.get("version") != version:
            raise RehearsalError("provenance version does not match --candidate-version")
        if record.get("image_revision") not in (None, "", commit):
            raise RehearsalError("controller image revision label is not the candidate commit")
        record["source"] = "explicit-provenance"
        return record
    try:
        head = subprocess.run(
            ["git", "rev-parse", "HEAD"], cwd=str(ROOT), check=True,
            capture_output=True, text=True, timeout=30).stdout.strip()
        dirty = subprocess.run(
            ["git", "status", "--porcelain", "--untracked-files=no"], cwd=str(ROOT),
            check=True, capture_output=True, text=True, timeout=30).stdout.strip()
    except (OSError, subprocess.SubprocessError) as error:
        raise RehearsalError("no provenance record and git HEAD is unreadable: %s" % error)
    if head != commit:
        raise RehearsalError("checkout HEAD is not the exact candidate commit")
    if dirty:
        raise RehearsalError("tracked files differ from the exact candidate commit")
    return {"commit": head, "version": version, "source": "git-head"}


def run_readiness_probe(script, host, port, version, username, timeout_seconds):
    environment = dict(os.environ)
    environment.setdefault(
        "QUALIFY_NPM_PREFIX",
        str(ROOT / "scripts" / "qualify-compose-client"),
    )
    command = [
        "node", str(script),
        "--host", host,
        "--port", str(port),
        "--version", version,
        "--username", username,
        "--timeout", str(timeout_seconds * 1000),
    ]
    result = subprocess.run(
        command, cwd=str(ROOT), env=environment, capture_output=True, text=True,
        timeout=timeout_seconds + 30)
    if result.returncode != 0:
        raise RehearsalError("Minecraft readiness probe failed: %s" % (
            (result.stderr or result.stdout).strip()[:1000]))
    try:
        parsed = json.loads(result.stdout.strip().splitlines()[-1])
    except (IndexError, json.JSONDecodeError) as error:
        raise RehearsalError("readiness probe returned malformed output: %s" % error)
    if parsed.get("joined") is not True:
        raise RehearsalError("readiness probe did not reach the Play state")
    return {"joined": True, "host": host, "port": port, "username": username}


def command(controller, server_id, text):
    result = controller.call("ServerService", "SendCommand", {
        "id": server_id, "command": text,
    })
    if not result.get("success"):
        raise RehearsalError("console command %r failed: %s" % (text, result.get("error")))
    return result.get("output", "")


def server_record(controller, server_id):
    return controller.call("ServerService", "GetServer", {"id": server_id}).get("server", {})


def register_admin(controller, username, password):
    try:
        controller.call("AuthService", "Register", {"username": username, "password": password})
    except ControllerError as error:
        if "already exists" not in error.message.lower():
            raise
    login = controller.call("AuthService", "Login", {"username": username, "password": password})
    token = login.get("token")
    if not token:
        raise RehearsalError("fixture administrator could not sign in")
    controller.token = token


def config_value(payload, key):
    for category in payload.get("categories", []):
        for prop in category.get("properties", []):
            if prop.get("key") == key:
                return prop.get("value")
    return None


def ensure_node(controller, name, host, storage_root, controller_storage_root, local):
    nodes = controller.call("NodeService", "ListNodes").get("nodes", [])
    for node in nodes:
        if node.get("host") == host:
            if bool(node.get("isLocal")) != bool(local):
                raise RehearsalError("existing node %s has the wrong local classification" % name)
            return node
    body = {
        "name": name, "host": host, "enabled": True,
        "maxMemoryMb": 4096, "maxServers": 4,
    }
    if storage_root:
        body["storageRoot"] = storage_root
    if controller_storage_root:
        body["controllerStorageRoot"] = controller_storage_root
    node = controller.call("NodeService", "CreateNode", body).get("node", {})
    if not node.get("id"):
        raise RehearsalError("node creation did not return an id")
    if bool(node.get("isLocal")) != bool(local):
        raise RehearsalError("node %s has the wrong local classification" % name)
    return node


def wait_status(controller, server_id, wanted, seconds, label):
    def current():
        server = server_record(controller, server_id)
        if server.get("status") == "SERVER_STATUS_ERROR" and wanted != "SERVER_STATUS_ERROR":
            raise RehearsalError("%s entered error: %s"
                                 % (label, server.get("statusReason") or "unknown"))
        if server.get("status") != wanted:
            raise RehearsalError("%s is %s, want %s" % (label, server.get("status"), wanted))
        return server
    return wait_for(label, seconds, current)


def wait_import_container(controller, server_id, seconds, label):
    """CreateServer commits the row before its asynchronous container exists."""
    def current():
        server = server_record(controller, server_id)
        if server.get("status") == "SERVER_STATUS_ERROR":
            raise RehearsalError("%s entered error: %s"
                                 % (label, server.get("statusReason") or "unknown"))
        if server.get("status") != "SERVER_STATUS_STOPPED":
            raise RehearsalError("%s is %s, want a stopped import"
                                 % (label, server.get("status")))
        if not server.get("containerId"):
            raise RehearsalError("%s has no container yet" % label)
        return server
    return wait_for(label, seconds, current)


def reset_servers(controller, server_ids):
    for server_id in server_ids:
        try:
            controller.call("ServerService", "StopServer", {
                "id": server_id, "requestId": "archive-rehearsal-cleanup-stop-" + server_id[:8],
            }, timeout=180)
        except (ControllerError, RehearsalError):
            pass
        try:
            controller.call("ServerService", "DeleteServer", {
                "id": server_id, "requestId": "archive-rehearsal-cleanup-delete-" + server_id[:8],
            }, timeout=300)
        except (ControllerError, RehearsalError):
            pass


def execute(args):
    workspace = Path(args.workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    evidence = workspace / "evidence"
    evidence.mkdir(parents=True, exist_ok=True)
    data_dir = workspace / "data"
    controller_storage = Path(args.remote_controller_storage).resolve()
    validate_workspace(workspace, data_dir, controller_storage)
    validate_remote_node(args.remote_docker, args.remote_storage_root,
                         str(controller_storage))
    controller_storage.mkdir(parents=True, exist_ok=True)

    report = {
        "schema": "server-archive-rehearsal-ci-v1",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "workspace": str(workspace),
        "candidate": {
            "commit": args.candidate_commit,
            "version": args.candidate_version,
        },
        "environment": {
            "base": args.base,
            "minecraft_version": args.mc_version,
            "remote_docker": args.remote_docker,
            "remote_storage_root": args.remote_storage_root,
            "remote_controller_storage": str(controller_storage),
        },
        "fixture": {
            "path": FIXTURE_PATH,
            "sha256": sha256_bytes(FIXTURE_BYTES),
            "excluded_path": EXCLUDED_PATH,
            "gameplay": GAMEPLAY_PROPERTIES,
        },
        "formats": {},
        "checks": {},
        "pass": False,
        "failed_checks": [],
        "limits": [
            "Disposable controller and Docker-in-Docker node, not a production remote host or node VM.",
            "One managed Vanilla Minecraft version and a synthetic fixture; no mods, modules, proxy, OIDC or operator ingress.",
            "The controller and both engines run only under the hosted job's disposable workspace; no live host, world, registry tag or release is touched.",
            "Go archive tests use fake Docker; this hosted run is the real-engine acceptance evidence, not a release publication gate.",
        ],
    }
    checks = report["checks"]

    def ok(name):
        checks[name] = True

    controller = Controller(args.base)
    created_servers = []
    try:
        provenance = verify_provenance(args.candidate_commit, args.candidate_version,
                                       args.provenance)
        report["candidate"]["provenance"] = provenance
        ok("candidate_provenance_matches_head")

        wait_for("controller health", args.health_timeout, controller.health)
        ok("controller_healthy")

        password = os.environ.get("REHEARSAL_ADMIN_PASSWORD") or base64.urlsafe_b64encode(
            os.urandom(24)).decode("ascii")
        register_admin(controller, args.admin_username, password)
        ok("fixture_admin_authenticated")

        # Offline readiness is a destination setting, not archive content. Set
        # it explicitly on the disposable controller so the source and both
        # imports agree without Docker overrides.
        controller.call("ConfigService", "UpdateGlobalSettings", {
            "updates": {"onlineMode": "false"},
        })
        settings = controller.call("ConfigService", "GetGlobalSettings")
        if config_value(settings, "onlineMode") != "false":
            raise RehearsalError("disposable controller did not accept offline mode")
        ok("fixture_online_mode_disabled")

        local = ensure_node(controller, "archive-rehearsal-local",
                            args.local_docker, "", "", True)
        remote = ensure_node(controller, "archive-rehearsal-remote",
                             args.remote_docker, args.remote_storage_root,
                             str(controller_storage), False)
        report["environment"]["local_node_id"] = local.get("id")
        report["environment"]["remote_node_id"] = remote.get("id")
        if local.get("id") == remote.get("id"):
            raise RehearsalError("local and remote nodes must be distinct")
        ok("local_node_registered")
        ok("remote_node_registered_and_remote")

        ping = controller.call("NodeService", "PingNode", {"id": remote["id"]})
        if ping.get("success") is not True:
            raise RehearsalError("remote Docker node is not reachable: %s" % ping.get("message"))
        ok("remote_docker_reachable")

        base_port = controller.call("ServerService", "GetNextAvailablePort").get("port")
        if not isinstance(base_port, int) or base_port < 1:
            raise RehearsalError("no free game port was allocated")
        source_port = next_free_port(base_port)
        zip_port = next_free_port(source_port + 1, {source_port})
        tar_port = next_free_port(zip_port + 1, {source_port, zip_port})
        if len({source_port, zip_port, tar_port}) != 3:
            raise RehearsalError("rehearsal game ports are not distinct")
        report["environment"].update({
            "source_port": source_port, "zip_port": zip_port,
            "tar_gz_port": tar_port,
        })

        source_name = "archive-rehearsal-source"
        result = controller.call("ServerService", "CreateServer", {
            "name": source_name,
            "description": "Disposable native archive source",
            "mcVersion": args.mc_version,
            "modLoader": "MOD_LOADER_VANILLA",
            "memory": 1024,
            "maxPlayers": 5,
            "port": source_port,
            "nodeId": local["id"],
            "startImmediately": True,
            "autoStart": False,
            "requestId": "archive-rehearsal-source-create",
        })
        source = result.get("server", {})
        source_id = source.get("id")
        if not source_id:
            raise RehearsalError("source server creation returned no id")
        created_servers.append(source_id)
        if source.get("nodeId") != local["id"]:
            raise RehearsalError("source server was not placed on the local node")
        assert_no_docker_overrides(source, "source server")
        ok("source_created_managed_vanilla_without_overrides")

        source = wait_status(controller, source_id, "SERVER_STATUS_RUNNING", 600,
                             "source Minecraft startup")
        if not source.get("containerId"):
            raise RehearsalError("source server has no Docker container")
        ok("source_running_on_local_node")

        wait_for("source console ready", 180,
                 lambda: command(controller, source_id, "list"))
        source_probe = wait_for(
            "source Minecraft Play state", 300,
            lambda: run_readiness_probe(
                args.readiness_script, args.readiness_host, source_port,
                args.mc_version, args.player_name, args.readiness_timeout))
        ok("source_minecraft_ready")

        # A running server must be refused before any bytes are read.
        try:
            controller.call("ServerService", "ExportServerArchive", {
                "serverId": source_id,
                "format": "SERVER_ARCHIVE_FORMAT_ZIP",
                "consistency": "SERVER_ARCHIVE_CONSISTENCY_STOPPED",
            }, timeout=ARCHIVE_TIMEOUT_SECONDS)
        except ControllerError as error:
            if error.code != "failed_precondition" and "stop the managed server" not in error.message:
                raise RehearsalError("running export failed for the wrong reason: %s" % error)
            ok("export_refused_while_running")
        else:
            raise RehearsalError("export accepted a running server")

        command(controller, source_id, "gamerule keepInventory true")
        command(controller, source_id, "save-all flush")
        ok("source_world_saved")

        controller.call("ServerService", "StopServer", {
            "id": source_id, "requestId": "archive-rehearsal-source-stop",
        }, timeout=300)
        source = wait_status(controller, source_id, "SERVER_STATUS_STOPPED", 180,
                             "source shutdown")
        assert_export_ready(source)
        ok("source_stopped_before_export")

        # Populate deterministic files and gameplay through the panel while the
        # server is stopped, so the archive is the only transport.
        controller.call("FileService", "UpdateFile", {
            "serverId": source_id,
            "path": FIXTURE_PATH,
            "content": base64.b64encode(FIXTURE_BYTES).decode("ascii"),
        })
        controller.call("FileService", "UpdateFile", {
            "serverId": source_id,
            "path": EXCLUDED_PATH,
            "content": base64.b64encode(EXCLUDED_BYTES).decode("ascii"),
        })
        properties = controller.call("FileService", "GetFile", {
            "serverId": source_id, "path": "server.properties",
        })
        encoded = properties.get("content")
        if not encoded:
            raise RehearsalError("source server.properties is unavailable")
        updated = apply_properties(base64.b64decode(encoded), GAMEPLAY_PROPERTIES)
        controller.call("FileService", "UpdateFile", {
            "serverId": source_id,
            "path": "server.properties",
            "content": base64.b64encode(updated).decode("ascii"),
        })
        source = server_record(controller, source_id)
        source_dir = Path(source.get("dataPath", ""))
        if not source_dir.is_dir():
            raise RehearsalError("source data directory is not visible to the harness")
        if sha256_file(source_dir / FIXTURE_PATH) != sha256_bytes(FIXTURE_BYTES):
            raise RehearsalError("fixture file did not persist on the source node")
        world_level = source_dir / "world" / "level.dat"
        if not world_level.is_file():
            raise RehearsalError("source world has no level.dat after save")
        report["fixture"]["world_level_dat_sha256"] = sha256_file(world_level)
        report["source"] = {
            "server_id": source_id,
            "node_id": local.get("id"),
            "port": source_port,
            "data_path": source.get("dataPath"),
            "container_id": source.get("containerId"),
            "status_before_export": source.get("status"),
            "readiness": source_probe,
        }
        ok("source_fixture_and_gameplay_written")

        archives = {}
        for archive_format, rpc_format, suffix, port in (
                ("zip", "SERVER_ARCHIVE_FORMAT_ZIP", ".zip", zip_port),
                ("tar.gz", "SERVER_ARCHIVE_FORMAT_TAR_GZ", ".tar.gz", tar_port)):
            exported = controller.call("ServerService", "ExportServerArchive", {
                "serverId": source_id,
                "format": rpc_format,
                "consistency": "SERVER_ARCHIVE_CONSISTENCY_STOPPED",
            }, timeout=ARCHIVE_TIMEOUT_SECONDS)
            session_id = exported.get("sessionId")
            filename = exported.get("filename", "")
            if not session_id or not filename.endswith(suffix):
                raise RehearsalError("%s export returned an unexpected session/filename"
                                     % archive_format)
            destination = evidence / ("source" + suffix)
            downloaded = controller.download_to(session_id, destination)
            if downloaded != as_int(exported.get("totalSize"), "export total_size"):
                raise RehearsalError("%s download size differs from export size" % archive_format)
            verified = verify_server_archive(
                destination, archive_format, args.candidate_version, args.mc_version,
                1024, 5)
            verified["filename"] = filename
            verified["export_total_size"] = as_int(exported.get("totalSize"),
                                                   "export total_size")
            verified["port"] = port
            archives[archive_format] = verified
            report["formats"] = archives
            ok("archive_%s_download_verified" % archive_format.replace(".", "_"))
        report["formats"] = archives
        source_hashes = source_file_hashes(source_dir)
        source_hashes.pop("server.properties", None)
        archived_hashes = {
            rel: record for rel, record in archives["zip"]["files"].items()
            if rel != "server.properties"
        }
        if source_hashes != archived_hashes:
            missing = sorted(set(source_hashes) - set(archived_hashes))
            extra = sorted(set(archived_hashes) - set(source_hashes))
            changed = sorted(rel for rel in set(source_hashes) & set(archived_hashes)
                             if source_hashes[rel] != archived_hashes[rel])
            raise RehearsalError(
                "archive does not match the stopped source bytes "
                "(missing=%s extra=%s changed=%s)"
                % (missing[:3], extra[:3], changed[:3]))
        ok("archive_matches_source_bytes")
        if archives["zip"]["canonical_manifest_sha256"] == \
                archives["tar.gz"]["canonical_manifest_sha256"]:
            ok("both_formats_share_identical_manifest")
        else:
            raise RehearsalError("ZIP and tar.gz manifests differ for identical source data")
        if archives["zip"]["files"] != archives["tar.gz"]["files"]:
            raise RehearsalError("ZIP and tar.gz file hashes differ")

        imports = {}
        for archive_format, suffix in (("zip", ".zip"), ("tar.gz", ".tar.gz")):
            verified = archives[archive_format]
            archive_path = evidence / ("source" + suffix)
            upload_id = controller.upload_file(archive_path, verified["filename"])
            preview = controller.call("ServerService", "PreviewServerArchive", {
                "uploadId": upload_id, "nodeId": remote["id"],
            }, timeout=ARCHIVE_TIMEOUT_SECONDS)
            if preview.get("format") != ("SERVER_ARCHIVE_FORMAT_ZIP" if archive_format == "zip"
                                         else "SERVER_ARCHIVE_FORMAT_TAR_GZ"):
                raise RehearsalError("%s preview reported the wrong format" % archive_format)
            if preview.get("schemaVersion") != ARCHIVE_SCHEMA:
                raise RehearsalError("%s preview reported the wrong schema" % archive_format)
            if preview.get("exporterVersion") != args.candidate_version:
                raise RehearsalError("%s preview exporter differs from the candidate" % archive_format)
            if preview.get("mcVersion") != args.mc_version:
                raise RehearsalError("%s preview Minecraft version differs" % archive_format)
            if preview.get("modLoader") != "MOD_LOADER_VANILLA":
                raise RehearsalError("%s preview is not managed Vanilla" % archive_format)
            if preview.get("fileCount") != verified["file_count"]:
                raise RehearsalError("%s preview file count differs" % archive_format)
            if as_int(preview.get("expandedBytes"), "preview expanded_bytes") != verified["expanded_bytes"]:
                raise RehearsalError("%s preview expanded bytes differ" % archive_format)
            required = as_int(preview.get("requiredBytes"), "preview required_bytes")
            if required < verified["expanded_bytes"]:
                raise RehearsalError("%s preview required bytes are too small" % archive_format)
            available_value = preview.get("availableBytes")
            if available_value is None:
                raise RehearsalError("%s preview did not report destination space"
                                     % archive_format)
            available = as_int(available_value, "preview available_bytes")
            if available < required:
                raise RehearsalError("%s preview reports insufficient destination space"
                                     % archive_format)
            if preview.get("findings"):
                raise RehearsalError("%s preview reported findings: %s"
                                     % (archive_format, preview["findings"]))
            ok("preview_%s_clean_on_remote_node" % archive_format.replace(".", "_"))

            name = "archive-import-" + ("zip" if archive_format == "zip" else "tar-gz")
            result = controller.call("ServerService", "CreateServer", {
                "name": name,
                "archiveUploadId": upload_id,
                "nodeId": remote["id"],
                "port": verified["port"],
                "requestId": "archive-rehearsal-import-" + archive_format.replace(".", "-"),
                "startImmediately": False,
                "autoStart": False,
            }, timeout=ARCHIVE_TIMEOUT_SECONDS)
            imported = result.get("server", {})
            imported_id = imported.get("id")
            if not imported_id:
                raise RehearsalError("%s import returned no server id" % archive_format)
            created_servers.append(imported_id)
            if imported.get("nodeId") != remote["id"]:
                raise RehearsalError("%s import was not placed on the remote node" % archive_format)
            assert_no_docker_overrides(imported, "%s import" % archive_format)
            ok("import_%s_created_without_overrides" % archive_format.replace(".", "_"))
            imported = wait_import_container(
                controller, imported_id, 600,
                "%s import container creation" % archive_format)
            assert_not_started(imported, "%s import" % archive_format)
            if imported.get("mcVersion") != args.mc_version:
                raise RehearsalError("%s import did not inherit the archive version" % archive_format)
            if imported.get("modLoader") != "MOD_LOADER_VANILLA":
                raise RehearsalError("%s import did not inherit managed Vanilla" % archive_format)
            if imported.get("memory") != 1024 or imported.get("maxPlayers") != 5:
                raise RehearsalError("%s import did not inherit archive runtime defaults"
                                     % archive_format)
            destination = remote_destination(controller_storage, imported.get("dataPath", ""))
            verified_files = verify_destination(destination, verified["files"])
            if len(verified_files) != verified["file_count"]:
                raise RehearsalError("%s destination file count differs" % archive_format)
            engine_state = remote_container_state(
                args.remote_docker, imported.get("containerId"))
            if engine_state["running"]:
                raise RehearsalError("%s imported container was already running before start"
                                     % archive_format)
            ok("import_%s_files_verified_before_start" % archive_format.replace(".", "_"))
            ok("import_%s_container_not_running_before_start"
               % archive_format.replace(".", "_"))
            imports[archive_format] = {
                "server_id": imported_id,
                "name": name,
                "node_id": imported.get("nodeId"),
                "port": verified["port"],
                "status_before_start": imported.get("status"),
                "container_id": imported.get("containerId"),
                "engine_state_before_start": engine_state,
                "data_path": imported.get("dataPath"),
                "destination": str(destination),
                "verified_files": len(verified_files),
                "preview": {
                    "format": preview.get("format"),
                    "schema_version": preview.get("schemaVersion"),
                    "exporter_version": preview.get("exporterVersion"),
                    "file_count": preview.get("fileCount"),
                    "expanded_bytes": as_int(preview.get("expandedBytes"),
                                             "preview expanded_bytes"),
                    "required_bytes": required,
                    "available_bytes": available,
                    "findings": preview.get("findings", []),
                },
                "readiness": None,
            }
            report["imports"] = imports

        for archive_format, record in imports.items():
            controller.call("ServerService", "StartServer", {
                "id": record["server_id"],
                "requestId": "archive-rehearsal-start-" + archive_format.replace(".", "-"),
            }, timeout=300)
            wait_status(controller, record["server_id"], "SERVER_STATUS_RUNNING", 600,
                        "%s imported Minecraft startup" % archive_format)
            engine_state = remote_container_state(
                args.remote_docker, record["container_id"])
            if not engine_state["running"]:
                raise RehearsalError("%s imported container is not running on the remote engine"
                                     % archive_format)
            ok("import_%s_container_running_on_remote_engine"
               % archive_format.replace(".", "_"))
            roster = wait_for(
                "%s console ready" % archive_format, 180,
                lambda: command(controller, record["server_id"], "list"))
            difficulty = wait_for(
                "%s gameplay seed" % archive_format, 180,
                lambda: command(controller, record["server_id"], "difficulty"))
            if "hard" not in difficulty.lower():
                raise RehearsalError("%s import did not keep the archived difficulty"
                                     % archive_format)
            readiness = wait_for(
                "%s Minecraft Play state" % archive_format, 300,
                lambda: run_readiness_probe(
                    args.readiness_script, args.readiness_host, record["port"],
                    args.mc_version, args.player_name, args.readiness_timeout))
            readiness["console_roster"] = roster.strip()[:200]
            readiness["engine_state"] = engine_state
            record["readiness"] = readiness
            ok("import_%s_minecraft_ready" % archive_format.replace(".", "_"))
            command(controller, record["server_id"], "save-all flush")
        report["imports"] = imports
        ok("both_imports_ready")

        report["pass"] = all(checks.values())
        report["failed_checks"] = [name for name, passed in checks.items() if not passed]
    except Exception as error:  # noqa: BLE001 - keep partial evidence on failure
        report["pass"] = False
        report["error"] = "%s: %s" % (type(error).__name__, error)
        report["failed_checks"] = [name for name, passed in checks.items() if not passed]
        error.rehearsal_report = report
        raise
    finally:
        reset_servers(controller, created_servers)

    return report


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", required=True, help="caller-owned disposable directory")
    parser.add_argument("--base", required=True, help="running controller base URL")
    parser.add_argument("--candidate-version", required=True)
    parser.add_argument("--candidate-commit", required=True)
    parser.add_argument("--provenance", help="JSON record written by the hosted-job shell")
    parser.add_argument("--local-docker", default="unix:///var/run/docker.sock")
    parser.add_argument("--remote-docker", required=True)
    parser.add_argument("--remote-storage-root", required=True)
    parser.add_argument("--remote-controller-storage", required=True)
    parser.add_argument("--mc-version", default="1.21.1")
    parser.add_argument("--admin-username", default="archive-rehearsal")
    parser.add_argument("--player-name", default="ArchiveQual")
    parser.add_argument("--readiness-host", default="127.0.0.1")
    parser.add_argument("--readiness-script",
                        default=str(ROOT / "scripts" / "qualify-archive-player.mjs"))
    parser.add_argument("--readiness-timeout", type=int, default=120)
    parser.add_argument("--health-timeout", type=int, default=120)
    args = parser.parse_args(argv)

    workspace = Path(args.workspace).resolve()
    workspace.mkdir(parents=True, exist_ok=True)
    try:
        report = execute(args)
    except Exception as error:  # noqa: BLE001 - fail closed AND leave evidence
        report = getattr(error, "rehearsal_report", None) or {
            "schema": "server-archive-rehearsal-ci-v1",
            "generated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "workspace": str(workspace),
            "candidate": {
                "commit": args.candidate_commit,
                "version": args.candidate_version,
            },
        }
        report["pass"] = False
        report["error"] = "%s: %s" % (type(error).__name__, error)
    (workspace / REPORT_NAME).write_text(
        json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if report.get("pass"):
        print(json.dumps({
            "pass": True,
            "report": str(workspace / REPORT_NAME),
            "formats": sorted(report.get("formats", {})),
        }, indent=2))
        return 0
    print(json.dumps({
        "pass": False,
        "error": report.get("error"),
        "failed_checks": report.get("failed_checks", []),
        "report": str(workspace / REPORT_NAME),
    }, indent=2), file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
