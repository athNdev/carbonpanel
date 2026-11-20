#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Qualify packaged Linux Panel binaries under systemd on a disposable runner."""

import argparse
import contextlib
import getpass
import json
import os
import platform
import secrets
import shutil
import socket
import subprocess
import sys
import tarfile
import time
import urllib.request
from pathlib import Path

from scripts import upgrade_rehearsal_ci as rehearsal


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(*args):
    return subprocess.run(args, check=True, text=True, capture_output=True, timeout=90).stdout.strip()


def verify_bundle(directory, destination, commit, arch):
    manifest_path = directory / "release-manifest.json"
    checksums_path = directory / "checksums.txt"
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    require(manifest["commit"] == commit, "Unexpected release manifest commit")
    entries = [item for item in manifest["artifacts"] if (item["os"], item["arch"]) == ("linux", arch)]
    require(len(entries) == 1, f"Expected one Linux {arch} archive in the manifest")
    entry = entries[0]
    archive_name = entry["name"]
    require(archive_name == Path(archive_name).name and archive_name.endswith(".tar.gz"),
            "Unexpected archive path")
    archive = directory / archive_name
    archive_digest = rehearsal.sha256_file(archive)
    checksum_entries = [line.split() for line in checksums_path.read_text(encoding="utf-8").splitlines()]
    matching = [parts[0] for parts in checksum_entries if len(parts) == 2 and parts[1] == archive_name]
    require(matching == [archive_digest] and entry["sha256"] == archive_digest,
            "Archive SHA-256 disagrees with release manifest or checksums.txt")
    destination.mkdir()
    with tarfile.open(archive, "r:gz") as bundle:
        files = bundle.getmembers()
        expected = {"carbon-panel", "carbon-storage", "config.example.yaml", "README.md",
                    "BUILD-INFO.json", "LICENSE", "LICENSE.MPL-2.0"}
        require(len(files) == len(expected) and {member.name for member in files} == expected
                and all(member.isfile() for member in files), "Unexpected archive members")
        bundle.extractall(destination, members=files, filter="data")
    build = json.loads((destination / "BUILD-INFO.json").read_text(encoding="utf-8"))
    require((build["version"], build["commit"], build["os"], build["arch"])
            == (manifest["version"], commit, "linux", arch), "BUILD-INFO.json disagrees with release manifest")
    version = build.get("semver", build["version"].removeprefix("v"))
    receipt = {
        "archive": archive_name,
        "archive_sha256": archive_digest,
        "checksums_sha256": rehearsal.sha256_file(checksums_path),
        "manifest_sha256": rehearsal.sha256_file(manifest_path),
        "commit": commit,
        "version": version,
        "binaries": {},
    }
    for program in ("carbon-panel", "carbon-storage"):
        binary = destination / program
        reported = run(str(binary), "--version")
        require(reported.startswith(f"{program} {version} ") and f"commit {commit}" in reported,
                f"{program} --version disagrees with the verified package")
        receipt["binaries"][program] = {"sha256": rehearsal.sha256_file(binary), "version": reported}
    return receipt


