# SPDX-License-Identifier: MPL-2.0
"""Offline coverage for the packaged restore provenance checks."""

import hashlib
import io
import json
from pathlib import Path
import tarfile
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch
from zipfile import ZipFile

from scripts.qualify_packaged_panel_restore import unpack_candidate


COMMIT = "a" * 40
RUN = "100"
NAME = "carbon-panel_v2.0.1_linux_amd64.tar.gz"
VERSION = f"carbon-panel 2.0.1 (commit {COMMIT}; built test; linux/amd64)"


class PackagedRestoreTests(unittest.TestCase):
    def setUp(self):
        scratch = TemporaryDirectory()
        self.addCleanup(scratch.cleanup)
        self.workspace = Path(scratch.name)
        self.artifact = self.workspace / "artifact.zip"
        self.report = {}

    def write_fixture(self, manifest_commit=COMMIT, checksum=None, symlink=False):
        contents = io.BytesIO()
        with tarfile.open(fileobj=contents, mode="w:gz") as archive:
            data = b"fixture not an executable"
            member = tarfile.TarInfo("carbon-panel")
            if symlink:
                member.type = tarfile.SYMTYPE
                member.linkname = "../../elsewhere"
                archive.addfile(member)
            else:
                member.size = len(data)
                archive.addfile(member, io.BytesIO(data))
        digest = hashlib.sha256(contents.getvalue()).hexdigest()
        manifest = {
            "version": "v2.0.1",
            "commit": manifest_commit,
            "artifacts": [{"name": NAME, "sha256": digest, "os": "linux", "arch": "amd64"}],
        }
        with ZipFile(self.artifact, "w") as staged:
            staged.writestr(NAME, contents.getvalue())
            staged.writestr("checksums.txt", f"{checksum or digest}  {NAME}\n")
            staged.writestr("release-manifest.json", json.dumps(manifest))
        return {
            "id": 1,
            "name": "verified-release-downloads",
            "workflow_run": {"id": int(RUN), "head_sha": COMMIT},
            "digest": "sha256:" + hashlib.sha256(self.artifact.read_bytes()).hexdigest(),
        }

    def unpack(self, metadata, version=VERSION):
        with patch("scripts.qualify_packaged_panel_restore.binary_version", return_value=version):
            return unpack_candidate(self.artifact, metadata, RUN, COMMIT, self.workspace, self.report)

    def test_verified_records_and_stamp_identify_binary(self):
        binary = self.unpack(self.write_fixture())
        self.assertEqual(binary.read_bytes(), b"fixture not an executable")
        self.assertEqual(self.report["artifact_id"], 1)
        self.assertEqual(self.report["binary_version"], VERSION)

    def test_tampered_outer_artifact_fails_closed(self):
        metadata = self.write_fixture()
        with self.artifact.open("ab") as artifact:
            artifact.write(b"changed")
        with self.assertRaisesRegex(ValueError, "verified downloads record"):
            self.unpack(metadata)

    def test_mismatched_release_run_fails_closed(self):
        metadata = self.write_fixture()
        metadata["workflow_run"]["id"] += 1
        with self.assertRaisesRegex(ValueError, "expected release run"):
            self.unpack(metadata)

    def test_release_workflow_head_can_differ_from_selected_product_revision(self):
        metadata = self.write_fixture()
        workflow_head = "b" * 40
        metadata["workflow_run"]["head_sha"] = workflow_head
        self.unpack(metadata)
        self.assertEqual(self.report["release_workflow_commit"], workflow_head)

    def test_mismatched_manifest_commit_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "release manifest"):
            self.unpack(self.write_fixture(manifest_commit="b" * 40))

    def test_mismatched_inner_checksum_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "staged checksum records"):
            self.unpack(self.write_fixture(checksum="b" * 64))

    def test_symlink_binary_refused(self):
        with self.assertRaisesRegex(ValueError, "regular Panel binary"):
            self.unpack(self.write_fixture(symlink=True))

    def test_wrong_binary_build_stamp_fails_closed(self):
        with self.assertRaisesRegex(ValueError, "unexpected build identity"):
            self.unpack(self.write_fixture(), version=VERSION.replace(COMMIT, "b" * 40))


if __name__ == "__main__":
    unittest.main()
