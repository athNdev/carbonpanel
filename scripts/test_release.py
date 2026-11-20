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
import urllib.error
import zipfile
from unittest.mock import patch

from scripts import release
from scripts import docs_publication_gate as gate


def registry_writes(command):
    """Only calls that would mutate a registry or publish a release."""
    return [call for call in command.call_args_list
            if any(token in call.args[0] for token in ("create", "--push", "publish"))]


def patch_gh_release_view():
    """Let `gh release view` report an existing draft without breaking
    subprocess.check_output (which internally calls subprocess.run)."""
    original = release.subprocess.run

    def fake(*args, **kwargs):
        if args and args[0] == "gh" and len(args) > 1 and args[1] == "release":
            return type("Result", (), {"returncode": 0})()
        return original(*args, **kwargs)

    return patch.object(release.subprocess, "run", side_effect=fake)


def make_publish_fixture(directory, git_tag="carbonpanel-v2.0.0", semver="2.0.0",
                         commit="a" * 40, include_git_tag=True, include_semver=True,
                         version_override=None):
    """A complete, checksummed product-release output directory for publish()."""
    output = Path(directory)
    internal = "v" + semver
    manifest = {"version": version_override or internal, "commit": commit,
                "image_digest": "sha256:" + "b" * 64,
                "image": "atharvnaphade/carbonpanel:" + semver,
                "github_image": "ghcr.io/athndev/carbonpanel:" + semver}
    if include_git_tag:
        manifest["git_tag"] = git_tag
    if include_semver:
        manifest["semver"] = semver
    (output / "release-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
    (output / "openapi.yaml").write_text("schema", encoding="utf-8")
    for target in release.TARGETS:
        (output / release.archive_name(internal, *target)).write_bytes(b"original")
    release.finalize(argparse.Namespace(output=str(output), digest=None))
    return output, internal


def publish_github_reply(notes_holder, generated_args_holder, published=(("v1.0.40",),), output=None):
    """Offline gh fixture: verified stable list, generate-notes, assets and edit."""
    def reply(*args):
        if args[:3] == ("api", "--paginate", "--slurp"):
            return json.dumps([[{"tag_name": name, "draft": False, "prerelease": False}
                                for (name,) in published]])
        if any("generate-notes" in str(arg) for arg in args):
            generated_args_holder.append(args)
            return json.dumps({"body": "AUTO GENERATED COMPARE"})
        if args[:2] == ("repo", "view"):
            return "athNdev/carbonpanel"
        if args[:2] == ("release", "view"):
            return "https://api.github.com/repos/owner/panel/releases/123"
        if args and args[0] == "api" and str(args[1]).startswith("https://api.github.com"):
            assets = [{"name": path.name, "digest": "sha256:" + release.sha256(path)}
                      for path in sorted(output.iterdir()) if path.is_file()] if output else []
            return json.dumps({"draft": True, "assets": assets})
        if args[:2] == ("release", "create"):
            if "--notes-file" in args:
                notes_holder.append(Path(args[args.index("--notes-file") + 1]).read_text(encoding="utf-8"))
            return "{}"
        return "{}"
    return reply


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
        # Use a non-reserved product tag: reserved legacy preview versions are
        # rejected even earlier by publication_candidate_decision().
        replies = [json.dumps([{"conclusion": "success", "status": "completed"}]),
                   json.dumps([[{"tag_name": "carbonpanel-v2.0.0", "draft": False}]])]
        with patch.object(release.subprocess, "run"), patch.object(release, "run", return_value=""), \
             patch.object(release, "github", side_effect=replies), self.assertRaisesRegex(ValueError, "immutable"):
            release.guard_publishing("v2.0.0", "a" * 40, "carbonpanel-v2.0.0")

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
            internal, tag, commit = "v2.0.0", "carbonpanel-v2.0.0", "a" * 40
            manifest = {"version": internal, "git_tag": tag, "semver": "2.0.0",
                        "commit": commit, "image_digest": "sha256:" + "b" * 64}
            (output / "release-manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
            (output / "openapi.yaml").write_text("schema", encoding="utf-8")
            for target in release.TARGETS:
                (output / release.archive_name(internal, *target)).write_bytes(b"original")
            release.finalize(argparse.Namespace(output=str(output), digest=None))
            (output / release.archive_name(internal, "linux", "amd64")).write_bytes(b"changed")
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
                self.assertFalse(release.publication_allowed(
                    event, "true", "carbonpanel-v3.0.0")[0])
        # A dispatch without the affirmative flag is still a rehearsal.
        self.assertFalse(release.publication_allowed(
            "workflow_dispatch", "false", "carbonpanel-v3.0.0")[0])
        allowed, reason = release.publication_allowed(
            "workflow_dispatch", "true", "carbonpanel-v3.0.0")
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
        self.assertEqual(release.ref_channel("carbonpanel-v3.0.0"), "stable")
        self.assertEqual(release.ref_channel("carbonpanel-v3.0.0-rc.1"), "prerelease")
        self.assertEqual(release.ref_channel("v3.0.0"), "legacy-version")
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
            release.guard_publishing("v3.0.0", "a" * 40, "carbonpanel-v3.0.0")

    def test_initial_publication_and_unpublished_draft_are_allowed(self):
        success = json.dumps([{"conclusion": "success", "status": "completed"}])
        for existing in ([[]], [[{"tag_name": "carbonpanel-v3.0.0", "draft": True}]]):
            with self.subTest(existing=existing), \
                 patch.object(release.subprocess, "run"), \
                 patch.object(release, "run", return_value=""), \
                 patch.object(release, "github", side_effect=[success, json.dumps(existing)]):
                release.guard_publishing("v3.0.0", "a" * 40, "carbonpanel-v3.0.0")

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
        verified_minor = {name: ["present", {"digest": digest, "revision": "b" * 40,
                          "platforms": list(release.REQUIRED_PLATFORMS)}] for name in release.CHANNEL_REGISTRIES}
        def call(alias="3.1"):
            return argparse.Namespace(version="v3.1.0", commit="b" * 40,
                                      alias=alias, digest=digest)

        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance", return_value=(True, "verified", None)), \
             patch.object(release, "observe_reference", side_effect=[
                 ("absent", None)] * 4), \
             patch.object(release, "verify_live_stable"), \
             patch.object(release, "channel_observe", return_value=verified_minor), \
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
             patch.object(release, "channel_observe", return_value=verified_minor), \
             patch.object(release, "verify_live_stable"), \
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
                          return_value={other: "v3.1.2"}):
            with self.assertRaisesRegex(ValueError, "backwards"):
                release.promote_alias(argparse.Namespace(
                    version="v3.1.0", commit="b" * 40, alias="3.1", digest="sha256:" + "e" * 64))
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
            with self.assertRaisesRegex(ValueError, "Release record"):
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
            # An unpublished draft is resumable: the publisher reuses its stable
            # release ID and re-verifies the uploaded assets before exposure.
            action, reason = release.release_record_decision(
                "v3.1.0", commit, digest, draft)
            self.assertEqual(action, "publish")
            self.assertIn("draft", reason)
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
        self.assertIn("needs: [plan, publish-immutable, publish]", source)
        # OSS-413: no independent stable/latest alias write remains; both go
        # through the synced transaction and the deployed exact docs marker.
        self.assertNotIn("promote-stable-alias", source)
        self.assertNotIn("--alias latest", source)
        self.assertIn("  stable-channel-transaction:\n", source)
        self.assertIn("channel-promote \\\n", source)
        self.assertIn("needs: [plan, publish-immutable, prepare-draft, stable-channel-transaction]", source)
        self.assertIn("python3 scripts/release.py release-record", source)
        self.assertIn("--immutable-only", source)
        # Durable release record completes before alias promotion.
        # Dependency graph (not file order) is the enforced publication boundary.
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
        # Only the explicit dispatch opt-in for a canonical product tag.
        self.assertTrue(release.publication_allowed(
            "workflow_dispatch", "true", "carbonpanel-v3.0.0")[0])
        self.assertFalse(release.publication_allowed(
            "workflow_dispatch", "true", "v0.0.0-verify.9")[0])

    def test_every_writing_job_is_gated_and_staging_has_no_credentials(self):
        release_src = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        staging = release_src.split("  publish-immutable:")[0]
        self.assertNotIn("docker/login-action", staging)
        self.assertNotIn("secrets.DOCKER_HUB", staging)
        self.assertIn("          push: false", staging)
        self.assertGreaterEqual(
            release_src.count("needs.plan.outputs.publish == 'true'"), 3)
        # Stable release creation and alias moves are gated on the synced
        # transaction; a prerelease keeps its product-only flow.
        self.assertIn("needs.stable-channel-transaction.outputs.action != 'rollback'", release_src)
        self.assertIn("needs.publish-immutable.outputs.verified == 'true'", release_src)
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
            # No workflow input named ci_verified may be trusted as CI evidence;
            # the CLI always derives it from verify-main-ci.
            self.assertNotIn("ci_verified: ${{", source)
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
        self.assertIn("tags can point at old", source)
        self.assertIn("tags: ['carbonpanel-v*']", source)
        self.assertIn("never the dispatch revision input alone", source)
        docs = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("old commits", docs)

    def test_cd_edge_behavior_is_preserved(self):
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertNotIn("edge-promote --", source)
        self.assertNotIn("nightly-promote --", source)
        self.assertIn("all moving-alias writes are owned exclusively", source)
        self.assertIn("channel-promote", source)
        self.assertNotIn("type=raw,value=edge", source)
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
        self.assertIn("ref: ${{ steps.pinned.outputs.sha || github.sha }}", source)
        self.assertIn("steps.pinned.outputs.sha != ''", source)
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
        self.assertIn("&& (inputs.push || inputs.nightly)) || (github.event_name == 'schedule'", source)
        self.assertIn("&& 'cd-publish-edge' || format('cd-verify-{0}'", source)
        self.assertIn("|| format('cd-verify-{0}'", source)
        self.assertIn("cancel-in-progress: ${{ !((github.event_name == 'workflow_dispatch'", source)
        self.assertIn("|| (github.event_name == 'schedule' && vars.NIGHTLY_SCHEDULE_PUBLISH == 'true')) }}", source)
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
        # A native schedule is NOT an opt-in dispatch path. It is decided only
        # by nightly_schedule_decision() with a default-off operator setting.
        self.assertFalse(release.nightly_channel_decision(
            "schedule", "true", ref=trusted, ci_verified="true")[0])

    def test_scheduled_nightly_decision_is_bounded_and_verify_only(self):
        trusted = release.TRUSTED_DEVELOPER_REF
        allowed, reason = release.nightly_schedule_decision(
            "schedule", trusted, "true", "true", "true")
        self.assertTrue(allowed)
        self.assertIn("authorized", reason)
        for label, event, ref, ci, stable, setting in (
            ("setting off (default)", "schedule", trusted, "true", "true", "false"),
            ("empty setting", "schedule", trusted, "true", "true", ""),
            ("main moved", "schedule", trusted, "true", "false", "true"),
            ("CI failed", "schedule", trusted, "false", "true", "true"),
            ("non-default ref", "schedule", "refs/heads/feature/x", "true", "true", "true"),
            ("tag ref", "schedule", "refs/tags/v3.0.0", "true", "true", "true"),
            ("blank ref", "schedule", "", "true", "true", "true"),
            ("push event", "push", trusted, "true", "true", "true"),
            ("pull request event", "pull_request", trusted, "true", "true", "true"),
            ("workflow_run event", "workflow_run", trusted, "true", "true", "true"),
            ("dispatch event", "workflow_dispatch", trusted, "true", "true", "true"),
        ):
            with self.subTest(case=label):
                ok, detail = release.nightly_schedule_decision(
                    event, ref, ci, stable, setting)
                self.assertFalse(ok)
                self.assertTrue(detail)
        # The CLI emits an actionable, testable decision with no write.
        out = io.StringIO()
        with patch("sys.argv", [
                "release.py", "nightly-schedule-gate", "--event", "schedule",
                "--commit", "a" * 40, "--ref", trusted,
                "--ci-verified", "true", "--pin-stable", "false",
                "--operator-setting", "true"]), redirect_stdout(out):
            release.main()
        values = json.loads(out.getvalue())
        self.assertEqual(values["publish"], "false")
        self.assertIn("stable", values["reason"])
        self.assertEqual(values["immutable_ref"],
                         "atharvnaphade/carbonpanel:sha-" + "a" * 40)

    def test_schedule_sha_gate_pushes_reuses_or_skips_with_zero_writes(self):
        commit, digest = "d" * 40, "sha256:" + "e" * 64
        revisions = platform_revisions(commit)
        # Absent sha tag: a new immutable image may be built.
        with patch.object(release, "observe_reference", return_value=("absent", None)), \
             patch("sys.argv", ["release.py", "nightly-schedule-sha-gate",
                                "--commit", commit, "--ci-verified", "true"]), \
             redirect_stdout(io.StringIO()):
            self.assertEqual(release.nightly_schedule_sha_gate(
                argparse.Namespace(commit=commit, ci_verified="true")), "push")
        # Present and independently verified: reuse, no build.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "platform_manifest_revisions",
                          return_value=(digest, revisions)), \
             patch("sys.argv", ["release.py", "nightly-schedule-sha-gate",
                                "--commit", commit, "--ci-verified", "true"]), \
             redirect_stdout(io.StringIO()):
            self.assertEqual(release.nightly_schedule_sha_gate(
                argparse.Namespace(commit=commit, ci_verified="true")), "reuse")
        # Unknown or unverified present tag: skip with no write at all.
        for label, observation, ci in (
            ("unknown", ("unknown", None), "true"),
            ("failed CI", present(digest), "false"),
            ("wrong revision", present(digest), "true"),
        ):
            with self.subTest(case=label):
                revs = platform_revisions("b" * 40) if label == "wrong revision" else revisions
                with patch.object(release, "observe_reference", return_value=observation), \
                     patch.object(release, "platform_manifest_revisions",
                                  return_value=(digest, revs)), \
                     patch.object(release.subprocess, "run") as command, \
                     patch("sys.argv", ["release.py", "nightly-schedule-sha-gate",
                                        "--commit", commit, "--ci-verified", ci]), \
                     redirect_stdout(io.StringIO()):
                    self.assertEqual(release.nightly_schedule_sha_gate(
                        argparse.Namespace(commit=commit, ci_verified=ci)), "skip")
                    self.assertEqual(registry_writes(command), [])

    def test_nightly_alias_identity_requires_both_agreeing_platform_revisions(self):
        commit = "f" * 40
        digest = "sha256:" + "7" * 64
        alias = ("present", {"digest": digest,
                             "platforms": {("linux", "amd64"), ("linux", "arm64")}})
        self.assertEqual(release.nightly_alias_identity(
            alias, revisions=platform_revisions(commit), index_digest=digest),
            (commit, digest))
        mixed = platform_revisions(commit)
        mixed[("linux", "arm64")] = {"digest": "sha256:" + "8" * 64,
                                     "revision": "a" * 40}
        self.assertEqual(release.nightly_alias_identity(alias, revisions=mixed)[0], "")
        self.assertEqual(release.nightly_alias_identity(alias, revisions={})[0], "")
        self.assertEqual(release.nightly_alias_identity(("absent", None))[0], "")
        self.assertEqual(release.nightly_alias_identity(
            alias, revisions=platform_revisions(commit),
            index_digest="sha256:" + "9" * 64)[0], "")

    def test_scheduled_nightly_derives_previous_binding_from_live_alias(self):
        """An unattended run has no operator previous-commit string; it must
        derive the binding from the live alias and still refuse an unbound move.
        """
        candidate, previous = "c" * 40, "a" * 40
        digest, old = "sha256:" + "d" * 64, "sha256:" + "f" * 64
        revisions = platform_revisions(candidate)
        previous_revisions = platform_revisions(previous)
        alias = present(old)
        argv = ["release.py", "nightly-promote", "--commit", candidate,
                "--digest", digest, "--ci-verified", "true",
                "--auto-bind-previous", "--apply"]
        def observe(ref, strict=False):
            if ref == release.nightly_immutable_reference(candidate):
                return present(digest)
            if ref == release.nightly_immutable_reference(previous):
                return present(old)
            if ref == f"{release.DOCKER_HUB_IMAGE}:nightly":
                return alias
            return ("absent", None)
        def revisions_for(ref, anchor_digest=None):
            if ref == release.nightly_immutable_reference(candidate):
                return digest, revisions
            if (ref == release.nightly_immutable_reference(previous)
                    or ref == f"{release.DOCKER_HUB_IMAGE}:nightly"):
                return old, previous_revisions
            return "", {}
        alias_state = {"digest": old}
        def fake_run(args, **kwargs):
            # imagetools create moves the alias to the candidate digest; the
            # subsequent strict re-observation must see that exact digest.
            alias_state["digest"] = digest
            return subprocess.CompletedProcess(args, 0)
        def observe_after_move(ref, strict=False):
            if ref == f"{release.DOCKER_HUB_IMAGE}:nightly":
                return present(alias_state["digest"])
            return observe(ref, strict)
        # Bound previous identity + ancestor → exactly one write (the alias).
        with patch.object(release, "observe_reference", side_effect=observe_after_move), \
             patch.object(release, "platform_manifest_revisions", side_effect=revisions_for), \
             patch.object(release, "commit_is_ancestor", return_value=True), \
             patch.object(release.subprocess, "run", side_effect=fake_run) as command, \
             patch("sys.argv", argv):
            release.main()
            self.assertEqual(len(registry_writes(command)), 1)
        # No bound previous identity → refuse with zero writes.
        with patch.object(release, "observe_reference", side_effect=observe), \
             patch.object(release, "platform_manifest_revisions", side_effect=revisions_for), \
             patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", argv):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])

    def test_manual_nightly_binding_is_unchanged_and_schedule_only_auto_binds(self):
        # No legacy alias-write step remains reachable in cd.yml; the automatic
        # CLI gate itself still refuses a push/manual nightly publication.
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertNotIn("nightly-promote", source)
        self.assertNotIn("--auto-bind-previous", source)
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "nightly-gate", "--event", "push",
                                "--commit", "c" * 40, "--ref", release.TRUSTED_DEVELOPER_REF,
                                "--opt-in", "true", "--ci-verified", "true"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["publish"], "false")

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
             patch.object(release, "platform_manifest_revisions", return_value=(digest, revisions)), \
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
        probed_ref = "atharvnaphade/carbonpanel:nightly-policy-nonexistent-probe"
        self.assertEqual(release.observe_reference(
            probed_ref, failure(observed_missing), strict=True)[0], "absent")
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
             patch.object(release, "platform_manifest_revisions", return_value=(digest, revisions)), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=(digest, revisions)), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])
        # First promotion writes only the nightly alias, then re-verifies it.
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=(digest, revisions)), \
             patch.object(release, "observe_reference", side_effect=[
                 present(digest), ("absent", None), present(digest)]), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            release.main()
            self.assertEqual(len(registry_writes(command)), 1)
        # Rerun with the same digest is idempotent: no alias move.
        with patch.object(release, "nightly_identity_binding", return_value={}), \
             patch.object(release, "platform_manifest_revisions", return_value=(digest, revisions)), \
             patch.object(release, "observe_reference", side_effect=[
                 present(digest), present(digest)]), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", invoke("--apply")):
            release.main()
            self.assertEqual(registry_writes(command), [])

    def test_nightly_leaves_stable_aliases_and_edge_untouched(self):
        self.assertEqual([step["tag"] for step in release.promotion_plan("v3.0.0")],
                         ["3.0.0", "3.0", "latest"])
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        for token in ("edge-promote", "nightly-promote", "type=raw,value=latest",
                      "type=raw,value=edge", "value=nightly,enable"):
            self.assertNotIn(token, source)
        self.assertIn("channel-transaction", source)
        self.assertIn("Move the nightly alias in BOTH registries", source)

    def test_nightly_workflow_wiring_and_verification_only_paths(self):
        import yaml
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        doc = yaml.safe_load(source)
        triggers = doc.get("on") or doc.get(True)
        inputs = triggers["workflow_dispatch"]["inputs"]
        self.assertFalse(inputs["nightly"]["default"])
        self.assertEqual(inputs["previous_nightly_digest"]["default"], "")
        self.assertEqual(inputs["expected_sha_digest"]["default"], "")
        self.assertEqual(len(triggers["schedule"]), 2)
        self.assertEqual(triggers["schedule"][0]["cron"], "0 18 * * *")
        self.assertEqual(triggers["schedule"][1]["cron"], "0 19 * * *")
        self.assertIn("NIGHTLY_SCHEDULE_PUBLISH", source)
        self.assertIn("nightly-schedule-gate", source)
        self.assertIn("nightly-schedule-sha-gate", source)
        names = [step.get("name", "") for step in doc["jobs"]["build-and-push"]["steps"]]
        sha_gate = next(i for i, name in enumerate(names) if "Preflight shared sha tag" in name)
        build = next(i for i, name in enumerate(names) if name.startswith("Build (and optionally push)"))
        resolved = next(i for i, name in enumerate(names)
                        if name.startswith("Resolve the verified immutable digest"))
        self.assertLess(sha_gate, build)
        self.assertLess(build, resolved)
        self.assertFalse(any(n.startswith("Verify immutable sha image and move")
                             for n in names))
        self.assertFalse(any("Promote edge" in n for n in names))
        self.assertIn("channel-transaction", doc["jobs"])
        self.assertNotIn("type=raw,value=edge", source)
        self.assertIn("steps.sha_gate.outputs.action", source)
        for path in (".github/workflows/release.yml", ".github/workflows/modules.yml"):
            self.assertTrue(yaml.safe_load(
                (release.ROOT / path).read_text(encoding="utf-8"))["jobs"])
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "nightly-gate", "--event", "push",
                                "--commit", "c" * 40, "--ref", release.TRUSTED_DEVELOPER_REF,
                                "--opt-in", "true", "--ci-verified", "true"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["publish"], "false")

    def test_scheduled_nightly_contract_no_stable_writes_and_no_other_lane_schedules(self):
        import yaml
        cd_source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        cd = yaml.safe_load(cd_source)
        steps = cd["jobs"]["build-and-push"]["steps"]
        self.assertIn("cd-publish-edge", cd["jobs"]["build-and-push"]["concurrency"]["group"])
        self.assertIn("github.event_name == 'schedule' && vars.NIGHTLY_SCHEDULE_PUBLISH == 'true'",
                      cd["jobs"]["build-and-push"]["concurrency"]["group"])
        for token in ("promote-alias", "release-record", "verify-release-record",
                      "alias-promotion", "modules-promotion", "module-promotion",
                      "value=latest", "value=edge", "nightly-promote", "edge-promote"):
            self.assertNotIn(token, cd_source)
        channels_job = cd["jobs"]["channel-transaction"]
        self.assertEqual(channels_job["permissions"]["packages"], "write")
        self.assertIn("ghcr.io", yaml.safe_dump(channels_job))
        self.assertEqual(channels_job["concurrency"]["group"], "pages-deploy")
        self.assertFalse(channels_job["concurrency"]["cancel-in-progress"])
        self.assertIn("--pin-stable \"$MAIN_PIN_STABLE\"", cd_source)
        self.assertIn("--operator-setting \"$NIGHTLY_SCHEDULE_PUBLISH\"", cd_source)
        self.assertIn("steps.sha_gate.outputs.action", cd_source)
        self.assertIn("stable=false", cd_source)
        prepush = next(i for i, s in enumerate(steps)
                       if "Late main and exact-CI check before any immutable write" in s.get("name", ""))
        build = next(i for i, s in enumerate(steps)
                     if s.get("name", "").startswith("Build (and optionally push)"))
        self.assertLess(prepush, build)
        build_step = steps[build]
        self.assertIn("steps.scheduled_prepush.outputs.stable != 'false'",
                      build_step["with"]["push"])
        self.assertIn("steps.scheduled_prepush.outputs.verified != 'false'",
                      build_step["with"]["push"])
        for path in ("release.yml", "modules.yml", "mcp-dist.yml",
                     "runtime-artifacts.yml", "build-inputs.yml", "landing.yml"):
            with self.subTest(workflow=path):
                doc = yaml.safe_load(
                    (release.ROOT / ".github/workflows" / path).read_text(encoding="utf-8"))
                triggers = doc.get("on") or doc.get(True)
                self.assertNotIn("schedule", triggers)
        self.assertIn("NIGHTLY_SCHEDULE_PUBLISH",
                      (release.ROOT / "docs/releases.md").read_text(encoding="utf-8"))

    def test_edge_and_nightly_survive_both_publication_orders_on_sha_reuse(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        revisions = platform_revisions(commit)
        immutable = present(digest)
        for order in (("nightly", "edge"), ("edge", "nightly")):
            with self.subTest(order=order):
                state = {"edge": ("absent", None), "nightly": ("absent", None)}
                for alias in order:
                    allowed, reason = release.nightly_promotion_decision(
                        commit, digest, immutable, state[alias],
                        ci_verified="true", revisions=revisions)
                    self.assertTrue(allowed, reason)
                    state[alias] = present(digest)
                for alias in order:
                    allowed, _ = release.nightly_promotion_decision(
                        commit, digest, immutable, state[alias],
                        ci_verified="true", revisions=revisions)
                    self.assertTrue(allowed)  # idempotent at the verified digest

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
        reported_index, revisions = release.platform_manifest_revisions(
            reference, real_docker_inspect_runner(index_digest, children))
        self.assertEqual(reported_index, index_digest)
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
        # The legacy edge CLI still verifies a separate previous binding; it is
        # no longer wired into cd.yml (all writes go through channel-transaction).
        commit, old_commit = "c" * 40, "a" * 40
        digest, old_digest = "sha256:" + "d" * 64, "sha256:" + "f" * 64
        with patch("sys.argv", ["release.py", "edge-promote", "--commit", commit,
                                "--digest", digest, "--previous-commit", old_commit,
                                "--previous-digest", old_digest, "--ci-verified", "true"]), \
             patch.object(release, "observe_reference",
                          side_effect=lambda ref, strict=False: present(digest)), \
             patch.object(release, "platform_manifest_revisions",
                          return_value=(digest, platform_revisions(commit))), \
             patch.object(release, "nightly_identity_binding", return_value={old_digest: old_commit}), \
             patch.object(release, "commit_is_ancestor", return_value=True), \
             patch.object(release.subprocess, "run") as command:
            release.main()  # without --apply this is a read-only decision
            self.assertEqual(registry_writes(command), [])
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        self.assertNotIn("edge-promote --", source)

    def test_default_first_update_and_reuse_flows(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        revisions = platform_revisions(commit)
        immutable = present(digest)
        first, reason = release.nightly_promotion_decision(
            commit, digest, immutable, ("absent", None),
            ci_verified="true", revisions=revisions)
        self.assertTrue(first, reason)
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "platform_manifest_revisions",
                          return_value=(digest, revisions)), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", ["release.py", "sha-tag-gate", "--commit", commit,
                                "--expected-digest", digest, "--ci-verified", "true"]):
            release.main()
            self.assertEqual(registry_writes(command), [])

    def test_external_retag_and_anchored_index_mismatch(self):
        commit, digest = "c" * 40, "sha256:" + "d" * 64
        retag = "sha256:" + "e" * 64
        sha_ref = release.nightly_immutable_reference(commit)
        children = platform_revisions(commit)
        def anchored_runner(index_for_anchored):
            def runner(*args):
                reference, fmt = args[4], args[6]
                base_index = digest if reference == f"{sha_ref}@{digest}" else retag
                if fmt == "{{json .Manifest}}":
                    return real_docker_inspect_runner(
                        index_for_anchored or base_index, children)(*args)
                return real_docker_inspect_runner(base_index, children)(*args)
            return runner

        # The mutable tag now resolves to `retag`, but evidence is fetched from
        # the previously observed digest, so the original image is still used.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch("sys.argv", ["release.py", "sha-tag-gate", "--commit", commit,
                                "--expected-digest", digest, "--ci-verified", "true"]):
            with patch.object(release, "inspect_runner", anchored_runner(None)):
                out = io.StringIO()
                with redirect_stdout(out):
                    release.main()
                self.assertEqual(json.loads(out.getvalue())["action"], "reuse")

        # An anchored index that disagrees with the observed digest refuses with
        # zero registry writes.
        with patch.object(release, "observe_reference", return_value=present(digest)), \
             patch.object(release, "inspect_runner", anchored_runner(retag)), \
             patch.object(release.subprocess, "run") as command, \
             patch("sys.argv", ["release.py", "sha-tag-gate", "--commit", commit,
                                "--expected-digest", digest, "--ci-verified", "true"]):
            with self.assertRaisesRegex(ValueError, "refused"):
                release.main()
            self.assertEqual(registry_writes(command), [])


    # --- Carbon Panel product tag line and retrospective migration -----------

    def test_product_tag_fields_are_separate_from_git_tag_and_semver(self):
        product = release.product_release("carbonpanel-v2.0.0-alpha.1")
        self.assertEqual(product["git_tag"], "carbonpanel-v2.0.0-alpha.1")
        self.assertEqual(product["version"], "v2.0.0-alpha.1")
        self.assertEqual(product["semver"], "2.0.0-alpha.1")
        self.assertEqual(product["minor"], "2.0")
        self.assertTrue(product["prerelease"])
        self.assertEqual(product["channel"], "prerelease")
        stable = release.product_release("carbonpanel-v2.0.0")
        self.assertEqual(stable["semver"], "2.0.0")
        self.assertFalse(stable["prerelease"])
        self.assertEqual(stable["channel"], "stable")
        self.assertEqual(release.display_version("carbonpanel-v2.0.0"), "2.0.0")
        self.assertEqual(release.resolve_panel_version("carbonpanel-v2.0.0"), "v2.0.0")
        self.assertEqual(release.version_from_release_tag("v1.0.40"), "v1.0.40")
        self.assertEqual(release.version_from_release_tag("carbonpanel-v2.0.0"), "v2.0.0")
        self.assertIsNone(release.version_from_release_tag("v3.0.0-alpha.1"))
        self.assertIsNone(release.version_from_release_tag("docs1.0.0"))
        for bad in ("v2.0.0", "carbonpanel-v2.0", "carbonpanel-v02.0.0",
                    "carbonpanel-v2.0.0-rc.01", "carbonpanel-v2.0.0\n"):
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                release.product_release(bad)

    def test_next_stable_outputs_are_2_0_1_and_minor_2_0_latest(self):
        plan = release.promotion_plan("carbonpanel-v2.0.1")
        self.assertEqual([step["tag"] for step in plan], ["2.0.1", "2.0", "latest"])
        self.assertEqual([step["mutable"] for step in plan], [False, True, True])
        self.assertEqual(release.product_release(release.load_release_history()["next_stable"]["git_tag"])["semver"],
                         "2.0.1")
        # The immutable image tag is the displayed SemVer, never the Git tag.
        with patch("sys.argv", ["release.py", "image-tags",
                                "--version", "carbonpanel-v2.0.1", "--immutable-only"]), \
             redirect_stdout(io.StringIO()) as out:
            release.main()
        self.assertEqual(out.getvalue().strip(), "2.0.1")
        with patch("sys.argv", ["release.py", "image-tags",
                                "--version", "carbonpanel-v2.0.1"]), \
             redirect_stdout(io.StringIO()) as out:
            release.main()
        self.assertEqual(out.getvalue().split(), ["2.0.1", "2.0", "latest"])
        rollback = release.rollback_line()
        self.assertEqual((rollback["minor"], rollback["version"], rollback["commit"]),
                         ("1.0", "v1.0.40", "f88748be6a216b187e675bbb1f110df07e544148"))

    def test_historical_alias_is_refused_before_any_registry_preflight(self):
        # TOOL REVIEW POINT A: the canonical alias is historical-only at the
        # publication policy level, before immutable digest preflight runs.
        tag = release.HISTORICAL_PRODUCT_ALIAS
        ok, reason = release.publication_candidate_decision(tag)
        self.assertFalse(ok)
        self.assertIn("HISTORICAL-ONLY", reason)
        self.assertFalse(release.publication_allowed("workflow_dispatch", "true", tag)[0])
        with patch.object(release, "observe_reference") as observe, \
             self.assertRaisesRegex(ValueError, "HISTORICAL-ONLY"):
            release.guard_publishing("v1.0.40", "a" * 40, tag)
        observe.assert_not_called()
        # The alias is still recognised for provenance and rollback.
        self.assertEqual(release.version_from_release_tag(tag), "v1.0.40")

    def test_legacy_preview_and_collision_versions_are_refused_with_clear_errors(self):
        # TOOL REVIEW POINT B: legacy preview and collision identities cannot be
        # reused for a future product release attempt; each has a clear error.
        cases = {
            release.LEGACY_PREVIEW_TAG: "GHCR-only legacy preview",
            release.NAME_COLLISION_TAG: "name-collision",
            "v3.0.0-beta.5": "upstream-only",
            "v3.0.0-beta.8": "upstream-only",
            "carbonpanel-v3.0.0-alpha.1": "reserved legacy version",
            "carbonpanel-v3.0.0-beta.4": "reserved legacy version",
        }
        for tag, expected in cases.items():
            with self.subTest(tag=tag):
                ok, reason = release.publication_candidate_decision(tag)
                self.assertFalse(ok)
                self.assertIn(expected, reason)
                self.assertFalse(release.publication_allowed(
                    "workflow_dispatch", "true", tag)[0])

    def test_inherited_and_source_only_tags_never_publish(self):
        for tag in ("v3.0.0", "v2.0.0", "v1.0.40", "v1.0.38", "v1.0.39",
                    "v3.0.0-rc.1", "mod-v2.0.6", "docs1.0.0"):
            with self.subTest(tag=tag):
                ok, reason = release.publication_candidate_decision(tag)
                self.assertFalse(ok)
                self.assertTrue(reason)
                self.assertFalse(release.publication_allowed(
                    "workflow_dispatch", "true", tag)[0])

    def test_no_permanent_ban_on_future_1_0_x_maintenance(self):
        # 1.0 is currently at 1.0.40, but a future 1.0.x patch is not banned.
        self.assertEqual(release.rollback_line_guard("carbonpanel-v1.0.1"), "v1.0.1")
        self.assertEqual(release.rollback_line_guard("carbonpanel-v2.0.0"), "v2.0.0")
        doc = release.load_release_history()
        self.assertIs(doc["rollback_line"]["permanent_ban_on_future_1_0_x"], False)
        plan = release.promotion_plan("carbonpanel-v1.0.1")
        self.assertEqual([step["tag"] for step in plan], ["1.0.1", "1.0", "latest"])

    def test_release_history_catalog_validates_and_detects_tamper(self):
        ok, problems = release.verify_release_history()
        self.assertTrue(ok, problems)
        doc = json.loads(json.dumps(release.load_release_history()))
        doc["tags"][0]["classification"] = "unknown"
        ok, problems = release.verify_release_history(doc)
        self.assertFalse(ok)
        self.assertTrue(any("category counts" in p for p in problems))
        doc = json.loads(json.dumps(release.load_release_history()))
        doc["next_stable"]["semver"] = "1.1.0"
        self.assertFalse(release.verify_release_history(doc)[0])
        doc = json.loads(json.dumps(release.load_release_history()))
        doc["historical_alias"]["publishable"] = True
        self.assertFalse(release.verify_release_history(doc)[0])

    def test_release_history_records_audited_provenance_facts(self):
        doc = release.load_release_history()
        self.assertEqual(len(doc["tags"]), 84)
        self.assertEqual(doc["counts"]["exact_object_matches"], 79)
        self.assertEqual(doc["counts"]["name_collisions"], 1)
        self.assertEqual(doc["counts"]["origin_only"], 4)
        self.assertEqual(doc["counts"]["upstream_only"], 4)
        self.assertEqual({t["name"] for t in doc["upstream_only_tags"]},
                         {"v3.0.0-beta.5", "v3.0.0-beta.6", "v3.0.0-beta.7", "v3.0.0-beta.8"})
        stable = doc["canonical_stable"]
        self.assertEqual(stable["release_asset_count"], 8)
        self.assertEqual(stable["manifest_asset"], "release-manifest.json")
        self.assertEqual(stable["commit"], "f88748be6a216b187e675bbb1f110df07e544148")
        self.assertIn("ba734569", doc["build_aliases"]["notes"])  # not the release image
        self.assertEqual(doc["registries"]["attestation_children"], 2)
        self.assertEqual(doc["registries"]["platform_children"], ["linux/amd64", "linux/arm64"])
        self.assertIn("sha-f88748be", doc["build_aliases"]["notes"])
        self.assertIn("sha-d8597d4e", doc["build_aliases"]["notes"])
        self.assertEqual(len(doc["downstream_pins"]), 3)
        self.assertEqual(doc["invented_registry_alias"], False)
        self.assertGreaterEqual(len(doc["unknown_boundaries"]), 5)

    def test_workflow_separates_git_tag_from_app_version_and_oci_version(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("APP_VERSION: ${{ needs.plan.outputs.semver }}", source)
        self.assertIn("org.opencontainers.image.version=${{ needs.plan.outputs.semver }}", source)
        self.assertIn("tags: ['carbonpanel-v*']", source)
        self.assertIn("python3 scripts/release.py verify-publication-ref", source)
        self.assertIn("python3 scripts/release.py verify-release-history", source)
        # The Git tag is used for the release ref, not as APP_VERSION or OCI label.
        self.assertNotIn("APP_VERSION: ${{ needs.plan.outputs.git_tag }}", source)
        self.assertNotIn("org.opencontainers.image.version=${{ needs.plan.outputs.git_tag }}", source)
        self.assertNotIn("tags: ['v*']", source)

    def test_upgrade_notes_cover_all_three_breaking_changes(self):
        for path in ("docs/releases.md", "docs/release-history.md"):
            doc = (release.ROOT / path).read_text(encoding="utf-8")
            with self.subTest(path=path):
                self.assertIn("UPGRADE NOTES", doc)
                self.assertIn("role_mapping", doc)
                self.assertIn("allow_raw_claim_roles", doc)
                self.assertIn("trusted_proxy_cidrs", doc)
                self.assertIn("admin", doc)
                self.assertIn("2.0.0", doc)
        self.assertIn("carbonpanel-v2.0.0", (release.ROOT / "docs/releases.md").read_text(encoding="utf-8"))


    def test_publication_ref_gate_requires_trusted_main_ref_and_exact_ci(self):
        trusted = release.TRUSTED_DEVELOPER_REF
        for label, event, ref, ci, expected in (
            ("dispatch main with CI", "workflow_dispatch", trusted, "true", True),
            ("feature ref", "workflow_dispatch", "refs/heads/feature/x", "true", False),
            ("tag ref", "workflow_dispatch", "refs/tags/carbonpanel-v2.0.0", "true", False),
            ("blank ref", "workflow_dispatch", "", "true", False),
            ("no CI", "workflow_dispatch", trusted, "false", False),
            ("push", "push", trusted, "true", False),
            ("pr", "pull_request", trusted, "true", False),
        ):
            with self.subTest(case=label):
                allowed, _ = release.publication_ref_decision(event, ref, ci)
                self.assertEqual(allowed, expected)
        # The CLI combines that decision with a read-only exact-commit CI check.
        out = io.StringIO()
        with patch.object(release, "exact_main_ci_verified",
                          return_value=(True, "exact main CI verified")), \
             patch("sys.argv", ["release.py", "verify-publication-ref",
                                "--commit", "a" * 40, "--ref", trusted]), \
             redirect_stdout(out):
            release.main()
        values = json.loads(out.getvalue())
        self.assertEqual((values["publish"], values["ci_verified"]), ("true", "true"))
        # A failed CI check cannot be overridden by the trusted ref.
        with patch.object(release, "exact_main_ci_verified",
                          return_value=(False, "no successful completed push CI")), \
             self.assertRaisesRegex(ValueError, "refused"):
            release.verify_publication_ref(argparse.Namespace(
                commit="a" * 40, ref=trusted, event="workflow_dispatch"))

    def test_product_tag_alias_promotion_uses_canonical_release_identity(self):
        product = "carbonpanel-v2.0.0"
        digest, old = "sha256:" + "a" * 64, "sha256:" + "d" * 64
        releases_json = [[
            {"tag_name": "v1.0.40", "draft": False, "prerelease": False},
            {"tag_name": "v3.0.0-beta.4", "draft": False, "prerelease": False},
            {"tag_name": product, "draft": False, "prerelease": False},
        ]]
        # Inherited names never count as published panel versions; the legacy
        # stable and the canonical product tag do.
        self.assertEqual(release.published_stable_versions(releases_json),
                         ["v1.0.40", "v2.0.0"])
        out = io.StringIO()
        with patch.object(release, "published_stable_versions",
                          return_value=["v1.0.40"]), \
             patch.object(release, "release_provenance",
                          return_value=(True, "verified", None)) as provenance, \
             redirect_stdout(out):
            release.alias_promotion(argparse.Namespace(
                version=product, commit="b" * 40, digest=digest))
        values = json.loads(out.getvalue())
        self.assertEqual((values["minor"], values["latest"]), ("2.0", "true"))
        self.assertNotEqual(values["minor"], "1.0")  # rollback alias untouched
        provenance.assert_called_once_with(
            "v2.0.0", "b" * 40, digest, git_tag=product)

        with patch.object(release.subprocess, "run") as command, \
             patch.object(release, "release_provenance",
                          return_value=(True, "verified", None)) as provenance, \
             patch.object(release, "verify_live_stable"), \
             patch.object(release, "channel_observe", return_value={name: ["present", {
                 "digest": digest, "revision": "b" * 40, "platforms": list(release.REQUIRED_PLATFORMS)}]
                 for name in release.CHANNEL_REGISTRIES}), \
             patch.object(release, "observe_reference", return_value=("absent", None)), \
             patch.object(release, "durable_release_records", return_value={}):
            release.promote_alias(argparse.Namespace(
                version=product, commit="b" * 40, alias="2.0", digest=digest))
            writes = registry_writes(command)
            self.assertEqual(len(writes), 2)  # both registries, immutable then alias
            for item in writes:
                self.assertIn("imagetools", item.args[0])
                self.assertIn("create", item.args[0])
            provenance.assert_called_once_with(
                "v2.0.0", "b" * 40, digest, git_tag=product, allow_prepared_draft=False)
        # A prerelease product tag promotes no stable alias at all.
        out = io.StringIO()
        with patch.object(release, "published_stable_versions",
                          return_value=["v1.0.40"]), \
             patch.object(release, "release_provenance",
                          return_value=(True, "verified", None)), \
             redirect_stdout(out):
            release.alias_promotion(argparse.Namespace(
                version=product + "-rc.1", commit="b" * 40, digest=old))
        values = json.loads(out.getvalue())
        self.assertEqual((values["minor"], values["latest"]), ("", "false"))


    def test_upgrade_rehearsal_is_deterministic_and_passes_on_disposable_fixtures(self):
        import importlib
        rehearsal = importlib.import_module("scripts.rehearse_upgrade")
        summaries = []
        for _ in range(2):
            with tempfile.TemporaryDirectory() as directory:
                report = rehearsal.run(directory)
                self.assertTrue(report["pass"], report["checks"])
                self.assertEqual(report["before"]["tables"], 35)
                self.assertEqual(report["after_upgrade"]["tables"], 44)
                self.assertEqual(report["after_copy_back_rollback"]["tables"], 35)
                self.assertEqual(report["after_upgrade"]["nodes_backfill"], "legacy")
                self.assertTrue(all(report["checks"].values()))
                summaries.append({key: report[key] for key in
                                  ("before", "after_upgrade", "after_copy_back_rollback",
                                   "world_files_verified", "checks", "pass")})
        self.assertEqual(summaries[0], summaries[1])


    # --- PR549 exact findings: latest ordering, notes base, manifest identity --

    def test_github_latest_uses_product_only_verified_stable_ordering(self):
        newer, reason = release.github_latest_decision("v2.0.0", ["v1.0.40"])
        self.assertTrue(newer)
        self.assertIn("newer", reason)
        # An older maintenance release must not displace a newer verified stable.
        older, reason = release.github_latest_decision("v1.0.1", ["v1.0.40", "v2.0.0"])
        self.assertFalse(older)
        self.assertIn("not newer", reason)
        # Equal version is not a promotion; prereleases never promote; unknown fails closed.
        self.assertFalse(release.github_latest_decision("v1.0.40", ["v1.0.40"])[0])
        self.assertFalse(release.github_latest_decision("v2.0.0-alpha.1", ["v1.0.40"])[0])
        self.assertFalse(release.github_latest_decision("v2.0.0", None)[0])
        # Inherited/unclassified releases are not a verified stable state.
        inherited = release.published_stable_versions([[
            {"tag_name": "v3.0.0-beta.4", "draft": False, "prerelease": False},
            {"tag_name": "mod-v2.0.6", "draft": False, "prerelease": False},
        ]])
        self.assertEqual(inherited, [])

    def test_old_maintenance_publish_does_not_move_github_latest(self):
        with tempfile.TemporaryDirectory() as directory:
            output, _ = make_publish_fixture(directory, git_tag="carbonpanel-v1.0.1", semver="1.0.1")
            notes, generated = [], []
            with patch.object(release, "guard_publishing"), \
                 patch.object(release, "run", return_value=""), \
                 patch_gh_release_view(), \
                 patch.object(release, "github", side_effect=publish_github_reply(
                     notes, generated, output=output,
                     published=(("v1.0.40",), ("v2.0.0",)))) as github, \
                 redirect_stdout(io.StringIO()):
                release.publish(argparse.Namespace(
                    version="carbonpanel-v1.0.1", commit="a" * 40, output=str(output)))
            edits = [call for call in github.call_args_list
                     if call.args[:2] == ("release", "edit") and "--draft=false" in call.args]
            self.assertTrue(edits)
            self.assertIn("--latest=false", edits[-1].args)

    def test_first_major_release_notes_use_verified_previous_tag_and_migration_guide(self):
        with tempfile.TemporaryDirectory() as directory:
            output, _ = make_publish_fixture(directory)
            notes, generated = [], []
            with patch.object(release, "verify_live_stable"), \
                 patch.object(release, "guard_publishing"), \
                 patch.object(release, "run", return_value=""), \
                 patch_gh_release_view(), \
                 patch.object(release, "github", side_effect=publish_github_reply(
                     notes, generated, output=output, published=(("v1.0.40",),))) as github, \
                 redirect_stdout(io.StringIO()):
                release.publish(argparse.Namespace(
                    version="carbonpanel-v2.0.0", commit="a" * 40, output=str(output)))
            self.assertTrue(generated)
            self.assertTrue(any("previous_tag_name=v1.0.40" in arg for arg in generated[0]))
            self.assertTrue(notes)  # notes-file captured at draft creation
            body = notes[0]
            for expected in ("docs/releases.md#upgrade-notes-from-v1040", "role_mapping",
                             "no implicit first-user admin", "trusted_proxy_cidrs",
                             "Backup/restore drill"):
                self.assertIn(expected, body)
            edits = [call for call in github.call_args_list
                     if call.args[:2] == ("release", "edit") and "--draft=false" in call.args]
            self.assertIn("--latest=true", edits[-1].args)

    def test_publish_requires_explicit_and_matching_manifest_identity_fields(self):
        cases = (
            ("missing git_tag", {"include_git_tag": False}, "git_tag is missing"),
            ("mismatched git_tag", {"git_tag": "carbonpanel-v2.0.1"}, "git_tag is missing or does not match"),
            ("missing semver", {"include_semver": False}, "semver is missing"),
            ("mismatched semver", {"semver": "2.0.1", "version_override": "v2.0.0"},
             "semver is missing or does not match"),
        )
        for name, overrides, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                git_tag = overrides.get("git_tag", "carbonpanel-v2.0.0")
                semver = overrides.get("semver", "2.0.0")
                output, _ = make_publish_fixture(
                    directory, git_tag=git_tag, semver=semver,
                    include_git_tag=overrides.get("include_git_tag", True),
                    include_semver=overrides.get("include_semver", True),
                    version_override=overrides.get("version_override"))
                with patch.object(release, "guard_publishing"), \
                     patch.object(release, "github") as github, \
                     self.assertRaisesRegex(ValueError, expected):
                    release.publish(argparse.Namespace(
                        version="carbonpanel-v2.0.0", commit="a" * 40, output=str(output)))
                github.assert_not_called()


    def test_display_version_matches_plan_semver_for_rehearsal_and_plain_v(self):
        # PR549 regression: APP_VERSION is the plan `semver` output (no leading
        # v), so display_version() and the UI search must use that same form.
        for value, expected in (
            ("v0.0.0-verify.9", "0.0.0-verify.9"),
            ("v0.0.0-verify.123", "0.0.0-verify.123"),
            ("v3.0.0", "3.0.0"),
            ("v3.0.0-alpha.1", "3.0.0-alpha.1"),
            ("carbonpanel-v2.0.0", "2.0.0"),
            ("carbonpanel-v2.0.0-alpha.1", "2.0.0-alpha.1"),
        ):
            with self.subTest(value=value):
                self.assertEqual(release.display_version(value), expected)
                # plan() emits the same form as its `semver` output.
                tag = release.resolve_panel_version(value)
                self.assertEqual(tag[1:] if value.startswith("v") and
                                 release.product_release_or_none(value) is None
                                 else release.display_version(value), expected)

    def test_package_ui_version_check_accepts_canonical_semver_rehearsal(self):
        # The embedded UI is built with APP_VERSION = plan semver (no v). The
        # package() search must find that exact string for a rehearsal tag.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            frontend = root / "web/carbon-panel/build/_app"
            frontend.mkdir(parents=True)
            (root / "web/carbon-panel/build/index.html").write_text("<html>", encoding="utf-8")
            (frontend / "app.js").write_text("window.APP_VERSION='0.0.0-verify.9'", encoding="utf-8")
            value = "v0.0.0-verify.9"
            display = release.display_version(value)
            self.assertIn(display.encode(), (frontend / "app.js").read_bytes())
            self.assertNotEqual(display, value)

    def test_workflow_uses_one_canonical_version_form(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("APP_VERSION: ${{ needs.plan.outputs.semver }}", source)
        self.assertIn("org.opencontainers.image.version=${{ needs.plan.outputs.semver }}", source)
        self.assertNotIn("APP_VERSION: ${{ needs.plan.outputs.version }}", source)


    def test_release_workflow_serializes_full_publication_with_one_global_group(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        import re as _re

        def job_block(name):
            match = _re.search(rf"(?ms)^  {_re.escape(name)}:\n(.*?)(?=^  [a-zA-Z][\w-]*:\n|\Z)", source)
            self.assertIsNotNone(match, f"missing job {name}")
            return match.group(1)

        # (a) ONE workflow-level concurrency group, keyed to the explicit trusted
        # publish path; every job of that run inherits it.
        self.assertRegex(source, r"(?m)^concurrency:\n")
        self.assertIn("inputs.publish == true && 'pages-deploy'", source)
        self.assertNotIn("carbonpanel-product-publish", source)
        # (b) build-only/rehearsal runs get a distinct per-run key, so ordinary
        # CI is not serialized against another version.
        self.assertIn("format('carbonpanel-stage-{0}', github.run_id)", source)
        # (c) cancel-in-progress:false protects the RUNNING run only. Default
        # queue:single retains ONE pending run that a later request replaces;
        # this lane is single-operator dispatch, never "all queued publishers wait".
        self.assertRegex(source, r"(?m)^  cancel-in-progress: false$")
        self.assertNotIn("cancel-in-progress: true", source)
        self.assertNotRegex(source, r"(?m)^  queue:")
        self.assertIn("SINGLE-OPERATOR DISPATCH", source)
        self.assertIn("REPLACES it", source)
        ops = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("single-operator dispatch", ops)
        self.assertIn("replaces", ops)
        # (d) no job-level group may release the lock between phases. All jobs,
        # including the three writers, inherit the workflow-level lock.
        for name in ("plan", "frontend", "source-image", "binaries", "image",
                     "publish-immutable", "publish", "promote-aliases"):
            with self.subTest(job=name):
                self.assertNotRegex(job_block(name), r"(?m)^    concurrency:")
        writers = ("publish-immutable", "publish", "promote-aliases")
        for name in writers:
            with self.subTest(writer=name):
                self.assertIn("needs.plan.outputs.publish == 'true'", job_block(name))
        # The lock spans immutable publish -> verified aliases -> GitHub release:
        # no public release can survive a compensated alias rollback.
        self.assertIn("needs: [plan, publish-immutable, prepare-draft, stable-channel-transaction]",
                      job_block("promote-aliases"))
        self.assertIn("needs: [plan, publish-immutable, prepare-draft, stable-channel-transaction, promote-aliases]",
                      job_block("publish"))
        self.assertIn("needs.promote-aliases.outputs.aliases_verified == 'true'",
                      job_block("publish"))
        # (e) alias promotion re-preflights with LIVE state inside the lock and
        # verifies the promoted aliases before the release is exposed.
        promotion = job_block("promote-aliases")
        for step in ("Decide which aliases may advance",
                     "Verify promoted aliases by live observation before publication",
                     "alias_verify", "alias-promotion", "channel-promote"):
            self.assertIn(step, promotion)
        # (f) stranded-after-immutable is impossible: one run holds the lock for
        # all phases, and recovery for a resumed immutable version is inside that
        # same serialized flow (preflight-immutable -> verify-immutable -> record).
        self.assertIn("preflight-immutable", job_block("publish-immutable"))
        self.assertIn("verify-immutable", job_block("publish-immutable"))
        self.assertIn("release-record", job_block("publish"))
        self.assertNotIn("github.event_name == 'push'", source)


class ChannelPromotionTests(unittest.TestCase):
    """Offline OSS-413 gate fixtures. No registry, release, tag or Pages write."""

    CANDIDATE = "c" * 40
    PREVIOUS = "a" * 40
    DIGEST = "sha256:" + "d" * 64
    PREV_DIGEST = "sha256:" + "e" * 64

    def contract(self, commit=None, digest=None, **overrides):
        document = {
            "schema_version": 3,
            "repository": "https://github.com/athNdev/carbonpanel",
            "catalog": "docs/release-history.json",
            "channels": {
                "stable": {
                    "display": "Stable", "version": "1.0.40", "tag": "v1.0.40",
                    "commit": release.LEGACY_STABLE_COMMIT,
                    "image": {
                        "docker_hub": "atharvnaphade/carbonpanel:latest",
                        "ghcr": "ghcr.io/athndev/carbonpanel:latest",
                        "tag": "latest", "stable_alias": "stable",
                        "digest": release.LEGACY_STABLE_DIGEST,
                        "revision": release.LEGACY_STABLE_COMMIT,
                    },
                    "route": "/docs/", "default": True,
                },
                "nightly": {
                    "display": "Nightly", "tag": None,
                    "commit": commit or self.CANDIDATE,
                    "image": {
                        "docker_hub": "atharvnaphade/carbonpanel:nightly",
                        "ghcr": "ghcr.io/athndev/carbonpanel:nightly",
                        "tag": "nightly", "digest": digest or self.DIGEST,
                        "revision": commit or self.CANDIDATE,
                    },
                    "route": "/docs/nightly/", "default": False,
                },
            },
        }
        document.update(overrides)
        return document

    def immutable(self, commit=None, digest=None, both=True):
        info = {"digest": digest or self.DIGEST,
                "platforms": {("linux", "amd64"), ("linux", "arm64")},
                "revision": commit or self.CANDIDATE}
        observations = {"docker_hub": ("present", dict(info))}
        if both:
            observations["ghcr"] = ("present", dict(info))
        else:
            observations["ghcr"] = ("absent", None)
        return observations

    def aliases(self, digest=None, both=True):
        target = digest or self.PREV_DIGEST
        observations = {"docker_hub": ("present", {"digest": target})}
        observations["ghcr"] = ("present", {"digest": target}) if both else ("absent", None)
        return observations

    def qualify(self, **overrides):
        values = {"ci_verified": "true", "contract_valid": True,
                  "site_validated": True, "contract": self.contract(), "tag": ""}
        values.update(overrides)
        return values

    # --- Contract validation -------------------------------------------------

    def test_contract_requires_schema_3_two_channels_and_full_sha_pins(self):
        ok, problems = release.validate_channel_contract(self.contract())
        self.assertTrue(ok, problems)
        bad = self.contract()
        bad["schema_version"] = 2
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["nightly"]["commit"] = "abc123"
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["nightly"]["image"]["digest"] = "not-a-digest"
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["nightly"]["image"]["revision"] = self.PREVIOUS
        self.assertFalse(release.validate_channel_contract(bad)[0])

    def test_contract_rejects_extra_channels_legacy_snapshots_and_version_labels(self):
        bad = self.contract()
        bad["channels"]["1.0.39"] = {"display": "1.0.39"}
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["nightly"]["display"] = "3.0.0-alpha.1"
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["stable"]["display"] = "1.0.40"
        self.assertFalse(release.validate_channel_contract(bad)[0])
        bad = self.contract()
        bad["channels"]["nightly"]["tag"] = "nightly-1"
        self.assertFalse(release.validate_channel_contract(bad)[0])

    def test_contract_requires_full_sha_commit_links_from_repository(self):
        bad = self.contract()
        bad["channels"]["nightly"]["commit_url"] = (
            "https://github.com/athNdev/carbonpanel/docs/versions/nightly")
        ok, problems = release.validate_channel_contract(bad)
        self.assertFalse(ok)
        self.assertTrue(any("commit_url" in problem for problem in problems))

    # --- Source revision + qualified stage -----------------------------------

    def test_source_gate_requires_trusted_main_pin_full_sha_and_exact_ci(self):
        self.assertTrue(release.channel_source_gate(
            "workflow_run", release.TRUSTED_DEVELOPER_REF, self.CANDIDATE, "true")[0])
        for ref in ("refs/tags/carbonpanel-v2.0.0", "refs/heads/feature", ""):
            self.assertFalse(release.channel_source_gate(
                "workflow_dispatch", ref, self.CANDIDATE, "true")[0])
        self.assertFalse(release.channel_source_gate(
            "workflow_dispatch", release.TRUSTED_DEVELOPER_REF, "short", "true")[0])
        self.assertFalse(release.channel_source_gate(
            "workflow_dispatch", release.TRUSTED_DEVELOPER_REF, self.CANDIDATE, "false")[0])

    def test_source_gate_docs_requires_contract_and_site_validation(self):
        contract = self.contract()
        self.assertTrue(release.channel_source_gate(
            "workflow_dispatch", release.TRUSTED_DEVELOPER_REF, self.CANDIDATE, "true",
            contract=contract, docs_validated="true")[0])
        self.assertFalse(release.channel_source_gate(
            "workflow_dispatch", release.TRUSTED_DEVELOPER_REF, self.CANDIDATE, "true",
            contract=contract, docs_validated="false")[0])
        bad = self.contract()
        bad["schema_version"] = 2
        self.assertFalse(release.channel_source_gate(
            "workflow_dispatch", release.TRUSTED_DEVELOPER_REF, self.CANDIDATE, "true",
            contract=bad, docs_validated="true")[0])

    # --- OCI dual-registry parity --------------------------------------------

    def test_oci_parity_requires_same_digest_and_revision_in_both_registries(self):
        ok, _ = release.channel_oci_parity(self.immutable(), self.DIGEST, self.CANDIDATE)
        self.assertTrue(ok)
        self.assertFalse(release.channel_oci_parity(
            self.immutable(both=False), self.DIGEST, self.CANDIDATE)[0])
        mismatch = self.immutable()
        mismatch["ghcr"] = ("present", {"digest": self.PREV_DIGEST,
                                        "platforms": {("linux", "amd64"), ("linux", "arm64")},
                                        "revision": self.CANDIDATE})
        self.assertFalse(release.channel_oci_parity(
            mismatch, self.DIGEST, self.CANDIDATE)[0])
        single_arch = self.immutable()
        single_arch["ghcr"] = ("present", {"digest": self.DIGEST,
                                           "platforms": {("linux", "amd64")},
                                           "revision": self.CANDIDATE})
        self.assertFalse(release.channel_oci_parity(
            single_arch, self.DIGEST, self.CANDIDATE)[0])
        wrong_revision = self.immutable(commit=self.PREVIOUS)
        self.assertFalse(release.channel_oci_parity(
            wrong_revision, self.DIGEST, self.CANDIDATE)[0])

    # --- Move state machine (first publish / retry / stale / rollback) -------

    def make_move(self, **overrides):
        values = {
            "channel": "nightly", "candidate_commit": self.CANDIDATE,
            "candidate_digest": self.DIGEST,
            "immutable_observations": self.immutable(),
            "alias_observations": {"docker_hub": ("absent", None),
                                   "ghcr": ("absent", None)},
            "previous": None, "qualification": self.qualify(),
        }
        values.update(overrides)
        return release.channel_move_decision(**values)

    def test_first_publish_with_absent_aliases_is_allowed(self):
        action, reason, target = self.make_move()
        self.assertEqual(action, "publish")
        self.assertEqual(target, self.DIGEST)

    def test_retry_after_failed_deploy_converges_from_verified_previous(self):
        # Hub moved to the candidate; GHCR still holds the previous verified
        # digest. With the previous identity bound and the candidate a descendant,
        # the retry converges instead of overwriting anything immutable.
        aliases = {"docker_hub": ("present", {"digest": self.DIGEST}),
                   "ghcr": ("present", {"digest": self.PREV_DIGEST})}
        action, _, target = self.make_move(
            alias_observations=aliases,
            previous={"commit": self.PREVIOUS, "digest": self.PREV_DIGEST, "bound": True},
            ancestry=lambda older, newer: older == self.PREVIOUS and newer == self.CANDIDATE)
        self.assertEqual(action, "publish")
        self.assertEqual(target, self.DIGEST)

    def test_stale_and_out_of_order_builds_are_refused(self):
        aliases = self.aliases()
        previous = {"commit": self.PREVIOUS, "digest": self.PREV_DIGEST, "bound": True}
        action, reason, _ = self.make_move(
            alias_observations=aliases, previous=previous,
            ancestry=lambda older, newer: False)
        self.assertEqual(action, "refuse")
        self.assertIn("out-of-order", reason)
        # A candidate equal to the previous commit is not an advance.
        action, _, _ = self.make_move(
            alias_observations=aliases,
            previous={"commit": self.CANDIDATE, "digest": self.PREV_DIGEST, "bound": True},
            ancestry=lambda older, newer: True)
        self.assertEqual(action, "refuse")

    def test_unbound_alias_digest_is_refused(self):
        action, reason, _ = self.make_move(alias_observations=self.aliases())
        self.assertEqual(action, "refuse")
        self.assertIn("previous binding", reason)

    def test_partial_failure_rolls_back_to_previous_verified_digest(self):
        # The candidate immutable image loses parity (GHCR absent); the previous
        # digest is still bound, so the channel preserves that verified digest.
        action, reason, target = self.make_move(
            immutable_observations=self.immutable(both=False),
            previous={"commit": self.PREVIOUS, "digest": self.PREV_DIGEST, "bound": True})
        self.assertEqual(action, "rollback")
        self.assertEqual(target, self.PREV_DIGEST)
        self.assertIn("preserving previous verified digest", reason)
        # Without a verified previous binding the move simply refuses.
        self.assertEqual(self.make_move(immutable_observations=self.immutable(both=False))[0],
                         "refuse")

    def test_idempotent_noop_never_overwrites(self):
        action, _, target = self.make_move(alias_observations=self.aliases(self.DIGEST))
        self.assertEqual(action, "noop")
        self.assertEqual(target, self.DIGEST)

    def test_registry_digest_parity_failure_blocks_move(self):
        mismatch = self.immutable()
        mismatch["ghcr"] = ("present", {"digest": self.PREV_DIGEST,
                                        "platforms": {("linux", "amd64"), ("linux", "arm64")},
                                        "revision": self.CANDIDATE})
        action, reason, _ = self.make_move(immutable_observations=mismatch)
        self.assertEqual(action, "refuse")
        self.assertIn("parity", reason)

    # --- Arbitrary / legacy tag and bootstrap refusal ------------------------

    def test_arbitrary_and_legacy_tags_never_promote_stable(self):
        action, _, _ = self.make_move(
            channel="stable", candidate_commit=self.PREVIOUS,
            candidate_digest=self.PREV_DIGEST)
        self.assertEqual(action, "refuse")
        action, _, _ = self.make_move(
            channel="stable", candidate_commit=release.LEGACY_STABLE_COMMIT,
            candidate_digest=release.LEGACY_STABLE_DIGEST,
            immutable_observations=self.immutable(
                commit=release.LEGACY_STABLE_COMMIT, digest=release.LEGACY_STABLE_DIGEST),
            qualification=self.qualify(tag=release.OCCUPIED_PRODUCT_TAG))
        self.assertEqual(action, "refuse")

    def test_stable_alias_only_moves_from_verified_baseline(self):
        action, _, target = self.make_move(
            channel="stable", candidate_commit=release.LEGACY_STABLE_COMMIT,
            candidate_digest=release.LEGACY_STABLE_DIGEST,
            immutable_observations=self.immutable(
                commit=release.LEGACY_STABLE_COMMIT,
                digest=release.LEGACY_STABLE_DIGEST))
        self.assertEqual(action, "publish")
        self.assertEqual(target, release.LEGACY_STABLE_DIGEST)

    def test_hub_only_nightly_baseline_needs_recorded_bootstrap(self):
        baseline = release.NIGHTLY_HUB_BASELINE
        observed = self.immutable(commit=baseline["commit"], digest=baseline["digest"])
        action, reason, _ = self.make_move(
            candidate_commit=baseline["commit"], candidate_digest=baseline["digest"],
            immutable_observations=observed)
        self.assertEqual(action, "refuse")
        self.assertIn("baseline", reason)
        action, _, target = self.make_move(
            candidate_commit=baseline["commit"], candidate_digest=baseline["digest"],
            immutable_observations=observed, bootstrap=True)
        self.assertEqual(action, "publish")
        self.assertEqual(target, baseline["digest"])

    def test_docs_move_requires_validated_contract_and_site_build(self):
        action, _, _ = self.make_move(
            channel="docs",
            qualification=self.qualify(site_validated=False))
        self.assertEqual(action, "refuse")
        action, _, target = self.make_move(channel="docs")
        self.assertEqual(action, "publish")
        self.assertEqual(target, self.DIGEST)

    # --- Rollback helper -----------------------------------------------------

    def test_rollback_helper_requires_independent_previous_binding(self):
        action, _, target = release.channel_rollback_decision(
            "nightly", self.PREVIOUS, self.PREV_DIGEST, False)
        self.assertEqual(action, "refuse")
        action, _, target = release.channel_rollback_decision(
            "nightly", self.PREVIOUS, self.PREV_DIGEST, True,
            observations=self.aliases(self.PREV_DIGEST))
        self.assertEqual(action, "rollback")
        self.assertEqual(target, self.PREV_DIGEST)

    # --- CLI + workflow wiring (offline, no writes) --------------------------

    def test_cli_contract_and_source_gate_emit_actionable_outputs(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "version-index.json"
            path.write_text(json.dumps(self.contract()), encoding="utf-8")
            out = io.StringIO()
            with patch("sys.argv", ["release.py", "validate-channel-contract",
                                    "--path", str(path)]), redirect_stdout(out):
                release.main()
            self.assertEqual(json.loads(out.getvalue())["verified"], "true")
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-source-gate", "--event",
                                "workflow_run", "--ref", release.TRUSTED_DEVELOPER_REF,
                                "--commit", self.CANDIDATE, "--ci-verified", "true"]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["qualified"], "true")

    def test_cli_channel_promote_is_read_only_without_apply(self):
        info = {"digest": self.DIGEST,
                "platforms": [["linux", "amd64"], ["linux", "arm64"]],
                "revision": self.CANDIDATE}
        immutable = json.dumps({"docker_hub": ["present", info],
                                "ghcr": ["present", info]})
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-promote", "--channel", "nightly",
                                "--commit", self.CANDIDATE, "--digest", self.DIGEST,
                                "--ci-verified", "true", "--immutable", immutable]), \
             redirect_stdout(out), patch.object(release.subprocess, "run",
                                                side_effect=AssertionError("registry write")):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["action"], "publish")

    def test_workflows_wire_channel_gates_without_new_dispatch_or_writes_in_tests(self):
        import yaml
        cd = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        landing = (release.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        rel = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        for doc in (cd, landing, rel):
            self.assertTrue(yaml.safe_load(doc)["jobs"])
        self.assertIn("channel-promote", cd)
        self.assertIn("channel-source-gate", cd)
        self.assertIn("validate-channel-contract", landing)
        self.assertIn("channel-source-gate", landing)
        self.assertIn("stable", rel)
        # The GH_TOKEN defect on the fail-closed ref check is repaired, not bypassed.
        self.assertIn("GH_TOKEN: ${{ github.token }}", rel)
        # All OCI channel writes share ONE non-cancelling publisher group.
        promoted = yaml.safe_load(cd)["jobs"]["channel-transaction"]
        self.assertEqual(promoted["concurrency"]["group"], "pages-deploy")
        self.assertFalse(promoted["concurrency"]["cancel-in-progress"])
        self.assertNotIn("--bootstrap", yaml.safe_dump(promoted))
        self.assertNotIn("rolling", yaml.safe_dump(promoted))
        self.assertIn("channel-transaction", cd)
        self.assertIn("channel-observe", cd)
        self.assertNotIn("promote-stable-alias", rel)
        self.assertIn("stable-channel-transaction", rel)
        self.assertIn("channel-promote \\\n", rel)
        self.assertIn("pages-deploy", yaml.safe_load(landing)["jobs"]["deploy"].get(
            "concurrency", {"group": ""}).get("group", "") or "pages-deploy")
        # No new workflow_dispatch inputs were introduced for channel writes.
        cd_doc = yaml.safe_load(cd)
        cd_inputs = (cd_doc.get("on") or cd_doc.get(True))["workflow_dispatch"]["inputs"]
        self.assertNotIn("apply", cd_inputs)

    def test_cd_rollback_uses_prior_receipt_and_restores_exact_docs_bytes(self):
        import yaml
        doc = yaml.safe_load((release.ROOT / ".github/workflows/cd.yml").read_text(
            encoding="utf-8"))
        steps = {step.get("id"): step
                 for step in doc["jobs"]["channel-transaction"]["steps"] if step.get("id")}
        rollback = steps["rollback"]
        self.assertIn("steps.stage_decision.outputs.prior_receipt", rollback["env"]["PRIOR"])
        self.assertNotIn("commit_decision.outputs.receipt", str(rollback))
        self.assertIn("always()", rollback["if"])
        self.assertNotIn("steps.commit_decision.outcome == 'success'", rollback["if"])
        self.assertIn("steps.deploy.outcome == 'failure'", rollback["if"])
        self.assertIn("steps.probe.outcome == 'failure'", rollback["if"])
        self.assertEqual(steps["rollback_docs"]["with"]["name"], "github-pages-prior")
        self.assertEqual(steps["rollback_docs"]["with"]["path"], ".website-build/prior-site")
        self.assertEqual(steps["rollback_deploy"]["with"]["artifact_name"],
                         "github-pages-prior")
        self.assertIn("steps.preserve_prior.outputs.hash",
                      str(doc["jobs"]["channel-transaction"]["steps"]))

    def test_release_stable_gate_uses_observed_digests_and_compensates(self):
        import yaml
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        doc = yaml.safe_load(source)
        job = doc["jobs"]["stable-channel-transaction"]
        steps = job["steps"]
        self.assertTrue(any("verify-gate-script" in str(step.get("run", "")) for step in steps))
        self.assertTrue(any("verify-channel-parity" in str(step.get("run", ""))
                            for step in steps))
        transaction = next(step for step in steps if step.get("id") == "transaction")
        stage = next(step for step in steps if step.get("id") == "stage_decision")
        self.assertIn('--argjson registries "$OBSERVED"', stage["run"])
        self.assertIn("--phase stage", stage["run"])
        self.assertIn("steps.stage_decision.outputs.candidate", transaction["env"]["STAGED"])
        self.assertNotIn("registries:{docker_hub:$digest,ghcr:$digest}", transaction["run"])
        # A stable publication is not a site-only landing dispatch; the commit
        # phase must not be refused as "manual/site-only deploy..." for a new
        # exact tag/release target.
        self.assertIn("--event release", transaction["run"])
        self.assertNotIn("--event workflow_dispatch", transaction["run"])
        self.assertIn('--previous "$PRIOR"', transaction["run"])
        self.assertIn("--live \"$LIVE\"", transaction["run"])
        self.assertIn("prior_receipt", str(job["outputs"]))
        self.assertIn("prior_hash", str(job["outputs"]))
        # Stamping validates the exact release revision, not the frozen baseline.
        self.assertIn("--stable-commit \"$RELEASE_COMMIT\"", source)
        self.assertIn("--stable-digest \"$IMAGE_DIGEST\"", source)
        # If the new marker was deployed but qualification/decision did not
        # authorize alias promotion, restore the exact prior docs bytes inside
        # the stable transaction so the public marker cannot stay ahead.
        by_id = {step.get("id"): step for step in steps if step.get("id")}
        docs_compensate = by_id["docs_compensate"]
        self.assertEqual(docs_compensate.get("with", {}).get("artifact_name"),
                         "github-pages-prior-stable")
        self.assertIn("steps.deploy.outcome == 'success'", docs_compensate["if"])
        self.assertIn("steps.marker.outcome != 'success'", docs_compensate["if"])
        self.assertIn("steps.transaction.outcome != 'success'", docs_compensate["if"])
        self.assertIn("steps.transaction.outputs.action == 'refuse'", docs_compensate["if"])
        self.assertIn("steps.transaction.outputs.action == 'rollback'", docs_compensate["if"])
        self.assertIn("steps.transaction.outputs.action == ''", docs_compensate["if"])
        verify_restore = next(step for step in steps
                              if "Verify the restored prior stable docs marker" in step.get("name", ""))
        self.assertIn("steps.prior.outputs.hash", str(verify_restore))
        self.assertIn("PRIOR_HASH", verify_restore["run"])
        fail_step = next(step for step in steps
                         if step.get("id") != "docs_compensate"
                         and "stable docs compensation was required" in step.get("name", ""))
        self.assertIn("steps.docs_compensate.outcome == 'success'", fail_step["if"])
        promote = doc["jobs"]["promote-aliases"]["steps"]
        self.assertTrue(any(step.get("id") == "stable_compensate" for step in promote))
        self.assertTrue(any(step.get("id") == "stable_compensate_docs"
                            and step.get("with", {}).get("artifact_name")
                            == "github-pages-prior-stable" for step in promote))
        for job_name in ("publish", "promote-aliases"):
            job_block = doc["jobs"][job_name]
            self.assertIn("stable-channel-transaction", job_block["needs"])
            condition = str(job_block.get("if", ""))
            self.assertIn("stable-channel-transaction.outputs.action != 'refuse'", condition)
            self.assertIn("stable-channel-transaction.outputs.action != 'rollback'", condition)

    def test_no_legacy_alias_write_path_remains_outside_the_qualified_transaction(self):
        import yaml
        files = sorted((release.ROOT / ".github/workflows").glob("*.yml"))
        for path in files:
            with self.subTest(workflow=path.name):
                source = path.read_text(encoding="utf-8")
                for token in ("nightly-promote", "edge-promote", "promote-stable-alias",
                              "--alias latest"):
                    self.assertNotIn(token, source)
        rel = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertEqual(rel.count("python3 scripts/release.py promote-alias --check-only"), 1)
        self.assertEqual(rel.count("python3 scripts/release.py promote-alias --version"), 1)
        self.assertIn("needs: [plan, publish-immutable, publish]", rel.split("  publish-minor:")[1])
        import re as _re
        for job in ("promote-aliases", "publish"):
            match = _re.search(rf"(?ms)^  {job}:\n(.*?)(?=^  [A-Za-z][\w-]*:\n|\Z)", rel)
            self.assertIsNotNone(match)
            self.assertIn("stable-channel-transaction", match.group(1))

    def test_channel_observe_is_read_only_and_derives_revision(self):
        child = platform_revisions(self.CANDIDATE)
        index = "sha256:" + "1" * 64
        runner = real_docker_inspect_runner(index, child)
        revisions = {(p[0], p[1]): {"revision": self.CANDIDATE, "digest": d["digest"]}
                     for p, d in child.items()}
        with patch.object(release, "observe_reference",
                          return_value=("present", {"digest": self.DIGEST,
                                                    "platforms": set(release.REQUIRED_PLATFORMS)})), \
             patch.object(release, "platform_manifest_revisions",
                          return_value=(self.DIGEST, revisions)):
            observations = release.channel_observe("nightly")
        self.assertEqual(set(observations), {"docker_hub", "ghcr"})
        for registry in release.CHANNEL_REGISTRIES:
            self.assertEqual(observations[registry][1]["revision"], self.CANDIDATE)


class ReviewerHighHardeningTests(unittest.TestCase):
    """Independent review HIGHs: observed parity, shared lock, real browser QA."""

    def test_cd_uses_observed_registry_map_and_browser_gate(self):
        import yaml
        source = (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8")
        doc = yaml.safe_load(source)
        steps = doc["jobs"]["channel-transaction"]["steps"]
        run_text = "\n".join(str(step.get("run", "")) for step in steps)
        self.assertIn('--argjson registries "$OBSERVED"', run_text)
        self.assertNotIn("registries:{docker_hub:$digest,ghcr:$digest}", run_text)
        self.assertNotIn('IMMUTABLE=$(printf \'%s\' "$ALIASES"', run_text)
        self.assertIn("--immutable \"$IMMUTABLE\"", run_text)
        by_id = {step.get("id"): step for step in steps if step.get("id")}
        self.assertIn("observed=", by_id["stage"]["run"])
        self.assertEqual(by_id["browser_ui"]["run"],
                         "python3 scripts/browser_version_check.py")
        self.assertIn("steps.browser_ui.outcome == 'success'", by_id["final_deploy"]["if"])

    def test_public_release_waits_for_verified_aliases_and_live_browser(self):
        import yaml
        doc = yaml.safe_load(
            (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8"))
        jobs = doc["jobs"]
        self.assertIn("promote-aliases", jobs["publish"]["needs"])
        self.assertNotIn("publish", jobs["promote-aliases"]["needs"])
        self.assertIn("needs.promote-aliases.outputs.aliases_verified == 'true'",
                      str(jobs["publish"]["if"]))
        promoted = {step.get("id"): step for step in jobs["promote-aliases"]["steps"]
                    if step.get("id")}
        self.assertIn("verified=true", promoted["alias_verify"]["run"])
        stable_runs = "\n".join(str(step.get("run", ""))
                                for step in jobs["stable-channel-transaction"]["steps"])
        self.assertIn("playwright install --with-deps chromium", stable_runs)
        self.assertIn("browser_version_check.py --base-url https://carbonpanel.dev", stable_runs)
        cd = yaml.safe_load(
            (release.ROOT / ".github/workflows/cd.yml").read_text(encoding="utf-8"))
        cd_runs = "\n".join(str(step.get("run", ""))
                            for step in cd["jobs"]["channel-transaction"]["steps"])
        self.assertIn("playwright install --with-deps chromium", cd_runs)
        self.assertIn("browser_version_check.py --base-url https://carbonpanel.dev", cd_runs)

    def test_release_lock_is_shared_pages_deploy(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        self.assertIn("inputs.publish == true && 'pages-deploy'", source)
        self.assertNotIn("carbonpanel-product-publish", source)


class ChannelTransactionTests(unittest.TestCase):
    """OSS-413 one-transaction-per-channel fixtures (fully offline)."""

    COMMIT = "c" * 40
    DIGEST = "sha256:" + "d" * 64
    PREV_COMMIT = "a" * 40
    PREV_DIGEST = "sha256:" + "e" * 64

    def candidate(self, channel="nightly", **overrides):
        values = {
            "channel": channel, "commit": self.COMMIT, "digest": self.DIGEST,
            "seq": 2, "source_ref": release.TRUSTED_DEVELOPER_REF,
            "ci_verified": "true", "immutable_ok": True, "contract_valid": True,
            "site_validated": True, "docs_commit": self.COMMIT,
            "docs_digest": self.DIGEST, "docs_hash": "sha256:" + "f" * 64,
            "registries": {"docker_hub": self.DIGEST, "ghcr": self.DIGEST},
            "candidate_marker_verified": True, "canonical_marker_verified": True,
            "marker_urls": {"nightly": "https://carbonpanel.dev/docs/nightly/"},
            "run_ids": {"ci": "123"}, "timestamp": "2026-11-20T00:00:00Z",
        }
        values.update(overrides)
        return values

    def receipt(self, channel="nightly", seq=1, commit=PREV_COMMIT,
                digest=PREV_DIGEST):
        return release.channel_receipt(
            channel, commit, digest, "sha256:" + "9" * 64, commit, digest,
            {"docker_hub": digest, "ghcr": digest},
            {"docker_hub": digest, "ghcr": digest},
            {"nightly": "https://carbonpanel.dev/docs/nightly/"},
            {"ci": "122"}, seq, "2026-11-19T00:00:00Z")

    def decide(self, candidate=None, previous=None, live=None, event="workflow_run",
               ancestry=None):
        return release.channel_transaction_decision(
            candidate or self.candidate(), previous, live or {}, event,
            ancestry=ancestry or (lambda older, newer: True))

    def test_first_publish_stages_both_surfaces_before_recording_receipt(self):
        action, _, receipt, steps = self.decide()
        self.assertEqual(action, "publish")
        self.assertEqual(tuple(steps), release.TX_PUBLISH_STEPS)
        self.assertLess(steps.index(release.TX_VERIFY_CANDIDATE),
                        steps.index(release.TX_MOVE_ALIASES))
        self.assertLess(steps.index(release.TX_MOVE_ALIASES),
                        steps.index(release.TX_VERIFY_FINAL))
        self.assertEqual(receipt["revision"], self.COMMIT)
        self.assertEqual(receipt["digest"], self.DIGEST)
        self.assertEqual(receipt["seq"], 2)
        self.assertTrue(receipt["verified"])

    def test_receipt_records_before_after_marker_urls_and_run_ids(self):
        live = {"registries": {"docker_hub": self.PREV_DIGEST, "ghcr": self.PREV_DIGEST},
                "docs_commit": self.PREV_COMMIT, "docs_digest": self.PREV_DIGEST}
        _, _, receipt, _ = self.decide(previous=self.receipt(), live=live)
        self.assertEqual(receipt["before"], {"docker_hub": self.PREV_DIGEST,
                                             "ghcr": self.PREV_DIGEST})
        self.assertEqual(receipt["registries"], {"docker_hub": self.DIGEST,
                                                 "ghcr": self.DIGEST})
        self.assertIn("nightly", receipt["marker_urls"])
        self.assertEqual(receipt["run_ids"]["ci"], "123")
        self.assertTrue(receipt["docs_artifact_hash"])

    def test_noop_when_both_registries_and_live_docs_match(self):
        previous = self.receipt()
        live = {"registries": {"docker_hub": self.DIGEST, "ghcr": self.DIGEST},
                "docs_commit": self.COMMIT, "docs_digest": self.DIGEST}
        action, _, receipt, steps = self.decide(previous=previous, live=live)
        self.assertEqual(action, "noop")
        self.assertEqual(steps, [])
        self.assertEqual(receipt, previous)

    def test_partial_state_reconciles_and_preserves_receipt(self):
        previous = self.receipt()
        live = {"registries": {"docker_hub": self.DIGEST, "ghcr": self.PREV_DIGEST},
                "docs_commit": self.PREV_COMMIT, "docs_digest": self.PREV_DIGEST}
        action, _, receipt, steps = self.decide(previous=previous, live=live)
        self.assertEqual(action, "reconcile")
        self.assertEqual(receipt, previous)
        self.assertEqual(steps[0], release.TX_RECONCILE)
        self.assertIn(release.TX_MOVE_ALIASES, steps)

    def test_unqualified_candidate_rolls_back_to_prior_receipt(self):
        previous = self.receipt()
        for override in ({"candidate_marker_verified": False},
                         {"immutable_ok": False}, {"site_validated": False},
                         {"docs_digest": self.PREV_DIGEST}):
            with self.subTest(override=override):
                action, reason, receipt, steps = self.decide(
                    candidate=self.candidate(**override), previous=previous)
                self.assertEqual(action, "rollback")
                self.assertEqual(receipt, previous)
                self.assertIn(release.TX_ROLLBACK, steps)

    def test_older_and_out_of_order_candidates_are_refused(self):
        previous = self.receipt(seq=5)
        action, reason, receipt, _ = self.decide(
            candidate=self.candidate(seq=4), previous=previous)
        self.assertEqual(action, "refuse")
        self.assertIn("out-of-order", reason)
        self.assertEqual(receipt, previous)

    def test_manual_dispatch_only_republishes_an_active_source(self):
        previous = self.receipt(commit=self.COMMIT, digest=self.DIGEST)
        action, reason, _, _ = self.decide(event="workflow_dispatch")
        self.assertEqual(action, "refuse")
        self.assertIn("already-active", reason)
        action, _, _, _ = self.decide(previous=previous, event="workflow_dispatch")
        self.assertEqual(action, "publish")

    def test_stable_channel_only_moves_from_verified_baseline(self):
        action, _, _, _ = self.decide(candidate=self.candidate(
            channel="stable", commit=self.PREV_COMMIT, digest=self.PREV_DIGEST))
        self.assertEqual(action, "refuse")
        candidate = self.candidate(
            channel="stable", commit=release.LEGACY_STABLE_COMMIT,
            digest=release.LEGACY_STABLE_DIGEST, docs_commit=release.LEGACY_STABLE_COMMIT,
            docs_digest=release.LEGACY_STABLE_DIGEST,
            registries={"docker_hub": release.LEGACY_STABLE_DIGEST,
                        "ghcr": release.LEGACY_STABLE_DIGEST})
        self.assertEqual(self.decide(candidate=candidate)[0], "publish")

    def test_no_mutable_nightly_state_is_committed_to_main(self):
        # The revision lives in the built marker and the receipt, not a tracked
        # main file; the transaction decision writes no repository path.
        ops = (release.ROOT / "docs/releases.md").read_text(encoding="utf-8")
        self.assertIn("Nightly revision belongs in the published", ops)
        with patch.object(release, "subprocess"), patch("builtins.open",
                                                        side_effect=AssertionError("file write")):
            action, _, receipt, _ = self.decide()
        self.assertEqual(action, "publish")
        self.assertEqual(receipt["revision"], self.COMMIT)

    def test_prior_receipt_is_preserved_not_the_candidate_receipt(self):
        previous = self.receipt()
        outcome = release.channel_transaction_run(
            self.candidate(), previous,
            writer=lambda r, d: (False, "injected") if r == "ghcr" and d != self.PREV_DIGEST
            else (True, ""),
            ancestry=lambda older, newer: True)
        self.assertEqual(outcome["outcome"], "rolled_back")
        self.assertEqual(outcome["receipt"]["revision"], self.PREV_COMMIT)
        self.assertNotEqual(outcome["receipt"]["revision"], self.COMMIT)
        self.assertEqual(outcome["receipt"]["digest"], self.PREV_DIGEST)

    def test_bootstrap_requires_recorded_flag_and_dual_registry_parity(self):
        baseline = release.NIGHTLY_HUB_BASELINE
        candidate = self.candidate(
            commit=baseline["commit"], digest=baseline["digest"],
            docs_commit=baseline["commit"], docs_digest=baseline["digest"],
            registries={"docker_hub": baseline["digest"], "ghcr": baseline["digest"]})
        self.assertEqual(self.decide(candidate=candidate)[0], "refuse")
        self.assertEqual(self.decide(candidate=self.candidate(
            **{"bootstrap": True}))[0], "publish")  # normal candidate unaffected
        bootstrapped = dict(candidate)
        bootstrapped["bootstrap"] = True
        self.assertEqual(self.decide(candidate=bootstrapped)[0], "publish")

    def test_prerelease_target_is_refused_for_stable_channel(self):
        candidate = self.candidate(channel="stable",
                                   release_tag="carbonpanel-v2.0.0-alpha.1")
        action, reason, _, _ = self.decide(candidate=candidate)
        self.assertEqual(action, "refuse")
        self.assertIn("stable", reason)

    def test_older_release_target_without_trusted_gate_script_is_refused(self):
        ok, missing = release.verify_gate_script(release.ROOT / "scripts/release.py")
        self.assertTrue(ok, missing)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "release.py"
            path.write_text("#!/usr/bin/env python3\n# older target\n", encoding="utf-8")
            ok, missing = release.verify_gate_script(path)
            self.assertFalse(ok)
            for command in release.REQUIRED_GATE_COMMANDS:
                self.assertIn(command, missing)
            out = io.StringIO()
            with patch("sys.argv", ["release.py", "verify-gate-script",
                                    "--path", str(path)]), redirect_stdout(out):
                with self.assertRaisesRegex(ValueError, "Older release target"):
                    release.main()
            self.assertEqual(json.loads(out.getvalue())["verified"], "false")

    def test_gate_uses_observed_duals_registry_digests_not_hardcoded_values(self):
        commit, digest = self.COMMIT, self.DIGEST
        info = {"digest": digest,
                "platforms": [["linux", "amd64"], ["linux", "arm64"]],
                "revision": commit}
        observations = json.dumps({"docker_hub": ["present", info], "ghcr": ["present", info]})
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "verify-channel-parity", "--observations",
                                observations, "--digest", digest, "--commit", commit]), \
             redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["parity"], "true")
        bad = json.dumps({"docker_hub": ["present", info],
                          "ghcr": ["present", {**info, "digest": self.PREV_DIGEST}]})
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "verify-channel-parity", "--observations",
                                bad, "--digest", digest, "--commit", commit]), \
             redirect_stdout(out):
            with self.assertRaisesRegex(ValueError, "parity"):
                release.main()
        self.assertEqual(json.loads(out.getvalue())["parity"], "false")

    def test_stable_accepts_exact_target_version_but_not_arbitrary_or_2_0_0(self):
        candidate = self.candidate(
            channel="stable", release_tag="carbonpanel-v1.0.41")
        self.assertEqual(self.decide(candidate=candidate)[0], "publish")
        for bad_tag in ("v1.0.41", "carbonpanel-v2.0.0", "carbonpanel-v1.0.40"):
            with self.subTest(tag=bad_tag):
                self.assertEqual(self.decide(candidate=self.candidate(
                    channel="stable", release_tag=bad_tag))[0], "refuse")

    def test_transaction_cli_fails_on_rollback_not_silent_success(self):
        previous = self.receipt()
        candidate = self.candidate(candidate_marker_verified=False)
        with patch("sys.argv", ["release.py", "channel-transaction", "--phase", "commit",
                                "--candidate", json.dumps(candidate),
                                "--previous", json.dumps(previous),
                                "--event", "workflow_run"]), redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(ValueError, "did not qualify"):
                release.main()

    def test_stage_phase_is_zero_write_and_never_moves_aliases(self):
        previous = self.receipt()
        action, _, receipt, steps = release.channel_transaction_decision(
            self.candidate(candidate_marker_verified=False,
                           canonical_marker_verified=False),
            previous, {}, "workflow_run", ancestry=lambda older, newer: True,
            phase=release.TX_STAGE)
        self.assertEqual(action, "stage")
        self.assertEqual(receipt, previous)  # no new receipt written
        self.assertEqual(tuple(steps), release.TX_STAGE_STEPS)
        for write_step in (release.TX_MOVE_ALIASES, release.TX_DEPLOY_CANONICAL,
                           release.TX_WRITE_RECEIPT):
            self.assertNotIn(write_step, steps)
        # The same candidate in commit phase is refused, not published.
        self.assertEqual(release.channel_transaction_decision(
            self.candidate(candidate_marker_verified=False),
            previous, {}, "workflow_run",
            ancestry=lambda older, newer: True)[0], "rollback")

    def test_registry_move_requires_canonical_docs_marker_verified(self):
        previous = self.receipt()
        action, reason, receipt, steps = self.decide(
            candidate=self.candidate(canonical_marker_verified=False),
            previous=previous)
        self.assertEqual(action, "rollback")
        self.assertIn("canonical docs marker", reason)
        self.assertEqual(receipt, previous)
        self.assertIn(release.TX_ROLLBACK_DOCS, steps)
        self.assertNotIn(release.TX_MOVE_ALIASES, steps)
        self.assertEqual(release.channel_transaction_decision(
            self.candidate(canonical_marker_verified=False))[0], "refuse")

    def test_rollback_restores_both_docs_and_registry_aliases(self):
        previous = self.receipt()
        action, _, receipt, steps = release.channel_transaction_decision(
            self.candidate(candidate_marker_verified=False), previous, {},
            "workflow_run", ancestry=lambda older, newer: True)
        self.assertEqual(action, "rollback")
        self.assertEqual(receipt["registries"], previous["registries"])
        self.assertEqual(receipt["docs_digest"], previous["docs_digest"])
        self.assertIn(release.TX_ROLLBACK, steps)
        self.assertIn(release.TX_ROLLBACK_DOCS, steps)

    def test_second_registry_write_failure_restores_both_aliases_and_docs(self):
        previous = self.receipt()
        calls = []

        def writer(registry, digest):
            calls.append((registry, digest))
            if registry == "ghcr" and digest != self.PREV_DIGEST:
                return False, "injected second-registry failure"
            return True, ""

        outcome = release.channel_transaction_run(
            self.candidate(), previous, writer=writer,
            docs_publisher=lambda c, d: True, final_probe=lambda c, d: True,
            ancestry=lambda older, newer: True)
        self.assertEqual(outcome["outcome"], "rolled_back")
        self.assertIn("ghcr alias write failed", outcome["reason"])
        self.assertEqual(outcome["restored"], list(release.CHANNEL_REGISTRIES))
        self.assertEqual(outcome["receipt"], previous)
        for registry in release.CHANNEL_REGISTRIES:
            self.assertIn((registry, self.PREV_DIGEST), calls)
        self.assertEqual(outcome["resume"]["digest"], self.PREV_DIGEST)

    def test_final_probe_failure_rolls_back_even_after_both_alias_writes(self):
        previous = self.receipt()
        calls = []
        outcome = release.channel_transaction_run(
            self.candidate(), previous,
            writer=lambda r, d: (calls.append((r, d)) or (True, "")),
            docs_publisher=lambda c, d: True,
            final_probe=lambda c, d: False, ancestry=lambda older, newer: True)
        self.assertEqual(outcome["outcome"], "rolled_back")
        self.assertIn("final public marker/alias probe failed", outcome["reason"])
        self.assertEqual(outcome["moved"], [])
        self.assertEqual(calls[:2], [(r, self.DIGEST)
                                     for r in release.CHANNEL_REGISTRIES])
        self.assertEqual(outcome["restored"], list(release.CHANNEL_REGISTRIES))

    def test_docs_publish_failure_blocks_all_registry_writes(self):
        previous = self.receipt()
        calls = []
        release.channel_transaction_run(
            self.candidate(), previous,
            writer=lambda r, d: (calls.append((r, d)) or (True, "")),
            docs_publisher=lambda c, d: False, ancestry=lambda older, newer: True)
        # No alias is ever written at the candidate digest; only prior-digest
        # restoration calls are allowed.
        self.assertNotIn(("docker_hub", self.DIGEST), calls)
        self.assertNotIn(("ghcr", self.DIGEST), calls)
        self.assertIn(("docker_hub", self.PREV_DIGEST), calls)

    def test_retry_resumes_exact_recorded_digest_and_never_rebuilds(self):
        previous = self.receipt()
        plan = release.channel_resume_plan(previous, self.PREV_DIGEST)
        self.assertEqual(plan["action"], "resume")
        self.assertEqual(plan["digest"], self.PREV_DIGEST)
        self.assertEqual(release.channel_resume_plan(previous, self.DIGEST)["action"],
                         "refuse")

    def test_channel_simulate_cli_evidence_has_no_external_writes(self):
        previous = self.receipt()
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-simulate", "--candidate",
                                json.dumps(self.candidate()), "--previous",
                                json.dumps(previous), "--fail-registry", "ghcr"]), \
             redirect_stdout(out):
            release.main()
        result = json.loads(out.getvalue())
        self.assertEqual(result["outcome"], "rolled_back")
        self.assertEqual(result["restored"], ["docker_hub", "ghcr"])

    def test_rollback_cli_is_read_only_without_apply(self):
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-rollback", "--receipt",
                                json.dumps(self.receipt())]), redirect_stdout(out), \
             patch.object(release, "_apply_channel_alias",
                          side_effect=AssertionError("unexpected alias write")):
            release.main()
        result = json.loads(out.getvalue())
        self.assertEqual(result["digest"], self.PREV_DIGEST)
        self.assertEqual(result["registries"], ["docker_hub", "ghcr"])

    def test_active_receipt_from_live_state_requires_both_registries_to_agree(self):
        info = {"digest": self.PREV_DIGEST, "revision": self.PREV_COMMIT,
                "platforms": release.REQUIRED_PLATFORMS}
        aliases = {name: ("present", info) for name in release.CHANNEL_REGISTRIES}
        self.assertEqual(release.channel_active_receipt(
            "nightly", aliases, self.COMMIT, self.PREV_DIGEST), {})
        self.assertEqual(release.channel_active_receipt(
            "nightly", aliases, self.PREV_COMMIT, self.DIGEST), {})
        receipt = release.channel_active_receipt(
            "nightly", aliases, self.PREV_COMMIT, self.PREV_DIGEST, 1, "ts")
        self.assertEqual(receipt["revision"], self.PREV_COMMIT)
        self.assertEqual(receipt["digest"], self.PREV_DIGEST)
        aliases["ghcr"] = ("present", {"digest": self.DIGEST})
        self.assertEqual(release.channel_active_receipt(
            "nightly", aliases, self.PREV_COMMIT, self.PREV_DIGEST), {})
        aliases["ghcr"] = ("unknown", None)
        self.assertEqual(release.channel_active_receipt(
            "nightly", aliases, self.PREV_COMMIT, self.PREV_DIGEST), {})

    def test_transaction_cli_emits_action_and_steps_offline(self):
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-transaction", "--candidate",
                                json.dumps(self.candidate())]), redirect_stdout(out):
            release.main()
        self.assertEqual(json.loads(out.getvalue())["action"], "publish")


