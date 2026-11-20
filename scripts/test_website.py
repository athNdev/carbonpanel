#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Integration checks for snapshot isolation and subpath publication."""
import hashlib
from html.parser import HTMLParser
import copy
import json
from pathlib import Path
from urllib.parse import urlsplit
import subprocess
import tempfile
import unittest
from scripts.build_website import (
    build_sitemap,
    export_snapshot,
    normalize_mdx,
    publish_design_system,
    publish_shared_index,
    render_astro_config,
    scoped_links,
)
from scripts import changelog_page
from scripts import version_contract as vc
from scripts.check_website import (
    Links, bases_from_index, check, check_channels, check_marker, check_version_ui,
)


class DesignPageParser(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = []
        self.fragments = []
        self.local_assets = []
        self.labels = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if "id" in attrs:
            self.ids.append(attrs["id"])
        if tag == "label" and attrs.get("for"):
            self.labels.append(attrs["for"])
        attr = "href" if tag in ("a", "link") else "src" if tag == "script" else None
        if not attr or not attrs.get(attr):
            return
        target = urlsplit(attrs[attr])
        if target.scheme or target.netloc:
            return
        if target.path:
            self.local_assets.append(target.path)
        elif target.fragment:
            self.fragments.append(target.fragment)


class WebsiteTests(unittest.TestCase):
    def test_snapshot_uses_committed_docs_and_examples_not_working_tree(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            def git(*args):
                return subprocess.check_output(["git", *args], cwd=root).decode().strip()
            git("init", "--quiet")
            for name in ("docs/carbon-panel/src/content/docs/intro.md",
                         "config.example.yaml", "docker-compose.yml", "buf.yaml", "buf.lock",
                         "proto/example.proto", "oidc/example/config.yml"):
                file = root / name
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text("pinned", encoding="utf-8")
            git("add", ".")
            git("-c", "user.name=Website Test", "-c", "user.email=test@example.com",
                "commit", "--quiet", "-m", "snapshot")
            revision = git("rev-parse", "HEAD")
            (root / "config.example.yaml").write_text("unreleased", encoding="utf-8")
            (root / "docs/carbon-panel/src/content/docs/intro.md").write_text("unreleased", encoding="utf-8")
            export = root / "export"
            export.mkdir()
            export_snapshot(root, revision, export)
            self.assertEqual((export / "config.example.yaml").read_text(), "pinned")
            self.assertEqual((export / "docs/carbon-panel/src/content/docs/intro.md").read_text(), "pinned")

    def test_nightly_candidate_sources_first_party_proxmox_page(self):
        # The Nightly candidate renders its Proxmox guide from the qualified
        # first-party revision and never at a canonical route.
        root = Path(__file__).resolve().parent.parent
        revision = "08a67f306c249a36dc742d7b166d13e2eb905d1f"  # origin/main: first-party Proxmox page
        probe = subprocess.run(["git", "cat-file", "-e", revision], cwd=root,
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if probe.returncode != 0:
            self.skipTest(f"qualified revision {revision} is not in this checkout")
        first_party_link = ("https://raw.githubusercontent.com/athNdev/carbonpanel/"
                            "main/scripts/proxmox/install-carbon-panel.sh")
        with tempfile.TemporaryDirectory() as temporary:
            export_snapshot(root, revision, Path(temporary))
            page = (Path(temporary)
                    / "docs/carbon-panel/src/content/docs/getting-started/proxmox.mdx")
            self.assertTrue(page.is_file())
            self.assertIn(first_party_link, page.read_text(encoding="utf-8"))
            self.assertEqual(vc.candidate_route(revision),
                             "/docs/candidate/" + revision + "/")

    def test_subpath_links_and_source_links(self):
        source = '[Config](/configuration/) <iframe src="/api-reference.html"></iframe> ' + \
                 '[External](https://example.com/) git clone https://example.com/repo ' + \
                 'https://github.com/athNdev/carbonpanel/blob/main/README.md'
        result = scoped_links(source, "/docs/", "a" * 40)
        self.assertIn("](/docs/configuration/)", result)
        self.assertIn('src="/docs/api-reference.html"', result)
        self.assertIn("[External](https://example.com/)", result)
        self.assertIn("git clone https://example.com/repo", result)
        self.assertIn("/blob/" + "a" * 40 + "/README.md", result)
        external = "https://github.com/another/project/blob/main/README.md"
        self.assertEqual(scoped_links(external, "/docs/", "a" * 40), external)

    def test_floating_ref_is_rejected_before_archive(self):
        with self.assertRaises(ValueError):
            export_snapshot(Path("."), "main", Path("."))

    def test_shared_index_is_published_at_root_and_docs(self):
        index = {"schema_version": 2, "stable": {"tag": "v1.0.40"}, "versions": []}
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            payload = publish_shared_index(output, index)
            root_index = output / "version-index.json"
            docs_index = output / "docs" / "version-index.json"
            self.assertEqual(root_index.read_text(encoding="utf-8"), payload)
            self.assertEqual(docs_index.read_text(encoding="utf-8"), payload)
            self.assertEqual(
                hashlib.sha256(root_index.read_bytes()).hexdigest(),
                hashlib.sha256(docs_index.read_bytes()).hexdigest())

    def test_hero_frontmatter_links_are_rebased(self):
        source = "---\nhero:\n  actions:\n      link: /introduction/\n      link: https://example.com/\n---\n"
        result = scoped_links(source, "/docs/versions/1.0.39/", "a" * 40)
        self.assertIn("link: /docs/versions/1.0.39/introduction/", result)
        self.assertIn("link: https://example.com/", result)

    def test_legacy_spdx_html_comment_compiles_as_mdx(self):
        source = "---\ntitle: x\n---\n<!--\nSPDX-License-Identifier: MIT\n-->\nBody"
        result = normalize_mdx(source)
        self.assertNotIn("<!--", result)
        self.assertIn("{/*", result)
        self.assertIn("Body", result)

    def test_sidebar_only_contains_pages_at_the_exact_commit(self):
        root = Path(__file__).resolve().parent.parent
        shell = (root / "docs/carbon-panel/astro.config.mjs").read_text(encoding="utf-8")
        config = render_astro_config(
            shell, ["", "introduction", "getting-started/docker-compose"], "/docs/")
        self.assertIn('slug: "introduction"', config)
        self.assertIn('slug: "getting-started/docker-compose"', config)
        self.assertNotIn("modpack-studio", config)
        self.assertIn("link: '../design-system/'", config)
        nested = render_astro_config(
            shell, ["", "introduction"], "/docs/versions/1.0.39/")
        self.assertIn("link: '../../../design-system/'", nested)


    def test_design_system_source_links_labels_and_fragments(self):
        page = Path(__file__).resolve().parent.parent / "docs/design-system/index.html"
        parser = DesignPageParser()
        parser.feed(page.read_text(encoding="utf-8"))
        self.assertEqual(len(parser.ids), len(set(parser.ids)), "Duplicate page IDs")
        for fragment in parser.fragments:
            self.assertIn(fragment, parser.ids, f"Missing section: {fragment}")
        for label in parser.labels:
            self.assertIn(label, parser.ids, f"Missing labelled control: {label}")
        for asset in parser.local_assets:
            self.assertTrue((page.parent / asset).is_file(), f"Missing local asset: {asset}")

    def test_design_system_is_published_with_current_canonical_tokens(self):
        root = Path(__file__).resolve().parent.parent
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            publish_design_system(root, output)
            design = output / "design-system"
            page = (design / "index.html").read_text(encoding="utf-8")
            self.assertIn('href="./tokens.css"', page)
            self.assertIn("github.com/athNdev/carbonpanel/blob/main/docs/design-system/README.md", page)
            self.assertNotIn("../../web/carbon-panel/", page)
            self.assertIn('property="og:image"', page)
            self.assertIn('content="https://carbonpanel.dev/assets/og-design-system.png?v=20261118"', page)
            self.assertEqual(
                (design / "tokens.css").read_bytes(),
                (root / "web/carbon-panel/src/lib/design-system/tokens.css").read_bytes())
            self.assertTrue((design / "site.css").is_file())
            self.assertTrue((design / "site.js").is_file())
            # Checkout links and font URLs must resolve within the same website,
            # including when it is published below a path such as /carbonpanel/.
            for href in ('../', '../docs/', '../docs/getting-started/docker-compose/'):
                self.assertIn(f'href="{href}"', page)
            self.assertIn('src="../assets/brand-horizontal-light.svg"', page)
            self.assertNotIn('../../landing/', page)
            self.assertNotIn('../carbon-panel/src/', page)
            css = (design / "site.css").read_text(encoding="utf-8")
            self.assertIn('../assets/fonts/inter-latin-wght-normal.woff2', css)
            self.assertNotIn('../../landing/', css)
            links = Links()
            links.feed((root / "landing/index.html").read_text(encoding="utf-8"))
            self.assertIn("./design-system/", links.urls)

    def test_not_found_ignores_only_its_synthetic_canonical(self):
        parser = Links(not_found=True)
        parser.feed('<link rel="canonical" href="/404/"><a href="/docs/">Docs</a>')
        self.assertEqual(parser.urls, ["/docs/"])

    def test_sitemap_includes_every_published_page_and_lastmod(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            (output / "index.html").write_text("home", encoding="utf-8")
            docs = output / "docs" / "guide"
            docs.mkdir(parents=True)
            (docs / "index.html").write_text("guide", encoding="utf-8")
            (output / "embed.html").write_text(
                '<meta name="robots" content="noindex, follow">', encoding="utf-8")
            (output / "404.html").write_text("not found", encoding="utf-8")

            build_sitemap(output, "/", "https://carbonpanel.dev", "2026-11-13")

            sitemap = (output / "sitemap.xml").read_text(encoding="utf-8")
            self.assertIn("<loc>https://carbonpanel.dev/</loc>", sitemap)
            self.assertIn("<loc>https://carbonpanel.dev/docs/guide/</loc>", sitemap)
            self.assertNotIn("404.html", sitemap)
            self.assertNotIn("embed.html", sitemap)
            self.assertEqual(sitemap.count("<lastmod>2026-11-13</lastmod>"), 2)

    def test_banner_dropdown_and_pinned_links_fail_closed(self):
        root = Path(__file__).resolve().parent.parent
        contract, catalog = vc.load(root)
        index = vc.version_index(contract, catalog, root)

        def banner(sha, pinned, channel, stable_href="/docs/"):
            entry = index["channels"][channel]
            return (
                '<html><head></head><body>'
                f'<div class="version-banner" data-channel-revision="{sha}" '
                f'data-channel-digest="{entry["image"]["digest"]}" '
                f'data-channel-promotion="{entry["promotion"]["state"]}" '
                f'data-channel-promoted="{str(entry["promotion"]["promoted"]).lower()}">'
                f'<a href="{pinned}">Pinned to {sha[:7]}</a>'
                '<label for="version-channel-select">Documentation version</label>'
                '<select id="version-channel-select" data-version-switcher '
                'aria-label="Documentation version" onchange="location.assign(this.value)">'
                f'<option value="{stable_href}">Stable</option>'
                '<option value="/docs/nightly/">Nightly</option>'
                '</select></div></body></html>')

        with tempfile.TemporaryDirectory() as temporary:
            site = Path(temporary)
            (site / "version-index.json").write_text(vc.render_index(index), encoding="utf-8")
            pages = {}
            for version in index["versions"]:
                target = site / version["route"].strip("/")
                target.mkdir(parents=True, exist_ok=True)
                pages[version["channel"]] = target / "index.html"
                pinned = f"{index['repository']}/commit/{version['commit']}"
                pages[version["channel"]].write_text(
                    banner(version["commit"], pinned, version["channel"]), encoding="utf-8")
                (target / "source.json").write_text(json.dumps({
                    "revision": version["commit"],
                    "source_commit": version["source_commit"],
                    "channel": version["channel"],
                    "image_digest": index["channels"][version["channel"]]["image"]["digest"],
                }), encoding="utf-8")
            bases = bases_from_index(site, "/")
            marker = vc.channel_marker(index)

            def validate_all():
                errors = []
                for page in pages.values():
                    check_version_ui(site, page, index, bases, errors, marker)
                return errors

            self.assertEqual(validate_all(), [])

            stable_page = pages["stable"]
            stable_sha = index["channels"]["stable"]["commit"]
            original = stable_page.read_text(encoding="utf-8")
            # Truncated SHA in the pinned URL is not an immutable link.
            stable_page.write_text(
                original.replace(f"/commit/{stable_sha}", f"/commit/{stable_sha[:7]}"),
                encoding="utf-8")
            self.assertTrue(any("non-canonical pinned commit URL" in item
                                for item in validate_all()))
            # Pinned links may never point at a generic versions index.
            stable_page.write_text(
                original.replace(f"/commit/{stable_sha}", "/docs/versions/"), encoding="utf-8")
            self.assertTrue(any("non-canonical pinned commit URL" in item
                                for item in validate_all()))
            # Broken switcher hrefs (double slash / wrong appended slug) fail.
            stable_page.write_text(
                original.replace('value="/docs/"', 'value="/docs/versions/1.0.40//"'),
                encoding="utf-8")
            self.assertTrue(any("not canonical" in item for item in validate_all()))
            # A legacy option label reintroduces a retired snapshot.
            stable_page.write_text(
                original.replace(">Stable</option>", ">1.0.39</option>"), encoding="utf-8")
            self.assertTrue(any("selector options must be Stable and Nightly" in item
                                for item in validate_all()))

    def test_public_channel_marker_rejects_stale_and_wrong_exact_input(self):
        root = Path(__file__).resolve().parent.parent
        contract, catalog = vc.load(root)
        index = vc.version_index(contract, catalog, root)
        with tempfile.TemporaryDirectory() as temporary:
            site = Path(temporary)
            marker = vc.channel_marker(index)
            (site / "channel-marker.json").write_text(
                json.dumps(marker, indent=2) + "\n", encoding="utf-8")
            self.assertEqual(check_marker(site, index), [])
            self.assertEqual(check_marker(site, index, {
                "revision": marker["channels"]["nightly"]["revision"],
                "digest": marker["channels"]["nightly"]["digest"],
                "state": "baseline",
            }), [])
            self.assertTrue(any("nightly marker digest is not the qualified" in item
                                for item in check_marker(site, index, {
                                    "revision": marker["channels"]["nightly"]["revision"],
                                    "digest": "sha256:" + "c" * 64})))
            stale = copy.deepcopy(marker)
            stale["channels"]["nightly"]["revision"] = "0" * 40
            (site / "channel-marker.json").write_text(
                json.dumps(stale, indent=2) + "\n", encoding="utf-8")
            self.assertTrue(any("marker revision disagrees" in item
                                for item in check_marker(site, index)))

    def test_promoted_build_requires_the_exact_external_input(self):
        root = Path(__file__).resolve().parent.parent
        contract, catalog = vc.load(root)
        revision = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
        digest = "sha256:" + "d" * 64
        expect = {"revision": revision, "digest": digest,
                  "state": "final", "receipt": "OSS-413-test-3"}
        promoted = vc.apply_promotion(contract, expect)
        index = vc.version_index(promoted, catalog, root)
        with tempfile.TemporaryDirectory() as temporary:
            site = Path(temporary)
            marker = vc.channel_marker(index)
            (site / "channel-marker.json").write_text(
                json.dumps(marker, indent=2) + "\n", encoding="utf-8")
            self.assertEqual(check_marker(site, index, expect), [])
            errors = []
            check_channels(index, errors, expect)
            self.assertEqual(errors, [])
            # Without the exact input, a promoted nightly is never accepted.
            errors = []
            check_channels(index, errors)
            self.assertTrue(any("stale nightly revision" in item for item in errors))
            self.assertTrue(any("promotion claim disagrees" in item for item in errors))
            self.assertTrue(any("qualif" in item for item in check_marker(site, index, {
                "revision": revision, "digest": "sha256:" + "e" * 64})))

    def test_a_page_without_social_metadata_fails_the_website_check(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "index.html").write_text(
                "<!doctype html><html lang=\"en\"><head><title>Bare</title></head>"
                "<body></body></html>", encoding="utf-8")
            with self.assertRaises(ValueError) as raised:
                check(root)
            self.assertIn("og:title", str(raised.exception))


class ChannelChangelogTests(unittest.TestCase):
    """Channel-correct changelog generated from the one version contract."""

    @classmethod
    def setUpClass(cls):
        cls.declaration = changelog_page.load_source()
        cls.notes = changelog_page.load_notes()
        contract, catalog = vc.load()
        cls.index = vc.version_index(
            vc.release_build_contract(vc.apply_promotion(contract, None)), catalog)

    def render(self, channel):
        current = next(v for v in self.index["versions"] if v["channel"] == channel)
        return changelog_page.render(self.index, current, self.declaration, self.notes)

    def test_one_h1_and_no_retired_or_collision_claims(self):
        for channel in ("stable", "nightly"):
            page = self.render(channel)
            self.assertIn("title: Changelog", page)
            self.assertFalse(page.startswith("# "))
            self.assertNotIn("\n# ", page)
            for stale in ("3.0.0-beta.4", "3.0.0-beta.3", "2.0.15", "1.0.38",
                          "working-tree changelog", "Versioned documentation"):
                self.assertNotIn(stale, page, channel)

    def test_stable_exact_identity_canonical_notes_and_upgrade_link(self):
        page = self.render("stable")
        stable = next(v for v in self.index["versions"] if v["channel"] == "stable")
        self.assertIn(stable["tag"], page)
        self.assertIn(stable["commit"][:12], page)
        self.assertIn("Canonical GitHub release notes:", page)
        self.assertIn(f"/releases/tag/{stable['tag']}", page)
        if stable["tag"] in self.declaration["upgrade_links"]:
            self.assertIn(self.declaration["upgrade_links"][stable["tag"]]["anchor"], page)
        else:
            self.assertNotIn("upgrade-notes-", page)
        self.assertIn("/docs/nightly/changelog/", page)

    def test_nightly_exact_revision_compare_and_stable_baseline(self):
        page = self.render("nightly")
        nightly = next(v for v in self.index["versions"] if v["channel"] == "nightly")
        stable = next(v for v in self.index["versions"] if v["channel"] == "stable")
        self.assertIn(nightly["commit"][:12], page)
        self.assertIn(f"/compare/{stable['commit']}...{nightly['commit']}", page)
        self.assertIn(stable["tag"], page)
        self.assertIn("Promotion state:", page)
        self.assertIn("/docs/changelog/", page)

    def test_exact_published_release_notes_and_tag_upgrade_link(self):
        index = copy.deepcopy(self.index)
        stable = index["versions"][0]
        stable["tag"], stable["display"] = "carbonpanel-v2.0.1", "2.0.1"
        page = changelog_page.render(index, stable, self.declaration, self.notes)
        self.assertIn("upgrade-notes-from-v1040", page)
        self.assertIn("/releases/tag/carbonpanel-v2.0.1", page)
        self.assertIn("## Downloads", page)
        self.assertIn("atharvnaphade/carbonpanel:2.0.1", page)

    def test_baseline_and_future_release_without_local_notes(self):
        """A tracked 1.0.40 Stable or a future tag must not abort the build."""
        index = copy.deepcopy(self.index)
        stable = index["versions"][0]
        stable["tag"], stable["display"] = "v1.0.40", "1.0.40"
        page = changelog_page.render(index, stable, self.declaration, {})
        self.assertIn("/releases/tag/v1.0.40", page)
        self.assertIn("No local attested copy", page)
        self.assertNotIn("upgrade-notes-from-v1040", page)
        self.assertFalse(page.startswith("# "))
        self.assertNotIn("\n# ", page)

    def test_presentation_fingerprint_includes_banner_source(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for rel in (changelog_page.SOURCE_PATH,
                        self.declaration["release_notes"]["carbonpanel-v2.0.1"]["path"],
                        changelog_page.BANNER_SOURCE):
                target = root / rel
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((Path(__file__).resolve().parent.parent / rel).read_bytes())
            first = changelog_page.presentation_fingerprint(root)["fingerprint"]
            banner = root / changelog_page.BANNER_SOURCE
            banner.write_text(banner.read_text(encoding="utf-8") + "\n", encoding="utf-8")
            second = changelog_page.presentation_fingerprint(root)["fingerprint"]
            self.assertNotEqual(first, second)

    def test_banner_shows_actual_release_and_matching_page_options(self):
        banner = (Path(__file__).resolve().parent.parent
                  / "docs/carbon-panel/src/components/VersionBanner.astro").read_text(encoding="utf-8")
        self.assertIn("current.display", banner)
        self.assertIn("current.tag", banner)
        self.assertIn("rolling ", banner)
        # Provenance markers and matching-page switcher are retained.
        self.assertIn("data-channel-revision={current.commit}", banner)
        self.assertIn("item.pages?.includes(slug)", banner)
        self.assertNotIn("current.channel + ' / ' + current.availability", banner)

    def test_builder_generates_and_excludes_changelog_from_rebasing(self):
        source = (Path(__file__).resolve().parent / "build_website.py").read_text(encoding="utf-8")
        self.assertIn("changelog_page.GENERATED_PAGE", source)
        self.assertIn("changelog_page.PINNED_PAGE", source)
        self.assertIn("changelog_page.presentation_fingerprint", source)
        self.assertIn('"changelog.mdx"', source)


if __name__ == "__main__":
    unittest.main()
