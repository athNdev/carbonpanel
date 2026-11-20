#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Focused integration coverage for the maintained operational-guide overlay."""
import copy
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from scripts import maintained_guides as mg
from scripts import version_contract as vc
from scripts.build_website import export_snapshot, normalize_mdx, scoped_links

ROOT = Path(__file__).resolve().parent.parent
OLD_COMMAND = "community-scripts/ProxmoxVE/main/ct/carbon-panel.sh"
OLD_ALIAS = "enter `update`"
OLD_SUMMARY = "<summary>Proxmox Helper Script</summary>"
NEW_SUMMARY = "<summary>Proxmox (existing guest, first-party script)</summary>"


def git(root, *args):
    return subprocess.check_output(["git", *args], cwd=root, text=True).strip()


def changed_files(before, after):
    return {path for path in after if before.get(path) != after.get(path)}


def digest_map(directory):
    return {path.relative_to(directory).as_posix(): hashlib.sha256(path.read_bytes()).hexdigest()
            for path in directory.rglob("*") if path.is_file()}


class MaintainedGuidesTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.declaration = mg.load(ROOT)
        cls.revision, cls.origin = mg.derive_revision(ROOT)
        contract, catalog = vc.load(ROOT)
        cls.index = vc.version_index(contract, catalog, ROOT)
        cls.pinned_pages = {v["channel"]: set(v["pages"]) for v in cls.index["versions"]}
        mg.validate(cls.declaration, ROOT, cls.revision, cls.pinned_pages)

    def _stage(self, commit, base, target=ROOT):
        """Export a real pinned channel and stage its maintained guides."""
        temporary = tempfile.TemporaryDirectory()
        snapshot = Path(temporary.name)
        export_snapshot(target, commit, snapshot)
        project = snapshot / "docs/carbon-panel"
        docs = project / "src/content/docs"
        public = project / "public"
        before = digest_map(project)
        baseline = (docs / "getting-started/proxmox.mdx").read_text(encoding="utf-8")
        records = mg.apply(self.declaration, docs, public, commit, revision=self.revision,
                           revision_origin=self.origin, root=target)
        rendered = {}
        for record in records:
            page = docs / record["target_file"]
            text = scoped_links(page.read_text(encoding="utf-8"), base, self.revision)
            page.write_text(normalize_mdx(text), encoding="utf-8")
            rendered[record["slug"]] = page.read_text(encoding="utf-8")
        return temporary, project, before, rendered, records, baseline

    def test_manifest_is_classification_only_and_revision_is_derived_not_pinned(self):
        raw = json.loads((ROOT / "docs/maintained-guides.json").read_text(encoding="utf-8"))
        self.assertNotIn("source", raw)
        for guide in raw["guides"]:
            for key, value in guide.items():
                self.assertFalse(any(token in key for token in ("sha", "revision", "commit")), key)
                self.assertIsInstance(value, (str, dict))
        self.assertEqual(self.revision, git(ROOT, "rev-parse", "HEAD"))
        self.assertEqual((self.revision, self.origin), mg.derive_revision(ROOT))
        self.assertFalse(hasattr(mg, "ENV_REVISION"))  # no stale secondary revision input

    def test_both_active_channels_render_the_first_party_guide(self):
        for version in self.index["versions"]:
            with self.subTest(channel=version["channel"]):
                temporary, project, before, rendered, records, baseline = self._stage(
                    version["commit"], version["route"])
                with temporary:
                    proxmox = rendered["getting-started/proxmox"]
                    faq = rendered["faq"]
                    # The exact pinned baseline really did carry the retired helper.
                    self.assertIn(OLD_COMMAND, baseline)
                    # First-party install, guest instructions and --compose update guidance.
                    self.assertIn("install-carbon-panel.sh", proxmox)
                    self.assertIn("inside the guest", proxmox)
                    self.assertIn("docker compose", proxmox)
                    self.assertIn("install-carbon-panel.sh update --dir", proxmox)
                    self.assertIn("--compose", proxmox)
                    self.assertIn(mg.GUIDE_SOURCE_LABEL, proxmox)
                    self.assertIn(
                        f"{self.declaration['repository']}/blob/{self.revision}/"
                        "docs/carbon-panel/src/content/docs/getting-started/proxmox.mdx",
                        proxmox)
                    self.assertIn(version["route"] + "getting-started/docker-compose/", proxmox)
                    # The retired third-party command is gone from the rendered page.
                    self.assertNotIn(OLD_COMMAND, proxmox)
                    self.assertNotIn('bash -c "$(curl -fsSL', proxmox)
                    # FAQ: corrected shared entry, old update alias gone, rest versioned.
                    self.assertIn(NEW_SUMMARY, faq)
                    self.assertIn("install-carbon-panel.sh update --dir /opt/carbon-panel", faq)
                    self.assertIn(version["route"] + "getting-started/proxmox/", faq)
                    self.assertNotIn(OLD_ALIAS, faq)
                    self.assertNotIn(OLD_SUMMARY, faq)
                    self.assertIn("<summary>Source or prebuilt binary</summary>", faq)
                    # Provenance attests the derived exact source, never the declaration.
                    document = json.loads((project / "public/maintained-guides.json").read_text(
                        encoding="utf-8"))
                    self.assertEqual(document["source"]["revision"], self.revision)
                    self.assertEqual(document["source"]["origin"], self.origin)
                    self.assertEqual(document["source"]["pinned_url"],
                                     f"{self.declaration['repository']}/commit/{self.revision}")
                    self.assertEqual(document["compatibility"], self.declaration["compatibility"])
                    for record in records:
                        self.assertEqual(record["channel_commit"], version["commit"])
                        self.assertEqual(record["source_revision"], self.revision)
                        expected = hashlib.sha256(mg.source_bytes(
                            ROOT, self.revision, record["source_path"])).hexdigest()
                        self.assertEqual(record["source_sha256"], expected)
                    # Only declared guides and the provenance file changed. Product
                    # pins, generated identity and unrelated pages stay untouched.
                    changed = changed_files(before, digest_map(project))
                    expected_files = {"src/content/docs/" + r["target_file"] for r in records}
                    expected_files.add("public/maintained-guides.json")
                    self.assertEqual(changed, expected_files)
                    self.assertNotIn("public/source.json", changed)

    def test_immutable_alias_matches_the_active_stable_route(self):
        stable = next(v for v in self.index["versions"] if v["channel"] == "stable")
        first, active, _, active_rendered, _, _ = self._stage(stable["commit"], stable["route"])
        with first:
            second, alias, _, alias_rendered, _, _ = self._stage(
                stable["commit"], stable["route"])
            with second:
                self.assertEqual(active_rendered, alias_rendered)
                self.assertEqual(
                    (active / "public/maintained-guides.json").read_text(encoding="utf-8"),
                    (alias / "public/maintained-guides.json").read_text(encoding="utf-8"))

    def test_already_current_channel_is_idempotent(self):
        """A later pinned Nightly that already contains #632 must not fail."""
        with tempfile.TemporaryDirectory() as temporary:
            snapshot = Path(temporary)
            export_snapshot(ROOT, self.revision, snapshot)
            project = snapshot / "docs/carbon-panel"
            docs = project / "src/content/docs"
            before = digest_map(project)
            baseline_faq = (docs / "faq.mdx").read_text(encoding="utf-8")
            self.assertIn(NEW_SUMMARY, baseline_faq)
            self.assertNotIn(OLD_SUMMARY, baseline_faq)
            records = mg.apply(self.declaration, docs, project / "public", self.revision,
                               revision=self.revision, revision_origin=self.origin, root=ROOT)
            after = digest_map(project)
            self.assertEqual(changed_files(before, after),
                             {"src/content/docs/getting-started/proxmox.mdx",
                              "public/maintained-guides.json"})
            mg.apply(self.declaration, docs, project / "public", self.revision,
                     revision=self.revision, revision_origin=self.origin, root=ROOT)
            self.assertEqual(after, digest_map(project))  # repeated overlay is a no-op
            self.assertEqual(len(records), 2)
            self.assertIn(mg.GUIDE_SOURCE_LABEL,
                          (docs / "getting-started/proxmox.mdx").read_text(encoding="utf-8"))
            self.assertIn("install-carbon-panel.sh update --dir",
                          (docs / "faq.mdx").read_text(encoding="utf-8"))
            self.assertNotIn(OLD_COMMAND,
                             (docs / "getting-started/proxmox.mdx").read_text(encoding="utf-8"))

    def test_second_committed_source_version_refreshes_without_manifest_edit(self):
        manifest_before = (ROOT / "docs/maintained-guides.json").read_bytes()
        declaration = copy.deepcopy(self.declaration)
        with tempfile.TemporaryDirectory() as temporary:
            repo = Path(temporary)
            git(repo, "init", "--quiet")
            git(repo, "config", "core.autocrlf", "false")
            source_dir = repo / "docs/carbon-panel/src/content/docs"
            (source_dir / "getting-started").mkdir(parents=True)
            proxmox = source_dir / "getting-started/proxmox.mdx"
            faq = source_dir / "faq.mdx"

            def write_sources(marker):
                proxmox.write_text(f"# first-party {marker}\n", encoding="utf-8")
                faq.write_text(
                    "  <details>\n    <summary>Proxmox (existing guest, first-party script)</summary>\n"
                    f"\nfirst-party {marker}\n\n  </details>\n", encoding="utf-8")

            def stage():
                snapshot = repo / "snapshot"
                if snapshot.exists():
                    import shutil
                    shutil.rmtree(snapshot)
                docs = snapshot / "docs/carbon-panel/src/content/docs"
                (docs / "getting-started").mkdir(parents=True)
                (docs / "getting-started/proxmox.mdx").write_text(
                    "retired community helper\n" + OLD_COMMAND + "\n", encoding="utf-8")
                (docs / "faq.mdx").write_text(
                    "  <details>\n    <summary>Proxmox Helper Script</summary>\n\n"
                    + OLD_ALIAS + "\n\n  </details>\n", encoding="utf-8")
                (docs / "getting-started/unrelated.mdx").write_text(
                    "pinned content\n", encoding="utf-8")
                return docs, snapshot / "docs/carbon-panel/public"

            def commit(message):
                git(repo, "add", ".")
                git(repo, "-c", "user.name=Guide Test", "-c", "user.email=test@example.com",
                    "commit", "--quiet", "-m", message)
                return mg.head_revision(repo)

            write_sources("v1")
            rev1 = commit("guide v1")
            docs, public = stage()
            records1 = mg.apply(declaration, docs, public, "pinned", revision=rev1, root=repo)
            out1 = (docs / "getting-started/proxmox.mdx").read_text(encoding="utf-8")

            write_sources("v2")
            rev2 = commit("guide v2")
            docs, public = stage()
            records2 = mg.apply(declaration, docs, public, "pinned", revision=rev2, root=repo)
            out2 = (docs / "getting-started/proxmox.mdx").read_text(encoding="utf-8")
            unrelated = (docs / "getting-started/unrelated.mdx").read_text(encoding="utf-8")

        self.assertNotEqual(rev1, rev2)
        self.assertIn("first-party v1", out1)
        self.assertIn("first-party v2", out2)
        self.assertNotIn("first-party v1", out2)
        self.assertNotEqual(records1[0]["source_sha256"], records2[0]["source_sha256"])
        self.assertEqual(unrelated, "pinned content\n")  # never targeted by the overlay
        self.assertEqual(declaration, self.declaration)
        self.assertEqual((ROOT / "docs/maintained-guides.json").read_bytes(), manifest_before)

    def test_missing_duplicate_incompatible_and_bad_entries_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            repo = Path(temporary)
            git(repo, "init", "--quiet")
            git(repo, "config", "core.autocrlf", "false")
            for guide in self.declaration["guides"]:
                path = repo / guide["source_path"]
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("source\n", encoding="utf-8")
            git(repo, "add", ".")
            git(repo, "-c", "user.name=Guide Test", "-c", "user.email=test@example.com",
                "commit", "--quiet", "-m", "sources")
            revision = mg.head_revision(repo)

            cases = {}

            duplicate_slug = copy.deepcopy(self.declaration)
            duplicate_slug["guides"].append(copy.deepcopy(duplicate_slug["guides"][0]))
            cases["duplicate slug"] = duplicate_slug

            duplicate_path = copy.deepcopy(self.declaration)
            duplicate_path["guides"][1]["source_path"] = duplicate_path["guides"][0]["source_path"]
            cases["duplicate path"] = duplicate_path

            empty = copy.deepcopy(self.declaration)
            empty["guides"] = []
            cases["missing guides"] = empty

            traversal = copy.deepcopy(self.declaration)
            traversal["guides"][0]["source_path"] = "../outside.mdx"
            cases["outside allowlist"] = traversal

            pinned_revision = copy.deepcopy(self.declaration)
            pinned_revision["source"] = {"revision": "a" * 40}
            cases["hand-pinned revision"] = pinned_revision

            no_provenance = copy.deepcopy(self.declaration)
            no_provenance["guides"][0]["provenance"] = {}
            cases["missing provenance"] = no_provenance

            for label, candidate in cases.items():
                with self.subTest(case=label), self.assertRaises(ValueError):
                    mg.validate(candidate, repo, revision, self.pinned_pages)

            with self.assertRaises(ValueError):
                mg.validate(self.declaration, repo, "0" * 40, self.pinned_pages)
            with self.assertRaises(ValueError):
                mg.validate(self.declaration, ROOT, self.revision, {"stable": set(), "nightly": set()})
            with self.assertRaises(ValueError):
                mg._details_block("<details><summary>Other</summary>x</details>", "Missing")


if __name__ == "__main__":
    unittest.main()
