# SPDX-License-Identifier: MPL-2.0
"""Release-boundary checks: version validation, complete inputs and integrity."""
import argparse
import io
import json
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from scripts import release


class ReleaseTests(unittest.TestCase):
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


if __name__ == "__main__":
    unittest.main()