def available_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def rpc(base, method, payload=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = urllib.request.Request(
        f"{base}/carbonpanel.v1.{method}", json.dumps(payload or {}).encode(), headers, method="POST"
    )
    with urllib.request.urlopen(request, timeout=10) as reply:
        return json.load(reply)


def bootstrap(base, account):
    status = rpc(base, "AuthService/GetAuthStatus")
    require(status.get("firstUserSetup") is True, "Fresh install did not require initial setup")
    user = rpc(base, "AuthService/Register", account)["user"]
    require(user["username"] == account["username"], "Registration did not create the requested account")
    return user["id"]


def login_and_list(base, account, expected_id):
    credentials = {field: account[field] for field in ("username", "password")}
    token = rpc(base, "AuthService/Login", credentials)["token"]
    user = rpc(base, "AuthService/GetCurrentUser", token=token)["user"]
    require(user["id"] == expected_id and user["username"] == account["username"],
            "Panel returned a different account after restart or upgrade")
    inventory = rpc(base, "ServerService/ListServers", token=token)
    require(not inventory.get("servers"), "Disposable fixture unexpectedly contains server records")


def prepare_install(directory, network, port):
    config = {
        "server": {"host": "127.0.0.1", "port": str(port)},
        "docker": {"host": "unix:///var/run/docker.sock", "network_name": network,
                   "reconcile_purge_orphans": False},
        "database": {"path": "./data/carbon-panel.db", "panel_backup": "off"},
        "storage": {"data_dir": "./data", "backup_dir": "./backups", "temp_dir": "./tmp"},
        "auth": {"local": {"enabled": True, "allow_registration": False},
                 "anonymous_access": False},
        "proxy": {"enabled": False},
    }
    # JSON is a valid YAML document and preserves all scalar types.
    (directory / "config.yaml").write_text(json.dumps(config), encoding="utf-8")


def wait_for_service(unit, base, expected_binary_sha):
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"{base}/readyz", timeout=2) as response:
                if response.status == 200:
                    break
        except OSError:
            time.sleep(0.5)
    else:
        raise RuntimeError(f"{unit} never reached HTTP readiness")
    with urllib.request.urlopen(f"{base}/", timeout=5) as response:
        require(response.status == 200 and response.headers.get_content_type() == "text/html",
                "Packaged UI did not serve HTML")
    pid = int(run("systemctl", "show", "--property=MainPID", "--value", unit))
    require(pid > 0 and rehearsal.sha256_file(Path(f"/proc/{pid}/exe")) == expected_binary_sha,
            "The active systemd process is not running the verified package binary")
    require(run("systemctl", "is-active", unit) == "active", "Systemd service is inactive")
    return pid


@contextlib.contextmanager
def service(directory, unit, log_file):
    command = (
        "sudo", "-n", "systemd-run", "--collect", "--quiet", f"--unit={unit}",
        f"--uid={getpass.getuser()}", "--property=Type=exec", "--property=TimeoutStopSec=20s",
        f"--property=WorkingDirectory={directory}", str(directory / "carbon-panel"),
    )
    subprocess.run((*command, "--config", "config.yaml"), check=True, timeout=30)
    try:
        yield
    finally:
        subprocess.run(("sudo", "-n", "systemctl", "stop", unit), check=True, timeout=30)
        journal = run("sudo", "-n", "journalctl", "--unit", unit, "--no-pager", "--output=short-iso")
        log_file.write_text(journal + "\n", encoding="utf-8")


