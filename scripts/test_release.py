# SPDX-License-Identifier: MPL-2.0
"""Release-boundary checks: version validation, complete inputs and integrity."""
import argparse
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import re
import shlex
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts import release


def registry_writes(command):
    """Only calls that would mutate a registry or publish a release."""
    return [call for call in command.call_args_list
            if any(token in call.args[0] for token in ("create", "--push", "publish"))]


def platform_revisions(commit):
    """Digest-addressed platform config evidence for offline fixtures."""
    return {
        ("linux", "amd64"): {"digest": "sha256:" + "a" * 64, "revision": commit},
        ("linux", "arm64"): {"digest": "sha256:" + "b" * 64, "revision": commit},
    }


def real_docker_inspect_runner(index_digest, children):
    """Realistic `imagetools inspect` output: index descriptors have null
    revision annotations; the revision label lives in each child config.
    """
    index = {
        "schemaVersion": 2,
        "mediaType": "application/vnd.oci.image.index.v1+json",
        "digest": index_digest,
        "manifests": [
            {"mediaType": "application/vnd.oci.image.manifest.v1+json",
             "digest": data["digest"],
             "annotations": {"org.opencontainers.image.revision": None},
             "platform": {"architecture": arch, "os": "linux"}}
            for (_, arch), data in children.items()
        ],
    }
    def runner(*args):
        reference, fmt = args[4], args[6]
        if fmt == "{{json .Manifest}}":
            return json.dumps(index)
        child = reference.rsplit("@", 1)[1]
        for (os_name, arch), data in children.items():
            if data["digest"] == child:
                return json.dumps({
                    "architecture": arch, "os": os_name,
                    "config": {"Labels": {
                        "org.opencontainers.image.revision": data["revision"]}},
                })
        raise AssertionError("unexpected child config request: " + child)
    return runner


def present(digest=None, platforms=None):
    """Registry fixture: a present, fully inspected multi-arch reference."""
    return ("present", {
        "digest": digest or "sha256:" + "a" * 64,
        "platforms": set(platforms) if platforms is not None
        else {("linux", "amd64"), ("linux", "arm64")},
    })


