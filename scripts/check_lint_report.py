#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Validate golangci-lint v2 JSON completion before accepting historical debt."""

import argparse
import json
from pathlib import Path
import sys


def check_report(data, exit_code):
    # v2.13.2 can retain IssuesFound (1) even when Report.Error is populated.
    # Its JSONResult contains Issues and Report (pkg/printers/json.go).
    if exit_code not in (0, 1):
        raise ValueError(f"linter did not complete (exit {exit_code})")
    if not isinstance(data, dict) or not isinstance(data.get("Issues"), list):
        raise ValueError("missing structured Issues list")
    report = data.get("Report")
    if not isinstance(report, dict):
        raise ValueError("missing structured Report")
    error = report.get("Error", "")
    if not isinstance(error, str) or error:
        raise ValueError(f"linter reported incomplete analysis: {error}")
    for issue in data["Issues"]:
        if not isinstance(issue, dict) or not isinstance(issue.get("FromLinter"), str) or not issue["FromLinter"]:
            raise ValueError("malformed lint finding")
        if issue["FromLinter"] == "typecheck":
            raise ValueError("typechecking failed; findings do not represent a complete scan")
    count = len(data["Issues"])
    if (exit_code == 1) != (count > 0):
        raise ValueError("linter exit status disagrees with findings")
    return {"outcome": "findings" if count else "clean", "count": count}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path)
    parser.add_argument("exit_code", type=int)
    args = parser.parse_args()
    try:
        data = json.loads(args.report.read_text(encoding="utf-8"))
        result = check_report(data, args.exit_code)
    except (OSError, ValueError) as error:
        print(f"Full lint validation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    sys.exit(main())
