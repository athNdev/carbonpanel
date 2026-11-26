# SPDX-License-Identifier: MPL-2.0
"""Offline qualification of the docs-only maintained-guide publication lane."""
import argparse
from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import zipfile

from scripts import docs_publication_gate as gate, version_contract


class LiveStateTests(unittest.TestCase):
    STABLE = gate.STABLE_BASELINE["commit"]
    NIGHTLY = gate.NIGHTLY_BASELINE["commit"]

    def index(self, state="baseline", promoted=False, revision=None, digest=None,
              receipt="", bootstrap=True):
        revision = revision or self.NIGHTLY
        digest = digest or gate.NIGHTLY_BASELINE["digest"]
        nightly = {
            "display": "Nightly", "tag": None, "commit": revision,
            "route": "/docs/nightly/",
            "image": {"tag": "nightly", "digest": digest, "revision": revision,
                      "bootstrap": bootstrap,
                      "bootstrap_note": "Verified Docker Hub baseline only."},
            "promotion": {"state": state, "promoted": promoted, "receipt": receipt},
        }
        return {
            "schema_version": 3,
            "repository": "https://github.com/athNdev/carbonpanel",
            "versions": [],
            "promotion": {"state": state, "promoted": promoted},
            "channels": {
                "stable": {
                    "display": "Stable", "tag": "v1.0.40", "commit": self.STABLE,
                    "version": "1.0.40",
                    "route": "/docs/",
                    "image": {"tag": "latest", "stable_alias": "stable",
                              "digest": gate.STABLE_BASELINE["digest"],
                              "revision": self.STABLE},
                    "promotion": {"state": "published", "promoted": True},
                },
                "nightly": nightly,
            },
        }

    def test_built_index_shape_validates_without_a_catalog_path(self):
        ok, problems = gate.validate_channel_contract(self.index())
        self.assertTrue(ok, problems)
        document = self.index()
        del document["versions"]
        ok, problems = gate.validate_channel_contract(document)
        self.assertFalse(ok)
        self.assertTrue(any("catalog" in problem for problem in problems))

    def test_baseline_is_rendered_without_a_promotion_input(self):
        resolved, reason = gate.resolve_live_docs_state(self.index())
        self.assertEqual(reason, "")
        self.assertEqual(resolved["state"], "baseline")
        self.assertEqual(resolved["revision"], self.NIGHTLY)
        self.assertEqual(resolved["digest"], gate.NIGHTLY_BASELINE["digest"])
        self.assertEqual(resolved["receipt"], "")

    def test_active_final_is_rendered_from_the_observed_immutable_pair(self):
        revision = "f" * 40
        digest = "sha256:" + "e" * 64
        document = self.index(state="final", promoted=True,
                              revision=revision, digest=digest,
                              receipt="final-receipt", bootstrap=False)
        resolved, reason = gate.resolve_live_docs_state(document)
        self.assertEqual(reason, "")
        self.assertEqual(resolved["state"], "final")
        self.assertEqual(resolved["revision"], revision)
        self.assertEqual(resolved["digest"], digest)
        self.assertEqual(resolved["receipt"], "final-receipt")

    def test_unknown_candidate_and_mismatched_states_fail_closed(self):
        cases = [
            self.index(state="candidate"),
            self.index(state="final", promoted=False),
            self.index(state="final", promoted=True, receipt=""),
            self.index(state="baseline", bootstrap=False),
            self.index(state="baseline", revision="a" * 40),
            self.index(state="mystery"),
        ]
        for document in cases:
            resolved, reason = gate.resolve_live_docs_state(document)
            self.assertIsNone(resolved, reason)
            self.assertTrue(reason)

    def test_receipt_guard_preserves_both_channels(self):
        live = self.index()
        built = json.loads(json.dumps(live))
        ok, problems = gate.verify_docs_receipt(built, live)
        self.assertTrue(ok, problems)
        built["channels"]["stable"]["commit"] = "b" * 40
        ok, _ = gate.verify_docs_receipt(built, live)
        self.assertFalse(ok)
        built = json.loads(json.dumps(live))
        built["promotion"] = {"state": "final", "promoted": True}
        ok, _ = gate.verify_docs_receipt(built, live)
        self.assertFalse(ok)


class PublishedStableTests(unittest.TestCase):
    def setUp(self):
        self.index = LiveStateTests().index()
        entry = self.index["channels"]["stable"]
        entry.update(tag="carbonpanel-v2.0.1", version="2.0.1", commit="a" * 40)
        entry["image"].update(revision=entry["commit"], digest="sha256:" + "b" * 64)
        self.stable = gate.stable_identity(self.index)

    def qualify(self, *, alias_override=None, latest_override=None, peeled_revision=None,
                manifest_verified=True):
        stable = gate.stable_identity(self.index)
        latest = {"tag_name": stable["tag"], "draft": False, "prerelease": False,
                  "published_at": "2026-11-20T00:00:00Z", **(latest_override or {})}
        present = {name: ("present", {"digest": stable["digest"], "revision": stable["commit"],
                                      "platforms": gate.release.REQUIRED_PLATFORMS})
                   for name in gate.release.CHANNEL_REGISTRIES}
        observations = {name: present for name in ("latest", "stable", stable["version"])}
        observations.update(alias_override or {})
        with patch.object(gate, "github_api", return_value=(
            lambda url: json.dumps(latest if url.endswith("/releases/latest")
                                  else {"sha": peeled_revision or stable["commit"]}), "https://api.example"
        )), patch.object(gate.release, "release_provenance",
                        return_value=(manifest_verified, "release digest mismatch", None)) as provenance, \
                patch.object(gate.release, "channel_observe",
                             side_effect=observations.__getitem__) as observe:
            result = gate.qualify_public_stable(self.index, "athNdev/carbonpanel")
            provenance.assert_called_once_with(f"v{stable['version']}", stable["commit"],
                                               stable["digest"], [[latest]], git_tag=stable["tag"])
            self.assertEqual([call.args[0] for call in observe.call_args_list], list(observations))
            return result

    def test_newer_release_renders_and_checks_against_the_observed_identity(self):
        stable = self.qualify()
        inputs = gate.stable_render_inputs(stable)
        source = gate.load_json(gate.ROOT / "docs/website-source.json")
        built = version_contract.release_build_contract(
            source, {f"CARBONPANEL_{key.upper()}": value for key, value in inputs.items()})
        self.assertEqual(gate.stable_identity(built), self.stable)
        self.assertEqual(gate.validate_channel_contract(built, self.index), (True, []))
        self.assertFalse(gate.validate_channel_contract(source, self.index)[0])
        self.assertIsNotNone(gate.resolve_live_docs_state(self.index, self.index)[0])

    def test_legacy_absent_stable_aliases_allow_only_the_exact_historical_renderer(self):
        self.index = LiveStateTests().index()
        missing = {name: ("absent", {}) for name in gate.release.CHANNEL_REGISTRIES}
        stable = self.qualify(alias_override={"stable": missing})
        self.assertEqual(set(gate.stable_render_inputs(stable).values()), {""})
        with self.assertRaisesRegex(ValueError, "legacy Stable identity differs"):
            gate.stable_render_inputs({**stable, "digest": "sha256:" + "f" * 64})

    def test_conflicting_or_unknown_live_evidence_refuses(self):
        partial = {"docker_hub": ("absent", {}), "ghcr": ("unknown", {})}
        cases = (
            ({"latest_override": {"tag_name": "v1.0.40"}}, "GitHub Latest"),
            ({"latest_override": {"prerelease": True}}, "GitHub Latest"),
            ({"peeled_revision": "d" * 40}, "peel"),
            ({"manifest_verified": False}, "release provenance"),
            ({"alias_override": {"stable": partial}}, "registry evidence"),
            ({"alias_override": {"2.0.1": partial}}, "registry evidence"),
        )
        for kwargs, reason in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(ValueError, reason):
                self.qualify(**kwargs)


