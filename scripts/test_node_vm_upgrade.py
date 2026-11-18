# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for staged upgrade/rollback and persistent-volume safety."""
import json
from pathlib import Path
import unittest

from scripts import node_vm_upgrade as upgrade

ROOT = Path(__file__).resolve().parent.parent
POLICY = json.loads((ROOT / "packaging/node-vm/config/upgrade-policy.json").read_text())


class UpgradeFixtureTests(unittest.TestCase):
    def setUp(self):
        self.old_files = {
            "carbon-node/agent": b"old-agent-bytes",
            "cloud-init/user-data": b"old-user-data",
        }

    def test_plan_upgrade_stages_before_promotion(self):
        plan = upgrade.plan_upgrade("0.1.0-fixture", "0.2.0-fixture",
                                    self.old_files, POLICY, "fixture-batch-0001")
        self.assertEqual(plan["batch_id"], "fixture-batch-0001")
        self.assertEqual(plan["upgrade_dir"], ".carbon-vm/upgrade/0.2.0-fixture")
        self.assertEqual(plan["undo_dir"], ".carbon-vm/undo/fixture-batch-0001")
        steps = plan["steps"]
        self.assertLess(steps.index("undo: write and verify " + plan["undo_dir"] +
                                    "/manifest.json before mutation"),
                        next(i for i, s in enumerate(steps) if s.startswith("promote:")))
        self.assertEqual(len(plan["undo_manifest"]["entries"]), 2)
        self.assertIn("/srv/carbon/worlds", plan["never_delete_paths"])
        self.assertEqual(plan["audit"]["outcome"], "planned")
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.plan_upgrade("0.1.0", "0.1.0", self.old_files, POLICY, "b")

    def test_verify_undo_and_refuse_on_mutation(self):
        plan = upgrade.plan_upgrade("a", "b", self.old_files, POLICY, "b1")
        self.assertTrue(upgrade.verify_undo(plan["undo_manifest"], self.old_files))
        mutated = dict(self.old_files)
        mutated["carbon-node/agent"] = b"tampered"
        self.assertFalse(upgrade.verify_undo(plan["undo_manifest"], mutated))

    def test_rollback_requires_verified_undo(self):
        plan = upgrade.plan_upgrade("a", "b", self.old_files, POLICY, "b1")
        ok = upgrade.plan_rollback(plan["undo_manifest"], self.old_files, POLICY)
        self.assertTrue(ok["no_volume_delete"])
        self.assertTrue(ok["keep_current_artifact_for_forensics"])
        mutated = dict(self.old_files)
        mutated["cloud-init/user-data"] = b"changed"
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.plan_rollback(plan["undo_manifest"], mutated, POLICY)

    def test_volume_import_is_copy_only_and_path_scoped(self):
        imp = upgrade.plan_volume_import("/owned/source.qcow2",
                                         "/srv/carbon/worlds/imported.qcow2",
                                         "abc123", POLICY)
        self.assertTrue(imp["copy_only"])
        self.assertFalse(imp["overwrite"])
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.plan_volume_import("/owned/src", "/etc/passwd", "abc", POLICY)

    def test_backup_and_recover_verify_hash(self):
        volume = b"persistent-world-bytes"
        backup = upgrade.plan_volume_backup("/srv/carbon/worlds/world",
                                            "/var/backups/carbon", volume, POLICY)
        self.assertTrue(backup["copy_only"])
        self.assertTrue(backup["preserve_source"])
        recovered = upgrade.plan_volume_recover(backup, volume,
                                                "/srv/carbon/worlds/restored", POLICY)
        self.assertTrue(recovered["no_volume_delete"])
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.plan_volume_recover(backup, b"corrupt", "/srv/carbon/worlds/restored", POLICY)

    def test_fixture_sequence_matches_planner(self):
        fixture = json.loads((ROOT / "packaging/node-vm/qualification/fixtures/upgrade-sequence.json").read_text())
        plan = upgrade.plan_upgrade(fixture["from_revision"], fixture["to_revision"],
                                    self.old_files, POLICY, fixture["batch_id"])
        self.assertEqual(plan["from_revision"], fixture["from_revision"])
        self.assertEqual(plan["to_revision"], fixture["to_revision"])
        self.assertEqual(plan["batch_id"], fixture["batch_id"])


if __name__ == "__main__":
    unittest.main()
