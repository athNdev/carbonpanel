#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Headless-browser qualification for the two published documentation channels.

Drives a real Chromium against the packaged site: direct/deep links, the
accessible channel dropdown, per-channel base URLs, canonical URLs, immutable
full-SHA pins, sitemap, search scope and negative cases. A candidate build is
qualified only at its non-active `/docs/candidate/<sha>/` staging path; canonical
routes are checked only in a final/default artifact. Set CHROME_PATH to a
Chromium-family executable. No network, registry or site deployment is used.
"""
import argparse
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
    candidates = [os.environ.get("CHROME_PATH"), Path("/usr/bin/chromium"),
                  Path("/usr/bin/google-chrome")]
    for candidate in candidates:
        if candidate and Path(candidate).is_file():
            return str(candidate)
    for name in ("chromium", "google-chrome", "chrome"):
        found = shutil.which(name)
        if found:
            return found
    raise RuntimeError("Set CHROME_PATH to a Chromium-family executable.")


def main():
    global BASE
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("site", nargs="?", default=str(SITE))
    parser.add_argument("--base-url", default="",
                        help="verify the DEPLOYED public HTTPS site instead of a local artifact")
    args = parser.parse_args()
    site = Path(args.site)
    live = bool(args.base_url)
    if live:
        origin = args.base_url.rstrip("/")
        BASE = origin
        index = json.loads(urlopen(origin + "/version-index.json", timeout=60).read())
        marker_path = None
        marker = json.loads(urlopen(origin + "/channel-marker.json", timeout=60).read())
        server = None
    else:
        index_path = site / "version-index.json"
        if not index_path.is_file():
            raise RuntimeError(f"{site} is not a packaged multi-channel site")
        index = json.loads(index_path.read_text(encoding="utf-8"))
        marker_path = site / "channel-marker.json"
        if not marker_path.is_file():
            raise RuntimeError("packaged site has no public channel-marker.json")
        marker = json.loads(marker_path.read_text(encoding="utf-8"))
    if index.get("schema_version") != 3 or sorted(index.get("channels", {})) != ["nightly", "stable"]:
        raise RuntimeError("packaged site does not have the schema 3 Stable/Nightly channels")
    channels = index["channels"]
    stable_version = channels["stable"]["version"]
    stable_route = f"/docs/versions/{stable_version}/"
    active_routes = ("/docs/", "/docs/nightly/", stable_route)
    for key, wanted in (("commit", os.environ.get("CARBONPANEL_STABLE_REVISION")),
                        ("tag", os.environ.get("CARBONPANEL_STABLE_TAG"))):
        if wanted and channels["stable"].get(key) != wanted:
            raise RuntimeError(f"public Stable {key} differs from the release input")
    digest = os.environ.get("CARBONPANEL_STABLE_DIGEST")
    if digest and channels["stable"]["image"]["digest"] != digest:
        raise RuntimeError("public Stable image digest differs from the release input")
    if marker != vc.channel_marker(index):
        raise RuntimeError("public channel marker disagrees with the version index")
    nightly_marker = marker["channels"]["nightly"]

    class QuietHandler(SimpleHTTPRequestHandler):
        def log_message(self, *_args):
            pass

    if not live:
        handler = functools.partial(QuietHandler, directory=str(site))
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        origin = f"http://127.0.0.1:{server.server_port}"
    binary = chrome_path()
    failures = []

    def published(path):
        """Does the exact URL exist (local artifact file or live public route)?"""
        if not live:
            return (site / path.lstrip("/")).is_file()
        try:
            urlopen(origin + "/" + path.lstrip("/"), timeout=10)
            return True
        except HTTPError:
            return False


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

    def shared_banner(channel):
        entry = marker["channels"][channel]
        return [
            "data-version-switcher", 'aria-label="Documentation version"',
            '<label for="version-channel-select"',
            'onchange="location.assign(this.value)"',
            f"{index['repository']}/commit/{entry['revision']}",
            f'data-channel-revision="{entry["revision"]}"',
            f'data-channel-digest="{entry["digest"]}"',
            f'data-channel-promotion="{entry["state"]}"',
            f'value="{channels["stable"]["route"]}"',
            f'value="{channels["nightly"]["route"]}"',
            ">Stable</option>", ">Nightly</option>",
            f'<link rel="canonical" href="{BASE}{entry["route"]}">',
        ]

    def absent(path):
        # A directory listing is not a published route; check for the page file.
        if published(path.lstrip("/") + "/index.html"):
            failures.append(f"{path}: unexpected active route in this artifact")

    def sitemap_locations(text):
        import re
        return set(re.findall(r"<loc>([^<]+)</loc>", text))

    for key, wanted in (("revision", os.environ.get("CARBONPANEL_EXPECT_NIGHTLY_REVISION")),
                        ("digest", os.environ.get("CARBONPANEL_EXPECT_NIGHTLY_DIGEST")),
                        ("state", os.environ.get("CARBONPANEL_EXPECT_PROMOTION_STATE"))):
        if wanted and nightly_marker[key] != wanted:
            failures.append(f"public nightly marker {key} is not the qualified {wanted}")

    is_candidate = marker["promotion"]["state"] == "candidate"
    staging = nightly_marker["route"] if is_candidate else None
    if is_candidate and not staging.startswith("/docs/candidate/"):
        failures.append(f"candidate marker route {staging} is not a non-active staging path")

    try:
        if is_candidate:
            # Public candidate probe happens at the staging path only.
            for width in (375, 1440):
                expect(staging, shared_banner("nightly"), width)
            reject(staging, [f'/commit/{nightly_marker["revision"][:7]}"'] + list(RETIRED))
            served = urlopen(origin + staging + "channel-marker.json", timeout=5).read().decode()
            if json.loads(served) != marker:
                failures.append("public staging marker differs from the packaged marker")
            for route in active_routes:
                absent(route)
            locations = sitemap_locations(
                urlopen(origin + "/sitemap.xml", timeout=5).read().decode())
            if f"{BASE}{staging}" not in locations:
                failures.append(f"sitemap.xml: missing staging route {staging}")
            for route in active_routes:
                if f"{BASE}{route}" in locations:
                    failures.append(f"sitemap.xml: candidate published active route {route}")
            if not published(staging.lstrip("/") + "pagefind/pagefind-entry.json"):
                failures.append("candidate: missing scoped pagefind index")
        else:
            expect("/docs/", shared_banner("stable") + ["Stable", "Nightly"])
            reject("/docs/", [f'/commit/{channels["stable"]["commit"][:7]}"'] + list(RETIRED))
            for width in (375, 1440):
                expect("/docs/", shared_banner("stable"), width)
                expect("/docs/nightly/", shared_banner("nightly"), width)
            deep = expect("/docs/getting-started/docker-compose/", [
                'value="/docs/getting-started/docker-compose/"',
            ])
            for version in index["versions"]:
                slug = "getting-started/docker-compose"
                href = f"{version['route'].rstrip('/')}/{slug}/" if slug in version["pages"] \
                    else version["route"]
                if f'value="{href}"' not in deep:
                    failures.append(f"deep link: missing canonical option {href}")
            expect(stable_route, [
                "data-version-switcher",
                f"{index['repository']}/commit/{channels['stable']['commit']}",
                f'<link rel="canonical" href="{BASE}{stable_route}">',
            ])
            for page, sha in (("/docs/", channels["stable"]["commit"]),
                              ("/docs/nightly/", channels["nightly"]["commit"]),
                              (stable_route, channels["stable"]["commit"])):
                reject(page, [f'/commit/{sha[:7]}"'] + list(RETIRED))
            for bad in ("/docs/versions/1.0.39/", "/docs/versions/3.0.0-alpha.1/",
                        "/docs/versions/v3.0.0-beta.4/", "/docs/versions/1.0.38/",
                        "/docs/versions/9.9.9/"):
                absent(bad)
            locations = sitemap_locations(
                urlopen(origin + "/sitemap.xml", timeout=5).read().decode())
            for route in active_routes:
                if f"{BASE}{route}" not in locations:
                    failures.append(f"sitemap.xml: missing {route}")
            for retired in RETIRED:
                if f"{BASE}/docs/versions/{retired}/" in locations:
                    failures.append(f"sitemap.xml: retired route {retired} is published")
            for version in index["versions"]:
                if not published(version["route"].lstrip("/") + "pagefind/pagefind-entry.json"):
                    failures.append(f"{version['channel_display']}: missing scoped pagefind index")
    finally:
        if server:
            server.shutdown()

    if failures:
        raise ValueError("browser version check failed:\n- " + "\n- ".join(sorted(set(failures))))
    print("Browser checks passed: Stable/Nightly dropdown at narrow and desktop widths, "
          "deep links, canonicals, full-SHA pins, sitemap, search scope and retired-route 404s."
          + (" Candidate verified at the non-active staging path only." if is_candidate else ""))


if __name__ == "__main__":
    main()
