#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
# Offline, scoped fixture for the portable Linux node installer (OSS-379).
# Builds the installer once, then exercises the exported CLI surface inside a
# temporary directory. No live host, no service, no network, no global config.
# Also parses the bash wrapper with bashlex and checks its dry-run guard.

import base64
import hashlib
import json
import os
import pathlib
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent
WRAPPER = ROOT / "scripts" / "install-node.sh"
GOCMD = ["go", "build"]
NOW = "2026-11-18T12:00:00Z"

results = []


def check(name, fn):
    try:
        fn()
    except Exception as exc:  # noqa: BLE001 - fixture reports all failures
        import traceback

        detail = str(exc) + "\n" + traceback.format_exc(limit=4)
        results.append((name, False, detail))
    else:
        results.append((name, True, ""))


def run(binary, verb, *flags, expect=0):
    proc = subprocess.run(
        [str(binary), verb, *flags], cwd=ROOT, capture_output=True, text=True
    )
    if proc.returncode != expect:
        raise AssertionError(
            f"{verb} exit {proc.returncode} (want {expect}): {proc.stderr.strip()}"
        )
    return proc


def jout(proc):
    return json.loads(proc.stdout)


def write(path, content):
    path = pathlib.Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def make_artifact(root, version, content):
    art = pathlib.Path(root) / ("artifact-" + version)
    write(art / "bin" / "carbon-node", content)
    digest = hashlib.sha256(content.encode()).hexdigest()
    manifest = {
        "schema": "carbon-panel-node-artifact/v1",
        "name": "carbon-node",
        "version": version,
        "os": "linux",
        "arch": "amd64",
        "files": {"bin/carbon-node": digest},
    }
    write(art / "manifest.json", json.dumps(manifest, indent=2))
    return art


