#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Headless-browser qualification for the two published documentation channels.

Drives a real Chromium against the packaged site: direct/deep links, the
accessible channel dropdown, per-channel base URLs, canonical URLs, immutable
full-SHA pins, sitemap, search scope and negative cases. Set CHROME_PATH to a
Chromium-family executable, or place one at a standard location. No network,
registry or site deployment is used.
"""
import functools
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import threading
from urllib.error import HTTPError
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))
from scripts import version_contract as vc  # noqa: E402

SITE = ROOT / ".website-build/site"
BASE = "https://carbonpanel.dev"
RETIRED = ("1.0.39", "3.0.0-alpha.1")


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
    index_path = site / "version-index.json"
    if not index_path.is_file():
        raise RuntimeError(f"{site} is not a packaged multi-channel site")
    index = json.loads(index_path.read_text(encoding="utf-8"))
    if index.get("schema_version") != 3 or sorted(index.get("channels", {})) != ["nightly", "stable"]:
        raise RuntimeError("packaged site does not have the schema 3 Stable/Nightly channels")
    channels = index["channels"]
    stable_sha = channels["stable"]["commit"]
    nightly_sha = channels["nightly"]["commit"]
    marker_path = site / "channel-marker.json"
    if not marker_path.is_file():
        raise RuntimeError("packaged site has no public channel-marker.json")
    marker = json.loads(marker_path.read_text(encoding="utf-8"))
    if marker != vc.channel_marker(index):
        raise RuntimeError("public channel marker disagrees with the version index")

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

    def dom(path, width=1440):
        for attempt in range(3):
            profile = Path(os.environ.get("TEMP", "/tmp")) / \
                f"cp-version-dom-{abs(hash((path, width)))}-{attempt}"
            try:
                result = subprocess.run(
                    [binary, "--headless", "--disable-gpu", "--no-sandbox",
                     "--disable-dev-shm-usage", "--disable-javascript",
                     "--no-first-run", "--no-default-browser-check",
                     f"--window-size={width},900", "--virtual-time-budget=500",
                     "--user-data-dir=" + str(profile),
                     "--dump-dom", origin + path], capture_output=True, text=True,
                    encoding="utf-8", errors="replace", timeout=30)
            except subprocess.TimeoutExpired:
                continue
            if result.stdout:
                return result.stdout
        failures.append(f"{path} ({width}px): browser did not return DOM")
        return ""

    def expect(path, needles, width=1440):
        page = dom(path, width)
        for needle in needles:
            if needle not in page:
                failures.append(f"{path} ({width}px): missing {needle!r}")
        return page

    def reject(path, needles, width=1440):
        page = dom(path, width)
        for needle in needles:
            if needle in page:
                failures.append(f"{path} ({width}px): unexpected {needle!r}")

    def shared_banner(channel, route=None):
        entry = marker["channels"][channel]
        route = route or entry["route"]
        return [
            "data-version-switcher", 'aria-label="Documentation version"',
            '<label for="version-channel-select"',
            'onchange="location.assign(this.value)"',
            f"{index['repository']}/commit/{entry['revision']}",
            f'data-channel-revision="{entry["revision"]}"',
            f'data-channel-digest="{entry["digest"]}"',
            f'data-channel-promotion="{entry["state"]}"',
            'value="/docs/"', 'value="/docs/nightly/"',
            ">Stable</option>", ">Nightly</option>",
            f'<link rel="canonical" href="{BASE}{route}">',
        ]

    expect_revision = os.environ.get("CARBONPANEL_EXPECT_NIGHTLY_REVISION")
    expect_digest = os.environ.get("CARBONPANEL_EXPECT_NIGHTLY_DIGEST")
    expect_state = os.environ.get("CARBONPANEL_EXPECT_PROMOTION_STATE")
    nightly_marker = marker["channels"]["nightly"]
    for key, wanted in (("revision", expect_revision), ("digest", expect_digest),
                        ("state", expect_state)):
        if wanted and nightly_marker[key] != wanted:
            failures.append(f"public nightly marker {key} is not the qualified {wanted}")

    try:
        home = expect("/docs/", shared_banner("stable") + [
            "Stable", "Nightly",
        ])
        reject("/docs/", [f'/commit/{stable_sha[:7]}"'] + list(RETIRED))
        for width in (375, 1440):
            expect("/docs/", shared_banner("stable"), width)
            expect("/docs/nightly/", shared_banner("nightly"), width)
        # Deep links are carried only when the target channel has that page.
        deep = expect("/docs/getting-started/docker-compose/", [
            'value="/docs/getting-started/docker-compose/"',
        ])
        for version in index["versions"]:
            slug = "getting-started/docker-compose"
            href = f"{version['route'].rstrip('/')}/{slug}/" if slug in version["pages"] \
                else version["route"]
            if f'value="{href}"' not in deep:
                failures.append(f"deep link: missing canonical option {href}")
        # Both immutable routes carry full-SHA pins, never a docs index URL.
        expect("/docs/versions/1.0.40/", [
            "data-version-switcher", f"{index['repository']}/commit/{stable_sha}",
            '<link rel="canonical" href="https://carbonpanel.dev/docs/versions/1.0.40/">',
        ])
        expect("/docs/nightly/", shared_banner("nightly"))
        for page, sha in (("/docs/", stable_sha), ("/docs/nightly/", nightly_sha),
                          ("/docs/versions/1.0.40/", stable_sha)):
            reject(page, [f'/commit/{sha[:7]}"'] + list(RETIRED))
        # Negative cases: retired doc snapshots and unsupported labels have no route.
        for bad in ("/docs/versions/1.0.39/", "/docs/versions/3.0.0-alpha.1/",
                    "/docs/versions/v3.0.0-beta.4/", "/docs/versions/1.0.38/",
                    "/docs/versions/9.9.9/"):
            try:
                urlopen(origin + bad, timeout=5)
                failures.append(f"{bad}: retired/unsupported version route is reachable")
            except HTTPError as error:
                if error.code != 404:
                    failures.append(f"{bad}: expected 404, got {error.code}")
        sitemap = urlopen(origin + "/sitemap.xml", timeout=5).read().decode()
        for route in ("/docs/", "/docs/versions/1.0.40/", "/docs/nightly/"):
            if f"{BASE}{route}" not in sitemap:
                failures.append(f"sitemap.xml: missing {route}")
        for retired in RETIRED:
            if f"/docs/versions/{retired}/" in sitemap:
                failures.append(f"sitemap.xml: retired route {retired} is published")
        for version in index["versions"]:
            route = version["route"].lstrip("/")
            pagefind = site / route / "pagefind/pagefind-entry.json"
            if not pagefind.is_file():
                failures.append(f"{version['channel_display']}: missing scoped pagefind index")
    finally:
        server.shutdown()
    if failures:
        raise ValueError("browser version check failed:\n- " + "\n- ".join(sorted(set(failures))))
    print("Browser checks passed: Stable/Nightly dropdown at narrow and desktop widths, "
          "deep links, canonicals, full-SHA pins, sitemap, search scope and retired-route 404s.")


if __name__ == "__main__":
    main()