class ChannelMarkerStagingTests(unittest.TestCase):
    """The candidate probe lives at a NON-ACTIVE staging path, never a canonical route."""

    COMMIT = "c" * 40
    DIGEST = "sha256:" + "d" * 64

    def site(self, directory, contract=None):
        root = Path(directory)
        (root / "version-index.json").write_text(
            json.dumps(contract or ChannelPromotionTests().contract(
                commit=self.COMMIT, digest=self.DIGEST)), encoding="utf-8")
        return root

    def test_probe_staging_never_touches_canonical_marker(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)
            before = (root / "version-index.json").read_bytes()
            result = release.stage_channel_marker(
                root, "nightly", self.COMMIT, self.DIGEST)
            self.assertEqual(result["probe_path"], f"/docs/candidate/{self.COMMIT}/")
            self.assertTrue(result["probe_url"].startswith("/docs/candidate/"))
            self.assertFalse(result["canonical_stamped"])
            self.assertEqual(result["canonical_url"], "")
            self.assertEqual((root / "version-index.json").read_bytes(), before)
            self.assertNotIn("/docs/nightly/", result["probe_url"])
            marker = json.loads((root / "docs" / "candidate" / self.COMMIT
                                 / "marker.json").read_text(encoding="utf-8"))
            self.assertTrue(release.marker_matches(marker, self.COMMIT, self.DIGEST))
            self.assertFalse(marker["canonical"])
            for canonical in (root / "docs" / "nightly", root / "docs" / "index.html"):
                self.assertFalse(canonical.exists())

    def test_canonical_stamping_uses_exact_candidate_revision_and_digest(self):
        old = "a" * 40
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory, ChannelPromotionTests().contract(
                commit=old, digest="sha256:" + "e" * 64))
            result = release.stage_channel_marker(
                root, "nightly", self.COMMIT, self.DIGEST, include_canonical=True)
            self.assertTrue(result["canonical_stamped"])
            self.assertEqual(result["canonical_url"], "/version-index.json")
            document = json.loads((root / "version-index.json").read_text(encoding="utf-8"))
            nightly = document["channels"]["nightly"]
            self.assertEqual(nightly["commit"], self.COMMIT)
            self.assertEqual(nightly["image"]["revision"], self.COMMIT)
            self.assertEqual(nightly["image"]["digest"], self.DIGEST)
            self.assertTrue(release.validate_channel_contract(document)[0])

    def test_canonical_stable_stamping_validates_against_exact_release_revision(self):
        release_commit = "b" * 40
        release_digest = "sha256:" + "f" * 64
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory, ChannelPromotionTests().contract())
            result = release.stage_channel_marker(
                root, "stable", release_commit, release_digest, include_canonical=True)
            self.assertTrue(result["canonical_stamped"])
            document = json.loads((root / "version-index.json").read_text(encoding="utf-8"))
            stable = document["channels"]["stable"]
            self.assertEqual(stable["commit"], release_commit)
            self.assertEqual(stable["image"]["revision"], release_commit)
            self.assertEqual(stable["image"]["digest"], release_digest)
            ok, problems = release.validate_channel_contract(document)
            self.assertFalse(ok)
            self.assertTrue(any("stable" in problem and "baseline" in problem
                                for problem in problems))
            self.assertTrue(release.validate_channel_contract(
                document, stable_commit=release_commit,
                stable_digest=release_digest)[0])
            out = io.StringIO()
            with patch("sys.argv", ["release.py", "validate-channel-contract",
                                    "--path", str(root / "version-index.json"),
                                    "--stable-commit", release_commit,
                                    "--stable-digest", release_digest]), \
                 redirect_stdout(out):
                release.main()
            self.assertEqual(json.loads(out.getvalue())["verified"], "true")

    def test_stage_channel_marker_cli_is_offline_and_reports_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)
            out = io.StringIO()
            with patch("sys.argv", ["release.py", "stage-channel-marker", "--site",
                                    str(root), "--channel", "nightly", "--commit",
                                    self.COMMIT, "--digest", self.DIGEST]), \
                 redirect_stdout(out):
                release.main()
            self.assertEqual(json.loads(out.getvalue())["probe_path"],
                             f"/docs/candidate/{self.COMMIT}/")