class ReleaseTests(unittest.TestCase):
    def test_unpublished_draft_is_read_by_its_stable_id(self):
        api_url = "https://api.github.com/repos/owner/panel/releases/123"
        def reply(*args):
            if args == ("release", "view", "v3.0.0-alpha.1", "--json", "apiUrl", "--jq", ".apiUrl"):
                return api_url
            if args == ("api", api_url):
                return json.dumps({"id": 123, "draft": True, "assets": []})
            raise AssertionError(f"Unsupported draft lookup: {args}")
        with patch.object(release, "github", side_effect=reply):
            self.assertTrue(release.get_release("v3.0.0-alpha.1")["draft"])

    def test_cd_concurrency_belongs_to_the_gated_publish_job(self):
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertNotRegex(source, r"(?m)^concurrency:")
        self.assertRegex(source, r"(?m)^    concurrency:\n(?:      #.*\n)*      group:")
        self.assertIn("github.event.workflow_run.event == 'push'", source)
        self.assertIn("github.event.workflow_run.conclusion == 'success'", source)

    def test_github_package_namespace_matches_the_repository(self):
        self.assertEqual(release.github_image("v3.0.0-alpha.1", "Owner/Fork"),
                         "ghcr.io/owner/fork:3.0.0-alpha.1")

    def test_version_accepts_stable_and_prerelease_tags_but_not_shell_or_path_input(self):
        for tag in ("v3.0.0", "v3.0.0-alpha.1", "v3.0.0-beta.5", "v3.0.0-rc.1", "v2.1.15"):
            self.assertEqual(release.version(tag), tag)
        for tag in ("main", "3.0.0", "v03.0.0", "v3.0.0-beta.05", "v3.0.0\n",
                    "v3.0.0/../../main", "v3.0.0;echo", "v3.0.0-", "v3.٠.0"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                release.version(tag)

    def test_archives_are_reproducible_and_preserve_binary_bytes(self):
        members = [("carbon-panel", b"binary\x00bytes", 0o755), ("LICENSE", b"license", 0o644)]
        with tempfile.TemporaryDirectory() as directory:
            for extension in (".zip", ".tar.gz"):
                first, second = Path(directory) / ("one" + extension), Path(directory) / ("two" + extension)
                release.write_archive(first, members, 1700000000)
                release.write_archive(second, list(reversed(members)), 1700000000)
                self.assertEqual(first.read_bytes(), second.read_bytes())
                self.assertEqual(release.read_archive(first), {name: data for name, data, _ in members})

    def test_archive_symlinks_and_traversal_are_refused(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "bundle.tar.gz"
            with tarfile.open(path, "w:gz") as archive:
                item = tarfile.TarInfo("carbon-panel")
                item.type, item.linkname = tarfile.SYMTYPE, "/etc/passwd"
                archive.addfile(item)
            with self.assertRaises(ValueError):
                release.read_archive(path)
            release.write_archive(path, [("../carbon-panel", b"data", 0o755)], 1700000000)
            with self.assertRaises(ValueError):
                release.read_archive(path)

    def test_publication_fails_closed_when_exact_commit_ci_is_missing_or_failed(self):
        for runs in ([], [{"conclusion": "failure", "status": "completed"}],
                     [{"conclusion": "", "status": "in_progress"}]):
            with self.subTest(runs=runs), patch.object(release.subprocess, "run"), \
                 patch.object(release, "github", return_value=json.dumps(runs)), self.assertRaises(ValueError):
                release.guard_publishing("v3.0.0-beta.5", "a" * 40)

    def test_published_version_cannot_be_overwritten(self):
        replies = [json.dumps([{"conclusion": "success", "status": "completed"}]),
                   json.dumps([[{"tag_name": "v3.0.0-beta.5", "draft": False}]])]
        with patch.object(release.subprocess, "run"), patch.object(release, "run", return_value=""), \
             patch.object(release, "github", side_effect=replies), self.assertRaisesRegex(ValueError, "immutable"):
            release.guard_publishing("v3.0.0-beta.5", "a" * 40)

    def test_collection_refuses_bundle_from_another_commit(self):
        with tempfile.TemporaryDirectory() as directory:
            source, output = Path(directory) / "inputs", Path(directory) / "release"
            source.mkdir()
            info = {"version": "v3.0.0-beta.5", "commit": "wrong", "os": "linux", "arch": "amd64"}
            release.write_archive(source / release.archive_name(info["version"], "linux", "amd64"),
                                  [("BUILD-INFO.json", json.dumps(info).encode(), 0o644)], 1700000000)
            with self.assertRaisesRegex(ValueError, "Mismatched"):
                release.collect(argparse.Namespace(version=info["version"], commit="a" * 40, source=str(source), output=str(output)))

    def test_missing_arm_image_prevents_release_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "manifest.json"
            manifest = {"manifests": [{"platform": {"os": "linux", "architecture": "amd64"}}]}
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaises(ValueError):
                release.verify_image(argparse.Namespace(manifest=str(path)))
            manifest["manifests"].append({"platform": {"os": "linux", "architecture": "arm64"}})
            manifest["manifests"].append({"platform": {"os": "unknown", "architecture": "unknown"}})
            path.write_text(json.dumps(manifest), encoding="utf-8")
            release.verify_image(argparse.Namespace(manifest=str(path)))

    def test_tampered_download_is_rejected_before_a_tag_or_release_is_written(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            tag, commit = "v3.0.0-beta.5", "a" * 40
            manifest = {"version": tag, "commit": commit, "image_digest": "sha256:" + "b" * 64}
            (output / "release-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            (output / "openapi.yaml").write_text("schema", encoding="utf-8")
            for target in release.TARGETS:
                (output / release.archive_name(tag, *target)).write_bytes(b"original")
            release.finalize(argparse.Namespace(output=str(output), digest=None))
            (output / release.archive_name(tag, "linux", "amd64")).write_bytes(b"changed")
            with patch.object(release, "guard_publishing"), patch.object(release, "github") as github, \
                 self.assertRaisesRegex(ValueError, "checksums"):
                release.publish(argparse.Namespace(version=tag, commit=commit, output=str(output)))
            github.assert_not_called()

    # --- Release/tag policy: deterministic, fixture-only boundary checks -----

    def test_publication_gate_allows_only_explicit_dispatch_opt_in(self):
        # Every automatic event stages only: branch push, version tag push,
        # pull request, workflow_run and schedule. A normal merge is a push.
        for event in ("push", "pull_request", "workflow_run", "schedule", "create"):
            with self.subTest(event=event):
                self.assertFalse(release.publication_allowed(event, "true", "v3.0.0")[0])
        # A dispatch without the affirmative flag is still a rehearsal.
        self.assertFalse(release.publication_allowed("workflow_dispatch", "false", "v3.0.0")[0])
        allowed, reason = release.publication_allowed("workflow_dispatch", "true", "v3.0.0")
        self.assertTrue(allowed)
        self.assertIn("explicit", reason)

    def test_verification_and_documentation_tags_are_never_publishable(self):
        for tag in ("v0.0.0-verify.5", "v3.0.0-test.1", "v3.0.0-docs.2", "v3.0.0-dryrun"):
            with self.subTest(tag=tag):
                self.assertTrue(release.is_verification_tag(tag))
                self.assertFalse(release.publication_allowed("workflow_dispatch", "true", tag)[0])
                with self.assertRaises(ValueError):
                    release.guard_publishing(tag, "a" * 40)
        self.assertEqual(release.ref_channel("v0.0.0-verify.5"), "verification")
        self.assertEqual(release.ref_channel("docs1.0.0"), "documentation")
        self.assertFalse(release.is_verification_tag("v3.0.0-alpha.1"))

    def test_release_domains_and_module_versions(self):
        self.assertEqual(release.ref_channel("v3.0.0"), "stable")
        self.assertEqual(release.ref_channel("v3.0.0-rc.1"), "prerelease")
        self.assertEqual(release.ref_channel("mod-v2.0.6"), "module")
        self.assertEqual(release.ref_channel("main", "branch"), "development")
        self.assertEqual(release.module_version("mod-v2.0.6"), "mod-v2.0.6")
        for value in ("v2.0.6", "mod-2.0.6", "mod-v02.0.6", "mod-v2.0.6-test.01",
                      "mod-v2.0.6/../x", "mod-v2.0.6\n"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                release.module_version(value)

    def test_promotion_order_puts_immutable_version_before_moving_aliases(self):
        stable = release.promotion_plan("v3.0.0")
        self.assertEqual([step["tag"] for step in stable], ["3.0.0", "3.0", "latest"])
        self.assertFalse(stable[0]["mutable"])
        self.assertTrue(all(step["mutable"] for step in stable[1:]))
        self.assertEqual([step["step"] for step in stable], [1, 2, 3])
        prerelease = release.promotion_plan("v3.0.0-alpha.1")
        self.assertEqual([step["tag"] for step in prerelease], ["3.0.0-alpha.1"])
        self.assertFalse(prerelease[0]["mutable"])  # no latest/minor for prereleases
        module = release.module_promotion_plan("mod-v2.0.6")
        self.assertEqual([step["tag"] for step in module], ["v2.0.6", "latest"])
        self.assertFalse(module[0]["mutable"])

    def test_image_tags_cli_is_display_only_and_can_emit_immutable_alone(self):
        out = io.StringIO()
        with redirect_stdout(out):
            release.image_tags(argparse.Namespace(
                version="v3.0.0", module=False, immutable_only=False))
        self.assertEqual(out.getvalue().splitlines(), ["3.0.0", "3.0", "latest"])
        out = io.StringIO()
        with redirect_stdout(out):
            release.image_tags(argparse.Namespace(
                version="v3.0.0", module=False, immutable_only=True))
        self.assertEqual(out.getvalue().splitlines(), ["3.0.0"])

    def test_missing_registry_credentials_fail_closed(self):
        self.assertEqual(release.missing_publish_credentials({}), ["docker_hub", "ghcr"])
        self.assertEqual(release.missing_publish_credentials({
            "DOCKER_HUB_USERNAME": "user", "DOCKER_HUB_PASSWORD": "token", "GITHUB_TOKEN": "token"}), [])
        self.assertEqual(release.missing_publish_credentials({
            "DOCKERHUB_USERNAME": "user", "DOCKERHUB_TOKEN": "token", "GITHUB_TOKEN": "token"
        }, ("docker_hub",)), [])
        self.assertEqual(release.missing_publish_credentials({}, ("ghcr",)), ["ghcr"])

    def test_preflight_reports_missing_registries_without_secret_values(self):
        out = io.StringIO()
        with patch.dict(release.os.environ, {}, clear=True), redirect_stdout(out):
            with self.assertRaisesRegex(ValueError, "docker_hub"):
                release.preflight(argparse.Namespace(registries=None))
        self.assertEqual(json.loads(out.getvalue())["missing"], ["docker_hub", "ghcr"])

    def test_stale_commit_cannot_publish(self):
        with patch.object(release.subprocess, "run",
                          side_effect=subprocess.CalledProcessError(1, "git")), \
             self.assertRaisesRegex(ValueError, "main-ancestor"):
            release.guard_publishing("v3.0.0", "a" * 40)

    def test_initial_publication_and_unpublished_draft_are_allowed(self):
        success = json.dumps([{"conclusion": "success", "status": "completed"}])
        for existing in ([[]], [[{"tag_name": "v3.0.0", "draft": True}]]):
            with self.subTest(existing=existing), \
                 patch.object(release.subprocess, "run"), \
                 patch.object(release, "run", return_value=""), \
                 patch.object(release, "github", side_effect=[success, json.dumps(existing)]):
                release.guard_publishing("v3.0.0", "a" * 40)

    def test_finalize_refuses_a_conflicting_image_digest(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            original = {"version": "v3.0.0", "image_digest": "sha256:" + "a" * 64}
            (output / "release-manifest.json").write_text(json.dumps(original), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "different image digest"):
                release.finalize(argparse.Namespace(output=str(output), digest="sha256:" + "b" * 64))
            # Repeating the identical digest is idempotent.
            release.finalize(argparse.Namespace(output=str(output), digest="sha256:" + "a" * 64))
            manifest = json.loads((output / "release-manifest.json").read_text(encoding="utf-8"))
            self.assertEqual(manifest["image_digest"], "sha256:" + "a" * 64)

    def test_publish_refuses_a_manifest_from_another_version(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / "release-manifest.json").write_text(json.dumps({
                "version": "v9.9.9", "commit": "a" * 40,
                "image_digest": "sha256:" + "b" * 64}), encoding="utf-8")
            with patch.object(release, "guard_publishing"), patch.object(release, "github") as github, \
                 self.assertRaisesRegex(ValueError, "match the release commit"):
                release.publish(argparse.Namespace(version="v3.0.0", commit="a" * 40, output=str(output)))
            github.assert_not_called()

    def test_partial_registry_publication_is_a_hard_failure(self):
        complete = {"manifests": [
            {"platform": {"os": "linux", "architecture": "amd64"}},
            {"platform": {"os": "linux", "architecture": "arm64"}},
        ]}
        incomplete = {"manifests": [
            {"platform": {"os": "linux", "architecture": "amd64"}},
        ]}
        with tempfile.TemporaryDirectory() as directory:
            good = Path(directory) / "docker.json"
            bad = Path(directory) / "ghcr.json"
            good.write_text(json.dumps(complete), encoding="utf-8")
            bad.write_text(json.dumps(incomplete), encoding="utf-8")
            with self.assertRaises(ValueError):
                release.verify_registries(argparse.Namespace(
                    docker_manifest=str(good), github_manifest=str(bad)))
            out = io.StringIO()
            with redirect_stdout(out):
                results = release.verify_registries(argparse.Namespace(
                    docker_manifest=str(good), github_manifest=str(good)))
            self.assertEqual(results, {"docker_hub": "verified", "ghcr": "verified"})

    # --- Publication events and real action outputs (fixture-only) -----------

    def test_observe_reference_distinguishes_absent_unknown_and_present(self):
        def missing(*args):
            raise subprocess.CalledProcessError(1, args, stderr="manifest unknown")
        def broken(*args):
            raise subprocess.CalledProcessError(1, args, stderr="connection refused")
        self.assertEqual(release.observe_reference("ref", missing)[0], "absent")
        self.assertEqual(release.observe_reference("ref", broken)[0], "unknown")
        text = "\n".join(["Digest:    sha256:" + "c" * 64, "Manifests:",
                          "  Platform:  linux/amd64", "  Platform:  linux/arm64"])
        state, info = release.observe_reference("ref", lambda *args: text)
        self.assertEqual(state, "present")
        self.assertEqual(info["platforms"], release.REQUIRED_PLATFORMS)

    def release_fixture(self, manifest):
        """Mock gh: list releases, then return the release-manifest asset."""
        def reply(*args):
            if "--paginate" in args:
                return json.dumps([[{"tag_name": "v3.0.0", "draft": False,
                                     "assets": [{"name": "release-manifest.json",
                                                 "id": 71}]}]])
            return json.dumps(manifest)
        return reply

    def test_immutable_preflight_publishes_when_absent_everywhere(self):
        out = io.StringIO()
        with patch.object(release, "observe_reference", return_value=("absent", None)), \
             patch.object(release, "resume_provenance", return_value=(False, "unused")), \
             redirect_stdout(out):
            action = release.preflight_immutable(argparse.Namespace(
                version="v3.0.0", commit="b" * 40, expected_digest=""))
        self.assertEqual(action, release.IMMUTABLE_PUBLISH)
        self.assertEqual(json.loads(out.getvalue())["action"], "publish")

    def test_immutable_preflight_refuses_conflicting_digest(self):
        digest = "sha256:" + "a" * 64
        with patch.object(release, "observe_reference",
                          side_effect=[present(digest), present("sha256:" + "b" * 64)]), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            with self.assertRaisesRegex(ValueError, "differs"):
                release.preflight_immutable(argparse.Namespace(
                    version="v3.0.0", commit="b" * 40, expected_digest=digest))

    def test_immutable_preflight_refuses_unknown_registry_error(self):
        with patch.object(release, "observe_reference",
                          side_effect=[("unknown", None), ("absent", None)]), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            with self.assertRaisesRegex(ValueError, "unknown"):
                release.preflight_immutable(argparse.Namespace(
                    version="v3.0.0", commit="b" * 40, expected_digest=""))

    def test_immutable_preflight_resumes_only_identical_verified_digest(self):
        digest = "sha256:" + "a" * 64
        args = argparse.Namespace(version="v3.0.0", commit="b" * 40, expected_digest=digest)
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            self.assertEqual(release.preflight_immutable(args), release.IMMUTABLE_RESUME)
        with patch.object(release, "observe_reference",
                          return_value=present(digest, {("linux", "amd64")})), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            with self.assertRaisesRegex(ValueError, "architectures"):
                release.preflight_immutable(args)
        with patch.object(release, "observe_reference",
                          side_effect=[present(digest), ("absent", None)]), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            with self.assertRaisesRegex(ValueError, "partial"):
                release.preflight_immutable(args)
        # An unverified provenance never resumes, even with matching digests.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "resume_provenance", return_value=(False, "no manifest")):
            with self.assertRaisesRegex(ValueError, "provenance"):
                release.preflight_immutable(args)

    def test_resume_requires_matching_digest_commit_version_and_revision(self):
        digest = "sha256:" + "a" * 64
        commit = "b" * 40
        good = {"version": "v3.0.0", "commit": commit,
                "revision": commit, "image_digest": digest}
        cases = [
            ("matching", good, None),
            ("wrong commit", {**good, "commit": "c" * 40}, "source commit mismatch"),
            ("wrong version", {**good, "version": "v3.0.1"}, "version mismatch"),
            ("wrong revision", {**good, "revision": "d" * 40}, "revision mismatch"),
            ("wrong digest", {**good, "image_digest": "sha256:" + "e" * 64}, "digest mismatch"),
        ]
        for label, manifest, expected_error in cases:
            with self.subTest(case=label):
                with patch.object(release, "observe_reference", return_value=present(digest)), \
                     patch.object(release, "github", side_effect=self.release_fixture(manifest)), \
                     patch.object(release, "run", return_value=""):
                    args = argparse.Namespace(
                        version="v3.0.0", commit=commit, expected_digest=digest)
                    if expected_error:
                        with self.assertRaisesRegex(ValueError, expected_error):
                            release.preflight_immutable(args)
                    else:
                        self.assertEqual(
                            release.preflight_immutable(args), release.IMMUTABLE_RESUME)
        # A different operator-supplied digest cannot resume an identical tag.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "resume_provenance", return_value=(True, "verified")):
            with self.assertRaisesRegex(ValueError, "differs"):
                release.preflight_immutable(argparse.Namespace(
                    version="v3.0.0", commit=commit,
                    expected_digest="sha256:" + "f" * 64))
        # Unavailable provenance fails closed.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "github",
                          side_effect=subprocess.CalledProcessError(1, ["gh"])), \
             patch.object(release, "run", return_value=""):
            with self.assertRaisesRegex(ValueError, "provenance"):
                release.preflight_immutable(argparse.Namespace(
                    version="v3.0.0", commit=commit, expected_digest=digest))

    def test_verify_immutable_checks_tag_digest_and_both_architectures(self):
        digest = "sha256:" + "a" * 64
        args = argparse.Namespace(version="v3.0.0", digest=digest)
        with patch.object(release, "observe_reference", return_value=present(digest)):
            out = io.StringIO()
            with redirect_stdout(out):
                release.verify_immutable(args)
            self.assertEqual(json.loads(out.getvalue())["verified"], "true")
        with patch.object(release, "observe_reference", return_value=present("sha256:" + "b" * 64)):
            with self.assertRaisesRegex(ValueError, "differs"):
                release.verify_immutable(args)
        with patch.object(release, "observe_reference",
                          return_value=present(digest, {("linux", "arm64")})):
            with self.assertRaisesRegex(ValueError, "architectures"):
                release.verify_immutable(args)
        with patch.object(release, "observe_reference", return_value=("absent", None)):
            with self.assertRaisesRegex(ValueError, "absent"):
                release.verify_immutable(args)

    def test_alias_promotion_outputs_actionable_step_values(self):
        digest = "sha256:" + "a" * 64
        def run_case(candidate, published):
            with patch.object(release, "published_stable_versions", return_value=published), \
                 patch.object(release, "release_provenance", return_value=(True, "verified", None)):
                out = io.StringIO()
                with redirect_stdout(out):
                    release.alias_promotion(argparse.Namespace(
                        version=candidate, commit="b" * 40, digest=digest))
                return json.loads(out.getvalue())

        forward = run_case("v3.1.0", ["v3.0.0", "v3.0.5"])
        self.assertEqual((forward["minor"], forward["latest"]), ("3.1", "true"))
        old = run_case("v3.0.0", ["v3.1.0"])
        self.assertEqual((old["minor"], old["latest"]), ("", "false"))
        self.assertIn("older", old["reason"])
        prerelease = run_case("v3.1.0-rc.1", ["v3.0.0"])
        self.assertEqual(prerelease["latest"], "false")
        unavailable = run_case("v3.1.0", None)
        self.assertEqual(unavailable["latest"], "false")
        self.assertIn("unavailable", unavailable["reason"])

    def test_promote_alias_preflights_record_registries_and_avoids_overwrite(self):
        digest = "sha256:" + "a" * 64
        other = "sha256:" + "d" * 64
        def call(alias="latest"):
            return argparse.Namespace(version="v3.1.0", commit="b" * 40,
                                      alias=alias, digest=digest)

        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", side_effect=[
                 ("absent", None), ("absent", None), present(digest), present(digest)]), \
             patch.object(release, "durable_release_records", return_value={}):
            release.promote_alias(call())
            writes = registry_writes(command)
            self.assertEqual(len(writes), 2)  # both registries, each re-verified
            for item in writes:
                self.assertIn("imagetools", item.args[0])
                self.assertIn("create", item.args[0])

        # Interruption then retry: alias already at the verified digest => no overwrite.
        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "durable_release_records", return_value={}):
            release.promote_alias(call())
            self.assertEqual(registry_writes(command), [])  # no overwrite on retry

        # Lagging/unknown registry state refuses any move.
        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", return_value=("unknown", None)):
            with self.assertRaisesRegex(ValueError, "unknown"):
                release.promote_alias(call())
            self.assertEqual(registry_writes(command), [])

        # An older dispatch cannot move an alias whose digest proves a newer version.
        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", return_value=present(other)), \
             patch.object(release, "durable_release_records",
                          return_value={other: "v3.2.0"}):
            with self.assertRaisesRegex(ValueError, "backwards"):
                release.promote_alias(argparse.Namespace(
                    version="v3.0.0", commit="b" * 40, alias="latest", digest="sha256:" + "e" * 64))
            self.assertEqual(registry_writes(command), [])

        # An untraceable alias digest is uncertainty: refuse, do not guess.
        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", return_value=present(other)), \
             patch.object(release, "durable_release_records", return_value={}):
            with self.assertRaisesRegex(ValueError, "provenance"):
                release.promote_alias(call())
            self.assertEqual(registry_writes(command), [])

        # No durable release record => no alias move at all.
        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(False, "no manifest", None)):
            with self.assertRaisesRegex(ValueError, "Durable release record"):
                release.promote_alias(call())
            self.assertEqual(registry_writes(command), [])

    def test_release_record_decision_and_cli(self):
        digest = "sha256:" + "a" * 64
        commit = "b" * 40
        good = {"version": "v3.1.0", "commit": commit, "image_digest": digest}
        absent = []
        published = [[{"tag_name": "v3.1.0", "draft": False,
                       "assets": [{"name": "release-manifest.json", "id": 9}]}]]
        draft = [[{"tag_name": "v3.1.0", "draft": True,
                   "assets": [{"name": "release-manifest.json", "id": 9}]}]]
        self.assertEqual(release.release_record_decision("v3.1.0", commit, digest, absent)[0], "publish")
        with patch.object(release, "github", return_value=json.dumps(good)):
            self.assertEqual(release.release_record_decision(
                "v3.1.0", commit, digest, published)[0], "verified")
            self.assertEqual(release.release_record_decision(
                "v3.1.0", commit, digest, draft)[0], "refuse")
        with patch.object(release, "github", side_effect=subprocess.CalledProcessError(1, ["gh"])):
            self.assertEqual(release.release_record_decision(
                "v3.1.0", commit, digest)[0], "refuse")
        out = io.StringIO()
        with patch.object(release, "release_record_decision", return_value=("verified", "ok")), \
             redirect_stdout(out):
            self.assertEqual(release.release_record(argparse.Namespace(
                version="v3.1.0", commit=commit, digest=digest)), "verified")
        self.assertEqual(json.loads(out.getvalue())["action"], "verified")

    def test_module_version_and_promotion_outputs(self):
        out = io.StringIO()
        with redirect_stdout(out):
            release.module_version_info(argparse.Namespace(version="mod-v2.0.6"))
        stable = json.loads(out.getvalue())
        self.assertEqual((stable["version"], stable["tag"], stable["ref"]),
                         ("v2.0.6", "mod-v2.0.6", "refs/tags/mod-v2.0.6"))
        self.assertEqual((stable["prerelease"], stable["alias"]), ("false", "latest"))
        out = io.StringIO()
        with redirect_stdout(out):
            release.module_version_info(argparse.Namespace(version="mod-v3.0.0-beta.1"))
        prerelease = json.loads(out.getvalue())
        self.assertEqual(prerelease["version"], "v3.0.0-beta.1")
        self.assertEqual(prerelease["alias"], "")  # prerelease never takes :latest

        def promote_with(published):
            out = io.StringIO()
            with patch.object(release, "published_module_versions", return_value=published), \
                 redirect_stdout(out):
                release.module_promotion(argparse.Namespace(
                    version="mod-v2.0.6", published=None, digest=""))
            return json.loads(out.getvalue())

        self.assertEqual(promote_with(["mod-v2.0.5"])["promote_latest"], "true")
        self.assertEqual(promote_with(["mod-v2.1.0"])["promote_latest"], "false")

    def test_release_workflow_has_distinct_staged_immutable_and_alias_phases(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("          push: false\n          outputs: type=oci,dest=dist/image.oci.tar", source)
        self.assertIn("  publish-immutable:\n", source)
        self.assertIn("python3 scripts/release.py preflight-immutable", source)
        self.assertIn("if: steps.preflight.outputs.action == 'publish'", source)
        self.assertIn("python3 scripts/release.py verify-immutable", source)
        self.assertIn("  promote-aliases:\n", source)
        self.assertIn("if: steps.promotion.outputs.minor != ''", source)
        self.assertIn("if: steps.promotion.outputs.latest == 'true'", source)
        self.assertIn("needs: [plan, publish-immutable, publish]", source)
        self.assertIn("python3 scripts/release.py release-record", source)
        self.assertIn("--immutable-only", source)
        # Durable release record completes before alias promotion.
        self.assertLess(source.index("  publish:\n"), source.index("  promote-aliases:\n"))
        self.assertNotIn("github.event_name == 'push'", source)
        self.assertNotIn("type=raw,value=latest", source)

    def test_modules_workflow_uses_exact_refs_and_separate_alias_promotion(self):
        source = (release.ROOT / ".github/workflows/modules.yml").read_text(encoding="utf-8")
        self.assertIn("python3 scripts/release.py module-version", source)
        self.assertIn('git show-ref --verify --quiet "refs/tags/$MODULE_TAG"', source)
        self.assertNotIn("git tag --list \"$TAG\"", source)
        self.assertIn("  promote-module-latest:\n", source)
        self.assertIn("python3 scripts/release.py module-promotion", source)
        self.assertIn("python3 scripts/release.py verify-reference", source)
        self.assertIn("docker buildx imagetools create --tag \"$IMAGE:latest\"", source)
        # The build step pushes only the immutable v-prefixed version tag.
        self.assertIn('--tag "$IMAGE:$MODULE_VERSION" .', source)
        self.assertNotIn("--tag $TAGS --push", source)

    def test_every_automatic_or_dry_event_stages_only(self):
        # Tag pushes (including old targets), branch pushes, PRs, CI events and
        # a dispatch with publish off can only reach the staging jobs.
        for event, publish_input in (
            ("push", ""), ("workflow_dispatch", "false"), ("workflow_dispatch", ""),
            ("pull_request", ""), ("workflow_run", ""), ("schedule", ""),
        ):
            for candidate in ("v3.0.0", "v0.0.0-verify.9", "mod-v2.0.6"):
                with self.subTest(event=event, candidate=candidate):
                    allowed, _ = release.publication_allowed(event, publish_input, candidate)
                    self.assertFalse(allowed)
        # Only the explicit dispatch opt-in for a publishable panel version.
        self.assertTrue(release.publication_allowed(
            "workflow_dispatch", "true", "v3.0.0")[0])
        self.assertFalse(release.publication_allowed(
            "workflow_dispatch", "true", "v0.0.0-verify.9")[0])

    def test_every_writing_job_is_gated_and_staging_has_no_credentials(self):
        release_src = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        staging = release_src.split("  publish-immutable:")[0]
        self.assertNotIn("docker/login-action", staging)
        self.assertNotIn("secrets.DOCKER_HUB", staging)
        self.assertIn("          push: false", staging)
        self.assertGreaterEqual(
            release_src.count("if: needs.plan.outputs.publish == 'true'"), 3)
        self.assertIn(
            "if: needs.plan.outputs.publish == 'true' && "
            "needs.publish-immutable.outputs.verified == 'true'", release_src)
        self.assertNotIn("if: github.event_name == 'push'", release_src)
        modules_src = (release.ROOT / ".github/workflows/modules.yml").read_text(encoding="utf-8")
        self.assertGreaterEqual(
            modules_src.count("github.event_name == 'workflow_dispatch' && inputs.publish"), 4)
        self.assertIn("needs.build-modules.outputs.promote_latest == 'true'", modules_src)

    def test_developer_channel_decision_requires_trusted_ref_and_ci_evidence(self):
        trusted = release.TRUSTED_DEVELOPER_REF
        cases = [
            ("dispatch trusted main with both opt-ins", "workflow_dispatch", "true", trusted, True, True),
            ("dispatch without channel opt-in", "workflow_dispatch", "false", trusted, True, False),
            ("dispatch without CI confirmation", "workflow_dispatch", "true", trusted, False, False),
            ("dispatch from feature ref", "workflow_dispatch", "true", "refs/heads/feature/x", True, False),
            ("dispatch with blank ref", "workflow_dispatch", "true", "", True, False),
            ("dispatch from tag ref", "workflow_dispatch", "true", "refs/tags/v3.0.0", True, False),
        ]
        for label, event, opt_in, ref, ci, expected in cases:
            with self.subTest(case=label):
                allowed, _ = release.developer_channel_decision(
                    event, opt_in, ref=ref, ci_verified=ci)
                self.assertEqual(allowed, expected)
        # Ordinary main CI, PRs, branch pushes and tag pushes never publish.
        for event, run_event, conclusion, ref in (
            ("workflow_run", "push", "success", trusted),
            ("workflow_run", "pull_request", "success", trusted),
            ("workflow_run", "push", "failure", trusted),
            ("push", "", "", "refs/heads/main"),
            ("push", "", "", "refs/tags/v3.0.0"),
            ("pull_request", "", "", trusted),
        ):
            with self.subTest(event=event, ref=ref):
                allowed, _ = release.developer_channel_decision(
                    event, "true", run_event, conclusion, ref, "true")
                self.assertFalse(allowed)

    def test_developer_channel_gate_cli_emits_actionable_outputs(self):
        out = io.StringIO()
        with redirect_stdout(out):
            allowed = release.developer_channel_gate(argparse.Namespace(
                event="workflow_dispatch", opt_in="true", run_event="",
                conclusion="", ref=release.TRUSTED_DEVELOPER_REF,
                ci_verified="true"))
        self.assertTrue(allowed)
        self.assertEqual(json.loads(out.getvalue())["publish"], "true")

    def test_developer_channel_gate_is_wired_without_disabling_workflows(self):
        cd = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        landing = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        for source, input_name in ((cd, "inputs.push"), (landing, "inputs.deploy")):
            self.assertIn("python3 scripts/release.py developer-channel-gate", source)
            self.assertIn("--ci-verified \"$CHANNEL_CI_VERIFIED\"", source)
            self.assertIn("CHANNEL_OPT_IN: ${{ " + input_name + " }}", source)
            self.assertIn("python3 scripts/release.py verify-main-ci", source)
            self.assertIn("CHANNEL_CI_VERIFIED: ${{ steps.ci.outputs.verified }}", source)
            self.assertIn("if: github.event_name == 'workflow_dispatch'", source)
            self.assertNotIn("ci_verified:", source)
        self.assertIn("  workflow_run:", cd)
        self.assertIn("push: ${{ (steps.channel.outputs.publish == 'true'", cd)
        self.assertIn("if: needs.build.outputs.publish == 'true'", landing)
        # Workflows remain active: build/validate steps are still present.
        self.assertIn("name: Build and validate the combined website", landing)
        self.assertIn("name: Assemble and verify both architectures locally", 
                      (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8"))
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("product merges stay held", docs)

    def test_pages_write_permissions_are_deploy_job_only(self):
        source = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        self.assertIn("permissions:\n  contents: read\n  actions: read", source)
        deploy = source.split("  deploy:\n", 1)[1].split("  indexable-changes:\n", 1)[0]
        self.assertIn("    permissions:\n      contents: read\n"
                      "      pages: write\n      id-token: write", deploy)
        for job in ("build:", "indexable-changes:", "notify-indexnow:"):
            block = source.split("  " + job, 1)[1]
            block = block.split("\n  ", 1)[0]
            self.assertNotIn("pages: write", block)
            self.assertNotIn("id-token: write", block)

    def test_indexnow_deferral_is_documented(self):
        landing = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        self.assertIn("Deliberate deferral: indexable-changes", landing)
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("IndexNow notification is deliberately deferred", docs)

    def test_staged_tag_scope_is_documented_honestly(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("tags can point at old commits", source)
        self.assertIn("never the dispatch revision input alone", source)
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("old commits", docs)

    def test_cd_edge_behavior_is_preserved(self):
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertIn("python3 scripts/release.py edge-promote", source)
        self.assertIn("if: steps.channel.outputs.publish == 'true'", source)
        self.assertNotIn("type=raw,value=edge", source)  # no implicit early edge move
        self.assertIn("github.event.workflow_run.conclusion == 'success'", source)

    def test_canonical_registry_names_are_consistent(self):
        self.assertEqual(release.DOCKER_HUB_IMAGE, "atharvnaphade/carbonpanel")
        self.assertEqual(release.github_image("v3.0.0", "AthNDev/CarbonPanel"),
                         "ghcr.io/athndev/carbonpanel:3.0.0")
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("atharvnaphade/carbonpanel", docs)
        self.assertIn("ghcr.io/athndev/carbonpanel", docs)


    # --- End-to-end CLI dispatch and workflow argument fixtures --------------

    def test_module_version_cli_dispatch_end_to_end(self):
        for value, expected_version, expected_alias in (
            ("mod-v2.0.6", "v2.0.6", "latest"),
            ("mod-v3.0.0-beta.1", "v3.0.0-beta.1", ""),
        ):
            with self.subTest(value=value):
                out = io.StringIO()
                with patch("sys.argv", ["release.py", "module-version",
                                        "--version", value]), redirect_stdout(out):
                    release.main()  # real argparse dispatch, not the handler
                values = json.loads(out.getvalue())
                self.assertEqual(values["version"], expected_version)
                self.assertEqual(values["tag"], value)
                self.assertEqual(values["ref"], "refs/tags/" + value)
                self.assertEqual(values["alias"], expected_alias)
        with patch("sys.argv", ["release.py", "module-version",
                                "--version", "v2.0.6"]):
            with self.assertRaises(ValueError):
                release.main()

    def test_modules_workflow_job_args_parse(self):
        source = (release.ROOT / ".github/workflows/modules.yml").read_text(encoding="utf-8")
        commands = re.findall(r"python3 scripts/release\.py ([a-z-]+) (.*)", source)
        self.assertTrue(commands)
        substitutions = {
            "$VERSION": "v2.0.6", "$MODULE_VERSION": "v2.0.6",
            "$MODULE_INPUT": "mod-v2.0.6", "$DIGEST": "sha256:" + "a" * 64,
            "$IMAGE": "atharvnaphade/carbonpanel-geyser",
            "$MODULE_TAG": "mod-v2.0.6",
            "$GITHUB_REF_NAME": "mod-v2.0.6",
        }
        parser = release.build_parser()
        seen = set()
        for command, raw_args in commands:
            with self.subTest(command=command):
                args = raw_args.strip().rstrip("\\").strip()
                for variable, value in substitutions.items():
                    args = args.replace("${" + variable[1:] + "}", value) \
                               .replace(variable, value)
                parsed = parser.parse_args([command] + shlex.split(args))
                self.assertEqual(parsed.command, command)
                seen.add(command)
        self.assertIn("module-version", seen)

    def test_indexnow_deferral_dry_run_cli(self):
        # Dispatch deploy: no push diff, therefore no URLs and no notification.
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "indexable-urls",
                                "--event", "workflow_dispatch",
                                "--before", "", "--paths", "landing/index.html"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue()), [])
        landing = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        self.assertIn("needs.indexable-changes.outputs.urls != '[]'", landing)
        # A real push diff still produces URLs through the same shared helper.
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "indexable-urls",
                                "--event", "push", "--before", "a" * 40,
                                "--paths", "docs/carbon-panel/src/content/docs/index.mdx"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue()), ["https://carbonpanel.dev/docs/"])

    def test_cd_checkout_is_pinned_and_publication_concurrency_is_isolated(self):
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertIn("id: pinned", source)
        self.assertIn("RUN_SHA: ${{ github.event.workflow_run.head_sha }}", source)
        self.assertIn("EVENT_SHA: ${{ github.sha }}", source)
        self.assertIn("ref: ${{ steps.pinned.outputs.sha }}", source)
        self.assertIn("fetch-depth: 0", source)
        self.assertIn("Assert checked-out HEAD equals the pinned commit", source)
        self.assertIn("CI_COMMIT: ${{ steps.pinned.outputs.sha }}", source)
        self.assertIn("value=sha-${{ steps.pinned.outputs.sha }}", source)
        self.assertIn("image.revision=${{ steps.pinned.outputs.sha }}", source)
        self.assertIn("APP_COMMIT=${{ steps.pinned.outputs.sha }}", source)
        # No branch-tip substitution anywhere in the pinned checkout path.
        self.assertNotIn("ref: ${{ github.ref }}", source)
        for line in source.splitlines():
            if any(token in line for token in ("CI_COMMIT:", "value=sha-",
                                                "image.revision=", "APP_COMMIT=")):
                self.assertNotIn("head_sha || github.sha", line)
        # Publication is one fixed, channel-wide, non-cancelling group: two
        # opted-in publishes of different commits cannot race on :edge.
        self.assertIn("&& (inputs.push || inputs.nightly) && 'cd-publish-edge'", source)
        self.assertIn("|| format('cd-verify-{0}'", source)
        self.assertIn("cancel-in-progress: ${{ github.event_name != 'workflow_dispatch' "
                      "|| !(inputs.push || inputs.nightly) }}", source)
        self.assertIn("&& 'cd-publish-edge' || format('cd-verify-{0}'", source)
        # Pin assertion precedes every side-effecting step.
        self.assertLess(source.index("Assert checked-out HEAD"),
                        source.index("Docker Hub Credentials & Login Check"))
        self.assertLess(source.index("Assert checked-out HEAD"),
                        source.index("name: Build (and optionally push) Docker Image"))

    def test_landing_checkout_uses_event_commit_implicitly(self):
        source = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        for step_name in ("Checkout", "Checkout history"):
            block = source.split("- name: " + step_name + "\n", 1)[1].split("\n      - ", 1)[0]
            self.assertIn("fetch-depth: 0", block)
            self.assertNotIn("ref:", block)

    def test_landing_deploy_group_is_fixed_and_non_cancellable(self):
        source = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        self.assertIn("&& inputs.deploy && 'pages-deploy'", source)
        self.assertIn("&& 'pages-deploy' || format('pages-verify-{0}'", source)
        self.assertIn("cancel-in-progress: ${{ github.event_name != 'workflow_dispatch' "
                      "|| !inputs.deploy }}", source)
        self.assertNotIn("  group: pages\n", source)
        # An opted-in deployment is still gated by the trusted-CI decision, not
        # by which concurrency group the run used.
        self.assertIn("if: needs.build.outputs.publish == 'true'", source)
        automatic = release.developer_channel_decision(
            "push", "true", ref=release.TRUSTED_DEVELOPER_REF, ci_verified="true")
        self.assertFalse(automatic[0])

    def test_cd_has_read_only_workflow_permissions(self):
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertIn("permissions:\n  contents: read\n  actions: read", source)
        self.assertIn("push: ${{ (steps.channel.outputs.publish == 'true'", source)


    # --- Exact main CI evidence for opt-in developer writes ------------------

    def test_exact_main_ci_verification_requires_ancestry_and_success(self):
        commit = "a" * 40
        success = [{"conclusion": "success", "status": "completed"}]
        with patch.object(release.subprocess, "run"), \
             patch.object(release, "github", return_value=json.dumps(success)):
            self.assertEqual(release.exact_main_ci_verified(commit),
                             (True, "exact main CI verified"))
        # Stale/wrong commit: not an ancestor of main.
        with patch.object(release.subprocess, "run",
                          side_effect=subprocess.CalledProcessError(1, ["git"])):
            ok, reason = release.exact_main_ci_verified(commit)
            self.assertFalse(ok)
            self.assertIn("ancestor", reason)
        # Ancestor, but CI for this exact SHA is absent/failed/incomplete.
        for runs in ([], [{"conclusion": "failure", "status": "completed"}],
                     [{"conclusion": "success", "status": "in_progress"}]):
            with self.subTest(runs=runs), patch.object(release.subprocess, "run"), \
                 patch.object(release, "github", return_value=json.dumps(runs)):
                ok, reason = release.exact_main_ci_verified(commit)
                self.assertFalse(ok)
                self.assertIn("successful completed push CI", reason)
        # API unavailable fails closed.
        with patch.object(release.subprocess, "run"), \
             patch.object(release, "github",
                          side_effect=subprocess.CalledProcessError(1, ["gh"])):
            ok, reason = release.exact_main_ci_verified(commit)
            self.assertFalse(ok)
            self.assertIn("unavailable", reason)
        # Empty/invalid commit cannot be verified.
        for value in ("", "main", "not-a-sha"):
            with self.subTest(value=value):
                self.assertFalse(release.exact_main_ci_verified(value)[0])

    def test_verify_main_ci_cli_end_to_end_and_checkbox_cannot_substitute(self):
        commit = "b" * 40
        out = io.StringIO()
        with patch.object(release.subprocess, "run"), \
             patch.object(release, "github", return_value=json.dumps(
                 [{"conclusion": "success", "status": "completed"}])), \
             patch("sys.argv", ["release.py", "verify-main-ci", "--commit", commit]), \
             redirect_stdout(out):
            release.main()
        values = json.loads(out.getvalue())
        self.assertEqual((values["verified"], values["commit"]), ("true", commit))
        # A checkbox opt-in alone never authorizes an opt-in write: the decision
        # still requires the verified output from the CI check.
        for verified in ("", "false"):
            with self.subTest(verified=verified):
                allowed, reason = release.developer_channel_decision(
                    "workflow_dispatch", "true",
                    ref=release.TRUSTED_DEVELOPER_REF, ci_verified=verified)
                self.assertFalse(allowed)
                self.assertNotIn("opt-in", reason)

    # --- Bounded nightly channel (offline fixtures) --------------------------

    def test_nightly_decision_requires_opt_in_trusted_ref_and_exact_ci(self):
        trusted = release.TRUSTED_DEVELOPER_REF
        allowed, _ = release.nightly_channel_decision(
            "workflow_dispatch", "true", ref=trusted, ci_verified="true")
        self.assertTrue(allowed)
        for label, event, opt_in, ref, ci in (
            ("missing opt-in", "workflow_dispatch", "false", trusted, "true"),
            ("failed CI", "workflow_dispatch", "true", trusted, "false"),
            ("feature ref", "workflow_dispatch", "true", "refs/heads/feature/x", "true"),
            ("blank ref", "workflow_dispatch", "true", "", "true"),
            ("tag ref", "workflow_dispatch", "true", "refs/tags/v3.0.0", "true"),
            ("main push", "push", "true", trusted, "true"),
            ("tag push", "push", "true", "refs/tags/v3.0.0", "true"),
            ("pull request", "pull_request", "true", trusted, "true"),
        ):
            with self.subTest(case=label):
                self.assertFalse(release.nightly_channel_decision(
                    event, opt_in, ref=ref, ci_verified=ci)[0])
        self.assertFalse(release.nightly_channel_decision(
            "workflow_run", "true", "push", "success", trusted, "true")[0])
        self.assertFalse(release.nightly_channel_decision(
            "schedule", "true", ref=trusted, ci_verified="true")[0])

    def test_sha_tag_gate_reuse_conflict_and_unknown(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        revisions = platform_revisions(commit)
        good = dict(expected_digest=digest, evidence={"digest": digest},
                    revisions=revisions, ci_verified="true")
        self.assertEqual(release.sha_tag_decision(commit, ("absent", None))[0], "push")
        # An expected digest with a missing tag refuses, as stable releases do.
        self.assertEqual(release.sha_tag_decision(
            commit, ("absent", None), expected_digest=digest)[0], "refuse")
        action, reused, _ = release.sha_tag_decision(commit, present(digest), **good)
        self.assertEqual((action, reused), ("reuse", digest))
        bad_revisions = dict(revisions)
        bad_revisions[("linux", "arm64")] = {
            "digest": "sha256:" + "b" * 64, "revision": "b" * 40}
        for label, observation, changes in (
            ("conflicting digest", present("sha256:" + "e" * 64), {}),
            ("missing expected digest", present(digest), {"expected_digest": ""}),
            ("incomplete platforms", present(digest, {("linux", "amd64")}), {}),
            ("unknown lookup", ("unknown", None), {}),
            ("missing CI evidence", present(digest), {"ci_verified": "false"}),
            ("platform revision mismatch", present(digest), {"revisions": bad_revisions}),
            ("untrusted evidence digest", present(digest),
             {"evidence": {"digest": "sha256:" + "9" * 64}}),
        ):
            with self.subTest(case=label):
                action, _, reason = release.sha_tag_decision(
                    commit, observation, **{**good, **changes})
                self.assertEqual(action, "refuse")
                self.assertTrue(reason)
        # CLI refusal produces no registry write of any kind.
        with patch.object(release, "observe_reference", return_value=("unknown", None)), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", ["release.py", "sha-tag-gate", "--commit", commit,
                                "--expected-digest", digest, "--ci-verified", "true",
                                "--pinned-evidence", json.dumps({"digest": digest})]):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])

    def test_observe_reference_strict_classification_for_publication_paths(self):
        def failure(message):
            error = subprocess.CalledProcessError(1, ["docker"])
            error.stderr = message
            error.output = ""
            def runner(*args):
                raise error
            return runner
        self.assertEqual(release.observe_reference(
            "ref", failure("MANIFEST_UNKNOWN: manifest unknown"), strict=True)[0], "absent")
        # Exact read-only Buildx probe output for a nonexistent tag (2026-11-18).
        observed_missing = ("ERROR: docker.io/atharvnaphade/carbonpanel:"
                            "nightly-policy-nonexistent-probe: not found")
        self.assertEqual(release.observe_reference(
            "ref", failure(observed_missing), strict=True)[0], "absent")
        for message in ("HTTP 404 not found", "unauthorized: not found",
                        "connection refused", "temporary server error",
                        "manifest unknown: connection reset"):
            with self.subTest(message=message):
                self.assertEqual(release.observe_reference(
                    "ref", failure(message), strict=True)[0], "unknown")

    def test_nightly_identity_binding_requires_live_sha_evidence(self):
        previous, old_digest = "a" * 40, "sha256:" + "f" * 64
        revisions = platform_revisions(previous)
        def observer(ref, strict=False):
            return present(old_digest)
        common = dict(observer=observer, revisions=revisions)
        self.assertEqual(release.nightly_identity_binding(previous, old_digest, **common),
                         {old_digest: previous})
        different = platform_revisions("b" * 40)
        self.assertEqual(release.nightly_identity_binding(
            previous, old_digest, revisions=different, observer=observer), {})
        self.assertEqual(release.nightly_identity_binding(
            previous, old_digest, revisions={}, observer=observer), {})
        self.assertEqual(release.nightly_identity_binding(
            previous, "sha256:" + "1" * 64, **common), {})
        self.assertEqual(release.nightly_identity_binding(
            previous, old_digest, observer=lambda ref, strict=False: ("absent", None),
            revisions=revisions), {})
        self.assertEqual(release.nightly_identity_binding("", old_digest, **common), {})

    def test_nightly_promotion_is_bound_to_observed_identity(self):
        candidate, previous = "c" * 40, "a" * 40
        digest, old_digest = "sha256:" + "d" * 64, "sha256:" + "f" * 64
        immutable = present(digest)
        revisions = platform_revisions(candidate)
        evidence = {"digest": digest}
        base = dict(ci_verified="true", revisions=revisions, evidence=evidence)
        self.assertTrue(release.nightly_promotion_decision(
            candidate, digest, immutable, ("absent", None), **base)[0])
        self.assertIn("no move", release.nightly_promotion_decision(
            candidate, digest, immutable, present(digest), **base)[1])
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, present(old_digest), **base)[0])
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, ("absent", None),
            **{**base, "ci_verified": "false"})[0])
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, ("absent", None),
            **{**base, "evidence": {"digest": "sha256:" + "9" * 64}})[0])
        bound = {old_digest: previous}
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, present(old_digest),
            previous_commit="b" * 40, identity_binding=bound, **base)[0])
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, present(old_digest),
            previous_commit=previous, identity_binding=bound,
            ancestor=lambda old, new: False, **base)[0])
        self.assertTrue(release.nightly_promotion_decision(
            candidate, digest, immutable, present(old_digest),
            previous_commit=previous, identity_binding=bound,
            ancestor=lambda old, new: True, **base)[0])
        wrong_revisions = dict(revisions)
        wrong_revisions[("linux", "amd64")] = {
            "digest": "sha256:" + "a" * 64, "revision": "b" * 40}
        self.assertFalse(release.nightly_promotion_decision(
            candidate, digest, immutable, ("absent", None),
            ci_verified="true", revisions=wrong_revisions, evidence=evidence)[0])

    def test_nightly_promote_apply_has_zero_writes_on_refusal_and_is_idempotent(self):
        candidate, previous = "c" * 40, "a" * 40
        digest, old_digest = "sha256:" + "d" * 64, "sha256:" + "f" * 64
        revisions = platform_revisions(candidate)
        def invoke(*extra):
            return ["release.py", "nightly-promote", "--commit", candidate,
                    "--digest", digest, "--previous-commit", previous,
                    "--previous-digest", old_digest, "--ci-verified", "true",
                    "--pinned-evidence", json.dumps({"digest": digest}), *extra]

        with patch.object(release, "observe_reference",
                          return_value=("unknown", None)), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])
        # First promotion writes only the nightly alias, then re-verifies it.
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release, "observe_reference", side_effect=[
                 present(digest), ("absent", None), present(digest)]), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            release.main()
            self.assertEqual(len(registry_writes(command)), 1)
        # Rerun with the same digest is idempotent: no alias move.
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release, "observe_reference", side_effect=[
                 present(digest), present(digest)]), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            release.main()
            self.assertEqual(registry_writes(command), [])

    def test_nightly_leaves_stable_aliases_and_edge_untouched(self):
        self.assertEqual(release.promotion_plan("v3.0.0")[0]["tag"], "3.0.0")
        self.assertEqual([step["tag"] for step in release.promotion_plan("v3.0.0")],
                         ["3.0.0", "3.0", "latest"])
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertNotIn("type=raw,value=latest", source)
        self.assertIn("edge-promote", source)
        self.assertNotIn("value=nightly,enable", source)  # alias moved after verification
        self.assertIn("nightly-promote", source)
        self.assertNotIn("type=raw,value=edge", source)

    def test_nightly_workflow_wiring_and_verification_only_paths(self):
        import yaml
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        doc = yaml.safe_load(source)
        triggers = doc.get("on") or doc.get(True)
        inputs = triggers["workflow_dispatch"]["inputs"]
        self.assertFalse(inputs["nightly"]["default"])
        self.assertEqual(inputs["previous_nightly_digest"]["default"], "")
        self.assertEqual(inputs["expected_sha_digest"]["default"], "")
        self.assertNotIn("schedule", triggers)
        names = [step.get("name", "") for step in doc["jobs"]["build-and-push"]["steps"]]
        sha_gate = next(i for i, name in enumerate(names) if "Preflight shared sha tag" in name)
        build = next(i for i, name in enumerate(names) if name.startswith("Build (and optionally push)"))
        edge_promote = next(i for i, name in enumerate(names)
                            if "Promote edge from the verified" in name)
        promote = next(i for i, name in enumerate(names) if "move only the nightly alias" in name)
        resolved = next(i for i, name in enumerate(names)
                        if name.startswith("Resolve the verified immutable digest"))
        self.assertLess(sha_gate, build)
        self.assertLess(build, resolved)
        self.assertLess(resolved, edge_promote)
        self.assertLess(resolved, promote)
        self.assertNotIn("type=raw,value=edge", source)
        self.assertIn("steps.sha_gate.outputs.action", source)
        self.assertIn("--previous-digest \"$PREVIOUS_DIGEST\"", source)
        self.assertIn("--ci-verified \"$CI_VERIFIED\"", source)
        self.assertIn("--pinned-evidence \"$PINNED_EVIDENCE\"", source)
        for path in (".github/workflows/release.yml", ".github/workflows/modules.yml"):
            self.assertTrue(yaml.safe_load(
                (release.ROOT / path).read_text(encoding="utf-8"))["jobs"])
        # Verification-only CLI path emits no publication.
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "nightly-gate", "--event", "push",
                                "--commit", "c" * 40, "--ref", release.TRUSTED_DEVELOPER_REF,
                                "--opt-in", "true", "--ci-verified", "true"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["publish"], "false")


    def test_edge_and_nightly_survive_both_publication_orders_on_sha_reuse(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        sha_ref = "atharvnaphade/carbonpanel:sha-" + commit
        revisions = platform_revisions(commit)
        for order in (("nightly-promote", "edge-promote"),
                      ("edge-promote", "nightly-promote")):
            with self.subTest(order=order):
                state = {"edge": ("absent", None), "nightly": ("absent", None)}
                def observe(ref, strict=False):
                    if ref == sha_ref:
                        return present(digest)
                    return state[ref.rsplit(":", 1)[1]]
                def fake_run(args, **kwargs):
                    target = args[args.index("--tag") + 1]
                    state[target.rsplit(":", 1)[1]] = present(digest)
                    return subprocess.CompletedProcess(args, 0)
                def argv_for(command):
                    step = ("Promote edge from the verified immutable digest"
                            if command == "edge-promote"
                            else "Verify immutable sha image and move only the nightly alias")
                    substitutions = {
                        "PINNED_COMMIT": commit, "IMAGE_DIGEST": digest,
                        "CI_VERIFIED": "true", "PINNED_EVIDENCE": "",
                        "PREVIOUS_COMMIT": "", "PREVIOUS_DIGEST": "",
                        "PREVIOUS_EDGE_COMMIT": "", "PREVIOUS_EDGE_DIGEST": "",
                    }
                    return self.workflow_argv(step, substitutions)

                writes = []
                for command in order:
                    argv = argv_for(command)
                    with patch.object(release, "observe_reference", side_effect=observe), \
                         patch.object(release, "platform_manifest_revisions",
                                      return_value=revisions), \
                         patch.object(release, "nightly_identity_binding", return_value={}), \
                         patch.object(release.subprocess, "run", side_effect=fake_run) as runner, \
                         patch("sys.argv", ["release.py"] + argv):
                        release.main()
                    writes.extend(registry_writes(runner))
                self.assertEqual(len(writes), 2)
                for alias in ("edge", "nightly"):
                    self.assertEqual(state[alias][1]["digest"], digest)
                # Repeating both commands writes nothing (sha reuse is idempotent).
                for command in order:
                    argv = argv_for(command)
                    with patch.object(release, "observe_reference", side_effect=observe), \
                         patch.object(release, "platform_manifest_revisions",
                                      return_value=revisions), \
                         patch.object(release, "nightly_identity_binding", return_value={}), \
                         patch.object(release.subprocess, "run") as runner, \
                         patch("sys.argv", ["release.py"] + argv):
                        release.main()
                        self.assertEqual(registry_writes(runner), [])


    # --- Recorded real Docker inspect shape and workflow CLI arguments -------

    def workflow_argv(self, step_name, substitutions):
        """Extract the real `python3 scripts/release.py ...` argv from a cd.yml step."""
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        block = source.split("- name: " + step_name + "\n", 1)[1].split("\n      - ", 1)[0]
        run_block = block.split("run: |\n", 1)[1]
        command = " ".join(line.strip() for line in run_block.splitlines() if line.strip())
        command = command.replace(" \\", " ")
        for variable, value in substitutions.items():
            command = command.replace("$" + variable, value)
        return shlex.split(command)[2:]

    def test_recorded_real_docker_config_revision_probe(self):
        commit = "c15b8d7d672d8d5fb6459a1317890e2b45adbbb1"
        index_digest = "sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001"
        children = {
            ("linux", "amd64"): {
                "digest": "sha256:" + "1" * 64, "revision": commit},
            ("linux", "arm64"): {
                "digest": "sha256:" + "2" * 64, "revision": commit},
        }
        reference = release.nightly_immutable_reference(commit)
        revisions = release.platform_manifest_revisions(
            reference, real_docker_inspect_runner(index_digest, children))
        for platform in release.REQUIRED_PLATFORMS:
            self.assertEqual(revisions[platform]["revision"], commit)
        self.assertEqual(revisions[("linux", "amd64")]["digest"], "sha256:" + "1" * 64)
        self.assertTrue(release.verify_sha_identity(
            commit, index_digest, present(index_digest), revisions)[0])
        # Descriptor annotations are null in this recorded shape: they are not evidence.
        bad = {key: dict(value) for key, value in revisions.items()}
        bad[("linux", "arm64")]["revision"] = "b" * 40
        self.assertFalse(release.verify_sha_identity(
            commit, index_digest, present(index_digest), bad)[0])
        bad[("linux", "arm64")]["revision"] = None
        self.assertFalse(release.verify_sha_identity(
            commit, index_digest, present(index_digest), bad)[0])

    def test_workflow_cli_arguments_move_existing_edge_with_separate_binding(self):
        commit, old_commit = "c" * 40, "a" * 40
        digest = "sha256:" + "d" * 64
        old_digest = "sha256:" + "f" * 64
        argv = self.workflow_argv(
            "Promote edge from the verified immutable digest",
            {"PINNED_COMMIT": commit, "IMAGE_DIGEST": digest,
             "CI_VERIFIED": "true", "PINNED_EVIDENCE": "",
             "PREVIOUS_EDGE_COMMIT": old_commit,
             "PREVIOUS_EDGE_DIGEST": old_digest})
        self.assertEqual(argv[0], "edge-promote")
        self.assertIn("--previous-commit", argv)
        self.assertIn(old_commit, argv)
        self.assertIn(old_digest, argv)

        sha_ref = release.nightly_immutable_reference(commit)
        previous_ref = release.nightly_immutable_reference(old_commit)
        children = {
            ("linux", "amd64"): {"digest": "sha256:" + "1" * 64, "revision": commit},
            ("linux", "arm64"): {"digest": "sha256:" + "2" * 64, "revision": commit},
        }
        previous_children = {
            ("linux", "amd64"): {"digest": "sha256:" + "3" * 64, "revision": old_commit},
            ("linux", "arm64"): {"digest": "sha256:" + "4" * 64, "revision": old_commit},
        }
        original_revisions = release.platform_manifest_revisions
        def revisions_for(reference):
            target = children if reference == sha_ref else (
                previous_children if reference == previous_ref else {})
            return original_revisions(
                reference, real_docker_inspect_runner("sha256:" + "9" * 64, target))
        state = {"edge": present(old_digest)}
        def observe(ref, strict=False):
            if ref == sha_ref:
                return present(digest)
            if ref == previous_ref:
                return present(old_digest)
            if ref == "atharvnaphade/carbonpanel:edge":
                return state["edge"]
            return ("absent", None)
        def fake_run(args, **kwargs):
            state["edge"] = present(digest)
            return subprocess.CompletedProcess(args, 0)
        with patch.object(release, "observe_reference", side_effect=observe), \
             patch.object(release, "platform_manifest_revisions", side_effect=revisions_for), \
             patch.object(release, "commit_is_ancestor", return_value=True), \
             patch.object(release.subprocess, "run", side_effect=fake_run) as command, \
             patch("sys.argv", ["release.py"] + argv):
            release.main()
            writes = registry_writes(command)
        self.assertEqual(len(writes), 1)
        self.assertEqual(state["edge"][1]["digest"], digest)


    def test_default_first_update_and_reuse_flows(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        revisions = platform_revisions(commit)
        # FIRST: no evidence and no previous binding; missing alias promotes.
        argv = self.workflow_argv(
            "Verify immutable sha image and move only the nightly alias",
            {"PINNED_COMMIT": commit, "IMAGE_DIGEST": digest,
             "CI_VERIFIED": "true", "PINNED_EVIDENCE": "",
             "PREVIOUS_COMMIT": "", "PREVIOUS_DIGEST": ""})
        state = {"nightly": ("absent", None)}
        def observe(ref, strict=False):
            if ref == "atharvnaphade/carbonpanel:nightly":
                return state["nightly"]
            return present(digest)
        def fake_run(args, **kwargs):
            state["nightly"] = present(digest)
            return subprocess.CompletedProcess(args, 0)
        with patch.object(release, "observe_reference", side_effect=observe), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release.subprocess, "run", side_effect=fake_run) as command, \
             patch("sys.argv", ["release.py"] + argv):
            release.main()
            self.assertEqual(len(registry_writes(command)), 1)
        # REUSE: sha gate with trusted per-platform config evidence returns reuse.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "platform_manifest_revisions", return_value=revisions), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", ["release.py", "sha-tag-gate", "--commit", commit,
                                "--expected-digest", digest, "--ci-verified", "true"]):
            release.main()
            self.assertEqual(registry_writes(command), [])


if __name__ == "__main__":
    unittest.main()
