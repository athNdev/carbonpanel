#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Rehearse the documented daily SQLite snapshot restore on a GitHub-hosted runner."""

import argparse
from contextlib import closing, contextmanager
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import sqlite3
import subprocess
import tarfile
import tempfile
import time
from urllib.error import URLError
from urllib.request import Request, urlopen
from zipfile import ZipFile

from scripts.upgrade_rehearsal_ci import binary_version, sha256_file, stable_archive_name


DOCKER_HOST = "unix:///var/run/docker.sock"
SNAPSHOT_NAME = "snapshot-world"
LATER_NAME = "renamed-after-snapshot"


def unpack_candidate(artifact, metadata, release_run, product_commit, workspace, evidence):
    digest = "sha256:" + sha256_file(artifact)
    if metadata["name"] != "verified-release-downloads" or digest != metadata["digest"]:
        raise ValueError("downloaded Actions artifact does not match the verified downloads record")
    source = metadata["workflow_run"]
    if str(source["id"]) != release_run or source["head_sha"] != product_commit:
        raise ValueError("Actions artifact does not match the expected release run and product commit")
    evidence.update(artifact_id=metadata["id"], artifact_digest=digest)

    with ZipFile(artifact) as staged:
        manifest_bytes = staged.read("release-manifest.json")
        manifest = json.loads(manifest_bytes)
        if manifest["commit"] != product_commit:
            raise ValueError("release manifest does not match the expected product commit")
        version = manifest["version"]
        archive_name = stable_archive_name(version, "linux", "amd64")
        if not re.fullmatch(r"carbon-panel_[a-zA-Z0-9.+-]+_linux_amd64\.tar\.gz", archive_name):
            raise ValueError("unexpected staged archive name")
        entries = [item for item in manifest["artifacts"] if item["name"] == archive_name]
        checksums = [
            digest for digest, filename in
            (line.split("  ", 1) for line in staged.read("checksums.txt").decode().splitlines())
            if filename == archive_name
        ]
        archive = workspace / archive_name
        with staged.open(archive_name) as source, archive.open("xb") as destination:
            shutil.copyfileobj(source, destination)

    archive_hash = sha256_file(archive)
    if (checksums != [archive_hash] or len(entries) != 1 or entries[0]["sha256"] != archive_hash
            or (entries[0]["os"], entries[0]["arch"]) != ("linux", "amd64")):
        raise ValueError("archive does not match both staged checksum records")
    evidence.update(archive=archive_name, archive_sha256=archive_hash)

    binary = workspace / "carbon-panel"
    with tarfile.open(archive, "r:gz") as bundle:
        members = [member for member in bundle.getmembers() if member.name == binary.name]
        if len(members) != 1 or not members[0].isfile():
            raise ValueError("staged archive must contain a single regular Panel binary")
        with bundle.extractfile(members[0]) as source, binary.open("xb") as destination:
            shutil.copyfileobj(source, destination)
    binary.chmod(0o700)
    reported_version = binary_version(binary)
    expected_version = f"carbon-panel {version.removeprefix('v')} (commit {product_commit};"
    if not reported_version.startswith(expected_version) or not reported_version.endswith("; linux/amd64)"):
        raise ValueError("packaged Panel binary reports an unexpected build identity")
    evidence.update(binary_version=reported_version, binary_sha256=sha256_file(binary))
    return binary


def docker(*args):
    return subprocess.run(
        ["docker", "--host", DOCKER_HOST, *args],
        check=True, capture_output=True, text=True, timeout=30,
    ).stdout.strip()


def rpc(port, method, payload=None, token=""):
    headers = {"Content-Type": "application/json", "Connect-Protocol-Version": "1"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = Request(
        f"http://127.0.0.1:{port}/carbonpanel.v1.{method}",
        json.dumps(payload or {}).encode(), headers,
    )
    with urlopen(request, timeout=10) as response:
        return json.load(response)


@contextmanager
def serving(binary, config, port):
    with tempfile.TemporaryFile(mode="w+b") as log:
        process = subprocess.Popen(
            [str(binary), "-config", str(config)], cwd=config.parent,
            stdout=log, stderr=subprocess.STDOUT,
        )
        try:
            deadline = time.monotonic() + 60
            while process.poll() is None and time.monotonic() < deadline:
                try:
                    with urlopen(f"http://127.0.0.1:{port}/readyz", timeout=2):
                        break
                except URLError:
                    time.sleep(0.5)
            else:
                raise RuntimeError("Panel did not become ready")
            yield
        except Exception:
            log.seek(0)
            print(log.read().decode(errors="replace")[-4000:])
            raise
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=30)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        if process.returncode != 0:
            raise RuntimeError(f"Panel exited with status {process.returncode}")