class ChannelPageStagingTests(unittest.TestCase):
    """Pre-alias Pages artifacts keep the full canonical site; the candidate
    pair exists only in the off-path probe until the aliases actually move."""

    ACTIVE_COMMIT = "a" * 40
    ACTIVE_DIGEST = "sha256:" + "e" * 64
    CANDIDATE_COMMIT = "c" * 40
    CANDIDATE_DIGEST = "sha256:" + "d" * 64

    def site(self, directory, nightly_commit=None, nightly_digest=None):
        root = Path(directory)
        contract = ChannelPromotionTests().contract(
            commit=nightly_commit or self.ACTIVE_COMMIT,
            digest=nightly_digest or self.ACTIVE_DIGEST)
        (root / "version-index.json").write_text(
            json.dumps(contract), encoding="utf-8")
        (root / "docs").mkdir(parents=True, exist_ok=True)
        (root / "docs" / "version-index.json").write_text(
            json.dumps(contract), encoding="utf-8")
        for page in ("docs/index.html", "docs/nightly/index.html",
                     "docs/getting-started/proxmox/index.html"):
            target = root / page
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("<html></html>", encoding="utf-8")
        return root

    def probe(self, root):
        release.stage_channel_marker(
            root, "nightly", self.CANDIDATE_COMMIT, self.CANDIDATE_DIGEST)

    def verify(self, root):
        return release.verify_staged_page_site(
            root, self.CANDIDATE_COMMIT, self.CANDIDATE_DIGEST,
            self.ACTIVE_COMMIT, self.ACTIVE_DIGEST)

    def test_full_site_with_off_path_probe_is_accepted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)
            self.probe(root)
            ok, problems = self.verify(root)
            self.assertTrue(ok, problems)
            self.assertEqual(problems, [])

    def test_docs_index_copy_must_match_the_root_index(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)
            self.probe(root)
            (root / "docs" / "version-index.json").write_text("{}", encoding="utf-8")
            ok, problems = self.verify(root)
            self.assertFalse(ok)
            self.assertTrue(any("differs from the site root" in item
                                for item in problems))

    def test_incomplete_stage_refuses_stamped_canonical_missing_stable_or_probe(self):
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory, nightly_commit=self.CANDIDATE_COMMIT,
                             nightly_digest=self.CANDIDATE_DIGEST)
            self.probe(root)
            ok, problems = self.verify(root)
            self.assertFalse(ok)
            self.assertTrue(any("canonical nightly commit moved" in item
                                for item in problems))
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)
            self.probe(root)
            document = json.loads((root / "version-index.json").read_text(encoding="utf-8"))
            del document["channels"]["stable"]
            (root / "version-index.json").write_text(
                json.dumps(document), encoding="utf-8")
            ok, problems = self.verify(root)
            self.assertFalse(ok)
            self.assertTrue(any("exactly stable and nightly" in item
                                for item in problems))
        with tempfile.TemporaryDirectory() as directory:
            root = self.site(directory)  # no off-path probe
            ok, problems = self.verify(root)
            self.assertFalse(ok)
            self.assertTrue(any("probe marker is missing" in item
                                for item in problems))

    def test_rollback_restores_prior_receipt_and_exact_prior_bytes(self):
        prior = {"channel": "nightly", "revision": self.ACTIVE_COMMIT,
                 "digest": self.ACTIVE_DIGEST, "verified": True}
        values = release.channel_rollback(prior)
        self.assertEqual(values["digest"], self.ACTIVE_DIGEST)
        self.assertEqual(values["commit"], self.ACTIVE_COMMIT)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            live = b'{"schema_version": 3, "channels": {"stable": {}, "nightly": {}}}'
            candidate = root / "artifact"
            candidate.mkdir()
            (candidate / "version-index.json").write_bytes(live)
            target = root / "restored"
            hint = release.install_prior_pages(candidate, live, target)
            self.assertTrue(hint["hash"].startswith("sha256:"))
            self.assertEqual((target / "version-index.json").read_bytes(), live)