class RenderedContentTests(unittest.TestCase):
    REVISION = "f93c3a9a5b1ee900bb55bbb1c59307b93cced431"

    def test_channel_content_and_stable_identity(self):
        live = LiveStateTests().index()
        index = json.loads(json.dumps(live))
        ok, problems = gate.verify_channel_docs(
            index, "stable", gate.STABLE_BASELINE["commit"],
            gate.STABLE_BASELINE["digest"], ["<h1>Proxmox</h1>"],
            ("Proxmox",), (), live)
        self.assertTrue(ok, problems)
        ok, problems = gate.verify_channel_docs(
            index, "nightly", gate.NIGHTLY_BASELINE["commit"],
            gate.NIGHTLY_BASELINE["digest"], ["<p>old helper script</p>"],
            ("raw.githubusercontent.com/athNdev/carbonpanel/main/scripts/proxmox/install-carbon-panel.sh",),
            (), live)
        self.assertFalse(ok)
        self.assertTrue(any("missing required content" in problem for problem in problems))

    def test_maintained_guide_provenance_guard(self):
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            index = LiveStateTests().index()
            (site / "version-index.json").write_text(json.dumps(index), encoding="utf-8")
            for name, entry in index["channels"].items():
                manifest = site / entry["route"].strip("/") / "maintained-guides.json"
                manifest.parent.mkdir(parents=True, exist_ok=True)
                manifest.write_text(json.dumps({
                    "schema_version": 1,
                    "source": {"revision": self.REVISION, "origin": "git HEAD"},
                    "channels": [{"slug": "getting-started/proxmox",
                                  "source_sha256": "a" * 64}],
                }), encoding="utf-8")
            ok, problems = gate.verify_maintained_guides(
                site, index["channels"], self.REVISION)
            self.assertTrue(ok, problems)
            ok, _ = gate.verify_maintained_guides(site, index["channels"], "b" * 40)
            self.assertFalse(ok)

    def test_cli_verifies_a_real_rendered_site_offline(self):
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            index = LiveStateTests().index()
            (site / "version-index.json").write_text(json.dumps(index), encoding="utf-8")
            for name, entry in index["channels"].items():
                route = site / entry["route"].strip("/")
                route.mkdir(parents=True, exist_ok=True)
                (route / "source.json").write_text(
                    json.dumps({"revision": entry["commit"]}), encoding="utf-8")
                page = route / "getting-started/proxmox/index.html"
                page.parent.mkdir(parents=True, exist_ok=True)
                page.write_text(
                    "<html>raw.githubusercontent.com/athNdev/carbonpanel/main/"
                    "scripts/proxmox/install-carbon-panel.sh</html>", encoding="utf-8")
                (route / "maintained-guides.json").write_text(json.dumps({
                    "schema_version": 1,
                    "source": {"revision": self.REVISION, "origin": "git HEAD"},
                    "channels": [{"slug": "getting-started/proxmox",
                                  "source_sha256": "a" * 64}],
                }), encoding="utf-8")
            out = io.StringIO()
            with patch("sys.argv", ["docs_publication_gate.py", "verify-channel-docs",
                                    "--site", str(site), "--channel", "nightly",
                                    "--commit", gate.NIGHTLY_BASELINE["commit"],
                                    "--digest", gate.NIGHTLY_BASELINE["digest"],
                                    "--page", "getting-started/proxmox",
                                    "--contains", "raw.githubusercontent.com/athNdev/carbonpanel/"
                                                  "main/scripts/proxmox/install-carbon-panel.sh"]), \
                 redirect_stdout(out):
                gate.main()
            self.assertEqual(json.loads(out.getvalue())["verified"], "true")


