#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Retry dependency transport failures before running Go checks, never the checks."""
import subprocess
import sys
import time


def download(command=("go", "mod", "download"), attempts=3, delay=2):
    for attempt in range(attempts):
        try:
            result = subprocess.run(command, check=False, timeout=180)
            status = result.returncode
        except subprocess.TimeoutExpired:
            status = 124
        if status == 0:
            return 0
        if attempt + 1 < attempts:
            print(f"Dependency download failed; retry {attempt + 2}/{attempts}", flush=True)
            time.sleep(delay * (2 ** attempt))
    return status


if __name__ == "__main__":
    sys.exit(download())