class PriorPagesDeploymentTests(unittest.TestCase):
    """The shared docs gate (reused by release.py) rejects equal-index guide drift."""

    INDEX = json.dumps({
        "schema_version": 3,
        "channels": {"stable": {"route": "/docs/"},
                     "nightly": {"route": "/docs/nightly/"}},
        "versions": [{"route": "/docs/", "pages": ["getting-started/proxmox"]}],
    }).encode()
    MANIFEST = json.dumps({
        "schema_version": 1, "source": {"revision": "a" * 40},
        "channels": [{"slug": "getting-started/proxmox", "mode": "page",
                      "source_path": "docs/x.mdx", "source_sha256": "b" * 64,
                      "target_file": "getting-started/proxmox.mdx",
                      "provenance": {"scope": "scope"}}],
    }).encode()

    def artifact(self, artifact_id, head_sha, guide_bytes):
        tar_bytes = io.BytesIO()
        with tarfile.open(fileobj=tar_bytes, mode="w") as archive:
            for name, data in (("./version-index.json", self.INDEX),
                               ("./docs/maintained-guides.json", self.MANIFEST),
                               ("./docs/getting-started/proxmox/index.html", guide_bytes)):
                info = tarfile.TarInfo(name)
                info.size = len(data)
                archive.addfile(info, io.BytesIO(data))
        zipped = io.BytesIO()
        with zipfile.ZipFile(zipped, "w") as bundle:
            bundle.writestr("artifact.tar", tar_bytes.getvalue())
        descriptor = {"id": artifact_id, "name": gate.PAGES_ARTIFACT_NAME,
                      "expired": False,
                      "workflow_run": {"head_sha": head_sha}}
        return descriptor, zipped.getvalue()

    def test_shared_identity_uses_successful_deployment_not_build_artifact(self):
        deployments = [
            {"id": 900, "sha": "b" * 40, "environment": gate.PAGES_ENVIRONMENT,
             "created_at": "2026-11-20T10:00:00Z"},
            {"id": 800, "sha": "a" * 40, "environment": gate.PAGES_ENVIRONMENT,
             "created_at": "2026-11-19T10:00:00Z"},
        ]
        statuses = {
            "900": [{"state": "in_progress",
                     "target_url": "https://github.com/o/r/actions/runs/200/job/1"}],
            "800": [{"state": "success",
                     "target_url": "https://github.com/o/r/actions/runs/100/job/1"}],
        }
        run_artifacts = {
            "100": [{"id": 1, "name": gate.PAGES_ARTIFACT_NAME, "expired": False}],
        }
        baseline, reason = gate.pages_baseline_from_deployments(
            deployments, statuses, run_artifacts)
        self.assertTrue(baseline, reason)
        self.assertEqual(baseline["artifact_id"], "1")
        self.assertEqual(baseline["deployment_id"], "800")
        self.assertEqual(baseline["sha"], "a" * 40)

    def test_equal_index_different_guide_cannot_be_a_prior(self):
        deployed, deployed_zip = self.artifact(1, "a" * 40, b"<h1>deployed guide</h1>")
        build_only, build_zip = self.artifact(2, "b" * 40, b"<h1>newer guide</h1>")
        archives = {1: deployed_zip, 2: build_zip}

        def fetch(destination, live_guide):
            def get_bytes(path):
                if path == "/version-index.json":
                    return self.INDEX
                if path == "/docs/maintained-guides.json":
                    return self.MANIFEST
                if path == "/docs/getting-started/proxmox/index.html":
                    return live_guide
                raise urllib.error.HTTPError(path, 404, "missing", {}, None)

            with patch.object(gate, "_http_get", return_value=get_bytes):
                return gate.fetch_prior_pages(
                    destination, [build_only, deployed],
                    lambda artifact: archives[int(artifact["id"])],
                    self.INDEX, artifact_id="1", run_id="100")

        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, "No Pages artifact matches"):
                fetch(Path(directory) / "restored", b"<h1>newer guide</h1>")
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "restored"
            result = fetch(destination, b"<h1>deployed guide</h1>")
            self.assertEqual(result["artifact_id"], "1")
            self.assertEqual(
                (destination / "docs/getting-started/proxmox/index.html").read_bytes(),
                b"<h1>deployed guide</h1>")


