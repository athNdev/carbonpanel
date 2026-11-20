#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Real-browser regression for the generated channel changelog and switcher.

Drives the built multi-channel site with the repository-pinned Playwright helper
in scripts/manual-review/e2e. Checks exactly one H1, the actual release/version
banner label, canonical release-note links, absence of retired headings, and a
working deep-path channel switcher at desktop and mobile widths. No deploy,
registry or release write is performed.
"""
import argparse
import functools
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parent.parent
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

SITE = ROOT / ".website-build/site"
E2E = ROOT / "scripts" / "manual-review" / "e2e"
DOM_HELPER = E2E / "changelog-switcher-dom.mjs"
WIDTHS = (1440, 390)
STALE = ("3.0.0-beta.4", "3.0.0-beta.3", "2.0.15", "1.0.38", "Versioned documentation")


def collect_dom(origin, requests):
    """One bounded Playwright session for every route/viewport/navigation."""
    if not DOM_HELPER.is_file():
        raise RuntimeError(f"missing pinned browser helper: {DOM_HELPER}")
    with tempfile.TemporaryDirectory(prefix="carbon-changelog-dom-") as temporary:
        output = Path(temporary) / "dom.json"
        command = ["node", str(DOM_HELPER), "--origin", origin,
                   "--out", str(output), "--requests", json.dumps(requests)]
        result = subprocess.run(command, cwd=str(E2E), capture_output=True,
                                text=True, encoding="utf-8", errors="replace", timeout=300)
        if result.returncode != 0:
            detail = (result.stderr or result.stdout or "").strip()
            raise RuntimeError(f"changelog browser helper failed: {detail}")
        return json.loads(output.read_text(encoding="utf-8"))


def text_of(html):
    return re.sub(r"\s+", " ", re.sub(r"<[^>]+>", " ", html)).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("site", nargs="?", default=str(SITE))
    parser.add_argument("--base-url", default="",
                        help="verify the DEPLOYED public HTTPS site instead of a local artifact")
    args = parser.parse_args()

    live = bool(args.base_url)
    site = Path(args.site)
    if live:
        origin = args.base_url.rstrip("/")
        index = json.loads(urlopen(origin + "/version-index.json", timeout=60).read())
        server = None
    else:
        index_path = site / "version-index.json"
        if not index_path.is_file():
            raise RuntimeError(f"{site} is not a packaged multi-channel site")
        index = json.loads(index_path.read_text(encoding="utf-8"))
        class QuietHandler(SimpleHTTPRequestHandler):
            def log_message(self, *_ignored):
                pass
        handler = functools.partial(QuietHandler, directory=str(site))
        server = ThreadingHTTPServer(("127.0.0.1", 0), handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        origin = f"http://127.0.0.1:{server.server_port}"

    try:
        channels = index["channels"]
        stable = channels["stable"]
        nightly = channels["nightly"]
        archive = f"/docs/versions/{stable['version']}/changelog/"
        stable_route, nightly_route = "/docs/changelog/", "/docs/nightly/changelog/"
        counterpart = {stable_route: nightly_route, archive: nightly_route,
                       nightly_route: stable_route}
        stable_labels = (str(stable["version"]), str(stable["tag"]))
        nightly_labels = ("Nightly", "rolling " + nightly["commit"][:12])
        canonical = (index["repository"].rstrip("/")
                     + "/releases/tag/" + str(stable["tag"]))
        routes = (stable_route, nightly_route, archive)

        requests = []
        for width in WIDTHS:
            for path in routes:
                requests.append({"key": f"{path}|{width}", "path": path, "width": width,
                                 "switchTo": counterpart[path]})
        dom = collect_dom(origin, requests)

        failures = []
        for width in WIDTHS:
            for path in routes:
                key = f"{path}|{width}"
                entry = dom.get(key)
                if not isinstance(entry, dict):
                    failures.append(f"{key}: no DOM response")
                    continue
                if "error" in entry:
                    failures.append(f"{key}: {entry['error']}")
                    continue
                if entry.get("status") != 200:
                    failures.append(f"{key}: HTTP {entry.get('status')}")
                html = str(entry.get("html") or "")
                h1 = len(re.findall(r"<h1[\s>]", html))
                if h1 != 1:
                    failures.append(f"{key}: expected exactly one H1, found {h1}")
                body = text_of(html)
                labels = nightly_labels if path == nightly_route else stable_labels
                banner = text_of(str(entry.get("banner") or ""))
                if not banner:
                    failures.append(f"{key}: .version-banner text is empty")
                for label in labels:
                    if label not in banner:
                        failures.append(f"{key}: .version-banner is missing {label!r}")
                if "Canonical GitHub release notes" not in body:
                    failures.append(f"{key}: missing canonical release-note link")
                links = entry.get("links") or []
                if not any(isinstance(link, dict) and link.get("href") == canonical
                           and str(stable["tag"]) in str(link.get("text") or "")
                           for link in links):
                    failures.append(f"{key}: canonical release-note anchor href is missing "
                                    f"or wrong: {canonical}")
                for stale in STALE:
                    if stale in body:
                        failures.append(f"{key}: stale heading {stale!r}")
                if f'value="{counterpart[path]}"' not in html:
                    failures.append(f"{key}: switcher lacks counterpart option {counterpart[path]}")
                if entry.get("switchError"):
                    failures.append(f"{key}: {entry['switchError']}")
                navigated = str(entry.get("navigatedUrl") or "").rstrip("/") + "/"
                if navigated and navigated != origin + counterpart[path]:
                    failures.append(f"{key}: switcher navigated to {navigated}")

        if failures:
            print("FAIL:\n- " + "\n- ".join(failures), file=sys.stderr)
            return 1
        print(f"Changelog browser checks passed: {len(routes)} routes x {len(WIDTHS)} widths, "
              f"one H1, real labels, canonical links and deep switcher both directions.")
        return 0
    finally:
        if server is not None:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    raise SystemExit(main())
