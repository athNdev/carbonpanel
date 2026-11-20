# SPDX-License-Identifier: MPL-2.0
"""Disposable registry and Pages fixtures for the OSS-413 recovery phases."""

import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import urllib.error

from scripts import nightly_recovery as recovery

PRIOR = "a" * 40
DOCS = "b" * 40
TARGET = "c" * 40
PRIOR_IMAGE = "sha256:" + "1" * 64
DOCS_IMAGE = "sha256:" + "2" * 64
TARGET_IMAGE = "sha256:" + "3" * 64
CONFIG = {
    "hub": {"commit": PRIOR, "digest": PRIOR_IMAGE, "build_run": 1},
    "docs": {"commit": DOCS, "digest": DOCS_IMAGE},
    "candidate": {"commit": TARGET, "digest": TARGET_IMAGE, "build_run": 2},
}


class RegistryFixture:
    def __init__(self, tags, images):
        self.tags = tags
        self.images = images

    def tag(self, name):
        return self.tags.get(name)

    def image(self, digest, commit, *, with_provenance=True):
        if self.images[digest] != commit:
            raise ValueError("source mismatch")
        return {arch: digest for arch in ("amd64", "arm64")}


class RecoveryRehearsal(unittest.TestCase):
    def setUp(self):
        self.hub = RegistryFixture({"nightly": PRIOR_IMAGE, f"sha-{PRIOR}": PRIOR_IMAGE,
                                    f"sha-{TARGET}": TARGET_IMAGE},
                                   {PRIOR_IMAGE: PRIOR, TARGET_IMAGE: TARGET})
        self.ghcr = RegistryFixture({f"sha-{TARGET}": TARGET_IMAGE}, {TARGET_IMAGE: TARGET})
        stage_marker = f"/docs/candidate/{TARGET}/marker.json"
        prior = {"/version-index.json": b"prior", "/docs/nightly/": b"prior HTML",
                 "/docs/nightly/schemav1.yaml": b"prior OpenAPI", stage_marker: None}
        stage = {**prior, stage_marker: b"verified candidate marker"}
        final = {**stage, "/version-index.json": b"final", "/docs/nightly/": b"final HTML",
                 "/docs/nightly/schemav1.yaml": b"final OpenAPI"}
        self.sites = {"prior": prior, "stage": stage, "final": final}
        self.live = prior.copy()
        self.plan = {"config": CONFIG, "probes": {
            name: {path: None if bytes_ is None else recovery.digest(bytes_)
                   for path, bytes_ in files.items()}
            for name, files in self.sites.items()
        }}
        self.writes = []

    def observe(self):
        state, _ = recovery.observe(self.plan, self.hub, self.ghcr, self.live.__getitem__)
        return state

    def mutate(self, action):
        self.writes.append(action)
        if action in self.sites:
            self.live = self.sites[action].copy()
        elif action == "ghcr":
            self.ghcr.tags["nightly"] = TARGET_IMAGE
        else:
            self.hub.tags["nightly"] = {"hub": TARGET_IMAGE, "hub-prior": PRIOR_IMAGE}[action]

    def rehearse(self, phase):
        action, expected = recovery.decide(phase, self.observe())
        self.assertIsNotNone(action)
        # Crash before the write. Rechecking observations authorizes the same operation.
        self.assertEqual(recovery.decide(phase, self.observe()), (action, expected))
        self.mutate(action)
        # Crash after the write and before the success receipt. Remote state is
        # the only completion evidence available to a fresh runner.
        self.assertEqual(self.observe(), expected)
        self.assertEqual(recovery.decide(phase, self.observe()), (None, expected))
        self.assertEqual(recovery.qualify_images(CONFIG, self.hub, self.ghcr), {
            "hub_prior": {arch: PRIOR_IMAGE for arch in ("amd64", "arm64")},
            "hub_candidate": {arch: TARGET_IMAGE for arch in ("amd64", "arm64")},
            "ghcr_candidate": {arch: TARGET_IMAGE for arch in ("amd64", "arm64")},
        })
        self.assertNotIn(f"sha-{PRIOR}", self.ghcr.tags)

    def test_forward_crash_resume_every_mutation(self):
        for phase in ("stage", "hub", "docs", "commit"):
            with self.subTest(phase=phase):
                self.rehearse(phase)
        self.assertEqual(self.writes, ["stage", "hub", "final", "ghcr"])
        self.assertEqual(self.observe(), ("candidate", "candidate", "final"))

    def test_compensation_after_staging(self):
        for phase in ("stage", "restore-docs"):
            self.rehearse(phase)
        self.assertEqual(self.observe(), ("prior", "absent", "prior"))

    def test_compensation_after_hub_move(self):
        for phase in ("stage", "hub", "restore-hub", "restore-docs"):
            self.rehearse(phase)
        self.assertEqual(self.observe(), ("prior", "absent", "prior"))

    def test_compensation_after_final_docs(self):
        for phase in ("stage", "hub", "docs", "restore-hub", "restore-docs"):
            self.rehearse(phase)
        self.assertEqual(self.observe(), ("prior", "absent", "prior"))
        self.assertEqual(self.writes, ["stage", "hub", "final", "hub-prior", "prior"])

    def test_ghcr_commit_point_is_forward_only(self):
        for phase in ("stage", "hub", "docs", "commit"):
            self.rehearse(phase)
        for phase in ("restore-hub", "restore-docs"):
            with self.assertRaisesRegex(ValueError, "no action is authorized"):
                recovery.decide(phase, self.observe())
        self.assertEqual(recovery.decide("inspect", self.observe()),
                         (None, ("candidate", "candidate", "final")))

    def test_refuses_wrong_phase_and_unowned_aliases(self):
        with self.assertRaises(ValueError):
            recovery.decide("hub", self.observe())
        self.hub.tags["nightly"] = DOCS_IMAGE
        with self.assertRaisesRegex(ValueError, "outside the plan"):
            self.observe()

    def test_mixed_pages_or_openapi_fail_closed(self):
        self.rehearse("stage")
        self.live["/docs/nightly/schemav1.yaml"] = self.sites["final"]["/docs/nightly/schemav1.yaml"]
        with self.assertRaisesRegex(ValueError, "no unique plan state"):
            self.observe()

    def test_candidate_missing_or_different_and_prior_lost_fail(self):
        for registry, tag, value in (
            (self.hub, f"sha-{PRIOR}", None),
            (self.ghcr, f"sha-{TARGET}", None),
            (self.ghcr, f"sha-{TARGET}", DOCS_IMAGE),
        ):
            original = registry.tags[tag]
            registry.tags[tag] = value
            with self.subTest(tag=tag, value=value), self.assertRaises(ValueError):
                recovery.qualify_images(CONFIG, self.hub, self.ghcr)
            registry.tags[tag] = original


