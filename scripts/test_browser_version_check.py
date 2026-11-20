# SPDX-License-Identifier: MPL-2.0
"""Fail-closed tests for the published-channel browser gate."""
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from scripts import browser_version_check as bvc
from scripts import version_contract as vc


def sample_index():
    digest = "sha256:" + "b" * 64
    return {
        "schema_version": 3,
        "repository": "https://github.com/athNdev/carbonpanel",
        "channels": {
            "stable": {
                "display": "Stable", "version": "1.0.40", "route": "/docs/",
                "commit": "a" * 40,
                "image": {"digest": digest,
                          "docker_hub": "atharvnaphade/carbonpanel:latest",
                          "ghcr": "ghcr.io/athndev/carbonpanel:latest"},
                "promotion": {"state": "published", "promoted": True},
            },
            "nightly": {
                "display": "Nightly", "route": "/docs/nightly/",
                "commit": "c" * 40,
                "image": {"digest": digest,
                          "docker_hub": "atharvnaphade/carbonpanel:nightly",
                          "ghcr": "ghcr.io/athndev/carbonpanel:nightly"},
                "promotion": {"state": "final", "promoted": True},
            },
        },
        "promotion": {"state": "baseline", "promoted": False},
        "versions": [],
    }


def site_with(index, root):
    site = Path(root)
    (site / "version-index.json").write_text(json.dumps(index), encoding="utf-8")
    (site / "channel-marker.json").write_text(
        json.dumps(vc.channel_marker(index)), encoding="utf-8")
    return site


def good_snapshot(paths, origin="http://127.0.0.1:1"):
    result = {}
    for path in paths:
        for width in bvc.BROWSER_WIDTHS:
            result[f"{path}|{width}"] = {
                "html": "<html><body>packaged site</body></html>",
                "status": 200, "url": origin + path,
            }
    return result


class BrowserCollectorFailClosedTests(unittest.TestCase):
    """A broken nightly snapshot must abort with the real browser error."""

    def run_check(self, mutate):
        index = sample_index()
        with tempfile.TemporaryDirectory() as root:
            site = site_with(index, root)
            def collector(origin, paths):
                snapshot = good_snapshot(paths, origin)
                outcome = mutate(snapshot)
                return snapshot if outcome is None else outcome
            with patch.object(bvc, "collect_dom", side_effect=collector), \
                    patch("sys.argv", ["browser_version_check.py", str(site)]), \
                    redirect_stdout(io.StringIO()):
                with self.assertRaises(ValueError) as caught:
                    bvc.main()
            return str(caught.exception)

    def test_explicit_browser_error_is_not_a_missing_marker(self):
        message = self.run_check(lambda snap: snap.update({
            "/docs/nightly/|375": {"error": "TimeoutError: page.goto timed out"}}))
        self.assertIn("browser qualification aborted", message)
        self.assertIn("TimeoutError: page.goto timed out", message)
        self.assertNotIn("missing", message)

    def test_empty_html_aborts(self):
        message = self.run_check(lambda snap: snap.update({
            "/docs/nightly/|375": {"html": "", "status": 200,
                                   "url": "http://127.0.0.1:1/docs/nightly/"}}))
        self.assertIn("browser returned empty DOM", message)

    def test_missing_entry_aborts(self):
        def drop(snap):
            snap.pop("/docs/nightly/|375", None)
        message = self.run_check(drop)
        self.assertIn("no browser result collected", message)

    def test_non_200_or_wrong_origin_aborts(self):
        message = self.run_check(lambda snap: snap.update({
            "/docs/nightly/|1440": {"html": "<html>ok</html>", "status": 404,
                                    "url": "http://127.0.0.1:1/docs/nightly/"}}))
        self.assertIn("HTTP status 404", message)
        message = self.run_check(lambda snap: snap.update({
            "/docs/nightly/|1440": {"html": "<html>ok</html>", "status": 200,
                                    "url": "https://elsewhere.invalid/"}}))
        self.assertIn("outside origin", message)


if __name__ == "__main__":
    unittest.main()
