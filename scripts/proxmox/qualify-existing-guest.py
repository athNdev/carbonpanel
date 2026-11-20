# SPDX-License-Identifier: MPL-2.0
"""Exercise the controller installer on a disposable hosted Linux guest."""

import argparse
import hashlib
import json
import os
import platform
import re
import secrets
import socket
import sqlite3
import subprocess
import tempfile
import time
from contextlib import closing
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen


def run(*args, check=True):
    result = subprocess.run(args, text=True, capture_output=True, timeout=180)
    if check and result.returncode:
        raise RuntimeError(f"{args[0]} exited {result.returncode}: {result.stderr.strip()}")
    return result


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def auth_rpc(method, payload=None, token=None):
    headers = {"Content-Type": "application/json", "Connect-Protocol-Version": "1"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    request = Request(
        f"http://127.0.0.1:8080/carbonpanel.v1.AuthService/{method}",
        json.dumps(payload or {}).encode(),
        headers,
    )
    with urlopen(request, timeout=10) as response:
        return json.load(response)


def wait_for_controller():
    deadline = time.monotonic() + 75
    while True:
        try:
            return auth_rpc("GetAuthStatus")
        except (URLError, TimeoutError):
            if time.monotonic() >= deadline:
                raise
            time.sleep(1)


def identity(credentials):
    token = auth_rpc("Login", credentials)["token"]
    user = auth_rpc("GetCurrentUser", token=token)["user"]
    if user["username"] != credentials["username"] or "admin" not in user["roles"]:
        raise RuntimeError("controller did not return the established local administrator")
    return user["id"]


def sqlite_integrity(database):
    with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as connection:
        return connection.execute("PRAGMA integrity_check").fetchall() == [("ok",)]


def installed_container(compose, directory, reference, revision, configured=False):
    container = run(*compose, "ps", "-q", "carbon-panel").stdout.strip()
    record, = json.loads(run("docker", "inspect", container).stdout)
    labels = record["Config"]["Labels"]
    if record["Config"]["Image"] != reference or labels.get("org.opencontainers.image.revision") != revision:
        raise RuntimeError("running controller does not match the requested image pin and revision")
    if record["HostConfig"]["Privileged"] or record["HostConfig"]["CapAdd"]:
        raise RuntimeError("installer broadened controller container privileges")
    mounts = {mount["Destination"]: mount for mount in record["Mounts"]}
    expected = {f"/app/{name}": str(directory / name) for name in ("data", "backups", "tmp")}
    expected["/var/run/docker.sock"] = "/var/run/docker.sock"
    if configured:
        expected["/app/config.yaml"] = str(directory / "config.yaml")
    for destination, source in expected.items():
        mount = mounts[destination]
        if mount["Type"] != "bind" or mount["Source"] != source:
            raise RuntimeError(f"unexpected bind mount for {destination}")
    if configured and mounts["/app/config.yaml"]["RW"]:
        raise RuntimeError("operator config mount must remain read-only")
    return {"container": container, "image_id": record["Image"], "ref": reference, "revision": revision}


def qualify(args, directory, report):
    installer = args.product / "scripts/proxmox/install-carbon-panel.sh"
    snapshot_script = args.product / "scripts/snapshot-panel-db.sh"
    compose_file = directory / "compose.yml"
    compose = ("docker", "compose", "-f", str(compose_file))
    invoke = ("bash", str(installer))
    checks = report["checks"]

    def record(name, passed):
        checks[name] = bool(passed)
        if not passed:
            raise RuntimeError(f"installer qualification failed: {name}")

    try:
        run(*invoke, "install", "--dir", str(directory), "--image", args.baseline_image)
        baseline = installed_container(compose, directory, args.baseline_image, args.baseline_commit)
        report["baseline"] = baseline
        record("clean_install", wait_for_controller().get("firstUserSetup") is True)
        credentials = {"username": "qualification-admin", "password": secrets.token_urlsafe(24) + "!Aa1"}
        auth_rpc("Register", {**credentials, "email": "admin@example.invalid"})
        administrator = identity(credentials)
        initial_compose = compose_file.read_text(encoding="utf-8")
        run(*invoke, "install", "--dir", str(directory), "--image", args.candidate_image)
        record("second_install_preserves_controller_and_compose",
               installed_container(compose, directory, args.baseline_image, args.baseline_commit) == baseline
               and compose_file.read_text(encoding="utf-8") == initial_compose)

        fixtures = {
            "data/servers/qualification/world/level.dat": "disposable world fixture",
            "backups/qualification.tar.zst": "disposable backup fixture",
            "tmp/qualification.txt": "disposable scratch fixture",
            "config.yaml": 'server:\n  host: "127.0.0.1"\n  port: "8080"\n',
        }
        for relative, contents in fixtures.items():
            path = directory / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(contents, encoding="utf-8")
        original = {relative: digest(directory / relative) for relative in fixtures}
        report["fixture_hashes"] = original

        # Operator customizations established before invoking the updater.
        configured_compose = initial_compose.replace(
            "# - ./config.yaml:/app/config.yaml:ro", "- ./config.yaml:/app/config.yaml:ro", 1
        ) + (
            f'  qualification-sentinel:\n    image: "{args.baseline_image}"\n'
            '    entrypoint: ["/bin/sh", "-c", "sleep 1800"]\n'
        )
        compose_file.write_text(configured_compose, encoding="utf-8")
        run(*compose, "up", "-d", "--no-deps", "qualification-sentinel")
        sentinel = run(*compose, "ps", "-q", "qualification-sentinel").stdout.strip()
        record("unrelated_service_started", sentinel)

        run("bash", str(snapshot_script), "--data-dir", str(directory / "data"))
        snapshot, = directory.glob("carbon-panel-db-*.sqlite")
        record("documented_online_snapshot_integrity", sqlite_integrity(snapshot))
        snapshot_hash = digest(snapshot)
        report["snapshot_sha256"] = snapshot_hash

        def set_pin(image):
            original_line = f'    image: "{args.baseline_image}"'
            compose_file.write_text(configured_compose.replace(original_line, f'    image: "{image}"', 1),
                                    encoding="utf-8")
            return digest(compose_file)

        missing_image = args.candidate_image.split("@")[0] + "@sha256:" + "0" * 64
        failed_config = set_pin(missing_image)
        failed_update = run(*invoke, "update", "--dir", str(directory), check=False)
        record("missing_image_refused_with_old_controller_running",
               failed_update.returncode != 0
               and "update pull failed" in failed_update.stderr
               and installed_container(compose, directory, args.baseline_image, args.baseline_commit) == baseline
               and identity(credentials) == administrator
               and digest(compose_file) == failed_config)

        updated_config = set_pin(args.candidate_image)
        run(*invoke, "update", "--dir", str(directory))
        candidate = installed_container(compose, directory, args.candidate_image, args.candidate_commit, configured=True)
        report["candidate"] = candidate
        wait_for_controller()
        record("candidate_recreated_and_original_login_survived",
               candidate["container"] != baseline["container"]
               and candidate["image_id"] != baseline["image_id"]
               and identity(credentials) == administrator)
        record("unrelated_service_not_recreated",
               run(*compose, "ps", "-q", "qualification-sentinel").stdout.strip() == sentinel)
        record("compose_config_and_persistent_files_preserved",
               digest(compose_file) == updated_config
               and digest(snapshot) == snapshot_hash
               and {relative: digest(directory / relative) for relative in fixtures} == original)
        record("upgraded_sqlite_integrity", sqlite_integrity(directory / "data/carbon-panel.db"))
    finally:
        if compose_file.is_file():
            run(*compose, "down")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--product", type=Path, required=True)
    parser.add_argument("--baseline-image", required=True)
    parser.add_argument("--baseline-commit", required=True)
    parser.add_argument("--candidate-image", required=True)
    parser.add_argument("--candidate-commit", required=True)
    args = parser.parse_args()
    if (os.environ.get("GITHUB_ACTIONS"), os.environ.get("RUNNER_ENVIRONMENT"), os.environ.get("RUNNER_OS")) != (
        "true", "github-hosted", "Linux"
    ):
        parser.error("provide a disposable GitHub-hosted Linux runner")
    if not all(re.fullmatch(r"[0-9a-f]{40}", commit) for commit in (args.baseline_commit, args.candidate_commit)):
        parser.error("both full product commits are required")
    if not all(re.fullmatch(r"[^@\s]+@sha256:[0-9a-f]{64}", image)
               for image in (args.baseline_image, args.candidate_image)):
        parser.error("both controller images must be pinned by digest")
    if not re.fullmatch(r"(localhost|127\.0\.0\.1):[0-9]+/.+@sha256:[0-9a-f]{64}", args.candidate_image):
        parser.error("the assembled candidate must be served by a runner-local registry")
    if run("git", "-C", str(args.product), "rev-parse", "HEAD").stdout.strip() != args.candidate_commit:
        parser.error("installer checkout must match the candidate product commit")

    runner = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
    if run("docker", "ps", "-aq", "--filter", "name=^/carbon-panel$").stdout.strip():
        raise RuntimeError("controller container name already in use")
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 8080))
    report = {
        "test": "existing-linux-guest-installer",
        "installer_commit": args.candidate_commit,
        "installer_sha256": digest(args.product / "scripts/proxmox/install-carbon-panel.sh"),
        "harness_commit": run("git", "rev-parse", "HEAD").stdout.strip(),
        "kernel": platform.release(),
        "docker_version": run("docker", "version", "--format", "{{.Server.Version}}").stdout.strip(),
        "compose_version": run("docker", "compose", "version", "--short").stdout.strip(),
        "checks": {},
        "pass": False,
    }
    try:
        directory = Path(tempfile.mkdtemp(prefix="panel-guest-", dir=runner)) / "installation"
        qualify(args, directory, report)
        report["pass"] = True
    finally:
        evidence = json.dumps(report, indent=2) + "\n"
        (runner / "panel-guest-installer-evidence.json").write_text(evidence, encoding="utf-8")
        print(evidence)


if __name__ == "__main__":
    main()