class QualificationTests(unittest.TestCase):
    def test_digest_address_checks_actual_bytes(self):
        with patch.object(recovery, "fetch", side_effect=[b'{"token":"token"}', b"{}"]):
            registry = recovery.Registry("example.invalid", "fixture", "https://example.invalid/token")
            with self.assertRaisesRegex(ValueError, "content differs"):
                registry.document("blobs", TARGET_IMAGE)

    def test_only_manifest_unknown_is_absence(self):
        from io import BytesIO

        for status, code, absent in ((404, "MANIFEST_UNKNOWN", True),
                                     (404, "NAME_UNKNOWN", False),
                                     (401, "UNAUTHORIZED", False)):
            body = BytesIO(json.dumps({"errors": [{"code": code}]}).encode())
            error = urllib.error.HTTPError("https://example.invalid", status, "error", {}, body)
            with patch.object(recovery, "fetch", side_effect=[b'{"token":"token"}', error]):
                registry = recovery.Registry("example.invalid", "fixture", "https://example.invalid/token")
                if absent:
                    self.assertIsNone(registry.tag("nightly"))
                else:
                    with self.assertRaises(urllib.error.HTTPError):
                        registry.tag("nightly")

    def test_buildkit_provenance_needs_repository_revision_and_subject(self):
        args = {"vcs:revision": TARGET, "vcs:source": "https://github.com/athNdev/carbonpanel.git"}
        statement = {"subject": [{"digest": {"sha256": TARGET_IMAGE[7:]}}],
                     "predicateType": "https://slsa.dev/provenance/v1",
                     "predicate": {"buildDefinition": {
                         "buildType": "https://github.com/moby/buildkit/docs",
                         "externalParameters": {"request": {"root": {"request": {"args": args}}}},
                     }}}
        recovery.verify_provenance(statement, TARGET_IMAGE, TARGET)
        with self.assertRaisesRegex(ValueError, "subject mismatch"):
            recovery.verify_provenance(statement, PRIOR_IMAGE, TARGET)
        with self.assertRaisesRegex(ValueError, "repository revision"):
            recovery.verify_provenance(statement, TARGET_IMAGE, PRIOR)
        args["vcs:source"] = "https://github.com/example/fork.git"
        with self.assertRaisesRegex(ValueError, "repository revision"):
            recovery.verify_provenance(statement, TARGET_IMAGE, TARGET)

    def test_push_log_receipt_requires_the_exact_immutable_build(self):
        record = {"path": ".github/workflows/cd.yml", "head_branch": "main", "head_sha": TARGET,
                  "conclusion": "success", "event": "workflow_dispatch",
                  "head_repository": {"full_name": recovery.REPOSITORY}}
        log = f"Build & Push #12 pushing manifest for docker.io/***/carbonpanel:sha-{TARGET}@{TARGET_IMAGE} 3.1s done\n"
        with patch.object(recovery, "api", return_value=record), patch.object(recovery, "run", return_value=log):
            recovery.verify_build_run(2, TARGET, TARGET_IMAGE)
            with self.assertRaisesRegex(ValueError, "lack the exact immutable push"):
                recovery.verify_build_run(2, TARGET, PRIOR_IMAGE)
        with patch.object(recovery, "api", return_value={**record, "head_branch": "topic"}):
            with self.assertRaisesRegex(ValueError, "trusted-main"):
                recovery.verify_build_run(2, TARGET, TARGET_IMAGE)

    def test_docs_tree_binding_covers_hidden_and_off_path_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            site = Path(directory)
            (site / ".nojekyll").write_bytes(b"")
            (site / "docs").mkdir()
            route = site / "docs/marker.json"
            route.write_bytes(b"first")
            initial = recovery.tree_digest(site)
            route.write_bytes(b"second")
            self.assertNotEqual(recovery.tree_digest(site), initial)

    def test_build_preserves_newer_verified_stable_identity(self):
        stable = {"commit": PRIOR, "tag": "carbonpanel-v1.0.41", "version": "1.0.41",
                  "image": {"revision": PRIOR, "digest": PRIOR_IMAGE}}
        public = {"channels": {"stable": stable}}
        hub = RegistryFixture({"latest": PRIOR_IMAGE, "stable": PRIOR_IMAGE}, {PRIOR_IMAGE: PRIOR})
        ghcr = RegistryFixture({"latest": PRIOR_IMAGE, "stable": PRIOR_IMAGE}, {PRIOR_IMAGE: PRIOR})
        identity = recovery.verify_stable(public, hub, ghcr)
        with patch.object(recovery.subprocess, "run") as builder:
            recovery.build_site(SimpleNamespace(run="17"), CONFIG["candidate"], "final", identity)
        self.assertEqual(len(builder.call_args_list), 2)
        for call in builder.call_args_list:
            env = call.kwargs["env"]
            self.assertEqual(env["CARBONPANEL_STABLE_TAG"], "carbonpanel-v1.0.41")
            self.assertEqual(env["CARBONPANEL_STABLE_REVISION"], PRIOR)
            self.assertEqual(env["CARBONPANEL_STABLE_DIGEST"], PRIOR_IMAGE)
        ghcr.tags["latest"] = DOCS_IMAGE
        with self.assertRaisesRegex(ValueError, "Stable/latest aliases disagree"):
            recovery.verify_stable(public, hub, ghcr)

    def test_legacy_stable_build_requires_exact_tracked_identity(self):
        tracked = recovery.stable_identity(json.loads(Path("docs/website-source.json").read_bytes()))
        with patch.object(recovery.subprocess, "run") as builder, \
                patch.dict(recovery.os.environ, {"CARBONPANEL_STABLE_TAG": "carbonpanel-v9.0.0"}):
            recovery.build_site(SimpleNamespace(run="17"), CONFIG["candidate"], "final", tracked)
        self.assertNotIn("CARBONPANEL_STABLE_TAG", builder.call_args.kwargs["env"])
        with self.assertRaisesRegex(ValueError, "Tracked legacy Stable source differs"):
            recovery.build_site(SimpleNamespace(run="17"), CONFIG["candidate"], "final",
                                {**tracked, "commit": PRIOR})

    def test_uncertain_pages_submission_blocks_mutation_until_provider_is_terminal(self):
        log = "Created deployment for " + TARGET + ", ID: first-plan-deployment"
        for status, settled in (("deployment_attempt_error", False), ("succeed", True),
                                ("deployment_cancelled", True), ("deployment_lost", False)):
            with self.subTest(status=status), patch.object(
                    recovery, "api", side_effect=[{"jobs": []}, {"status": status}]), patch.object(
                        recovery, "run", return_value=log):
                if settled:
                    recovery.require_failed_pages_run_settled("10")
                else:
                    with self.assertRaisesRegex(ValueError, "unconfirmed status"):
                        recovery.require_failed_pages_run_settled("10")


if __name__ == "__main__":
    unittest.main()