class GuideLivenessTests(unittest.TestCase):
    REVISION = "f93c3a9a5b1ee900bb55bbb1c59307b93cced431"
    PATH = "docs/carbon-panel/src/content/docs/getting-started/proxmox.mdx"
    DECLARATION = {
        "schema_version": 1,
        "repository": "https://github.com/athNdev/carbonpanel",
        "compatibility": {"require_slug_in_every_pinned_channel": True,
                          "preserve_product_pin_and_schema": True},
        "guides": [{"slug": "getting-started/proxmox", "mode": "page",
                    "source_path": PATH,
                    "provenance": {"title": "Proxmox (existing guest)",
                                   "scope": "The whole page: first-party controller install.",
                                   "reason": "Operational guidance is maintained on the docs source."}}],
    }
    SCOPE = "The whole page: first-party controller install."

    def candidate(self, content=b"# first-party guide"):
        return gate.guide_fingerprint(self.DECLARATION, self.REVISION,
                                      {self.PATH: content})

    def evidence(self, revision=None, sha256=None, source_path=None,
                 mode=None, scope=None, declaration_hash=None):
        if declaration_hash is None:
            declaration_hash = gate.guide_fingerprint(
                self.DECLARATION, self.REVISION,
                {self.PATH: b"# first-party guide"})["declaration_sha256"]
        record = {
            "slug": "getting-started/proxmox",
            "source_path": source_path or self.PATH,
            "mode": mode or "page",
            "provenance": {"scope": self.SCOPE if scope is None else scope},
            "source_sha256": sha256 or "a" * 64,
        }
        manifest = {
            "schema_version": 1,
            "declaration_sha256": declaration_hash,
            "source": {"revision": revision or self.REVISION, "origin": "git HEAD"},
            "channels": [record],
        }
        return {"stable": json.loads(json.dumps(manifest)),
                "nightly": json.loads(json.dumps(manifest))}

    def test_fingerprint_tracks_declaration_and_exact_blobs(self):
        first = self.candidate()
        self.assertTrue(gate.valid_commit(first["revision"]))
        changed = self.candidate(b"# updated first-party guide")
        self.assertNotEqual(first["fingerprint"], changed["fingerprint"])
        declaration = json.loads(json.dumps(self.DECLARATION))
        declaration["compatibility"]["preserve_product_pin_and_schema"] = False
        altered = gate.guide_fingerprint(declaration, self.REVISION,
                                         {self.PATH: b"# first-party guide"})
        self.assertNotEqual(first["fingerprint"], altered["fingerprint"])

    def test_stale_missing_and_current_live_receipts(self):
        candidate = self.candidate()
        digest = candidate["guides"][0]["source_sha256"]
        current = self.evidence(sha256=digest)
        self.assertFalse(gate.guide_change_decision(candidate, current)["publish"])
        self.assertTrue(gate.guide_change_decision(candidate, {})["publish"])
        self.assertTrue(gate.guide_change_decision(
            candidate, self.evidence(sha256="c" * 64))["publish"])
        self.assertTrue(gate.guide_change_decision(
            candidate, self.evidence(sha256=digest, scope="A different scope"))["publish"])
        self.assertTrue(gate.guide_change_decision(
            candidate, self.evidence(sha256=digest, source_path="docs/other.mdx"))["publish"])
        self.assertTrue(gate.guide_change_decision(
            candidate, self.evidence(sha256=digest, mode="fragment"))["publish"])
        partial = self.evidence(sha256=digest)
        partial["nightly"] = None
        self.assertTrue(gate.guide_change_decision(candidate, partial)["publish"])

    def test_selector_only_declaration_change_publishes(self):
        candidate = self.candidate()
        digest = candidate["guides"][0]["source_sha256"]
        stale = self.evidence(sha256=digest, declaration_hash="f" * 64)
        decision = gate.guide_change_decision(candidate, stale)
        self.assertTrue(decision["publish"])

    def test_noop_across_unrelated_commits_with_identical_inputs(self):
        candidate = self.candidate()
        digest = candidate["guides"][0]["source_sha256"]
        other_commit = self.evidence(revision="b" * 40, sha256=digest)
        decision = gate.guide_change_decision(candidate, other_commit)
        self.assertFalse(decision["publish"])
        self.assertEqual(decision["reason"], "live maintained guide receipt is current")
        # The source revision is still a deployment qualification, not a
        # liveness trigger; the built site must stamp the tested commit.
        ok, problems = gate.verify_public_maintained_guides(other_commit, self.REVISION)
        self.assertFalse(ok)
        self.assertTrue(problems)

    def test_live_evidence_reader_handles_a_missing_route(self):
        def get_bytes(path):
            if "nightly" in path:
                raise FileNotFoundError(path)
            return json.dumps(self.evidence()["stable"]).encode()
        evidence = gate.fetch_live_guide_evidence(get_bytes)
        self.assertIsInstance(evidence["stable"], dict)
        self.assertIsNone(evidence["nightly"])

    def test_public_guide_guard_requires_tested_revision(self):
        ok, problems = gate.verify_public_maintained_guides(
            self.evidence(), self.REVISION)
        self.assertTrue(ok, problems)
        ok, _ = gate.verify_public_maintained_guides(
            self.evidence(), "d" * 40)
        self.assertFalse(ok)

    def test_recheck_main_tip_uses_two_reads(self):
        calls = []

        def fake_api(_repository):
            def read(url):
                calls.append(url)
                return json.dumps({"sha": self.REVISION}).encode()
            return read, "https://api.github.com/repos/x"

        with patch.object(gate, "github_api", side_effect=fake_api):
            result = gate.recheck_main_tip("athNdev/carbonpanel", self.REVISION)
        self.assertTrue(result["verified"])
        self.assertEqual(len(calls), 2)

        def moved_api(_repository):
            def read(url):
                calls.append(url)
                return json.dumps({"sha": "e" * 40}).encode()
            return read, "https://api.github.com/repos/x"

        with patch.object(gate, "github_api", side_effect=moved_api):
            result = gate.recheck_main_tip("athNdev/carbonpanel", self.REVISION)
        self.assertFalse(result["verified"])


