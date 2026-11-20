#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Offline fixtures for the deterministic Docker Hub description view."""
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from unittest.mock import patch

from scripts import dockerhub_description as description

ROOT = Path(__file__).resolve().parent.parent
CONFIG = ROOT / "docs" / "dockerhub-description.json"
README = ROOT / "README.md"
VERSION_INDEX = ROOT / "docs" / "version-index.json"
WORKFLOW = ROOT / ".github" / "workflows" / "dockerhub-description.yml"


def generate_to(path, config=None, readme=None, index=None, live_index=None, check=True):
    command = [sys.executable, str(ROOT / "scripts" / "dockerhub_description.py"), "generate",
               "--config", str(config or CONFIG), "--readme", str(readme or README),
               "--output", str(path)]
    if index:
        command += ["--version-index", str(index)]
    if live_index:
        command += ["--live-index", str(live_index)]
    else:
        command += ["--skip-live-index"]
    return subprocess.run(command, cwd=str(ROOT), capture_output=True, text=True, check=check)


def promoted_index(tag="carbonpanel-v2.0.1", version="2.0.1"):
    index = json.loads(VERSION_INDEX.read_text(encoding="utf-8"))
    commit, digest = "1" * 40, "sha256:" + "2" * 64
    for stable in (index["stable"], index["channels"]["stable"]):
        stable.update({"tag": tag, "version": version, "commit": commit, "digest": digest})
    image = index["channels"]["stable"]["image"]
    image.update({"digest": digest, "revision": commit, "tag": "latest",
                  "docker_hub": "atharvnaphade/carbonpanel:latest"})
    return index


