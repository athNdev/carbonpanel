# SPDX-License-Identifier: MPL-2.0
"""Exercise the actual dependency subprocess boundary with recorded failure shape."""
from pathlib import Path
import contextlib
import io
import sys
import tempfile
import unittest

from scripts.go_mod_download import download


class GoModuleDownloadTests(unittest.TestCase):
    def exercise(self, failures):
        with tempfile.TemporaryDirectory() as directory:
            source, counter = Path(directory) / "fetch.py", Path(directory) / "attempts"
            source.write_text(
                "from pathlib import Path\nimport sys\n"
                "counter = Path(sys.argv[1])\n"
                "attempt = int(counter.read_text()) + 1 if counter.exists() else 1\n"
                "counter.write_text(str(attempt))\n"
                f"if attempt <= {failures}:\n"
                "    sys.exit(1)\n", encoding="utf-8")
            with contextlib.redirect_stdout(io.StringIO()):
                result = download((sys.executable, str(source), str(counter)), delay=0)
            return result, int(counter.read_text())

    def test_transport_failure_recovers_before_checks_start(self):
        self.assertEqual(self.exercise(2), (0, 3))

    def test_persistent_failure_still_fails_the_job(self):
        self.assertEqual(self.exercise(9), (1, 3))

    def test_success_has_no_duplicate_download(self):
        self.assertEqual(self.exercise(0), (0, 1))


if __name__ == "__main__":
    unittest.main()