class PagesBaselineTests(unittest.TestCase):
    DEPLOYMENT = "7948601117"
    RUN = "44786485570"
    JOB = "136463335046"
    ARTIFACT = "14654581812"
    SHA = "2245f1669381afaff49381cb3f3ac153786c8085"

    def fixture(self):
        deployments = [
            {"id": self.DEPLOYMENT, "environment": "github-pages", "sha": self.SHA,
             "created_at": "2026-11-20T17:47:49Z"},
            {"id": "7948700163", "environment": "github-pages", "sha": self.SHA,
             "created_at": "2026-11-20T17:52:45Z"},
            {"id": "7951625248", "environment": "github-pages", "sha": "86f80535011072c45f2cf951cb4078d675398f08",
             "created_at": "2026-11-20T20:19:30Z"},
        ]
        statuses = {
            self.DEPLOYMENT: [{"state": "success", "target_url":
                               "https://github.com/athNdev/carbonpanel/actions/runs/"
                               + self.RUN + "/job/" + self.JOB}],
            "7948700163": [{"state": "success", "target_url":
                            "https://github.com/athNdev/carbonpanel/actions/runs/"
                            + self.RUN + "/job/136466009627"}],
            "7951625248": [{"state": "success", "target_url":
                            "https://github.com/athNdev/carbonpanel/actions/runs/"
                            "44809544673/job/136544870678"}],
        }
        jobs = {
            self.JOB: {"id": int(self.JOB), "run_id": int(self.RUN), "head_sha": self.SHA, "steps": [
                {"number": 1, "name": "Deploy the off-path Stable candidate probe", "conclusion": "success"},
                {"number": 2, "name": "Deploy the synchronized stable docs marker once", "conclusion": "success"},
            ]},
            "136466009627": {"id": 136466009627, "run_id": int(self.RUN), "head_sha": self.SHA, "steps": [
                {"number": 1, "name": "Deploy the exact prior stable docs bytes", "conclusion": "skipped"},
            ]},
            "136544870678": {"id": 136544870678, "run_id": 44809544673,
                             "head_sha": deployments[-1]["sha"], "steps": [
                {"number": 1, "name": "Deploy the final canonical artifact", "conclusion": "skipped"},
                {"number": 2, "name": "Deploy the exact prior docs bytes", "conclusion": "skipped"},
            ]},
        }
        artifacts = [
            {"id": self.ARTIFACT, "name": "github-pages", "expired": False},
            {"id": "14653469517", "name": "github-pages-prior-stable", "expired": False},
            {"id": "14654218656", "name": "github-pages-stable-probe", "expired": False},
        ]
        return deployments, statuses, jobs, artifacts

    def test_observed_latest_success_resolves_to_exact_artifact(self):
        deployments, statuses, jobs, artifacts = self.fixture()
        identity, reason = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        self.assertEqual(reason, "")
        baseline, reason = gate.pages_baseline_artifact(identity, artifacts)
        self.assertEqual(reason, "")
        self.assertEqual((baseline["deployment_id"], baseline["run_id"],
                          baseline["artifact_id"], baseline["sha"]),
                         (self.DEPLOYMENT, self.RUN, self.ARTIFACT, self.SHA))

    def test_unknown_state_fails_closed(self):
        deployments, statuses, jobs, _ = self.fixture()
        statuses["7951625248"][0]["state"] = "in_progress"
        baseline, _ = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        self.assertEqual(baseline["run_id"], self.RUN)
        statuses["7951625248"][0]["state"] = "unrecognised"
        baseline, reason = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        self.assertIsNone(baseline)
        self.assertIn("unknown state", reason)

    def test_rollback_selects_its_own_artifact_and_expiry_keeps_identity(self):
        deployments, statuses, jobs, artifacts = self.fixture()
        jobs["136466009627"]["steps"][0]["conclusion"] = "success"
        statuses["7948700163"][0]["state"] = "failure"
        identity, _ = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        baseline, reason = gate.pages_baseline_artifact(identity, artifacts)
        self.assertEqual(reason, "")
        self.assertEqual(baseline["sha"], self.SHA)
        self.assertEqual(baseline["run_id"], self.RUN)
        self.assertEqual(baseline["deployment_id"], "7948700163")
        self.assertEqual(baseline["artifact_id"], "14653469517")
        artifacts[1]["expired"] = True
        baseline, reason = gate.pages_baseline_artifact(identity, artifacts)
        self.assertIsNotNone(baseline)
        self.assertEqual(baseline["sha"], self.SHA)
        self.assertEqual(baseline["artifact_id"], "")
        self.assertFalse(baseline["artifact_verified"])
        self.assertIn("unavailable or expired", reason)

    def test_unknown_jobs_and_unsuccessful_deploy_outcomes_fail_closed(self):
        deployments, statuses, jobs, _ = self.fixture()
        latest = jobs["136544870678"]
        latest["steps"][-1]["conclusion"] = "failure"
        baseline, reason = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        self.assertIsNone(baseline)
        self.assertIn("unverified final Pages step", reason)
        latest["steps"] = []
        baseline, reason = gate.pages_deployment_identity(deployments, statuses, jobs.get)
        self.assertIsNone(baseline)
        self.assertIn("no recognized Pages deploy steps", reason)

    def test_exact_artifact_must_byte_match_live(self):
        live = b'{"schema_version":3}'
        artifacts = [
            {"id": self.ARTIFACT, "name": "github-pages", "expired": False,
             "workflow_run": {"id": int(self.RUN), "head_sha": self.SHA}},
            {"id": "999", "name": "github-pages", "expired": False},
        ]

        def bundle(content):
            buffer = io.BytesIO()
            with zipfile.ZipFile(buffer, "w") as archive:
                archive.writestr("version-index.json", content)
            return buffer.getvalue()

        def download(artifact):
            return bundle(live if artifact["id"] == self.ARTIFACT else b"stale")

        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "prior"
            with patch.object(gate, "verify_prior_bytes", return_value=(True, [])):
                result = gate.fetch_prior_pages(
                    destination, artifacts, download, live,
                    artifact_id=self.ARTIFACT, run_id=self.RUN)
            self.assertEqual(result["artifact_id"], self.ARTIFACT)
            self.assertEqual(result["live_hash"], "sha256:" + gate.sha256_bytes(live))
            self.assertEqual((destination / "version-index.json").read_bytes(), live)
            with self.assertRaises(ValueError):
                gate.fetch_prior_pages(
                    destination, artifacts, download, live, artifact_id="missing")


class WorkflowWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = (gate.ROOT / ".github/workflows/docs-only-publish.yml").read_text(
            encoding="utf-8")

    def test_shared_pages_lock_and_ci_driven_trigger(self):
        self.assertIn("workflow_run:", self.text)
        self.assertIn('workflows: ["Continuous Integration"]', self.text)
        self.assertIn("branches:\n      - main", self.text)
        self.assertIn("cancel-in-progress: false", self.text)
        self.assertNotIn("workflow_dispatch:", self.text)

    def test_only_eligible_ci_uses_the_shared_lock(self):
        group = [line for line in self.text.splitlines() if line.strip().startswith("group:")][0]
        for token in ("github.event.workflow_run.event == 'push'",
                      "github.event.workflow_run.head_branch == 'main'",
                      "github.event.workflow_run.conclusion == 'success'",
                      "'pages-deploy'",
                      "format('docs-only-verify-{0}', github.event.workflow_run.id)"):
            self.assertIn(token, group)
        self.assertNotIn("group: pages-deploy\n", self.text)

    def test_live_state_is_resolved_before_the_build(self):
        self.assertLess(self.text.index("resolve-live-docs-state"),
                        self.text.index("python3 scripts/build_website.py"))
        self.assertIn("--nightly-revision \"$LIVE_REVISION\"", self.text)
        self.assertIn("--promotion-state final", self.text)
        self.assertIn("python3 scripts/build_website.py\n            python3 scripts/check_website.py",
                      self.text)

    def test_both_publishers_render_and_validate_the_qualified_stable_pin(self):
        landing = (gate.ROOT / ".github/workflows/landing.yml").read_text(encoding="utf-8")
        for workflow in (self.text, landing):
            self.assertIn("--verify-stable", workflow)
            self.assertLess(workflow.index("--verify-stable"), workflow.index("scripts/build_website.py\n"))
            for field in ("TAG", "REVISION", "DIGEST"):
                self.assertIn("CARBONPANEL_STABLE_" + field, workflow)
            self.assertIn("--stable-source", workflow)
            self.assertIn("vars.NIGHTLY_RECOVERY_ACTIVE != 'true'", workflow)
        self.assertLess(self.text.index("guide-evidence"), self.text.index('if [ "$RECOVERY_ACTIVE" = true ]'))
        self.assertNotIn("site-only-$ACTIVE_COMMIT", landing)

    def test_no_channel_writes_and_exact_prior_baseline(self):
        for forbidden in ("channel-promote", "channel-transaction", "--apply",
                          "workflow_dispatch", "release.py publish"):
            self.assertNotIn(forbidden, self.text)
        self.assertIn("pages-baseline", self.text)
        self.assertIn("fetch-prior-pages", self.text)
        self.assertIn("--repository \"$GITHUB_REPOSITORY\" --run-id \"$RUN_ID\"", self.text)
        self.assertIn("--run-id \"$RUN_ID\" --artifact-id \"$ARTIFACT_ID\"", self.text)
        self.assertIn("steps.baseline.outputs.artifact_id == ''", self.text)
        self.assertIn("test \"sha256:$LIVE_HASH\" = \"$PRIOR_HASH\"", self.text)
        self.assertIn("artifact_name: github-pages-prior", self.text)

    def test_both_routes_and_real_source_metadata_are_checked(self):
        for needle in ("--channel stable", "--channel nightly",
                       "verify-maintained-guides", "verify-docs-receipt",
                       "--base-url https://carbonpanel.dev"):
            self.assertIn(needle, self.text)
        self.assertNotIn("raw.githubusercontent.com", self.text)
        self.assertNotIn("grep -q", self.text)
        self.assertIn("verify-rendered-guides", self.text)
        self.assertIn("verify-publication-receipt", self.text)
        self.assertTrue(hasattr(gate, "verify_prior_bytes"))
        self.assertIn("python3 scripts/release.py verify-main-ci --commit", self.text)
        self.assertFalse(hasattr(gate, "verify_main_ci"))

    def test_least_privilege_includes_deployment_status_reads(self):
        self.assertIn("permissions:\n  contents: read\n  actions: read\n  deployments: read",
                      self.text)

    def test_liveness_compares_guide_bytes_not_a_single_diff(self):
        self.assertNotIn('git diff --name-only "${CHANGE_SHA}^1"', self.text)
        self.assertIn("guide-fingerprint", self.text)
        self.assertIn("guide-evidence", self.text)
        self.assertIn("guide-change-decision", self.text)
        self.assertLess(self.text.index("guide-change-decision"),
                        self.text.index("python3 scripts/build_website.py"))

    def test_recheck_and_rollback_flow(self):
        self.assertIn("recheck-main-tip", self.text)
        self.assertIn("--require-publish", self.text)
        self.assertLess(self.text.index("recheck-main-tip"),
                        self.text.index("Upload the full preserved site"))
        self.assertIn("needs.deploy.result == 'failure'", self.text)
        self.assertIn("needs.deploy.result != 'skipped'", self.text)
        self.assertNotIn("needs.deploy.outcome == 'failure'", self.text)
        self.assertGreaterEqual(self.text.count("recheck-main-tip"), 2)
        first_recheck = self.text.index("recheck-main-tip")
        second_recheck = self.text.index("recheck-main-tip", first_recheck + 1)
        self.assertLess(second_recheck,
                        self.text.index("Deploy the full preserved site once"))
        self.assertIn("Verify restored live bytes match the resolved baseline", self.text)

    def test_baseline_gating_cannot_restore_on_pre_write_failure(self):
        self.assertIn("continue-on-error: true", self.text)
        self.assertIn("Assert a verified rollback baseline is available", self.text)
        self.assertIn("needs.deploy.outputs.deploy_started == 'true'", self.text)
        self.assertIn("Mark the Pages write as attempted", self.text)
        prior = self.text.index("Fetch and byte-verify the exact prior deployed docs")
        assertion = self.text.index("Assert a verified rollback baseline is available")
        self.assertLess(prior, assertion)
        self.assertIn("no exact or reconstructed prior baseline was fully verified", self.text)
        self.assertIn("if: always() && steps.decision_record.outputs.publish == 'true'", self.text)
        self.assertNotIn("steps.prior.outcome == 'success' || steps.recover.outcome == 'success'",
                         self.text)
        self.assertIn("needs.publish.outputs.rollback_artifact == 'success'", self.text)

    def test_recovery_is_sha_anchored_and_rebuilds_receipt(self):
        self.assertIn("--deployment-sha \"$DEPLOY_SHA\"", self.text)
        self.assertIn("--trusted-declaration \"$GITHUB_WORKSPACE/docs/maintained-guides.json\"",
                      self.text)
        self.assertIn("git fetch origin main --no-tags", self.text)
        self.assertIn("git worktree add --detach .recover \"$DEPLOY_SHA\"", self.text)
        src = Path(__file__).with_name("docs_publication_gate.py").read_text(encoding="utf-8")
        self.assertIn("legacy-reconstructed", src)
        self.assertIn("stamp_publication_receipt(site, candidate, live, deployment_sha)", src)
        self.assertIn("verify_rendered_guide_links(_local_get(site), live, root)", src)

    def test_live_read_creates_its_parent_directory(self):
        self.assertIn("mkdir -p .website-build", self.text)
        self.assertLess(self.text.index("mkdir -p .website-build"),
                        self.text.index("-o .website-build/live-version-index.json"))


