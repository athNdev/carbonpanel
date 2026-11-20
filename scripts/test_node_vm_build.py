# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for the reproducible node-VM build driver."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from scripts import node_vm_build as build

ROOT = Path(__file__).resolve().parent.parent

# Two lines from the real, GPG-signed upstream SHA256SUMS (evidence in
# packaging/node-vm/provenance/RETRIEVAL-EVIDENCE.md). Public data, not a secret.
PUBLISHED_SUMS = (
    "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94 "
    "*ubuntu-24.04-server-cloudimg-amd64.img\n"
    "50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e "
    "*ubuntu-24.04-server-cloudimg-arm64.img\n"
)


class BuildFixtureTests(unittest.TestCase):
    def setUp(self):
        self.root, self.pkg, self.config, self.provenance, self.policy, self.upgrade = \
            build.load_inputs(ROOT, "fixture")

    def test_inputs_load_and_pin_provenance(self):
        self.assertEqual(self.config["cloud_init"]["datasource"], "NoCloud")
        self.assertEqual(self.config["toolchain"]["qemu_img"], "8.2.0")
        # Offline tests use the explicitly synthetic fixture profile.
        self.assertEqual(self.provenance["profile"], "fixture")
        self.assertTrue(self.provenance["synthetic"])
        self.assertEqual(self.provenance["images"][0]["digest_status"], "fixture-placeholder")
        self.assertFalse(self.policy["fork_allowed"])
        self.assertTrue(self.policy["contract_is_shared"])

        # The real artifact profile is immutable, dated and signature-backed.
        _, _, _, real, _, _ = build.load_inputs(ROOT, "artifact")
        self.assertEqual(real["profile"], "artifact")
        self.assertFalse(real["synthetic"])
        for image in real["images"]:
            self.assertEqual(image["digest_status"], "verified")
            self.assertRegex(image["image_url"], r"/release-20261017/")
            self.assertNotRegex(image["expected_sha256"], r"^(0|1|a|b|f)\1{63}$")
            self.assertTrue(build.verify_artifact_provenance(real, image))
            self.assertTrue(build.verify_image_checksum(image, PUBLISHED_SUMS))
            with self.assertRaises(build.BuildError):
                build.verify_image_checksum(image, "0" * 64 + " *wrong.img\n")
        # Artifact mode emits a real-pin plan, but never builds a QCOW2.
        artifact_plan = build.offline_build(ROOT, None, profile="artifact")["plan"]
        self.assertEqual(artifact_plan["provenance_profile"], "artifact")
        self.assertIn("release-20261017", artifact_plan["base_image"]["image_url"])
        self.assertTrue(artifact_plan["no_qcow2_built"])
        # Fixture digests must be refused by artifact verification.
        with self.assertRaises(build.BuildError):
            build.verify_artifact_provenance(real, self.provenance["images"][0])

    def test_base_image_selection(self):
        image = build.base_image(self.provenance, "ubuntu-24.04-amd64")
        self.assertEqual(image["arch"], "amd64")
        with self.assertRaises(build.BuildError):
            build.base_image(self.provenance, "does-not-exist")

    def test_render_templates_have_only_public_metadata(self):
        rendered = build.render_templates(ROOT, self.config, self.policy,
                                          "fixture-instance-0001", "fixture01")
        self.assertEqual(set(rendered), {"user-data", "meta-data", "network-config"})
        for name, text in rendered.items():
            self.assertNotIn("@@", name + text)
        self.assertIn("fixture-instance-0001", rendered["meta-data"])
        self.assertIn("#cloud-config", rendered["user-data"])
        self.assertIn('match:\n      name: "en*"', rendered["network-config"])
        self.assertNotIn("ens160:", rendered["network-config"])
        self.assertIn("dhcp4: true", rendered["network-config"])
        # the first-boot script is embedded as base64, not as an executable secret
        self.assertIn("encoding: b64", rendered["user-data"])
        self.assertTrue(build.check_no_baked_secrets(self.config, self.policy, rendered))
        for forbidden in ("BEGIN PRIVATE KEY", "enrollment_secret:", "password:"):
            self.assertNotIn(forbidden, rendered["user-data"])

    def test_baked_secret_is_refused(self):
        rendered = build.render_templates(ROOT, self.config, self.policy,
                                          "fixture-instance-0001", "fixture01")
        poisoned = dict(rendered)
        poisoned["user-data"] += "\nenrollment_secret: SENTINEL_SECRET_DO_NOT_EMBED\n"
        with self.assertRaises(build.BuildError):
            build.check_no_baked_secrets(self.config, self.policy, poisoned)
        poisoned = dict(rendered)
        poisoned["meta-data"] += "\n-----BEGIN PRIVATE KEY-----\n"
        with self.assertRaises(build.BuildError):
            build.check_no_baked_secrets(self.config, self.policy, poisoned)
        bad_config = dict(self.config)
        bad_config["password"] = "x"
        with self.assertRaises(build.BuildError):
            build.check_no_baked_secrets(bad_config, self.policy, rendered)
        bad_policy = dict(self.policy)
        bad_policy["fork_allowed"] = True
        with self.assertRaises(build.BuildError):
            build.check_no_baked_secrets(self.config, bad_policy, rendered)

    def test_manifest_is_deterministic_and_hashes_files(self):
        files = {"user-data": "alpha", "meta-data": "beta"}
        m1 = build.deterministic_manifest(files, "1700000000", "fixture-placeholder")
        m2 = build.deterministic_manifest(files, "1700000000", "fixture-placeholder")
        self.assertEqual(m1, m2)
        self.assertEqual(m1["generated_at"], "2023-11-14T22:13:20Z")
        by_path = {entry["path"]: entry for entry in m1["files"]}
        self.assertEqual(by_path["nocloud/user-data"]["sha256"], hashlib.sha256(b"alpha").hexdigest())
        self.assertEqual(len(m1["sha256"]), 64)
        different = build.deterministic_manifest(files, "1700000001", "fixture-placeholder")
        self.assertNotEqual(m1["sha256"], different["sha256"])

    def test_offline_build_writes_seed_and_plan(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = build.offline_build(ROOT, tmp, "fixture-instance-0001", "fixture01", check=True)
            out = Path(tmp)
            self.assertTrue((out / "nocloud" / "user-data").exists())
            self.assertTrue((out / "nocloud" / "meta-data").exists())
            self.assertTrue((out / "nocloud" / "network-config").exists())
            manifest = json.loads((out / "seed-manifest.json").read_text())
            plan = json.loads((out / "build-plan.json").read_text())
            self.assertEqual(manifest["sha256"], result["manifest"]["sha256"])
            self.assertEqual(len(manifest["files"]), 3)
            self.assertTrue(plan["fixture_only"])
            self.assertEqual(plan["provenance_profile"], "fixture")
            self.assertEqual(plan["base_image"]["digest_status"], "fixture-placeholder")
            self.assertTrue(plan["commands_not_executed"])
            self.assertNotIn("secret", json.dumps(plan).lower())

    def test_distinct_instance_ids_change_the_seed(self):
        a = build.offline_build(ROOT, None, "fixture-instance-0001", "fixture01")
        b = build.offline_build(ROOT, None, "fixture-instance-0002", "fixture02")
        self.assertNotEqual(a["manifest"]["sha256"], b["manifest"]["sha256"])


if __name__ == "__main__":
    unittest.main()
