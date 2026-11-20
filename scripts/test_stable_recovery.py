# SPDX-License-Identifier: MPL-2.0
import copy
import itertools
import unittest

from scripts import stable_recovery as recovery


class InterruptedPublicationTests(unittest.TestCase):
    def setUp(self):
        self.prior = {"tag": "v1.0.40", "digest": "sha256:" + "a" * 64, "commit": "1" * 40, "index": "a" * 64}
        self.target = {"tag": "carbonpanel-v2.0.1", "digest": "sha256:" + "b" * 64,
                       "commit": "2" * 40, "index": "b" * 64}
        self.repo = "example/panel"
        self.journal = {"repo": self.repo, "run": "42", "record": 7, "schema": 1,
                        "workflow_sha": "c" * 40, "prior": self.prior, "target": self.target}
        self.run = {"id": 42, "repository": {"full_name": self.repo}, "head_repository": {"full_name": self.repo},
                    "head_sha": "c" * 40, "head_branch": "main", "path": ".github/workflows/release.yml",
                    "event": "workflow_dispatch", "status": "completed", "conclusion": "cancelled"}
        self.record = {"tag_name": self.target["tag"], "id": 7, "draft": True}
        self.latest = {"tag_name": self.prior["tag"], "draft": False, "prerelease": False}

    def authorize(self, **overrides):
        values = dict(journal=self.journal, run=self.run, repository=self.repo, run_id="42",
                      record=self.record, latest=self.latest, prior=self.prior, target=self.target)
        recovery.authorize(**(values | overrides))

    def test_only_original_failed_trusted_transaction_can_restore(self):
        self.authorize()
        for attrs in ({"event": "pull_request"}, {"head_branch": "feature"}, {"id": 13},
                      {"head_sha": "d" * 40}, {"head_repository": {"full_name": "fork/panel"}},
                      {"path": ".github/workflows/ci.yml"}, {"conclusion": "success"},
                      {"status": "in_progress"}):
            with self.subTest(attrs=attrs), self.assertRaises(ValueError):
                self.authorize(run=self.run | attrs)

    def test_publication_or_changed_ids_never_rollback(self):
        for overrides in ({"record": self.record | {"draft": False}}, {"record": self.record | {"id": 8}},
                          {"latest": self.latest | {"tag_name": self.target["tag"]}},
                          {"prior": self.prior | {"index": "d" * 64}},
                          {"target": self.target | {"commit": "c" * 40}}, {"run_id": "45"}):
            with self.subTest(overrides=overrides), self.assertRaises(ValueError):
                self.authorize(**overrides)

    @staticmethod
    def observed(item):
        return ["present", {"digest": item["digest"], "revision": item["commit"],
                            "platforms": [("linux", "amd64"), ("linux", "arm64")]}]

    def aliases(self, items):
        result = {}
        it = iter(items)
        for alias in ("latest", "stable"):
            result[alias] = {registry: next(it) for registry in ("docker_hub", "ghcr")}
        return result

    def test_every_partial_alias_write_is_classified_before_mutation(self):
        for states in itertools.product((False, True), repeat=4):
            observations = self.aliases(self.observed(self.target if advanced else self.prior) for advanced in states)
            self.assertEqual((False, any(states)), recovery.classify_aliases(self.prior, self.target, observations.get))
        prior = self.observed(self.prior)
        untouched = self.aliases((prior, prior, ["absent", {}], prior))
        self.assertEqual((True, False), recovery.classify_aliases(self.prior, self.target, untouched.get))
        for slot in range(4):
            for invalid in (["unknown", {}], self.observed(self.target | {"commit": "f" * 40}),
                            ["present", {"digest": self.prior["digest"], "revision": self.prior["commit"],
                                         "platforms": [("linux", "amd64")]}]):
                states = [copy.deepcopy(prior) for _ in range(4)]
                states[slot] = invalid
                with self.subTest(slot=slot, invalid=invalid), self.assertRaises(ValueError):
                    recovery.classify_aliases(self.prior, self.target, self.aliases(states).get)
        with self.assertRaises(ValueError):
            recovery.classify_aliases(self.prior, self.target,
                self.aliases((self.observed(self.target), prior, ["absent", {}], prior)).get)


if __name__ == "__main__":
    unittest.main()
