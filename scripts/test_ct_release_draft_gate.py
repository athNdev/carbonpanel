#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Narrow regression for the controller-ct draft release path."""
import json
import unittest

from scripts import ct_release_draft_gate as gate

VERSION = "controller-ct-v1.0.0"
SHA = "a" * 40
API = "https://api.github.com/repos/athNdev/carbonpanel/releases/123456"


class DraftViewTests(unittest.TestCase):
    def view(self, **overrides):
        data = {
            "isDraft": True,
            "tagName": VERSION,
            "targetCommitish": SHA,
            "apiUrl": API,
        }
        data.update(overrides)
        return data

    def test_draft_view_yields_release_id_url(self):
        self.assertEqual(gate.draft_api_url(self.view(), VERSION, SHA), API)

    def test_non_draft_wrong_tag_wrong_target_and_bad_url_refused(self):
        for bad in (
            self.view(isDraft=False),
            self.view(tagName="v1.0.0"),
            self.view(targetCommitish="b" * 40),
            self.view(apiUrl="https://api.github.com/repos/x/y/releases/tags/z"),
        ):
            with self.assertRaises(gate.GateError):
                gate.draft_api_url(bad, VERSION, SHA)


class AssetTests(unittest.TestCase):
    def setUp(self):
        self.archive = "c" * 64
        self.checksum = "d" * 64
        self.receipt = "e" * 64

    def assets_text(self):
        return "\n".join([
            f"{gate.ASSET_NAMES[0]} sha256:{self.archive}",
            f"{gate.ASSET_NAMES[1]} sha256:{self.checksum}",
            f"{gate.ASSET_NAMES[2]} sha256:{self.receipt}",
        ])

    def test_exact_assets_pass(self):
        result = gate.verify_assets(self.assets_text(), self.archive,
                                    self.checksum, self.receipt)
        self.assertEqual(len(result), 3)

    def test_wrong_digest_missing_or_extra_asset_refused(self):
        with self.assertRaises(gate.GateError):
            gate.verify_assets(self.assets_text().replace("sha256:" + self.receipt,
                                                          "sha256:" + "f" * 64),
                               self.archive, self.checksum, self.receipt)
        with self.assertRaises(gate.GateError):
            gate.verify_assets("\n".join(self.assets_text().splitlines()[:-1]),
                               self.archive, self.checksum, self.receipt)
        with self.assertRaises(gate.GateError):
            gate.verify_assets(self.assets_text() + "\nextra.txt sha256:" + "0" * 64,
                               self.archive, self.checksum, self.receipt)


if __name__ == "__main__":
    unittest.main()