def database_state(path):
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro&immutable=1", uri=True)) as connection:
        integrity = connection.execute("PRAGMA integrity_check").fetchall()
        if integrity != [("ok",)]:
            raise RuntimeError("Panel DB integrity check failed")
        return connection.execute("SELECT id, name FROM servers ORDER BY id").fetchall()


def rehearse(binary, workspace, network, evidence):
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    data = workspace / "data"
    world = data / "disposable-server" / "world"
    backups = workspace / "game-backups"
    world.mkdir(parents=True)
    backups.mkdir()
    markers = [world / "level.dat", backups / "world-backup.marker"]
    for path in markers:
        path.write_bytes(secrets.token_bytes(128))
    original_markers = {str(path.relative_to(workspace)): sha256_file(path) for path in markers}
    evidence["world_and_backup_sha256"] = original_markers

    live_db = data / "carbon-panel.db"
    config = workspace / "config.yaml"
    settings = {
        "server": {"host": "127.0.0.1", "port": str(port)},
        "auth": {"local": {"enabled": True}},
        "docker": {"host": DOCKER_HOST, "network_name": network, "reconcile_purge_orphans": False},
        "database": {"path": str(live_db), "panel_backup": "off"},
        "storage": {"data_dir": str(data), "backup_dir": str(backups), "temp_dir": str(workspace / "scratch")},
    }
    config.write_text(json.dumps(settings), encoding="utf-8")
    credentials = {"username": "restore-admin", "password": "Aa9!" + secrets.token_urlsafe(24)}
    server_id = "disposable-server"
    checks = evidence["checks"]

    def check(name, condition):
        checks[name] = bool(condition)
        if not condition:
            raise RuntimeError(name)

    def expect_inventory(stage, name):
        token = rpc(port, "AuthService/Login", credentials)["token"]
        user = rpc(port, "AuthService/GetCurrentUser", token=token)["user"]
        servers = rpc(port, "ServerService/ListServers", token=token)["servers"]
        check(f"login_and_inventory_{stage}", user["id"] == user_id and
              [(server["id"], server["name"]) for server in servers] == [(server_id, name)])

    with serving(binary, config, port):
        user_id = rpc(port, "AuthService/Register", credentials)["user"]["id"]

    # Seed only persisted metadata for a stopped world. Restoring the control
    # plane should never start a Minecraft process or replace the world bytes.
    with closing(sqlite3.connect(live_db)) as connection, connection:
        connection.execute(
            """INSERT INTO servers (id, name, mod_loader, mc_version, status, data_path, created_at, updated_at)
               VALUES (?, ?, 'vanilla', '1.21.1', 'stopped', ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)""",
            (server_id, SNAPSHOT_NAME, str(world.parent)),
        )

    settings["database"]["panel_backup"] = "daily"
    config.write_text(json.dumps(settings), encoding="utf-8")
    with serving(binary, config, port):
        deadline = time.monotonic() + 30
        snapshots = []
        while not snapshots and time.monotonic() < deadline:
            snapshots = list((data / "backups").glob("panel-????????.sqlite"))
            if not snapshots:
                time.sleep(0.25)
        check("packaged_panel_created_daily_snapshot", len(snapshots) == 1)
        snapshot = snapshots[0]
        check("daily_snapshot_integrity_and_inventory", database_state(snapshot) == [(server_id, SNAPSHOT_NAME)])
        expect_inventory("at_snapshot", SNAPSHOT_NAME)
        snapshot_hash = sha256_file(snapshot)
        evidence["snapshot_sha256"] = snapshot_hash

    # Keep the daily snapshot fixed across the subsequent startups.
    settings["database"]["panel_backup"] = "off"
    config.write_text(json.dumps(settings), encoding="utf-8")
    with closing(sqlite3.connect(live_db)) as connection, connection:
        connection.execute("UPDATE servers SET name = ? WHERE id = ?", (LATER_NAME, server_id))
    with serving(binary, config, port):
        expect_inventory("before_restore", LATER_NAME)
    live_hash = sha256_file(live_db)
    sidecars = [Path(str(live_db) + suffix) for suffix in ("-wal", "-shm")]
    stale = b"synthetic stale sidecar"
    for path in sidecars:
        path.write_bytes(stale)
    previous = Path(str(live_db) + ".pre-restore")
    restore = [str(binary), "-config", str(config), "-restore-panel-backup"]
    corrupt = workspace / "corrupt.sqlite"
    corrupt.write_bytes(b"intentionally unreadable sqlite")
    rejected = subprocess.run([*restore, str(corrupt)], capture_output=True, text=True, timeout=60, cwd=workspace)
    check("corrupt_snapshot_refused", rejected.returncode != 0 and "refusing to restore" in rejected.stdout + rejected.stderr)
    check("corrupt_snapshot_preserves_live_files", sha256_file(live_db) == live_hash and
          all(path.read_bytes() == stale for path in sidecars) and not previous.exists())

    restored = subprocess.run([*restore, str(snapshot)], capture_output=True, text=True, timeout=60, cwd=workspace)
    if restored.returncode != 0:
        print(restored.stdout + restored.stderr)
    check("offline_restore_exit_zero", restored.returncode == 0)
    check("offline_restore_removed_stale_sidecars", not any(path.exists() for path in sidecars))
    check("offline_restore_kept_previous_database", sha256_file(previous) == live_hash and
          database_state(previous) == [(server_id, LATER_NAME)])
    check("restored_database_matches_snapshot", sha256_file(live_db) == snapshot_hash and
          database_state(live_db) == [(server_id, SNAPSHOT_NAME)])
    check("daily_snapshot_bytes_preserved", sha256_file(snapshot) == snapshot_hash)
    with serving(binary, config, port):
        expect_inventory("after_restore", SNAPSHOT_NAME)
    check("world_and_game_backup_bytes_preserved", original_markers ==
          {str(path.relative_to(workspace)): sha256_file(path) for path in markers})


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact", type=Path, required=True)
    parser.add_argument("--metadata", type=Path, required=True)
    parser.add_argument("--product-commit", required=True)
    parser.add_argument("--release-run", required=True)
    parser.add_argument("--harness-commit", required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    if os.environ.get("GITHUB_ACTIONS") != "true" or os.environ.get("RUNNER_ENVIRONMENT") != "github-hosted":
        parser.error("Run this rehearsal on a disposable GitHub-hosted runner")

    evidence = {
        "started_utc": datetime.now(timezone.utc).isoformat(),
        "runner": platform.platform(),
        "qualification_run": os.environ["GITHUB_RUN_ID"],
        "release_run": args.release_run,
        "product_commit": args.product_commit,
        "harness_commit": args.harness_commit,
        "profile": "native linux/amd64 Panel daily SQLite snapshot and offline restore",
        "checks": {},
    }
    try:
        with tempfile.TemporaryDirectory(prefix="panel-restore-", dir=os.environ["RUNNER_TEMP"]) as scratch:
            workspace = Path(scratch)
            binary = unpack_candidate(args.artifact, json.loads(args.metadata.read_text(encoding="utf-8")),
                                      args.release_run, args.product_commit, workspace, evidence)
            evidence["docker_version"] = docker("version", "--format", "{{.Server.Version}}")
            network = "panel-restore-" + secrets.token_hex(8)
            network_id = docker("network", "create", network)
            try:
                rehearse(binary, workspace, network, evidence)
            finally:
                docker("network", "rm", network_id)
        evidence["status"] = "passed"
    except Exception as error:
        evidence.update(status="failed", error=str(error))
    report = json.dumps(evidence, indent=2) + "\n"
    args.evidence.write_text(report, encoding="utf-8")
    print(report, end="")
    return 0 if evidence["status"] == "passed" else 1


if __name__ == "__main__":
    raise SystemExit(main())