class LiveByteVerificationTests(unittest.TestCase):
    """Pre-receipt 404s are facts; network failures and byte drift are not."""

    def test_live_bytes_distinguishes_404_from_network_failure(self):
        def get_bytes(path):
            if path == "/missing":
                raise urllib.error.HTTPError(path, 404, "not found", {}, None)
            if path == "/broken":
                raise urllib.error.URLError("network down")
            return b"ok"
        observed = gate.live_bytes(get_bytes, ["/ok", "/missing"])
        self.assertEqual(observed["/ok"], b"ok")
        self.assertIsNone(observed["/missing"])
        with self.assertRaises(urllib.error.URLError):
            gate.live_bytes(get_bytes, ["/broken"])

    def test_prior_bytes_accept_pre_receipt_404_and_reject_drift(self):
        index_bytes = json.dumps(LiveStateTests().index()).encode()
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            (site / "version-index.json").write_bytes(index_bytes)
            def get_bytes(path):
                if path == "/version-index.json":
                    return index_bytes
                raise urllib.error.HTTPError(path, 404, "not found", {}, None)
            with patch.object(gate, "_http_get", return_value=get_bytes):
                ok, problems = gate.verify_prior_bytes(site, "https://carbonpanel.dev")
            self.assertTrue(ok, problems)

            def broken(path):
                raise urllib.error.URLError("network down")
            with patch.object(gate, "_http_get", return_value=broken):
                with self.assertRaises(urllib.error.URLError):
                    gate.verify_prior_bytes(site, "https://carbonpanel.dev")

    def test_page_hashes_are_keyed_by_real_url_paths_and_verified(self):
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            index = LiveStateTests().index()
            (site / "version-index.json").write_text(json.dumps(index), encoding="utf-8")
            for name, entry in index["channels"].items():
                route = site / entry["route"].strip("/")
                manifest = {"schema_version": 1,
                            "source": {"revision": "a" * 40},
                            "channels": [{"slug": "getting-started/proxmox",
                                          "mode": "page",
                                          "source_path": "docs/x.mdx",
                                          "source_sha256": "b" * 64,
                                          "target_file": "getting-started/proxmox.mdx",
                                          "provenance": {"scope": "scope"}}]}
                manifest_dir = route
                manifest_dir.mkdir(parents=True, exist_ok=True)
                (manifest_dir / "maintained-guides.json").write_text(
                    json.dumps(manifest), encoding="utf-8")
                page = manifest_dir / "getting-started/proxmox/index.html"
                page.parent.mkdir(parents=True, exist_ok=True)
                page.write_text("guide", encoding="utf-8")
            candidate = GuideLivenessTests().candidate()
            receipt = gate.stamp_publication_receipt(
                site, candidate, index, GuideLivenessTests.REVISION)
            self.assertIn("/version-index.json", receipt["page_hashes"])
            self.assertIn("/docs/getting-started/proxmox/index.html",
                          receipt["page_hashes"])
            self.assertIn("/docs/nightly/getting-started/proxmox/index.html",
                          receipt["page_hashes"])
            local = gate._local_get(site)
            ok, problems = gate.verify_publication_receipt(
                receipt, candidate, index, GuideLivenessTests.REVISION, local)
            self.assertTrue(ok, problems)

    def test_rendered_guide_link_is_generic_and_exact(self):
        index = LiveStateTests().index()
        revision = "a" * 40
        link = ("https://github.com/athNdev/carbonpanel/blob/" + revision
                + "/docs/carbon-panel/src/content/docs/getting-started/proxmox.mdx")
        manifest = {"source": {"revision": revision},
                    "channels": [{"slug": "getting-started/proxmox",
                                  "source_path": "docs/carbon-panel/src/content/docs/"
                                                 "getting-started/proxmox.mdx",
                                  "target_file": "getting-started/proxmox.mdx"}]}

        def get_bytes(path):
            if path.endswith("maintained-guides.json"):
                return json.dumps(manifest).encode()
            return ("<html>" + link + "</html>").encode()
        ok, problems = gate.verify_rendered_guide_links(get_bytes, index)
        self.assertTrue(ok, problems)

        def stale(path):
            if path.endswith("maintained-guides.json"):
                return json.dumps(manifest).encode()
            return b"<html>old helper script</html>"
        ok, problems = gate.verify_rendered_guide_links(stale, index)
        self.assertFalse(ok)

    def test_fragment_uses_declared_anchor_and_source_content(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs/maintained-guides.json").write_text(json.dumps({
                "guides": [{"slug": "faq",
                            "source_summary": "Proxmox (existing guest, first-party script)",
                            "target_summary": "Proxmox Helper Script"}]}), encoding="utf-8")
            index = LiveStateTests().index()
            manifest = {"source": {"revision": "a" * 40}, "channels": [
                {"slug": "faq", "mode": "fragment",
                 "source_path": "docs/carbon-panel/src/content/docs/faq.mdx",
                 "target_file": "faq.mdx", "source_sha256": "b" * 64}]}

            def get_bytes(path):
                if path.endswith("maintained-guides.json"):
                    return json.dumps(manifest).encode()
                return (b"<html><details><summary>Proxmox (existing guest, "
                        b"first-party script)</summary>"
                        b"<p>This is the exact first-party installation method for "
                        b"existing guests on Proxmox.</p></details></html>")
            source = ("<details>\n<summary>Proxmox (existing guest, first-party script)</summary>\n"
                      "\nThis is the exact first-party installation method for existing "
                      "guests on Proxmox.\n</details>")
            with patch.object(gate.subprocess, "check_output", return_value=source.encode()):
                ok, problems = gate.verify_rendered_guide_links(get_bytes, index, str(root))
            self.assertTrue(ok, problems)

            def stale(path):
                if path.endswith("maintained-guides.json"):
                    return json.dumps(manifest).encode()
                return (b"<html><details><summary>Proxmox Helper Script</summary>"
                        b"<p>old third-party helper script</p></details></html>")
            with patch.object(gate.subprocess, "check_output", return_value=source.encode()):
                ok, problems = gate.verify_rendered_guide_links(stale, index, str(root))
            self.assertFalse(ok)

    def test_legacy_recovery_without_declaration_or_receipt(self):
        sha = "f93c3a9a5b1ee900bb55bbb1c59307b93cced431"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            site = root / ".website-build/site"
            pages = ["docs/getting-started/proxmox/index.html", "docs/faq/index.html",
                     "docs/nightly/getting-started/proxmox/index.html",
                     "docs/nightly/faq/index.html"]
            index_bytes = json.dumps(LiveStateTests().index()).encode()
            (site / "version-index.json").parent.mkdir(parents=True, exist_ok=True)
            (site / "version-index.json").write_bytes(index_bytes)
            for page in pages:
                target = site / page
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(("bytes for " + page).encode("utf-8"))
            declaration = root / "trusted-maintained-guides.json"
            declaration.write_text(json.dumps({"guides": [
                {"slug": "getting-started/proxmox"}, {"slug": "faq"}]}), encoding="utf-8")
            live_files = {"/version-index.json": index_bytes}
            for page in pages:
                live_files["/" + page] = ("bytes for " + page).encode("utf-8")

            def get_bytes(path):
                if path == "/publication-receipt.json":
                    raise urllib.error.HTTPError(path, 404, "not found", {}, None)
                return live_files[path]

            def fake_run(args, **kwargs):
                if args[:2] == ["git", "rev-parse"]:
                    return type("R", (), {"stdout": sha})()
                return type("R", (), {"returncode": 0})()

            with patch.object(gate, "_http_get", return_value=get_bytes), \
                 patch.object(gate.subprocess, "run", side_effect=fake_run), \
                 patch.object(gate.subprocess, "check_call",
                              wraps=gate.subprocess.check_call) as build:
                script = root / "scripts" / "build_website.py"
                script.parent.mkdir()
                script.write_text("from pathlib import Path\n"
                                  "assert Path(__file__).parent.parent == Path.cwd()\n",
                                  encoding="utf-8")
                result = gate.recover_prior_pages(
                    root / "prior", "https://carbonpanel.dev", sha, os.path.relpath(root),
                    str(declaration))
                build.assert_called_once()
                self.assertEqual(result["mode"], "legacy-reconstructed")
                self.assertFalse((root / "prior" / "publication-receipt.json").exists())

                live_files["/docs/faq/index.html"] = b"different bytes"
                with self.assertRaises(ValueError):
                    gate.recover_prior_pages(
                        root / "prior", "https://carbonpanel.dev", sha, str(root),
                        str(declaration))

                def broken(path):
                    if path == "/publication-receipt.json":
                        raise urllib.error.URLError("network down")
                    return get_bytes(path)
                with patch.object(gate, "_http_get", return_value=broken):
                    with self.assertRaises(urllib.error.URLError):
                        gate.recover_prior_pages(
                            root / "prior", "https://carbonpanel.dev", sha, str(root),
                            str(declaration))

    def test_recovery_rejects_a_sha_not_on_origin_main(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(gate.subprocess, "run") as runner:
                runner.return_value = type("R", (), {"returncode": 1, "stdout": "a" * 40})()
                with self.assertRaises(ValueError):
                    gate.recover_prior_pages(
                        Path(directory) / "prior", "https://carbonpanel.dev",
                        "a" * 40, str(Path(directory)))


class PublishedRouteLivenessTests(unittest.TestCase):
    """The documented /docs/ index mirror is part of publication liveness."""

    def candidate_and_evidence(self):
        candidate = GuideLivenessTests().candidate()
        digest = candidate["guides"][0]["source_sha256"]
        return candidate, GuideLivenessTests().evidence(sha256=digest)

    def test_missing_docs_mirror_triggers_publication(self):
        candidate, evidence = self.candidate_and_evidence()
        evidence["published_route_problems"] = [
            "published route /docs/version-index.json is missing"]
        decision = gate.guide_change_decision(candidate, evidence)
        self.assertTrue(decision["publish"])
        self.assertIn("/docs/version-index.json", " ".join(decision["problems"]))

    def test_drifted_docs_mirror_triggers_publication(self):
        candidate, evidence = self.candidate_and_evidence()
        evidence["published_route_problems"] = [
            "docs version index disagrees with the site root copy"]
        self.assertTrue(gate.guide_change_decision(candidate, evidence)["publish"])

    def test_matching_routes_keep_the_noop(self):
        candidate, evidence = self.candidate_and_evidence()
        evidence["published_route_problems"] = []
        evidence["published_routes"] = {"/version-index.json": "a" * 64,
                                        "/docs/version-index.json": "a" * 64}
        self.assertFalse(gate.guide_change_decision(candidate, evidence)["publish"])

    def test_missing_and_drifted_routes_are_classified(self):
        index_bytes = b'{"schema_version":3}'
        def get_bytes(path):
            if path == "/docs/version-index.json":
                raise urllib.error.HTTPError(path, 404, "not found", {}, None)
            return index_bytes
        state = gate.published_route_liveness(get_bytes)
        self.assertEqual(state["problems"],
                         ["published route /docs/version-index.json is missing"])

        def drifted(path):
            return index_bytes if path == "/version-index.json" else b"{}"
        state = gate.published_route_liveness(drifted)
        self.assertEqual(state["problems"],
                         ["docs version index disagrees with the site root copy"])

        def broken(_path):
            raise urllib.error.URLError("network down")
        with self.assertRaises(urllib.error.URLError):
            gate.published_route_liveness(broken)


class ChannelPresentationReceiptTests(unittest.TestCase):
    """Presentation is a separate channel input, with legacy compatibility."""

    HASH = "a" * 64

    def base_candidate(self, presentation="", **overrides):
        value = {"fingerprint": "b" * 64, "declaration_sha256": "c" * 64,
                 "manifest_identity_hash": "d" * 64,
                 "presentation_fingerprint": presentation}
        value.update(overrides)
        return value

    def receipt(self, presentation=True):
        value = {"declaration_sha256": "c" * 64, "guide_fingerprint": "b" * 64,
                 "manifest_identity_hash": "d" * 64, "source_revision": "e" * 40,
                 "channels": {}, "page_hashes": {}}
        if presentation:
            value["presentation_fingerprint"] = self.HASH
        return value

    def test_legacy_receipt_without_presentation_is_accepted(self):
        ok, problems = gate.verify_publication_receipt(
            self.receipt(presentation=False), self.base_candidate(),
            {"channels": {}}, "e" * 40)
        self.assertTrue(ok, problems)

    def test_exact_presentation_hash_required_when_persisted(self):
        ok, problems = gate.verify_publication_receipt(
            self.receipt(), self.base_candidate(presentation=self.HASH),
            {"channels": {}}, "e" * 40)
        self.assertTrue(ok, problems)
        ok, problems = gate.verify_publication_receipt(
            self.receipt(), self.base_candidate(presentation="f" * 64),
            {"channels": {}}, "e" * 40)
        self.assertFalse(ok)
        self.assertTrue(any("presentation fingerprint" in p for p in problems))

    def index_with_versions(self):
        """Canonical plus stable-archive routes carrying the presentation."""
        return {"versions": [
            {"channel": "stable", "route": "/docs/",
             "immutable_route": "/docs/versions/2.0.1/"},
            {"channel": "nightly", "route": "/docs/nightly/",
             "immutable_route": "/docs/nightly/"},
        ], "channels": {}}

    def site_with_presentation(self, root):
        for path in ("/docs/presentation.json",
                     "/docs/versions/2.0.1/presentation.json",
                     "/docs/nightly/presentation.json"):
            target = root / path.lstrip("/")
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(json.dumps({"fingerprint": self.HASH,
                                          "generator_sha256": "a" * 64}),
                              encoding="utf-8")
        for path in ("/docs/changelog/index.html",
                     "/docs/versions/2.0.1/changelog/index.html",
                     "/docs/nightly/changelog/index.html"):
            target = root / path.lstrip("/")
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("<h1>Changelog</h1>", encoding="utf-8")

    def test_stamp_reads_and_attests_real_presentation_at_canonical_and_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            self.site_with_presentation(site)
            index = self.index_with_versions()
            receipt = gate.stamp_publication_receipt(
                site, self.base_candidate(presentation=self.HASH), index, "e" * 40)
            for path in ("/docs/presentation.json",
                         "/docs/versions/2.0.1/presentation.json",
                         "/docs/nightly/presentation.json",
                         "/docs/changelog/index.html",
                         "/docs/versions/2.0.1/changelog/index.html",
                         "/docs/nightly/changelog/index.html"):
                self.assertIn(path, receipt["page_hashes"], path)
            ok, problems = gate.verify_publication_receipt(
                receipt, self.base_candidate(presentation=self.HASH), index,
                "e" * 40, gate._local_get(site))
            self.assertTrue(ok, problems)

    def test_stamp_rejects_wrong_or_missing_presentation_and_changelog(self):
        index = self.index_with_versions()
        candidate = self.base_candidate(presentation=self.HASH)
        cases = (
            ("canonical fingerprint wrong", lambda s: (
                s / "docs/presentation.json").write_text(
                    json.dumps({"fingerprint": "f" * 64}), encoding="utf-8")),
            ("stable archive fingerprint wrong", lambda s: (
                s / "docs/versions/2.0.1/presentation.json").write_text(
                    json.dumps({"fingerprint": "f" * 64}), encoding="utf-8")),
            ("canonical presentation missing", lambda s: (
                s / "docs/presentation.json").unlink()),
            ("stable archive presentation missing", lambda s: (
                s / "docs/versions/2.0.1/presentation.json").unlink()),
            ("nightly presentation missing", lambda s: (
                s / "docs/nightly/presentation.json").unlink()),
            ("archive changelog missing", lambda s: (
                s / "docs/versions/2.0.1/changelog/index.html").unlink()),
        )
        for label, mutate in cases:
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                site = Path(directory)
                self.site_with_presentation(site)
                mutate(site)
                with self.assertRaises(ValueError):
                    gate.stamp_publication_receipt(site, candidate, index, "e" * 40)

    def test_verify_rejects_echoed_hash_on_wrong_or_missing_live_presentation(self):
        index = self.index_with_versions()
        candidate = self.base_candidate(presentation=self.HASH)
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            self.site_with_presentation(site)
            receipt = gate.stamp_publication_receipt(site, candidate, index, "e" * 40)
            get_bytes = gate._local_get(site)
            ok, problems = gate.verify_publication_receipt(
                receipt, candidate, index, "e" * 40, get_bytes)
            self.assertTrue(ok, problems)

            canonical = site / "docs/presentation.json"
            canonical.write_text(json.dumps({"fingerprint": "f" * 64}), encoding="utf-8")
            ok, problems = gate.verify_publication_receipt(
                receipt, candidate, index, "e" * 40, get_bytes)
            self.assertFalse(ok)
            self.assertTrue(any("fingerprint" in p for p in problems), problems)

            canonical.unlink()
            def missing(path):
                raise urllib.error.HTTPError(path, 404, "not found", {}, None)
            ok, problems = gate.verify_publication_receipt(
                receipt, candidate, index, "e" * 40, missing)
            self.assertFalse(ok)
            self.assertTrue(any("presentation" in p for p in problems), problems)

            # A receipt that attests no presentation/changelog bytes fails even
            # when the echoed presentation_fingerprint field is correct.
            stripped = json.loads(json.dumps(receipt))
            for path in ("docs/presentation.json", "docs/versions/2.0.1/presentation.json",
                         "docs/nightly/presentation.json", "docs/changelog/index.html",
                         "docs/versions/2.0.1/changelog/index.html",
                         "docs/nightly/changelog/index.html"):
                stripped["page_hashes"].pop("/" + path, None)
            self.site_with_presentation(site)
            ok, problems = gate.verify_publication_receipt(
                stripped, candidate, index, "e" * 40, gate._local_get(site))
            self.assertFalse(ok)
            self.assertTrue(any("does not attest channel bytes" in p for p in problems), problems)

    def test_verify_requires_presentation_key_when_candidate_declares_it(self):
        ok, problems = gate.verify_publication_receipt(
            self.receipt(presentation=False), self.base_candidate(presentation=self.HASH),
            {"channels": {}}, "e" * 40)
        self.assertFalse(ok)
        self.assertTrue(any("missing the presentation fingerprint" in p for p in problems))

    def test_legacy_stamp_omits_presentation_key_and_keeps_bytes(self):
        """Pre-presentation receipts must keep their historical bytes exactly."""
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            legacy_index = LiveStateTests().index()
            (site / "version-index.json").write_text(
                json.dumps(legacy_index), encoding="utf-8")
            candidate = GuideLivenessTests().candidate()
            self.assertNotIn("presentation_fingerprint", candidate)
            receipt = gate.stamp_publication_receipt(
                site, candidate, legacy_index, GuideLivenessTests.REVISION)
            self.assertNotIn("presentation_fingerprint", receipt)
            raw = (site / "publication-receipt.json").read_bytes()
            self.assertNotIn(b"presentation_fingerprint", raw)
            ok, problems = gate.verify_publication_receipt(
                receipt, candidate, legacy_index, GuideLivenessTests.REVISION,
                gate._local_get(site))
            self.assertTrue(ok, problems)

    def test_pre_presentation_reconstruct_has_no_presentation_input(self):
        candidate = GuideLivenessTests().candidate()
        self.assertNotIn("presentation_fingerprint", candidate)
        manifests = GuideLivenessTests().evidence(
            sha256=candidate["guides"][0]["source_sha256"])
        evidence = {channel: {"channels": manifests[channel]["channels"],
                              "declaration_sha256": manifests[channel]["declaration_sha256"],
                              "published_routes": {}, "published_route_problems": []}
                    for channel in gate.CHANNELS}
        decision = gate.guide_change_decision(candidate, evidence)
        self.assertFalse(decision["publish"], decision)

    def test_guide_change_decision_notices_presentation_only_change(self):
        candidate = self.base_candidate(presentation=self.HASH)
        candidate["guides"] = []
        live = {}
        for channel in gate.CHANNELS:
            live[channel] = {"channels": [], "declaration_sha256": "c" * 64,
                             "presentation": {"fingerprint": "f" * 64}}
        live["published_routes"] = {}
        live["published_route_problems"] = []
        decision = gate.guide_change_decision(candidate, live)
        self.assertTrue(decision["publish"])
        self.assertTrue(any("presentation" in p for p in decision["problems"]))
        for channel in gate.CHANNELS:
            live[channel]["presentation"] = {"fingerprint": self.HASH}
        decision = gate.guide_change_decision(candidate, live)
        self.assertFalse(decision["publish"], decision)


if __name__ == "__main__":
    unittest.main()
