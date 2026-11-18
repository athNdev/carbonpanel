#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for human-readable immutable Docker build aliases."""
import io
import json
from pathlib import Path
import re
import shlex
import subprocess
import unittest
from unittest.mock import patch

from scripts import docker_readable_aliases as aliases

ROOT = Path(__file__).resolve().parent.parent
CHECKED_IN = ROOT / "docs" / "docker-readable-aliases.json"
FULL_PLATFORMS = [("linux", "amd64"), ("linux", "arm64")]


def present(digest, platforms=None):
    return ("present", {
        "digest": digest,
        "platforms": set(platforms or FULL_PLATFORMS),
    })


def entry(tag, digest, timestamp="2026-11-18T00:00:00.000000Z", commit=None):
    return {
        "tag": tag,
        "commit_id": commit or tag[len("sha-"):],
        "digest": digest,
        "platforms": list(FULL_PLATFORMS),
        "observed_last_updated": timestamp,
    }


class DockerReadableAliasTests(unittest.TestCase):
    def checked_in(self):
        return json.loads(CHECKED_IN.read_text(encoding="utf-8"))

    def test_mapper_reproduces_all_19_inventory_aliases_exactly(self):
        mapping = self.checked_in()
        recomputed = aliases.build_alias_map(mapping["original_sha_tags"])
        expected = {item["tag"]: item["mapping_ref"]
                    for item in mapping["original_sha_tags"]}
        for item in recomputed["aliases"]:
            self.assertEqual(item["final_alias"], expected[item["canonical_tag"]])
        self.assertEqual(len(recomputed["aliases"]), 19)
        self.assertEqual(recomputed["counts"]["distinct_digests"], 19)
        self.assertEqual(recomputed["counts"]["digest_collision_groups"], 0)
        self.assertEqual(recomputed["counts"]["same_second_collision_groups"], 0)
        self.assertTrue(aliases.verify_mapping(mapping))

    def test_deterministic_recomputation_ignores_entry_order(self):
        mapping = self.checked_in()
        forward = aliases.build_alias_map(mapping["original_sha_tags"])
        backward = aliases.build_alias_map(list(reversed(mapping["original_sha_tags"])))
        self.assertEqual(forward, backward)

    def test_digest_collision_emits_one_alias_and_retains_other_sha_refs(self):
        digest = "sha256:" + "a" * 64
        entries = [
            entry("sha-" + "1" * 40, digest, "2026-11-18T00:00:02Z"),
            entry("sha-" + "2" * 40, digest, "2026-11-18T00:00:01Z"),
        ]
        result = aliases.build_alias_map(entries)
        self.assertEqual(len(result["aliases"]), 1)
        first = result["aliases"][0]
        self.assertEqual(first["canonical_tag"], "sha-" + "2" * 40)
        self.assertEqual(first["retained_sha_tags"], ["sha-" + "1" * 40])
        self.assertEqual(first["final_alias"], "build-2026-11-18-000001")

    def test_same_second_collision_gets_deterministic_digest_suffix(self):
        entries = [
            entry("sha-" + "1" * 40, "sha256:" + "b" * 64, "2026-11-18T00:00:02Z"),
            entry("sha-" + "1" * 40, "sha256:" + "c" * 64, "2026-11-18T00:00:02Z"),
        ]
        result = aliases.build_alias_map(entries)
        self.assertEqual([item["final_alias"] for item in result["aliases"]], [
            "build-2026-11-18-000002",
            "build-2026-11-18-000002-2",
        ])
        self.assertEqual(result["counts"]["same_second_collision_groups"], 1)
        ranks = sorted(item["collision_rank"] for item in result["aliases"])
        self.assertEqual(ranks, [1, 2])

    def test_tag_last_pushed_is_preferred_and_no_alias_carries_a_sha_fragment(self):
        raw = entry("sha-" + "1" * 40, "sha256:" + "a" * 64, "2026-11-18T00:00:09Z")
        raw["tag_last_pushed"] = "2026-11-18T00:00:05Z"
        result = aliases.build_alias_map([raw])
        self.assertEqual(result["aliases"][0]["final_alias"], "build-2026-11-18-000005")
        self.assertEqual(result["aliases"][0]["timestamp_source"], "tag_last_pushed")
        self.assertNotIn("1" * 12, result["aliases"][0]["final_alias"])
        for item in self.checked_in()["aliases"]:
            self.assertRegex(item["final_alias"], r"^build-\d{4}-\d{2}-\d{2}-\d{6}(-\d+)?$")

    def test_channel_tags_and_invalid_inputs_are_refused(self):
        for tag in aliases.CHANNEL_TAGS:
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                aliases.build_alias_map([entry(tag, "sha256:" + "a" * 64)])
        bad = [
            entry("sha-" + "1" * 40, "not-a-digest"),
            entry("sha-" + "1" * 40, "sha256:" + "a" * 64, "2026-11-18T00:00:00"),
            {**entry("sha-" + "1" * 40, "sha256:" + "a" * 64), "platforms": ["linux/amd64"]},
        ]
        for item in bad:
            with self.subTest(item=item), self.assertRaises(ValueError):
                aliases.build_alias_map([item])

    def test_plan_reuses_idempotent_alias_and_refuses_conflict_or_unknown(self):
        mapping = self.checked_in()
        mapping["aliases"] = mapping["aliases"][:1]
        item = mapping["aliases"][0]
        source_ref = "atharvnaphade/carbonpanel:sha-" + item["commit_id"]
        target_ref = "atharvnaphade/carbonpanel:" + item["final_alias"]
        def observer(ref, strict=False):
            if ref == source_ref:
                return present(item["digest"])
            return present(item["digest"])
        plan, problems = aliases.alias_publication_plan(mapping, observer)
        self.assertEqual(problems, [])
        self.assertEqual(plan[0]["action"], "reuse")
        with patch.object(subprocess, "run") as command:
            aliases.apply_aliases(mapping, observer, lambda *a, **k: command(*a, **k), True)
            command.assert_not_called()

        def conflicting(ref, strict=False):
            if ref == source_ref:
                return present(item["digest"])
            return present("sha256:" + "f" * 64)
        plan, problems = aliases.alias_publication_plan(mapping, conflicting)
        self.assertTrue(problems)
        with self.assertRaisesRegex(ValueError, "refused"):
            aliases.apply_aliases(mapping, conflicting, lambda *a, **k: None, True)

        def unknown(ref, strict=False):
            if ref == source_ref:
                return present(item["digest"])
            return ("unknown", None) if ref == target_ref else present(item["digest"])
        plan, problems = aliases.alias_publication_plan(mapping, unknown)
        self.assertTrue(problems)
        self.assertEqual(plan, [])

    def test_apply_adds_only_alias_refs_without_rebuild_or_channel_moves(self):
        mapping = self.checked_in()
        calls = []
        # Source refs resolve to their mapped digest; all aliases are absent.
        digests = {f"sha-{item['commit_id']}": item["digest"] for item in mapping["aliases"]}
        def observe(ref, strict=False):
            name = ref.rsplit(":", 1)[1]
            if name in digests:
                return present(digests[name])
            return ("absent", None)
        def runner(*args, **kwargs):
            calls.append(args)
            return subprocess.CompletedProcess(args, 0)
        plan = aliases.apply_aliases(mapping, observe, runner, True)
        self.assertEqual(len(plan), 19)
        self.assertEqual(len(calls), 19)
        for call in calls:
            self.assertEqual(call[0:4], ("docker", "buildx", "imagetools", "create"))
            self.assertNotIn("build", call)
            self.assertNotIn("--push", call)
            target = call[call.index("--tag") + 1]
            self.assertIn(":build-", target)
            self.assertTrue(target.rsplit(":", 1)[1].startswith("build-"))
            self.assertNotIn(target, aliases.CHANNEL_TAGS)
            self.assertNotIn("sha-", target.rsplit(":", 1)[1])

    def test_gate_requires_main_dispatch_confirm_and_verified_ci(self):
        main = "refs/heads/main"
        self.assertTrue(aliases.publication_gate(
            "workflow_dispatch", "true", main, "true")[0])
        for event, confirm, ref, ci in (
            ("push", "true", main, "true"),
            ("workflow_dispatch", "false", main, "true"),
            ("workflow_dispatch", "true", "refs/heads/feature/x", "true"),
            ("workflow_dispatch", "true", main, "false"),
            ("schedule", "true", main, "true"),
        ):
            with self.subTest(event=event, ref=ref):
                self.assertFalse(aliases.publication_gate(event, confirm, ref, ci)[0])


    def test_workflow_contract_is_dispatch_only_and_publication_free_by_default(self):
        import yaml
        path = ROOT / ".github" / "workflows" / "docker-readable-aliases.yml"
        source = path.read_text(encoding="utf-8")
        document = yaml.safe_load(source)
        triggers = document.get("on") or document.get(True)
        self.assertEqual(set(triggers), {"workflow_dispatch"})
        self.assertFalse(triggers["workflow_dispatch"]["inputs"]["confirm"]["default"])
        self.assertFalse(document["concurrency"]["cancel-in-progress"])
        self.assertIn("python3 scripts/release.py verify-main-ci", source)
        self.assertIn("python3 scripts/docker_readable_aliases.py gate", source)
        self.assertIn("python3 scripts/docker_readable_aliases.py validate", source)
        self.assertIn("--dry-run", source)
        self.assertIn("steps.gate.outputs.publish == 'true'", source)
        self.assertNotIn("docker buildx build", source)
        self.assertNotIn("--push", source)
        self.assertNotIn("latest", source.split("jobs:")[0])  # no channel trigger


    def workflow_commands(self, publish):
        """Extract the actual python commands from each run: step.

        A tiny deterministic evaluator handles the explicit PUBLISH_ALIASES
        shell branch; shlex tokenizes the selected command exactly as a shell
        would. No registry access occurs.
        """
        import yaml
        source = (ROOT / ".github" / "workflows" / "docker-readable-aliases.yml").read_text(
            encoding="utf-8")
        document = yaml.safe_load(source)
        result = {}
        for step in document["jobs"]["add-aliases"]["steps"]:
            block = step.get("run", "")
            if not block.strip() or "python3 scripts/" not in block:
                continue
            selected = []
            taking = True
            if "if [" in block:
                taking = False
                for raw in block.splitlines():
                    text = raw.strip()
                    if text.startswith("if ["):
                        taking = ('= "true"' in text and publish) or (
                            '= "false"' in text and not publish)
                    elif text == "else":
                        taking = not taking
                    elif text == "fi":
                        taking = False
                    elif taking and text.startswith("python3 "):
                        selected.append(shlex.split(text.rstrip(" \\")))
            else:
                for raw in block.splitlines():
                    text = raw.strip()
                    if text.startswith("python3 "):
                        selected.append(shlex.split(text.rstrip(" \\")))
            result[step["name"]] = selected
        return result, source

    def test_workflow_validate_input_and_apply_dry_run_follow_the_gate(self):
        source = (ROOT / ".github" / "workflows" / "docker-readable-aliases.yml").read_text(
            encoding="utf-8")
        # A plain scalar ending in backslash folds '\ ' into a literal escaped
        # space and broke the real backfill run. Forbid it in every run: step.
        self.assertNotRegex(source, r"(?m)^\s*run:\s+[^\n]*\\\s*$")
        self.assertNotIn("&& '' || '--dry-run'", source)
        self.assertIn("PUBLISH_ALIASES: ${{ steps.gate.outputs.publish }}", source)

        for publish in (True, False):
            commands, _ = self.workflow_commands(publish)
            validate = commands["Validate the checked-in alias mapping"][0]
            self.assertEqual(validate[:2], ["python3", "scripts/docker_readable_aliases.py"])
            self.assertIn("--input", validate)
            self.assertEqual(validate[validate.index("--input") + 1],
                             "docs/docker-readable-aliases.json")
            apply = commands[
                "Add missing aliases by exact digest (dry run unless confirmed)"][0]
            self.assertEqual(apply[:2], ["python3", "scripts/docker_readable_aliases.py"])
            self.assertEqual(apply[2], "apply")
            self.assertIn("--mapping", apply)
            if publish:
                self.assertNotIn("--dry-run", apply)
            else:
                self.assertIn("--dry-run", apply)

        # Execute the exact rendered validate command as harmless evidence.
        commands, _ = self.workflow_commands(True)
        validate = commands["Validate the checked-in alias mapping"][0]
        completed = subprocess.run(validate, cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn('"aliases": 19', completed.stdout)

    def test_apply_verifies_each_target_digest_and_preserves_sha_and_channel_refs(self):
        mapping = self.checked_in()
        prefix = "atharvnaphade/carbonpanel:"
        digests = {f"sha-{item['commit_id']}": item["digest"] for item in mapping["aliases"]}
        alias_digests = {item["final_alias"]: item["digest"] for item in mapping["aliases"]}
        sha_state = {prefix + tag: present(digest) for tag, digest in digests.items()}
        channel_state = {prefix + tag: present("sha256:" + "9" * 64)
                         for tag in aliases.CHANNEL_TAGS}
        state = {**sha_state, **channel_state}
        def observe(ref, strict=False):
            return state.get(ref, ("absent", None))
        def runner(*args, **kwargs):
            target = args[args.index("--tag") + 1]
            state[target] = present(alias_digests[target.rsplit(":", 1)[1]])
            return subprocess.CompletedProcess(args, 0)
        aliases.apply_aliases(mapping, observe, runner, True)
        for item in mapping["aliases"]:
            state_ref = prefix + item["final_alias"]
            self.assertEqual(state[state_ref][0], "present")
            self.assertEqual(state[state_ref][1]["digest"], item["digest"])
            self.assertTrue(set(FULL_PLATFORMS) <= state[state_ref][1]["platforms"])
        for ref, observation in {**sha_state, **channel_state}.items():
            self.assertEqual(state[ref], observation)
        # Conflict refuses with zero writes even after a successful dry run.
        conflict_state = dict(state)
        conflict_state[prefix + mapping["aliases"][0]["final_alias"]] = present(
            "sha256:" + "e" * 64)
        with self.assertRaisesRegex(ValueError, "refused"):
            aliases.apply_aliases(mapping, lambda ref, strict=False: conflict_state.get(
                ref, ("absent", None)), lambda *a, **k: self.fail("unexpected write"), True)


if __name__ == "__main__":
    unittest.main()