def exercise(directory, other_package, name, expected, candidate, evidence, checks):
    unit_prefix = f"carbon-panel-qualify-{name}-{secrets.token_hex(5)}"
    network = unit_prefix
    port = available_port()
    base = f"http://127.0.0.1:{port}"
    account = {"username": "qualification-admin", "email": "qualification@example.test",
               "password": f"HostedFixture!{secrets.token_hex(16)}"}
    prepare_install(directory, network, port)
    db_path = directory / "data" / "carbon-panel.db"
    canaries = [
        directory / "config.yaml",
        directory / "data" / "servers" / "qualification" / "world" / "level.dat",
        directory / "backups" / "disposable-backup-marker",
    ]
    for canary in canaries[1:]:
        canary.parent.mkdir(parents=True, exist_ok=True)
        canary.write_bytes(secrets.token_bytes(64))
    canary_hashes = [rehearsal.sha256_file(path) for path in canaries]
    first_digest = expected["binaries"]["carbon-panel"]["sha256"]
    candidate_digest = candidate["binaries"]["carbon-panel"]["sha256"]

    try:
        first_unit = f"{unit_prefix}-initial.service"
        with service(directory, first_unit, evidence / f"{name}-initial.journal"):
            wait_for_service(first_unit, base, first_digest)
            user_id = bootstrap(base, account)
            login_and_list(base, account, user_id)
            checks[f"{name}_initial_service"] = "ready, embedded UI, first admin and inventory"
        first_tables = rehearsal.tables(db_path)
        require(first_tables and rehearsal.integrity(db_path) == "ok", "Initial SQLite integrity check failed")
        if other_package is not None:
            snapshot = directory.parent / "before-upgrade.db"
            rehearsal.back_up_database(db_path, snapshot)
            require(rehearsal.integrity(snapshot) == "ok" and rehearsal.tables(snapshot) == first_tables,
                    "Pre-upgrade SQLite backup did not match baseline schema")
            shutil.copyfile(canaries[0], directory.parent / "before-upgrade-config.yaml")
            checks["upgrade_preflight"] = {
                "db_backup_sha256": rehearsal.sha256_file(snapshot),
                "config_backup_sha256": rehearsal.sha256_file(directory.parent / "before-upgrade-config.yaml"),
                "schema_tables": len(first_tables),
            }
            for program in candidate["binaries"]:
                shutil.copy2(other_package / program, directory / program)
                require(rehearsal.sha256_file(directory / program) == candidate["binaries"][program]["sha256"],
                        f"Replacement {program} digest does not match the staged package")
        running_unit = f"{unit_prefix}-candidate.service"
        with service(directory, running_unit, evidence / f"{name}-candidate.journal"):
            pid = wait_for_service(running_unit, base, candidate_digest)
            login_and_list(base, account, user_id)
            run("sudo", "-n", "systemctl", "restart", running_unit)
            restarted = wait_for_service(running_unit, base, candidate_digest)
            require(pid != restarted, "Systemd restart did not start a new process")
            login_and_list(base, account, user_id)
            checks[f"{name}_restart"] = "new systemd process, same admin, ready and serving embedded UI"
        final_tables = rehearsal.tables(db_path)
        require(first_tables <= final_tables and rehearsal.integrity(db_path) == "ok",
                "Candidate data directory failed additive schema or integrity checks")
        require([rehearsal.sha256_file(path) for path in canaries] == canary_hashes,
                "Configuration, world or backup bytes changed")
        checks[f"{name}_persistent_state"] = {
            "schema_tables": len(final_tables),
            "canary_sha256": {str(path.relative_to(directory)): digest
                              for path, digest in zip(canaries, canary_hashes)},
        }
    finally:
        network_result = subprocess.run(("docker", "network", "inspect", network), capture_output=True, timeout=10)
        if network_result.returncode == 0:
            run("docker", "network", "rm", network)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--candidate-commit", required=True)
    parser.add_argument("--baseline-commit", required=True)
    parser.add_argument("--release-run", required=True)
    parser.add_argument("--actions-artifact-id", required=True)
    parser.add_argument("--actions-artifact-digest", required=True)
    parser.add_argument("--baseline-tag", required=True)
    args = parser.parse_args()
    require(platform.system() == "Linux" and os.environ.get("GITHUB_ACTIONS") == "true"
            and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted",
            "This test needs a disposable GitHub-hosted Linux runner")
    require(Path("/proc/1/comm").read_text(encoding="utf-8").strip() == "systemd", "Systemd must be PID 1")
    workspace = args.workspace.resolve()
    require(workspace.is_relative_to(Path(os.environ["RUNNER_TEMP"]).resolve()), "Use a runner temp workspace")
    workspace.mkdir()
    evidence = workspace / "evidence"
    evidence.mkdir()
    arch = {"x86_64": "amd64", "aarch64": "arm64"}[platform.machine()]
    report = {
        "qualifier_commit": os.environ["GITHUB_SHA"],
        "qualifier_run": os.environ["GITHUB_RUN_ID"],
        "candidate_release_run": args.release_run,
        "candidate_actions_artifact_id": args.actions_artifact_id,
        "candidate_actions_artifact_digest": args.actions_artifact_digest,
        "baseline_release_tag": args.baseline_tag,
        "platform": f"linux/{arch}",
        "docker": run("docker", "version", "--format", "{{.Server.Version}}"),
        "systemd": run("systemctl", "--version").splitlines()[0],
        "checks": {},
        "pass": False,
    }
    try:
        fresh = workspace / "fresh-install"
        upgrade = workspace / "upgrade-install"
        candidate = verify_bundle(args.candidate, fresh, args.candidate_commit, arch)
        report["candidate"] = candidate
        baseline = verify_bundle(args.baseline, upgrade, args.baseline_commit, arch)
        require(f"v{baseline['version']}" == args.baseline_tag, "Baseline bundle is not the requested release")
        require(baseline["commit"] != candidate["commit"], "Baseline and candidate must come from distinct commits")
        report["baseline"] = baseline
        exercise(fresh, None, "fresh", candidate, candidate, evidence, report["checks"])
        exercise(upgrade, fresh, "upgrade", baseline, candidate, evidence, report["checks"])
        report["pass"] = True
    except (OSError, RuntimeError, subprocess.SubprocessError, ValueError, KeyError) as error:
        report["error"] = str(error)
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr)
        raise
    finally:
        receipt = json.dumps(report, indent=2) + "\n"
        (evidence / "report.json").write_text(receipt, encoding="utf-8")
        print(receipt)


if __name__ == "__main__":
    main()
