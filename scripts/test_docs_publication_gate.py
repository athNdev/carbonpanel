# SPDX-License-Identifier: MPL-2.0
"""Offline qualification of the docs-only maintained-guide publication lane."""
import argparse
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import zipfile

from scripts import docs_publication_gate as gate


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
    DEPLOYMENT = "7924139974"
    RUN = "44602759613"
    ARTIFACT = "14568077771"
    SHA = "f93c3a9a5b1ee900bb55bbb1c59307b93cced431"

    def fixture(self):
        deployments = [
            {"id": self.DEPLOYMENT, "environment": "github-pages", "sha": self.SHA,
             "created_at": "2026-11-19T19:53:09Z"},
            {"id": "7912890948", "environment": "github-pages", "sha": "6" * 40,
             "created_at": "2026-11-19T12:19:59Z"},
        ]
        statuses = {
            self.DEPLOYMENT: [{"state": "success", "target_url":
                               "https://github.com/athNdev/carbonpanel/actions/runs/"
                               + self.RUN + "/job/1"}],
            "7912890948": [{"state": "success", "target_url":
                            "https://github.com/athNdev/carbonpanel/actions/runs/"
                            "44500000000/job/1"}],
        }
        artifacts = {
            self.RUN: [{"id": self.ARTIFACT, "name": "github-pages", "expired": False,
                        "workflow_run": {"id": int(self.RUN), "head_sha": self.SHA}}],
            "44500000000": [{"id": "111", "name": "github-pages", "expired": False}],
        }
        return deployments, statuses, artifacts

    def test_observed_latest_success_resolves_to_exact_artifact(self):
        deployments, statuses, artifacts = self.fixture()
        baseline, reason = gate.pages_baseline_from_deployments(
            deployments, statuses, artifacts)
        self.assertEqual(reason, "")
        self.assertEqual((baseline["deployment_id"], baseline["run_id"],
                          baseline["artifact_id"], baseline["sha"]),
                         (self.DEPLOYMENT, self.RUN, self.ARTIFACT, self.SHA))

    def test_unknown_state_fails_closed(self):
        deployments, statuses, artifacts = self.fixture()
        statuses[self.DEPLOYMENT][0]["state"] = "in_progress"
        baseline, _ = gate.pages_baseline_from_deployments(
            deployments, statuses, artifacts)
        self.assertEqual(baseline["run_id"], "44500000000")
        statuses[self.DEPLOYMENT][0]["state"] = "unrecognised"
        baseline, reason = gate.pages_baseline_from_deployments(
            deployments, statuses, artifacts)
        self.assertIsNone(baseline)
        self.assertIn("unknown state", reason)

    def test_two_artifacts_and_all_expired_keep_deployment_identity(self):
        deployments, statuses, artifacts = self.fixture()
        artifacts[self.RUN].append(
            {"id": "222", "name": "github-pages-prior", "expired": False})
        baseline, reason = gate.pages_baseline_from_deployments(
            deployments, statuses, artifacts)
        self.assertEqual(reason, "")
        self.assertEqual(baseline["sha"], self.SHA)
        self.assertEqual(baseline["run_id"], self.RUN)
        self.assertEqual(len(baseline["artifact_candidates"]), 2)
        for artifact in artifacts[self.RUN]:
            artifact["expired"] = True
        baseline, reason = gate.pages_baseline_from_deployments(
            deployments, statuses, artifacts)
        self.assertIsNotNone(baseline)
        self.assertEqual(baseline["sha"], self.SHA)
        self.assertEqual(baseline["artifact_id"], "")
        self.assertFalse(baseline["artifact_verified"])
        self.assertIn("unavailable or expired", reason)

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

    def test_no_channel_writes_and_exact_prior_baseline(self):
        for forbidden in ("channel-promote", "channel-transaction", "--apply",
                          "workflow_dispatch", "release.py publish"):
            self.assertNotIn(forbidden, self.text)
        self.assertIn("pages-baseline", self.text)
        self.assertIn("fetch-prior-pages", self.text)
        self.assertIn("--repository \"$GITHUB_REPOSITORY\" --run-id \"$RUN_ID\"", self.text)
        self.assertNotIn("--artifact-id \"$ARTIFACT_ID\"", self.text)
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
                 patch.object(gate.subprocess, "check_call", return_value=0):
                result = gate.recover_prior_pages(
                    root / "prior", "https://carbonpanel.dev", sha, str(root),
                    str(declaration))
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


if __name__ == "__main__":
    unittest.main()
