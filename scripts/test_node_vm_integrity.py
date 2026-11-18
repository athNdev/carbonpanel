# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for checksum, SBOM and signing-plan generation."""
import json
from pathlib import Path
import unittest

from scripts import node_vm_integrity as integrity

ROOT = Path(__file__).resolve().parent.parent


class IntegrityFixtureTests(unittest.TestCase):
    def setUp(self):
        self.config = json.loads(
            (ROOT / "packaging/node-vm/config/build-config.json").read_text())
        self.provenance = json.loads(
            (ROOT / "packaging/node-vm" / self.config["provenance_file"]).read_text())

    def test_checksum_manifest_deterministic_and_verifiable(self):
        files = {"b.txt": b"two", "a.txt": b"one"}
        text = integrity.build_checksum_manifest(files)
        self.assertEqual(text.splitlines()[0].split()[1], "a.txt")
        self.assertEqual(text, integrity.build_checksum_manifest({"a.txt": b"one", "b.txt": b"two"}))
        self.assertTrue(integrity.verify_checksum_manifest(text, files))
        self.assertFalse(integrity.verify_checksum_manifest(text, {"a.txt": b"tampered", "b.txt": b"two"}))
        self.assertFalse(integrity.verify_checksum_manifest(text, {"a.txt": b"one"}))

    def test_sbom_is_deterministic_cyclonedx(self):
        artifacts = {"agent.pyz": b"fixture-agent"}
        a = integrity.build_sbom(self.config, self.provenance, artifacts)
        b = integrity.build_sbom(self.config, self.provenance, artifacts)
        self.assertEqual(a, b)
        self.assertEqual(a["bomFormat"], "CycloneDX")
        self.assertEqual(a["specVersion"], "1.5")
        self.assertTrue(a["serialNumber"].startswith("urn:uuid:"))
        self.assertEqual(a["metadata"]["timestamp"], "2023-11-14T22:13:20Z")
        names = [c["name"] for c in a["components"]]
        self.assertEqual(names, sorted(names))
        self.assertIn("qemu_img", names)
        self.assertIn("ubuntu-24.04-amd64", names)
        self.assertIn("agent.pyz", names)
        blob = json.dumps(a)
        for forbidden in ("PRIVATE KEY", "enrollment_secret", "token", "password"):
            self.assertNotIn(forbidden, blob)

    def test_signing_material_in_config_is_refused(self):
        integrity.check_no_signing_secret(self.config)
        bad = dict(self.config)
        bad["signing_key_path"] = "/etc/private/key.pem"
        with self.assertRaises(integrity.IntegrityError):
            integrity.check_no_signing_secret(bad)

    def test_signing_plan_has_no_baked_key(self):
        plan = integrity.signing_plan(["SHA256SUMS"])
        self.assertFalse(plan["execute"])
        self.assertTrue(plan["no_baked_key"])
        self.assertIn("CARBON_VM_SIGNING_KEY_ID", plan["key_supplied_via_environment"])
        blob = json.dumps(plan)
        self.assertNotIn("/etc/", blob)
        self.assertNotIn("PRIVATE KEY", blob)
        with self.assertRaises(integrity.IntegrityError):
            integrity.signing_plan([])

    def test_generate_produces_all_artifacts(self):
        result = integrity.generate(ROOT)
        self.assertEqual(set(result), {"SHA256SUMS", "sbom.cdx.json", "signing-plan.json"})
        sbom = json.loads(result["sbom.cdx.json"])
        self.assertEqual(sbom["bomFormat"], "CycloneDX")
        self.assertIn("build-config.json", result["SHA256SUMS"])
        self.assertFalse(json.loads(result["signing-plan.json"])["execute"])

    def test_generate_is_deterministic(self):
        self.assertEqual(integrity.generate(ROOT), integrity.generate(ROOT))


if __name__ == "__main__":
    unittest.main()