def build_binary(tmp):
    out = pathlib.Path(tmp) / "carbon-node-installer"
    proc = subprocess.run(
        GOCMD + ["-o", str(out), "./cmd/carbon-node-installer"],
        cwd=ROOT,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise AssertionError(f"go build failed: {proc.stderr}")
    return out


def test_wrapper_syntax_and_guard():
    text = WRAPPER.read_text(encoding="utf-8")
    try:
        from tree_sitter import Language, Parser
        import tree_sitter_bash

        parser = Parser(Language(tree_sitter_bash.language()))
        tree = parser.parse(text.encode("utf-8"))

        def has_error(node):
            if node.type == "ERROR" or node.is_missing:
                return True
            return any(has_error(child) for child in node.children)

        assert not has_error(tree.root_node), "tree-sitter found a shell syntax error"
    except ImportError:
        # Fall back to structural checks if no parser is installed.
        assert "#!/usr/bin/env bash" in text
        assert "set -euo pipefail" in text
    forbidden = ["systemctl", "rm -rf /", "chmod 777", " >>/etc/", " >/etc/"]
    for token in forbidden:
        assert token not in text, f"wrapper contains forbidden global action: {token}"
    assert "set -euo pipefail" in text
    assert "--apply" in text and 'APPLY=true' in text
    assert "DRY RUN" in text
    for flag in ("--state-dir", "--data-dir", "--artifact", "--backup-dir"):
        assert flag in text, f"wrapper does not expose {flag}"
    # render-daemon is read-only and must stay in the no-apply allowlist.
    assert "verify | status | render-daemon)" in text
    assert "--daemon-spec" in text
    for global_path in ("/etc/docker", "/var/lib/docker", "/var/run/docker.sock"):
        assert global_path not in text, f"wrapper contains a global host path: {global_path}"


def main():
    with tempfile.TemporaryDirectory(prefix="node-installer-fixture-") as tmp:
        binary = build_binary(tmp)
        state = pathlib.Path(tmp) / "state"
        data = pathlib.Path(tmp) / "data"

        def t_identity_and_bundle():
            one = jout(run(binary, "init", "-state-dir", str(state)))
            two = jout(run(binary, "init", "-state-dir", str(state)))
            assert one["node_id"] == two["node_id"], "identity regenerated"
            other = jout(run(binary, "init", "-state-dir", str(pathlib.Path(tmp) / "state2")))
            assert other["node_id"] != one["node_id"], "identities collide"

            bundle = jout(
                run(
                    binary,
                    "bundle",
                    "-state-dir",
                    str(state),
                    "-label",
                    "node-fixture",
                    "-capabilities",
                    "ROOTLESS,PRIVATE_SOCKET",
                    "-ttl",
                    "10m",
                )
            )
            assert bundle["node_id"] == one["node_id"]
            assert bundle["scope"]["node_id"] == one["node_id"]
            assert bundle["scope"]["capabilities"] == ["ROOTLESS", "PRIVATE_SOCKET"]
            assert bundle["approved_public_key_spki"] == one["approved_public_key_spki"]

            secret = (state / "enrollment.secret").read_text(encoding="utf-8").strip()
            assert len(secret) >= 32
            salt = base64.b64decode(bundle["enrollment_salt"])
            want = hashlib.sha256(salt + secret.encode()).digest()
            assert base64.b64encode(want).decode() == bundle["enrollment_secret_digest"], "digest mismatch"
            for name in ("bundle.json", "create_enrollment_request.json", "identity.json"):
                text = (state / name).read_text(encoding="utf-8")
                assert secret not in text, f"{name} leaked the secret"
                assert "PRIVATE KEY" not in text, f"{name} leaked a private key"
            create_req = json.loads((state / "create_enrollment_request.json").read_text())
            for field in (
                "nodeId",
                "approvedPublicKeySpki",
                "approvedKeyFingerprint",
                "enrollmentSecretDigest",
                "enrollmentSalt",
                "csrPre",
            ):
                assert field in create_req, f"shared proto field missing: {field}"
            return bundle

        bundle = {}

        def t_claim():
            challenge = {
                "challenge_id": "chal-fixture",
                "server_nonce": base64.b64encode(b"0123456789abcdef0123456789abcdef").decode(),
                "expires_at": "2026-11-18T12:05:00Z",
                "node_id": bundle["node_id"],
                "enrollment_id": "enr-fixture",
                "channel_context": base64.b64encode(b"server-derived-channel").decode(),
            }
            chfile = pathlib.Path(tmp) / "challenge.json"
            write(chfile, json.dumps(challenge))
            proc = run(
                binary,
                "claim",
                "-state-dir",
                str(state),
                "-challenge",
                str(chfile),
                "-now",
                NOW,
            )
            claim = jout(proc)
            assert claim["enrollmentToken"] == (state / "enrollment.secret").read_text().strip()
            assert claim["challengeId"] == "chal-fixture"
            assert claim["freshClaimSignature"], "missing fresh claim signature"
            replay = run(
                binary,
                "claim",
                "-state-dir",
                str(state),
                "-challenge",
                str(chfile),
                "-now",
                NOW,
                expect=1,
            )
            assert "already consumed" in replay.stderr, replay.stderr

        def t_artifact_upgrade_rollback():
            write(data / "world" / "level.dat", "precious-world")
            a1 = make_artifact(tmp, "1.0.0", "binary-v1")
            a2 = make_artifact(tmp, "2.0.0", "binary-v2")
            run(
                binary,
                "verify",
                "-artifact",
                str(a1),
                "-target-os",
                "linux",
                "-target-arch",
                "amd64",
            )
            write(a1 / "bin" / "carbon-node", "tampered")
            bad = run(
                binary,
                "verify",
                "-artifact",
                str(a1),
                "-target-os",
                "linux",
                "-target-arch",
                "amd64",
                expect=1,
            )
            assert "integrity" in bad.stderr.lower(), bad.stderr
            write(a1 / "bin" / "carbon-node", "binary-v1")

            up = jout(
                run(
                    binary,
                    "upgrade",
                    "-state-dir",
                    str(state),
                    "-artifact",
                    str(a1),
                    "-target-os",
                    "linux",
                    "-target-arch",
                    "amd64",
                )
            )
            assert up["active_version"] == "1.0.0"
            up2 = jout(
                run(
                    binary,
                    "upgrade",
                    "-state-dir",
                    str(state),
                    "-artifact",
                    str(a2),
                    "-target-os",
                    "linux",
                    "-target-arch",
                    "amd64",
                )
            )
            assert up2["active_version"] == "2.0.0"
            rb = jout(run(binary, "rollback", "-state-dir", str(state)))
            assert rb["active_version"] == "1.0.0"
            assert (data / "world" / "level.dat").read_text() == "precious-world"

        def t_volume():
            binding = jout(
                run(binary, "import", "-state-dir", str(state), "-data-dir", str(data))
            )
            assert binding["file_count"] >= 1 and binding["volume_id"]
            backup = pathlib.Path(tmp) / "snapshot"
            run(binary, "backup", "-data-dir", str(data), "-backup-dir", str(backup))
            again = run(
                binary,
                "backup",
                "-data-dir",
                str(data),
                "-backup-dir",
                str(backup),
                expect=1,
            )
            assert "already exists" in again.stderr
            write(data / "world" / "level.dat", "damaged")
            write(data / "keep-me.txt", "unrelated")
            run(
                binary,
                "recover",
                "-data-dir",
                str(data),
                "-backup-dir",
                str(backup),
            )
            assert (data / "world" / "level.dat").read_text() == "precious-world"
            assert (data / "keep-me.txt").exists(), "recovery deleted unrelated data"
            write(backup / "world" / "level.dat", "tampered")
            bad = run(
                binary,
                "recover",
                "-data-dir",
                str(data),
                "-backup-dir",
                str(backup),
                expect=1,
            )
            assert "integrity" in bad.stderr.lower()
            assert (data / "world" / "level.dat").read_text() == "precious-world"

        def t_daemon_render():
            def spec(mode, socket, uid):
                return {
                    "node_id": "node-daemon-cli",
                    "mode": mode,
                    "config_path": "/srv/carbon-node/state/daemon.json",
                    "socket_path": socket,
                    "data_root": "/srv/carbon-node/state/docker/data",
                    "exec_root": "/srv/carbon-node/state/docker/exec",
                    "containerd_namespace": "carbon-node-cli",
                    "bridge_cidr": "10.231.0.1/24",
                    "port_range": "30000-31000",
                    "storage_driver": "overlay2",
                    "runtime_uid": uid,
                }

            for mode, socket, uid in (
                ("rootless", "/run/user/1000/carbon-node/docker.sock", 1000),
                ("rootful", "/run/carbon-node/docker.sock", 0),
            ):
                specfile = pathlib.Path(tmp) / ("daemon-" + mode + ".json")
                write(specfile, json.dumps(spec(mode, socket, uid)))
                rendered = jout(run(binary, "render-daemon", "-daemon-spec", str(specfile)))
                assert rendered["mode"] == mode
                unit, config = rendered["unit"], rendered["config"]
                cfg = json.loads(config)
                # One source of options: config keys appear exactly once, and
                # the unit repeats no option flag (only --config-file).
                assert len(cfg) == len(set(cfg))
                for key in ("hosts", "data-root", "exec-root", "storage-driver"):
                    assert config.count('"' + key + '"') == 1, key
                assert config.count('"no-new-privileges"') == 1
                assert "--config-file=/srv/carbon-node/state/daemon.json" in unit
                for flag in ("--host=", "--data-root=", "--storage-driver=", "--bip="):
                    assert flag not in unit, f"unit duplicates {flag}"
                assert unit.count("ExecStart=") == 1
                if mode == "rootless":
                    assert "WantedBy=default.target" in unit
                    assert "ExecStart=/usr/bin/dockerd-rootless.sh" in unit
                    assert "XDG_RUNTIME_DIR=/run/user/1000" in unit
                    assert "\nUser=" not in unit
                    assert "systemctl --user" in rendered["install_hint"]
                else:
                    assert "User=root" in unit and "Group=root" in unit
                    assert "CapabilityBoundingSet=CAP_SYS_ADMIN" in unit
                    assert "AmbientCapabilities=CAP_SYS_ADMIN" in unit
                    assert "WantedBy=multi-user.target" in unit
                    assert "CapabilityBoundingSet=\n" not in unit
            # Specs that target global config or public sockets are refused.
            bad = spec("rootless", "/run/user/1000/carbon-node/docker.sock", 1000)
            bad["config_path"] = "/etc/docker/daemon.json"
            badfile = pathlib.Path(tmp) / "daemon-bad.json"
            write(badfile, json.dumps(bad))
            ref = run(binary, "render-daemon", "-daemon-spec", str(badfile), expect=1)
            assert "caller-owned" in ref.stderr or "global host config" in ref.stderr, ref.stderr

        check("wrapper syntax and dry-run guard", test_wrapper_syntax_and_guard)

        # Bundle must be built before the claim test consumes the secret.
        def t_bundle_only():
            bundle.update(t_identity_and_bundle())

        check("first-boot identity and secret-free bundle", t_bundle_only)
        check("challenge-bound claim and replay refusal", t_claim)
        check("artifact verify / upgrade / rollback", t_artifact_upgrade_rollback)
        check("persistent volume import / backup / recover", t_volume)
        check("daemon one-source options, per-user rootless, rootful capabilities", t_daemon_render)

    failed = [r for r in results if not r[1]]
    for name, ok, detail in results:
        print(("PASS  " if ok else "FAIL  ") + name + ("" if ok else " :: " + detail))
    print(f"{len(results) - len(failed)}/{len(results)} offline installer fixtures passed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
