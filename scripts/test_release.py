# SPDX-License-Identifier: MPL-2.0
"""Release-boundary checks: version validation, complete inputs and integrity."""
import argparse
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
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
        self.assertRegex(source, r"(?m)^    concurrency:\n      group:")
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
        self.assertIn("  workflow_run:", cd)
        self.assertIn("push: ${{ steps.channel.outputs.publish == 'true'", cd)
        self.assertIn("if: needs.build.outputs.publish == 'true'", landing)
        # Workflows remain active: build/validate steps are still present.
        self.assertIn("name: Build and validate the combined website", landing)
        self.assertIn("name: Assemble and verify both architectures locally", 
                      (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8"))
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("product merges stay held", docs)

    def test_pages_write_permissions_are_deploy_job_only(self):
        source = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        self.assertIn("permissions:\n  contents: read\n\nconcurrency:", source)
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
        self.assertIn("type=raw,value=edge,enable=${{ github.ref == 'refs/heads/main' }}", source)
        self.assertIn("github.event.workflow_run.conclusion == 'success'", source)

    def test_canonical_registry_names_are_consistent(self):
        self.assertEqual(release.DOCKER_HUB_IMAGE, "atharvnaphade/carbonpanel")
        self.assertEqual(release.github_image("v3.0.0", "AthNDev/CarbonPanel"),
                         "ghcr.io/athndev/carbonpanel:3.0.0")
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("atharvnaphade/carbonpanel", docs)
        self.assertIn("ghcr.io/athndev/carbonpanel", docs)


if __name__ == "__main__":
    unittest.main()
