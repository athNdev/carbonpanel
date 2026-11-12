# SPDX-License-Identifier: MPL-2.0
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from scripts.check_lint_report import check_report


class LintReportTest(unittest.TestCase):
    def test_clean_and_historical_debt(self):
        self.assertEqual(check_report({"Issues": [], "Report": {}}, 0), {"outcome": "clean", "count": 0})
        self.assertEqual(check_report({"Issues": [{"FromLinter": "errcheck"}], "Report": {}}, 1), {"outcome": "findings", "count": 1})

    def test_incomplete_or_invalid_reports_fail(self):
        cases = [
            ({"Issues": [{"FromLinter": "errcheck"}], "Report": {"Error": "analyzer failed"}}, 1),
            ({"Issues": [], "Report": {"Error": "analyzer failed"}}, 0),
            ({"Issues": [{"FromLinter": "typecheck"}], "Report": {}}, 1),
            ({"Issues": [], "Report": {}}, 7),
            ({"Issues": [], "Report": {}}, 1),
            ({"Issues": [{"FromLinter": "errcheck"}], "Report": {}}, 0),
            ({"Issues": []}, 0),
            ({"Report": {}}, 0),
            ({"Issues": None, "Report": {}}, 0),
            ({"Issues": [], "Report": None}, 0),
            ({"Issues": [{}], "Report": {}}, 1),
            (None, 0),
        ]
        for data, status in cases:
            with self.subTest(data=data, status=status):
                with self.assertRaises(ValueError):
                    check_report(data, status)

    def test_command_rejects_mixed_error_missing_and_malformed_files(self):
        script = Path(__file__).with_name("check_lint_report.py")
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / "report.json"
            for contents in [None, '{"Issues":', json.dumps({"Issues": [{"FromLinter": "errcheck"}], "Report": {"Error": "analyzer failed"}})]:
                if contents is not None:
                    report.write_text(contents, encoding="utf-8")
                result = subprocess.run([sys.executable, str(script), str(report), "1"], capture_output=True, text=True, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Full lint validation failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