class DockerHubDescriptionTests(unittest.TestCase):
    def test_generation_is_deterministic_and_within_the_docker_hub_limit(self):
        with tempfile.TemporaryDirectory() as folder:
            first = Path(folder) / "first.md"
            second = Path(folder) / "second.md"
            first_run = generate_to(first)
            second_run = generate_to(second)
            self.assertEqual(first.read_text(encoding="utf-8"),
                             second.read_text(encoding="utf-8"))
            summary = json.loads(first_run.stdout)
            second_summary = json.loads(second_run.stdout)
            summary.pop("output")
            second_summary.pop("output")
            self.assertEqual(second_summary, summary)
            self.assertLessEqual(summary["bytes"], 25000)
            self.assertGreater(summary["bytes"], 10000)
            self.assertEqual(summary["bytes"], len(first.read_bytes()))
            self.assertEqual(summary["sha256"], description.body_sha256(
                first.read_text(encoding="utf-8")))

    def test_stable_identity_is_resolved_from_the_published_version_contract(self):
        config = json.loads(CONFIG.read_text(encoding="utf-8"))
        self.assertNotIn("stable_version", config)
        self.assertNotIn("stable_commit", config)
        stable = description.resolve_stable(VERSION_INDEX)
        self.assertEqual(stable["version"], "1.0.40")
        self.assertEqual(stable["tag"], "v1.0.40")
        self.assertEqual(stable["commit"], "f88748be6a216b187e675bbb1f110df07e544148")
        self.assertEqual(stable["image_tag"], "latest")
        with tempfile.TemporaryDirectory() as folder:
            body = Path(folder) / "body.md"
            summary = json.loads(generate_to(body).stdout)
            self.assertEqual(summary["stable_version"], stable["version"])
            self.assertEqual(summary["stable_commit"], stable["commit"])
            text = body.read_text(encoding="utf-8")
            self.assertIn("`1.0.40` (`v1.0.40`", text)
            self.assertIn("docker pull atharvnaphade/carbonpanel:1.0.40", text)

    def test_generated_body_has_absolute_links_and_both_channel_explanations(self):
        config = json.loads(CONFIG.read_text(encoding="utf-8"))
        self.assertIn("Release channels", config["sections"])
        self.assertIn("## Release channels", README.read_text(encoding="utf-8"))
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / "body.md"
            generate_to(target)
            body = target.read_text(encoding="utf-8")
            self.assertIn("https://github.com/athNdev/carbonpanel/blob/main/README.md", body)
            self.assertIn("https://github.com/athNdev/carbonpanel/tree/showcase-assets/assets/showcase", body)
            self.assertIn("https://carbonpanel.dev/docs/", body)
            self.assertIn("https://carbonpanel.dev/docs/nightly/", body)
            self.assertIn("https://github.com/athNdev/carbonpanel/releases", body)
            self.assertIn("https://github.com/athNdev/carbonpanel/blob/main/docs/carbon-panel/src/content/docs/guides/mcp-server.mdx", body)
            self.assertIn("https://hub.docker.com/r/atharvnaphade/carbonpanel", body)
            self.assertIn("https://github.com/athNdev/carbonpanel/blob/main/docs/releases.md", body)
            self.assertIn("## Release channels", body)
            self.assertIn("`:latest` follows tested releases", body)
            self.assertIn("`:nightly` follows qualified main builds as a preview", body)
            self.assertIn("contain changes ahead of Stable", body)
            self.assertNotIn("there is no `:stable` tag", body)
            self.assertNotIn("## AI assistant integration (MCP)", body)
            self.assertNotIn("The verified stable is", body)
            relative = [link for link in re.findall(r"\]\(([^)]+)\)", body)
                        if not link.startswith(("http://", "https://", "#", "mailto:"))]
            self.assertEqual(relative, [])

    def test_readme_has_no_hardcoded_stable_version_and_hub_section_matches_it(self):
        readme = README.read_text(encoding="utf-8")
        self.assertNotIn("The verified stable is", readme)
        self.assertNotIn("development snapshot", readme)
        self.assertNotIn("1.0.40", readme)
        self.assertIn("Prereleases never move `latest` or a minor alias", readme)
        with tempfile.TemporaryDirectory() as folder:
            target = Path(folder) / "body.md"
            generate_to(target)
            readme_section = description.split_sections(readme)["Release channels"]
            hub_section = description.split_sections(
                target.read_text(encoding="utf-8"))["Release channels"]
            self.assertEqual(hub_section, description.absolute_links(
                readme_section, "athNdev/carbonpanel", "main"))

    def test_channel_links_are_derived_from_the_shared_readme_section(self):
        readme = README.read_text(encoding="utf-8")
        with tempfile.TemporaryDirectory() as folder:
            custom = Path(folder) / "README.md"
            custom.write_text(readme.replace("https://carbonpanel.dev/docs/nightly/",
                                             "https://carbonpanel.dev/docs/preview/"),
                              encoding="utf-8")
            body = Path(folder) / "body.md"
            generate_to(body, readme=custom)
            text = body.read_text(encoding="utf-8")
            self.assertIn("https://carbonpanel.dev/docs/preview/", text)
            self.assertNotIn("https://carbonpanel.dev/docs/nightly/", text)

            without_section = Path(folder) / "no-channels.md"
            without_section.write_text(
                readme.split("## Release channels")[0] + "## Quick start\n\nInstall.\n",
                encoding="utf-8")
            result = generate_to(body, readme=without_section, check=False)
            self.assertEqual(result.returncode, 2)
            self.assertIn("Release channels", result.stderr)

    def test_relative_links_become_absolute_and_images_use_raw_host(self):
        text = """\
[docs](docs/releases.md#upgrade)
![shot](assets/shot.png)
[license](./LICENSE.MPL-2.0)
[external](https://example.org/a)
[anchor](#quick-start)
```
[kept](docs/not-converted.md)
```
"""
        result = description.absolute_links(text, "athNdev/carbonpanel", "main")
        self.assertIn("[docs](https://github.com/athNdev/carbonpanel/blob/main/docs/releases.md#upgrade)", result)
        self.assertIn("![shot](https://raw.githubusercontent.com/athNdev/carbonpanel/main/assets/shot.png)", result)
        self.assertIn("[license](https://github.com/athNdev/carbonpanel/blob/main/LICENSE.MPL-2.0)", result)
        self.assertIn("[external](https://example.org/a)", result)
        self.assertIn("[anchor](#quick-start)", result)
        self.assertIn("[kept](docs/not-converted.md)", result)

    def test_missing_readme_section_fails_actionably(self):
        config = json.loads(CONFIG.read_text(encoding="utf-8"))
        config["sections"] = ["Section that does not exist"]
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "config.json"
            path.write_text(json.dumps(config), encoding="utf-8")
            with self.assertRaises(description.DescriptionError) as raised:
                description.build_body(description.load_config(path),
                                       README.read_text(encoding="utf-8"),
                                       description.resolve_stable(VERSION_INDEX))
            self.assertIn("Section that does not exist", str(raised.exception))
            self.assertIn("docs/dockerhub-description.json", str(raised.exception))

    def test_oversize_body_is_refused_before_publication(self):
        config = json.loads(CONFIG.read_text(encoding="utf-8"))
        config["max_bytes"] = 10
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "config.json"
            path.write_text(json.dumps(config), encoding="utf-8")
            result = generate_to(Path(folder) / "body.md", config=path, check=False)
            self.assertEqual(result.returncode, 2)
            self.assertIn("over the 10-byte limit", result.stderr)

    def test_config_validation_refuses_bad_identity_and_limit(self):
        original = json.loads(CONFIG.read_text(encoding="utf-8"))
        bad = [
            {**original, "repository": "bad repo/name"},
            {**original, "mcp_guide_url": "/relative/guide"},
            {**original, "max_bytes": 25001},
            {**original, "short_description": "x" * 101},
            {**original, "sections": []},
        ]
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "config.json"
            for item in bad:
                with self.subTest(item=item):
                    path.write_text(json.dumps(item), encoding="utf-8")
                    with self.assertRaises(description.DescriptionError):
                        description.load_config(path)

    def test_unpublished_or_incomplete_contract_is_refused(self):
        original = json.loads(VERSION_INDEX.read_text(encoding="utf-8"))
        drafts = []
        candidate = json.loads(json.dumps(original))
        candidate["channels"]["stable"]["promotion"] = {"state": "candidate", "promoted": True}
        drafts.append(candidate)
        incomplete = json.loads(json.dumps(original))
        incomplete["stable"]["commit"] = ""
        drafts.append(incomplete)
        disagree = json.loads(json.dumps(original))
        disagree["stable"]["tag"] = "v9.9.9"
        drafts.append(disagree)
        with tempfile.TemporaryDirectory() as folder:
            path = Path(folder) / "index.json"
            for index in drafts:
                with self.subTest(index=index["stable"]["tag"]):
                    path.write_text(json.dumps(index), encoding="utf-8")
                    with self.assertRaises(description.DescriptionError):
                        description.resolve_stable(path)

    def test_subsequent_stable_promotion_updates_body_without_config_edit(self):
        promoted = promoted_index()
        with tempfile.TemporaryDirectory() as folder:
            index = Path(folder) / "version-index.json"
            index.write_text(json.dumps(promoted), encoding="utf-8")
            body = Path(folder) / "body.md"
            summary = json.loads(generate_to(body, index=index).stdout)
            text = body.read_text(encoding="utf-8")
            self.assertEqual(summary["stable_version"], "2.0.1")
            self.assertEqual(summary["stable_commit"], "1" * 40)
            self.assertIn("`2.0.1` (`carbonpanel-v2.0.1`", text)
            self.assertIn("docker pull atharvnaphade/carbonpanel:2.0.1", text)
            self.assertIn("sha256:" + "2" * 12, text)
            self.assertNotIn("`1.0.40`", text)
            self.assertNotIn("The verified stable is", text)
            self.assertLessEqual(summary["bytes"], 25000)

    def test_live_published_stable_wins_over_lagging_checked_in_baseline(self):
        promoted = promoted_index()
        with tempfile.TemporaryDirectory() as folder:
            live = Path(folder) / "live.json"
            live.write_text(json.dumps(promoted), encoding="utf-8")
            body = Path(folder) / "body.md"
            summary = json.loads(generate_to(body, live_index=live).stdout)
            text = body.read_text(encoding="utf-8")
            self.assertEqual(summary["stable_source"], "live published version index")
            self.assertEqual(summary["stable_version"], "2.0.1")
            self.assertIn("`2.0.1` (`carbonpanel-v2.0.1`", text)
            self.assertNotIn("`1.0.40`", text)
            # A lagging live index must not silently move the body backwards.
            stale = Path(folder) / "stale.json"
            stale.write_text(json.dumps(json.loads(VERSION_INDEX.read_text(
                encoding="utf-8"))), encoding="utf-8")
            promoted_baseline = Path(folder) / "promoted-baseline.json"
            promoted_baseline.write_text(json.dumps(promoted), encoding="utf-8")
            result = generate_to(body, index=promoted_baseline,
                                 live_index=stale, check=False)
            self.assertEqual(result.returncode, 2)
            self.assertIn("pre-promotion identity", result.stderr)

    def test_live_index_outage_after_promotion_fails_closed(self):
        promoted = promoted_index()
        with tempfile.TemporaryDirectory() as folder:
            baseline = Path(folder) / "baseline.json"
            baseline.write_text(json.dumps(promoted), encoding="utf-8")
            output = Path(folder) / "body.md"
            arguments = description.build_parser().parse_args([
                "generate", "--config", str(CONFIG), "--readme", str(README),
                "--version-index", str(baseline), "--output", str(output)])
            with patch.object(description, "fetch_live_index",
                              side_effect=description.DescriptionError("HTTP 503")):
                with self.assertRaises(description.DescriptionError) as raised:
                    description.command_generate(arguments)
            self.assertIn("HTTP 503", str(raised.exception))
            self.assertFalse(output.exists())

    def test_offline_fixture_mode_is_explicit(self):
        with tempfile.TemporaryDirectory() as folder:
            output = Path(folder) / "body.md"
            summary = json.loads(generate_to(output).stdout)
            self.assertEqual(summary["stable_source"], "checked-in version index")
            self.assertIn("`1.0.40`", output.read_text(encoding="utf-8"))

    def test_legacy_and_product_tags_share_release_contract_parsing(self):
        baseline = description.resolve_stable(VERSION_INDEX)
        self.assertEqual(baseline["tag"], "v1.0.40")
        self.assertEqual(baseline["version"], "1.0.40")
        promoted = description.stable_identity(promoted_index(), "live")
        self.assertEqual(promoted["tag"], "carbonpanel-v2.0.1")
        self.assertEqual(promoted["version"], "2.0.1")
        self.assertGreater(description.semver_key(promoted["version"]),
                           description.semver_key(baseline["version"]))
        with self.assertRaises(description.DescriptionError):
            description.stable_identity({"stable": {"tag": "v9.9.9"}}, "bad")

    def test_resolve_main_from_empty_working_directory_without_git(self):
        import urllib.error
        class Response(io.BytesIO):
            def __enter__(self):
                return self
            def __exit__(self, *args):
                return False
        def opener(url, timeout=None):
            self.assertIn("api.github.com/repos/athNdev/carbonpanel/git/ref/heads/main", url)
            return Response(json.dumps({"object": {"sha": "a" * 40}}).encode("utf-8"))
        previous = Path.cwd()
        with tempfile.TemporaryDirectory() as folder:
            empty = Path(folder)
            self.assertEqual(list(empty.iterdir()), [])
            try:
                os.chdir(empty)
                self.assertEqual(description.resolve_main_sha(
                    "athNdev/carbonpanel", "schedule", "", opener=opener), "a" * 40)
                self.assertEqual(description.resolve_main_sha(
                    "athNdev/carbonpanel", "workflow_run", "b" * 40, opener=opener), "a" * 40)
                self.assertEqual(description.resolve_main_sha(
                    "athNdev/carbonpanel", "push", "c" * 40, opener=opener), "c" * 40)
            finally:
                os.chdir(previous)

    def test_read_back_tolerates_bounded_propagation(self):
        body = json.loads((ROOT / "docs" / "version-index.json").read_text(encoding="utf-8"))
        expected = "# derived body\n"
        responses = [
            {"full_description": "stale", "description": "old"},
            {"full_description": expected, "description": "new"},
        ]
        waits = []
        def opener(url, timeout=None):
            return io.BytesIO(json.dumps(responses.pop(0)).encode("utf-8"))
        data, matches, explanation, count = description.read_back_with_retry(
            expected, "atharvnaphade/carbonpanel", opener=opener,
            attempts=3, delay=0, sleep=waits.append)
        self.assertTrue(matches)
        self.assertEqual(count, 1)
        self.assertEqual(waits, [0])
        self.assertEqual(data["description"], "new")

    def test_main_advance_guard_refuses_stale_publication(self):
        self.assertEqual(description.command_assert_head(
            description.build_parser().parse_args(
                ["assert-head", "--expected", "a" * 40, "--observed", "a" * 40])), 0)
        with self.assertRaises(description.DescriptionError) as raised:
            description.require_same_commit("a" * 40, "b" * 40, "scheduled sync")
        self.assertIn("main advanced", str(raised.exception))
        self.assertIn("refusing to publish", str(raised.exception))

    def test_check_and_verify_with_matching_and_stale_live_bodies(self):
        with tempfile.TemporaryDirectory() as folder:
            body = Path(folder) / "body.md"
            summary = json.loads(generate_to(body).stdout)
            config = json.loads(CONFIG.read_text(encoding="utf-8"))
            matching = Path(folder) / "matching.json"
            matching.write_text(json.dumps({
                "full_description": body.read_text(encoding="utf-8"),
                "description": config["short_description"],
            }), encoding="utf-8")
            stale = Path(folder) / "stale.json"
            stale.write_text(json.dumps({
                "full_description": "old body without Stable or MCP\n",
                "description": "a different older tagline",
            }), encoding="utf-8")

            outputs = Path(folder) / "outputs"
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(outputs)}):
                code = description.command_check(description.build_parser().parse_args(
                    ["check", "--expected", str(body), "--live-file", str(matching)]))
            self.assertEqual(code, 0)
            written = outputs.read_text(encoding="utf-8")
            self.assertIn("changed=false", written)
            self.assertIn("matches=true", written)
            self.assertIn("expected_sha256=" + summary["sha256"], written)
            self.assertIn("short_description_ok=true", written)

            outputs.unlink()
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(outputs)}):
                description.command_check(description.build_parser().parse_args(
                    ["check", "--expected", str(body), "--live-file", str(stale),
                     "--force"]))
            written = outputs.read_text(encoding="utf-8")
            self.assertIn("changed=true", written)
            self.assertIn("matches=false", written)
            self.assertIn("short_description_ok=false", written)

            self.assertEqual(description.command_verify(description.build_parser().parse_args(
                ["verify", "--expected", str(body), "--live-file", str(matching),
                 "--check-short-description"])), 0)
            buffer = io.StringIO()
            with redirect_stdout(buffer):
                code = description.command_verify(description.build_parser().parse_args(
                    ["verify", "--expected", str(body), "--live-file", str(stale)]))
            self.assertEqual(code, 1)
            self.assertIn("drifted", buffer.getvalue())

    def test_short_description_only_drift_triggers_a_repair_write(self):
        with tempfile.TemporaryDirectory() as folder:
            body = Path(folder) / "body.md"
            generate_to(body)
            live = Path(folder) / "hub.json"
            live.write_text(json.dumps({
                "full_description": body.read_text(encoding="utf-8"),
                "description": "an older tagline",
            }), encoding="utf-8")
            outputs = Path(folder) / "outputs"
            with patch.dict(os.environ, {"GITHUB_OUTPUT": str(outputs)}):
                description.command_check(description.build_parser().parse_args(
                    ["check", "--expected", str(body), "--live-file", str(live)]))
            written = outputs.read_text(encoding="utf-8")
            self.assertIn("matches=true", written)
            self.assertIn("short_description_ok=false", written)
            self.assertIn("changed=true", written)

    def test_verification_uses_the_real_docker_hub_description_field(self):
        with tempfile.TemporaryDirectory() as folder:
            body = Path(folder) / "body.md"
            generate_to(body)
            config = json.loads(CONFIG.read_text(encoding="utf-8"))
            live = Path(folder) / "hub.json"
            live.write_text(json.dumps({
                "full_description": body.read_text(encoding="utf-8"),
                "description": config["short_description"],
            }), encoding="utf-8")
            self.assertEqual(description.command_verify(description.build_parser().parse_args(
                ["verify", "--expected", str(body), "--live-file", str(live),
                 "--check-short-description"])), 0)
            self.assertEqual(description.published_short_description(
                {"description": "tagline"}), "tagline")
            self.assertEqual(description.published_short_description(
                {"short_description": "legacy fixture"}), "legacy fixture")

    def test_credentials_preflight_fails_closed_without_printing_values(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(description.DescriptionError) as raised:
                description.require_credentials()
            self.assertIn("DOCKER_HUB_USERNAME", str(raised.exception))
            self.assertIn("DOCKER_HUB_PASSWORD", str(raised.exception))
        with patch.dict(os.environ, {
                "DOCKER_HUB_USERNAME": "sensitive-user",
                "DOCKER_HUB_TOKEN": "sensitive-token"}):
            self.assertEqual(description.command_require_credentials(
                description.build_parser().parse_args(["require-credentials"])), 0)

    def test_fetch_reports_public_api_errors_without_credentials(self):
        import urllib.error
        def failing(url, timeout=None):
            raise urllib.error.HTTPError(url, 404, "Not Found", {}, None)
        with self.assertRaises(description.DescriptionError) as raised:
            description.fetch_repository("bad/name", opener=failing)
        self.assertIn("HTTP 404", str(raised.exception))


class DockerHubDescriptionWorkflowTests(unittest.TestCase):
    def test_dedicated_workflow_is_least_privileged_and_reads_back(self):
        import yaml
        document = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
        triggers = document.get("on") or document.get(True)
        self.assertEqual(triggers["push"]["branches"], ["main"])
        self.assertIn("README.md", triggers["push"]["paths"])
        self.assertIn("docs/version-index.json", triggers["push"]["paths"])
        self.assertIn("docs/dockerhub-description.json", triggers["push"]["paths"])
        self.assertEqual(len(triggers["schedule"]), 1)
        self.assertIn("workflow_dispatch", triggers)
        self.assertFalse(triggers["workflow_dispatch"]["inputs"]["force"]["default"])
        self.assertEqual(triggers["workflow_run"]["workflows"], ["Carbon Panel Release"])
        self.assertEqual(triggers["workflow_run"]["types"], ["completed"])
        self.assertNotIn("release", triggers)
        self.assertNotIn("pull_request", triggers)
        self.assertNotIn("pull_request_target", triggers)
        self.assertEqual(document["permissions"], {"contents": "read"})
        self.assertFalse(document["concurrency"]["cancel-in-progress"])
        job = document["jobs"]["update"]
        self.assertIn("workflow_run.conclusion == 'success'", job["if"])
        self.assertIn("workflow_run.head_branch == 'main'", job["if"])
        source = WORKFLOW.read_text(encoding="utf-8")
        self.assertNotIn("git ls-remote", source)
        self.assertIn("resolve-main", source)
        steps = job["steps"]
        names = [step.get("name", "") for step in steps]
        scripts_checkout = next(i for i, name in enumerate(names)
                                if name.startswith("Checkout workflow scripts"))
        target = next(i for i, name in enumerate(names) if name.startswith("Resolve target main"))
        checkout = next(i for i, name in enumerate(names) if name.startswith("Checkout the target"))
        generate = next(i for i, name in enumerate(names) if name.startswith("Generate"))
        drift = next(i for i, name in enumerate(names) if name.startswith("Compare the public body"))
        race = next(i for i, name in enumerate(names) if name.startswith("Guard against promotion race"))
        creds = next(i for i, name in enumerate(names) if name.startswith("Preflight Docker Hub write"))
        publish = next(i for i, name in enumerate(names) if name.startswith("Publish derived body"))
        verify = next(i for i, name in enumerate(names) if name.startswith("Verify public read-back"))
        self.assertLess(scripts_checkout, target)
        self.assertLess(target, checkout)
        self.assertLess(checkout, generate)
        self.assertLess(generate, drift)
        self.assertLess(drift, race)
        self.assertLess(race, creds)
        self.assertLess(creds, publish)
        self.assertLess(publish, verify)
        self.assertIn("steps.drift.outputs.changed == 'true'", steps[publish]["if"])
        self.assertIn("readme-filepath:", source)
        self.assertIn("enable-url-completion: false", source)
        self.assertIn("peter-evans/dockerhub-description@", source)
        self.assertIn("assert-head", source)
        self.assertIn("--repository \"${{ github.repository }}\"", source)
        self.assertIn("dockerhub_description.py verify", source)
        self.assertIn("--check-short-description", source)

    def test_cd_and_release_no_longer_overwrite_the_description(self):
        for name in ("cd.yml", "release.yml"):
            source = (ROOT / ".github" / "workflows" / name).read_text(encoding="utf-8")
            with self.subTest(workflow=name):
                self.assertNotIn("peter-evans/dockerhub-description", source)
                self.assertNotIn("Sync Docker Hub repository description", source)


if __name__ == "__main__":
    unittest.main()