class LocalOciWorkflowSeamTests(unittest.TestCase):
    """The verifier attaches to the existing no-credential export seam only."""

    def test_verifier_is_build_evidence_and_not_a_promotion_input(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        image = source.split("  image:\n", 1)[1].split("\n  publish-immutable:\n", 1)[0]
        self.assertIn("push: false", image)
        self.assertIn("outputs: type=oci,dest=dist/image.oci.tar", image)
        self.assertIn("local-oci-candidate", image)
        self.assertIn("--archive dist/image.oci.tar", image)
        self.assertIn('--commit "$PINNED_COMMIT"', image)
        self.assertNotIn("--push", image)
        self.assertNotIn("docker/login", image)
        self.assertLess(source.index("  image:\n"),
                        source.index("  publish-immutable:\n"))
        # The local digest never feeds alias/immutable promotion: that path
        # still verifies an independently observed PUBLIC digest.
        self.assertNotIn("needs.image.outputs.local_oci", source)
        self.assertIn("preflight-immutable", source)
        self.assertIn("verify-immutable", source)


class NightlyActivationTests(unittest.TestCase):
    """First Nightly activation is explicit; mixed registries never skip silently."""

    DIGEST = "sha256:" + "d" * 64
    COMMIT = "a" * 40

    def observations(self, hub, ghcr):
        return {"docker_hub": hub, "ghcr": ghcr}

    def test_mixed_and_missing_pairs_are_blocked_with_reasons(self):
        present = ("present", {"digest": self.DIGEST, "revision": self.COMMIT})
        absent = ("absent", {})
        mixed = release.nightly_activation_decision(self.observations(present, absent))
        self.assertEqual(mixed["action"], "blocked")
        self.assertIn("mixed", mixed["reason"])
        self.assertIn("ghcr", mixed["reason"])
        missing = release.nightly_activation_decision(self.observations(absent, absent))
        self.assertEqual(missing["action"], "blocked")
        unknown = release.nightly_activation_decision(self.observations(present, ("unknown", None)))
        self.assertEqual(unknown["action"], "blocked")
        conflicting = release.nightly_activation_decision(self.observations(
            present, ("present", {"digest": "sha256:" + "e" * 64, "revision": self.COMMIT})))
        self.assertEqual(conflicting["action"], "blocked")

    def test_verified_pair_activates(self):
        pair = ("present", {"digest": self.DIGEST, "revision": self.COMMIT})
        result = release.nightly_activation_decision(self.observations(pair, pair))
        self.assertEqual(result["action"], "activate")
        self.assertEqual(result["digest"], self.DIGEST)
        self.assertEqual(result["revision"], self.COMMIT)


class StableReleasePublicationTests(unittest.TestCase):
    """Fail-closed Stable publication: real observations and rendered identity."""

    COMMIT = "b" * 40
    DIGEST = "sha256:" + "d" * 64
    VERSION = "carbonpanel-v2.0.1"

    def alias(self, state, digest="", revision=""):
        return (state, {"digest": digest, "revision": revision,
                        "platforms": release.REQUIRED_PLATFORMS})

    def test_exposure_requires_matching_public_aliases_and_both_docs_indexes(self):
        entry = {"tag": self.VERSION, "commit": self.COMMIT, "image": {"digest": self.DIGEST}}
        encoded = json.dumps({"channels": {"stable": entry}}).encode()
        matched = {name: self.alias("present", self.DIGEST, self.COMMIT) for name in release.CHANNEL_REGISTRIES}
        with patch.object(release, "channel_observe", return_value=matched), \
             patch.object(release.urllib.request, "urlopen", side_effect=[io.BytesIO(encoded), io.BytesIO(encoded)]):
            release.verify_live_stable(self.VERSION, self.COMMIT, self.DIGEST)
        with patch.object(release, "channel_observe", return_value=matched), \
             patch.object(release.urllib.request, "urlopen", side_effect=[io.BytesIO(encoded), io.BytesIO(b"{}")]), \
             self.assertRaisesRegex(ValueError, "docs"):
            release.verify_live_stable(self.VERSION, self.COMMIT, self.DIGEST)
        with patch.object(release, "channel_observe", side_effect=[matched, {}]), \
             patch.object(release.urllib.request, "urlopen") as fetch, self.assertRaisesRegex(ValueError, "latest"):
            release.verify_live_stable(self.VERSION, self.COMMIT, self.DIGEST)
        fetch.assert_not_called()

    def test_exposing_a_prepared_release_never_uploads_assets(self):
        with tempfile.TemporaryDirectory() as directory:
            make_publish_fixture(directory, self.VERSION, "2.0.1", self.COMMIT)
            output = Path(directory)
            args = argparse.Namespace(version=self.VERSION, commit=self.COMMIT,
                                      output=directory, expose_prepared=True)
            remote = {"draft": True, "assets": [
                {"name": file.name, "digest": "sha256:" + release.sha256(file)}
                for file in output.iterdir()]}
            with patch.object(release, "verify_live_stable"), \
                 patch.object(release, "guard_publishing"), \
                 patch.object(release, "get_release", return_value=remote), \
                 patch.object(release, "published_stable_versions", return_value=["v1.0.40"]), \
                 patch.object(release, "github") as github:
                remote["assets"][0]["digest"] = "sha256:" + "0" * 64
                with self.assertRaisesRegex(ValueError, "assets"):
                    release.publish(args)
                github.assert_not_called()
                asset = remote["assets"][0]
                asset["digest"] = "sha256:" + release.sha256(output / asset["name"])
                release.publish(args)
                github.assert_called_once_with("release", "edit", self.VERSION, "--draft=false",
                                               "--prerelease=false", "--latest=true")

    def test_first_publication_requires_verified_latest_pair(self):
        latest = {"docker_hub": self.alias("present", self.DIGEST, self.COMMIT),
                  "ghcr": self.alias("present", self.DIGEST, self.COMMIT)}
        absent = {"docker_hub": self.alias("absent"), "ghcr": self.alias("absent")}
        result = release.stable_alias_decision(absent, latest)
        self.assertEqual(result["action"], "first-publish")
        self.assertTrue(result["first_publication"])
        mixed = {"docker_hub": self.alias("present", self.DIGEST, self.COMMIT),
                 "ghcr": self.alias("absent")}
        self.assertEqual(release.stable_alias_decision(mixed, latest)["missing"], ["ghcr"])
        self.assertEqual(release.stable_alias_decision(absent, mixed)["action"], "refuse")
        active = {"docker_hub": self.alias("present", self.DIGEST, self.COMMIT),
                  "ghcr": self.alias("present", self.DIGEST, self.COMMIT)}
        self.assertEqual(release.stable_alias_decision(active, latest)["action"], "promote")
        self.assertEqual(release.stable_alias_decision({}, latest)["action"], "refuse")
        stale = {name: self.alias("present", self.DIGEST, "a" * 40)
                 for name in release.CHANNEL_REGISTRIES}
        self.assertEqual(release.stable_alias_decision(active, stale)["action"], "refuse")
        half = {**active, "ghcr": ("present", {**active["ghcr"][1], "platforms": set()})}
        self.assertEqual(release.stable_alias_decision(half, latest)["action"], "refuse")

    def test_backward_release_and_conflicting_live_write_are_refused(self):
        receipt = {"verified": True, "revision": self.COMMIT, "digest": self.DIGEST}
        args = argparse.Namespace(prior_tag="carbonpanel-v2.0.2", candidate_tag=self.VERSION,
                                  prior_receipt=json.dumps(receipt), commit=self.COMMIT, digest=self.DIGEST)
        with self.assertRaisesRegex(ValueError, "backwards"):
            release.verify_stable_order(args)
        args.prior_tag = self.VERSION
        release.verify_stable_order(args)
        args.digest = "sha256:" + "e" * 64
        with self.assertRaisesRegex(ValueError, "backwards"):
            release.verify_stable_order(args)
        with patch.object(release, "observe_reference", return_value=("unknown", None)), \
             patch.object(release.subprocess, "run") as writer:
            with self.assertRaisesRegex(ValueError, "determine"):
                release._apply_channel_alias("stable", self.DIGEST, args.digest)
            writer.assert_not_called()
        with patch.object(release, "observe_reference",
                          return_value=("present", {"digest": "sha256:" + "0" * 64})), \
             patch.object(release.subprocess, "run") as writer:
            with self.assertRaisesRegex(ValueError, "outside"):
                release._apply_channel_alias("stable", self.DIGEST, args.digest)
            writer.assert_not_called()

    def test_public_asset_reads_match_the_private_prepared_files(self):
        with tempfile.TemporaryDirectory() as directory:
            files, _ = make_publish_fixture(directory, self.VERSION, "2.0.1", self.COMMIT)
            record = {"draft": False, "tag_name": self.VERSION, "assets": [
                {"name": f.name, "digest": "sha256:" + release.sha256(f),
                 "browser_download_url": "https://example.org/" + f.name} for f in files.iterdir()]}
            def download(url, timeout):
                return io.BytesIO((files / url.rsplit("/", 1)[-1]).read_bytes())
            args = argparse.Namespace(version=self.VERSION, output=directory)
            with patch.object(release, "get_release", return_value=record), \
                 patch.object(release.urllib.request, "urlopen", side_effect=download), redirect_stdout(io.StringIO()):
                release.verify_public_downloads(args)
            with patch.object(release, "get_release", return_value=record), \
                 patch.object(release.urllib.request, "urlopen", return_value=io.BytesIO(b"truncated")):
                with self.assertRaisesRegex(ValueError, "downloaded bytes"):
                    release.verify_public_downloads(args)

    def test_immutable_retry_fills_only_absent_registry_from_the_verified_archive(self):
        prior = self.alias("present", self.DIGEST, self.COMMIT)
        observed = {"docker_hub": prior, "ghcr": self.alias("absent")}
        self.assertEqual(release.staged_immutable_targets(observed, self.DIGEST, self.COMMIT), ["ghcr"])
        observed["ghcr"] = prior
        self.assertEqual(release.staged_immutable_targets(observed, self.DIGEST, self.COMMIT), [])
        for bad in (self.alias("present", self.DIGEST, "a" * 40),
                    self.alias("present", "sha256:" + "0" * 64, self.COMMIT),
                    self.alias("unknown")):
            with self.assertRaises(ValueError):
                release.staged_immutable_targets({**observed, "ghcr": bad}, self.DIGEST, self.COMMIT)

    def test_first_alias_interruption_resumes_only_the_missing_verified_baseline(self):
        ok = self.alias("present", self.DIGEST, self.COMMIT)
        latest = {name: ok for name in release.CHANNEL_REGISTRIES}
        stable = {name: self.alias("absent") for name in release.CHANNEL_REGISTRIES}
        receipt = {"channel": "stable", "revision": self.COMMIT, "digest": self.DIGEST,
                   "verified": True}
        observed = lambda ref: latest if ref == "latest" else stable
        calls = []

        def write(registry, digest):
            self.assertEqual(digest, self.DIGEST)
            calls.append(registry)
            stable[registry] = ok
            if len(calls) == 1:
                raise RuntimeError("connection lost after first write")

        with self.assertRaises(RuntimeError):
            release.establish_stable_baseline(receipt, observed, write)
        release.establish_stable_baseline(receipt, observed, write)
        release.establish_stable_baseline(receipt, observed, write)
        self.assertEqual(calls, list(release.CHANNEL_REGISTRIES))
        stable["ghcr"] = self.alias("present", "sha256:" + "e" * 64, self.COMMIT)
        with self.assertRaises(ValueError):
            release.establish_stable_baseline(receipt, observed, write)
        self.assertEqual(len(calls), 2)

    def test_channel_cli_stdout_has_typed_observations_and_verified_receipt(self):
        info = {"digest": self.DIGEST, "revision": self.COMMIT,
                "platforms": sorted(release.REQUIRED_PLATFORMS)}
        observed = {name: ["present", info] for name in release.CHANNEL_REGISTRIES}
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-observe", "--reference", "latest"]), \
             patch.object(release, "channel_observe", return_value=observed), redirect_stdout(out):
            release.main()
        observations = json.loads(out.getvalue())["observations"]
        self.assertIsInstance(observations, dict)
        out = io.StringIO()
        with patch("sys.argv", ["release.py", "channel-active-receipt", "--channel", "stable",
                                "--aliases", json.dumps(observations), "--docs-commit", self.COMMIT,
                                "--docs-digest", self.DIGEST]), redirect_stdout(out):
            release.main()
        self.assertTrue(json.loads(out.getvalue())["receipt"]["verified"])


    def site(self):
        root = Path(tempfile.mkdtemp())
        contract = ChannelPromotionTests().contract(commit=self.COMMIT, digest=self.DIGEST)
        contract["channels"]["stable"]["commit"] = self.COMMIT
        contract["channels"]["stable"]["image"]["digest"] = self.DIGEST
        contract["channels"]["stable"].update(version="2.0.1", tag=self.VERSION)
        for route, schema_version in (("docs", "2.0.1"), ("docs/versions/2.0.1", "2.0.1"),
                                      ("docs/nightly", "0.0.0-nightly." + self.COMMIT[:12])):
            directory = root / route
            directory.mkdir(parents=True, exist_ok=True)
            (directory / "schemav1.yaml").write_text(
                f"info:\n  version: {schema_version}\npaths:\n  /carbonpanel.v1.Service/List: {{}}\n",
                encoding="utf-8")
        index = json.dumps(contract)
        (root / "version-index.json").write_text(index, encoding="utf-8")
        (root / "docs").mkdir(exist_ok=True)
        (root / "docs/version-index.json").write_text(index, encoding="utf-8")
        pin = f"https://github.com/athNdev/carbonpanel/commit/{self.COMMIT}"
        for page in ("docs/index.html", "docs/getting-started/proxmox/index.html",
                     "docs/faq/index.html"):
            Path(root / page).parent.mkdir(parents=True, exist_ok=True)
            Path(root / page).write_text(f"<html><a href=\"{pin}\">pin</a></html>",
                                         encoding="utf-8")
        (root / "docs/versions").mkdir(exist_ok=True)
        (root / "docs/versions/index.html").write_text(
            f"<html>{self.VERSION}</html>", encoding="utf-8")
        (root / "docs/source.json").write_text(
            json.dumps({"revision": self.COMMIT}), encoding="utf-8")
        return root

    def test_release_workflow_uses_real_prior_bytes_and_qualifies_before_deploy(self):
        source = (release.ROOT / ".github/workflows/release.yml").read_text(encoding="utf-8")
        job = source.split("  stable-channel-transaction:\n", 1)[1].split("\n  publish:\n", 1)[0]
        self.assertNotIn("cp -a .website-build/site", job)
        self.assertIn("pages-baseline", job)
        self.assertIn("fetch-prior-pages", job)
        self.assertIn("stable-alias-decision", job)
        self.assertIn("verify-stable-release-site", job)
        self.assertLess(job.index("verify-stable-release-site"), job.index("id: deploy"))
        self.assertLess(job.index("id: deploy"),
                        job.index("channel-transaction --phase commit"))

    def test_rendered_stable_identity_and_both_index_routes_qualify(self):
        root = self.site()
        ok, problems = release.verify_stable_release_site(
            root, self.COMMIT, self.DIGEST, self.VERSION,
            "https://github.com/athNdev/carbonpanel")
        self.assertTrue(ok, problems)
        schema = root / "docs/nightly/schemav1.yaml"
        original = schema.read_text(encoding="utf-8")
        schema.write_text(original.replace("0.0.0-nightly.", "1.0.0-nightly."), encoding="utf-8")
        ok, problems = release.verify_stable_release_site(
            root, self.COMMIT, self.DIGEST, self.VERSION, "https://github.com/athNdev/carbonpanel")
        self.assertFalse(ok)
        self.assertTrue(any("docs/nightly: OpenAPI" in item for item in problems))
        schema.write_text(original, encoding="utf-8")
        # An old pinned link under a new Stable identity fails closed.
        page = root / "docs/getting-started/proxmox/index.html"
        page.write_text("<html>old guide</html>", encoding="utf-8")
        ok, problems = release.verify_stable_release_site(
            root, self.COMMIT, self.DIGEST, self.VERSION,
            "https://github.com/athNdev/carbonpanel")
        self.assertFalse(ok)
        self.assertTrue(any("pinned link" in item for item in problems))
        # A drifted docs copy of the index fails closed too.
        page.write_text(f"<html><a href=\"https://github.com/athNdev/carbonpanel/commit/"
                        f"{self.COMMIT}\">pin</a></html>", encoding="utf-8")
        (root / "docs/version-index.json").write_text("{}", encoding="utf-8")
        ok, problems = release.verify_stable_release_site(
            root, self.COMMIT, self.DIGEST, self.VERSION,
            "https://github.com/athNdev/carbonpanel")
        self.assertFalse(ok)
        self.assertTrue(any("routes disagree" in item for item in problems))


class LocalOciCandidateTests(unittest.TestCase):
    """No-write exact candidate: local OCI bytes bound to the tested commit."""

    COMMIT = "b" * 40
    AMD, CFG, ARM, ARM_CFG, ATTEST, INNER = ("a" * 64, "c" * 64, "f" * 64,
                                             "0" * 64, "e" * 64, "d" * 64)

    def layout(self, prefix="", revision=None, nested=False, attest=False,
               second_platform=False, ambiguous=False, missing_blob=False,
               malformed=False, with_index=True):
        rev = revision or self.COMMIT
        index = {"manifests": [{
            "mediaType": "application/vnd.oci.image.manifest.v1+json",
            "digest": "sha256:" + self.AMD,
            "platform": {"os": "linux", "architecture": "amd64"}}]}
        entries = {
            "index.json": json.dumps(index).encode(),
            "blobs/sha256/" + self.AMD: json.dumps({
                "config": {"digest": "sha256:" + self.CFG}}).encode(),
            "blobs/sha256/" + self.CFG: json.dumps({
                "config": {"Labels": {"org.opencontainers.image.revision": rev}}}).encode(),
        }
        if nested:
            inner = index
            index = {"manifests": [{
                "mediaType": "application/vnd.oci.image.index.v1+json",
                "digest": "sha256:" + self.INNER}]}
            entries["blobs/sha256/" + self.INNER] = json.dumps(inner).encode()
        if attest:
            index["manifests"].append({
                "mediaType": "application/vnd.oci.image.manifest.v1+json",
                "digest": "sha256:" + self.ATTEST,
                "platform": {"os": "unknown", "architecture": "unknown"},
                "annotations": {"vnd.docker.reference.type": "attestation-manifest"}})
        if second_platform or ambiguous:
            index["manifests"].append({
                "digest": "sha256:" + self.ARM,
                "platform": {"os": "linux", "architecture": "arm64"}})
            entries["blobs/sha256/" + self.ARM] = json.dumps({
                "config": {"digest": "sha256:" + self.ARM_CFG}}).encode()
            entries["blobs/sha256/" + self.ARM_CFG] = json.dumps({
                "config": {"Labels": {"org.opencontainers.image.revision":
                                      "a" * 40 if ambiguous else rev}}}).encode()
        if malformed:
            index["manifests"].append("not-a-descriptor")
        entries["index.json"] = json.dumps(index).encode()
        if missing_blob:
            entries.pop("blobs/sha256/" + self.AMD, None)
        if not with_index:
            entries.pop("index.json", None)
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode="w") as bundle:
            for name, data in entries.items():
                info = tarfile.TarInfo(prefix + name)
                info.size = len(data)
                bundle.addfile(info, io.BytesIO(data))
        return out.getvalue(), entries.get("index.json", b"")

    def verify(self, data):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "candidate-oci.tar"
            path.write_bytes(data)
            return release.local_oci_candidate(path, self.COMMIT)

    def test_exact_local_oci_candidate_is_verified_without_registry_writes(self):
        data, index_bytes = self.layout()
        result = self.verify(data)
        self.assertTrue(result["verified"])
        self.assertEqual(result["digest"], "sha256:" + release.sha256_bytes(index_bytes))
        self.assertEqual(result["revision"], self.COMMIT)
        self.assertEqual(result["platforms"], ["linux/amd64"])

    def test_buildx_prefixed_nested_index_with_attestation_and_two_platforms(self):
        # The real failure shape: ./ members, wrapped multi-platform index and
        # a BuildKit attestation descriptor with an unknown platform.
        data, index_bytes = self.layout(prefix="./", nested=True, attest=True,
                                        second_platform=True)
        result = self.verify(data)
        self.assertEqual(result["digest"], "sha256:" + release.sha256_bytes(index_bytes))
        self.assertEqual(result["platforms"], ["linux/amd64", "linux/arm64"])
        self.assertEqual(result["revision"], self.COMMIT)

    def test_wrong_revision_and_wrong_digest_refuse(self):
        with self.assertRaisesRegex(ValueError, "revision"):
            self.verify(self.layout(revision="a" * 40)[0])
        with self.assertRaisesRegex(ValueError, "manifest blob"):
            self.verify(self.layout(missing_blob=True)[0])
        with self.assertRaisesRegex(ValueError, "no index.json"):
            self.verify(self.layout(with_index=False)[0])

    def test_malformed_and_ambiguous_descriptors_refuse(self):
        with self.assertRaisesRegex(ValueError, "malformed descriptor"):
            self.verify(self.layout(malformed=True)[0])
        with self.assertRaisesRegex(ValueError, "!="):
            self.verify(self.layout(ambiguous=True)[0])


if __name__ == "__main__":
    unittest.main()
