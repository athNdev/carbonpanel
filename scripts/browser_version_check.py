#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Headless-browser qualification for the multi-version documentation.

Drives a real Chromium against the packaged site: direct/deep links, the common
version switcher, per-version base URLs, canonical URLs, search scope, sitemap
and negative cases. Set CHROME_PATH to a Chromium-family executable, or place
one at a standard location. No network, registry or site deployment is used.
"""
import functools
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import subprocess
import sys
import threading
from urllib.error import HTTPError
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parent.parent
SITE = ROOT / ".website-build/site"
BASE = "https://carbonpanel.dev"


def chrome_path():
    candidates = [os.environ.get("CHROME_PATH")]
    candidates += [
        Path("/usr/bin/chromium"), Path("/usr/bin/google-chrome"),
    ]
    for candidate in candidates:
        if candidate and Path(candidate).is_file():
            return str(candidate)
    for name in ("chromium", "google-chrome", "chrome"):
        found = shutil.which(name)
        if found:
            return found
    raise RuntimeError("Set CHROME_PATH to a Chromium-family executable.")


def main():
    site = Path(sys.argv[1]) if len(sys.argv) > 1 else SITE
    if not (site / "version-index.json").is_file():
        raise RuntimeError(f"{site} is not a packaged multi-version site")
    class QuietHandler(SimpleHTTPRequestHandler):
        def log_message(self, *_args):
            pass

    handler = functools.partial(QuietHandler, directory=str(site))
    server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    origin = f"http://127.0.0.1:{server.server_port}"
    binary = chrome_path()
    failures = []

    def dom(path):
        for attempt in range(3):
            profile = Path(os.environ.get("TEMP", "/tmp")) / \
                f"cp-version-dom-{abs(hash(path))}-{attempt}"
            try:
                result = subprocess.run(
                    [binary, "--headless", "--disable-gpu", "--no-sandbox",
                     "--disable-dev-shm-usage", "--disable-javascript",
                     "--no-first-run", "--no-default-browser-check",
                     "--virtual-time-budget=500", "--user-data-dir=" + str(profile),
                     "--dump-dom", origin + path], capture_output=True, text=True,
                    encoding="utf-8", errors="replace", timeout=30)
            except subprocess.TimeoutExpired:
                continue
            if result.stdout:
                return result.stdout
        failures.append(f"{path}: browser did not return DOM")
        return ""

    def expect(path, needles):
        page = dom(path)
        for needle in needles:
            if needle not in page:
                failures.append(f"{path}: missing {needle!r}")
        return page

    try:
        home = expect("/docs/", [
            "data-version-switcher", "1.0.40", "1.0.39", "3.0.0-alpha.1",
            '<link rel="canonical" href="https://carbonpanel.dev/docs/">',
        ])
        if "3.0.0-beta.4" in home:
            failures.append("/docs/: excluded beta.4 label leaked into the switcher")
        # Deep link is carried into versions that contain the page.
        expect("/docs/getting-started/docker-compose/", [
            "/docs/versions/1.0.39/getting-started/docker-compose/",
            "/docs/versions/3.0.0-alpha.1/getting-started/docker-compose/",
        ])
        expect("/docs/versions/1.0.39/", [
            "data-version-switcher", "source-only",
            'href="/docs/versions/1.0.39/versions/"',
        ])
        expect("/docs/versions/1.0.39/versions/", ["source only, no download"])
        expect("/docs/versions/3.0.0-alpha.1/", [
            "data-version-switcher", "preview",
            'href="/docs/versions/3.0.0-alpha.1/versions/"',
        ])
        expect("/docs/versions/1.0.40/", [
            "data-version-switcher", "stable",
            'href="/docs/versions/1.0.40/versions/"',
        ])
        expect("/docs/versions/1.0.40/getting-started/docker-compose/", [
            "data-version-switcher", "_astro/",
        ])
        # Negative cases: excluded tag and an unknown version have no route.
        for bad in ("/docs/versions/v3.0.0-beta.4/", "/docs/versions/1.0.38/",
                    "/docs/versions/9.9.9/"):
            try:
                urlopen(origin + bad, timeout=5)
                failures.append(f"{bad}: unsupported version route is reachable")
            except HTTPError as error:
                if error.code != 404:
                    failures.append(f"{bad}: expected 404, got {error.code}")
        # Sitemap and canonical URLs include every version home.
        sitemap = urlopen(origin + "/sitemap.xml", timeout=5).read().decode()
        for route in ("/docs/", "/docs/versions/1.0.40/",
                      "/docs/versions/1.0.39/", "/docs/versions/3.0.0-alpha.1/"):
            if f"{BASE}{route}" not in sitemap:
                failures.append(f"sitemap.xml: missing {route}")
        # Search scope: each version has its own generated index.
        for version in ("1.0.40", "1.0.39", "3.0.0-alpha.1"):
            index = site / "docs/versions" / version / "pagefind/pagefind-entry.json"
            if not index.is_file():
                failures.append(f"{version}: missing scoped pagefind index")
    finally:
        server.shutdown()
    if failures:
        raise ValueError("browser version check failed:\n- " + "\n- ".join(sorted(set(failures))))
    print("Browser checks passed: switcher, deep links, version bases, canonicals, "
          "sitemap, search scope and negative routes.")


if __name__ == "__main__":
    main()
