# SPDX-License-Identifier: MPL-2.0
"""Fail-closed tests for the showcase asset manifest gate.

The PNGs here are tiny synthetic fixtures used ONLY to exercise validation;
they are never published. Real screenshots come from the packaged app journey.
"""

from __future__ import annotations

import json
import os
import struct
import sys
import tempfile
import unittest
import zlib
from datetime import datetime, timezone
from pathlib import Path
import re

sys.path.insert(0, str(Path(__file__).resolve().parent))
sys.path.insert(0, str(Path(__file__).resolve().parent / "review"))
import showcase_assets as sa  # noqa: E402
import region_coverage as rc  # noqa: E402

# Derive the browser driver version from the authoritative pin so this fixture
# cannot silently drift behind scripts/manual-review/e2e/package.json again.
_DRIVER_PACKAGE = (
    Path(__file__).resolve().parent
    / "manual-review"
    / "e2e"
    / "package.json"
)
REVIEW_DRIVER_VERSION = json.loads(_DRIVER_PACKAGE.read_text(encoding="utf-8"))["dependencies"]["playwright"]


def chunk(kind: bytes, body: bytes) -> bytes:
    return (
        struct.pack(">I", len(body))
        + kind
        + body
        + struct.pack(">I", zlib.crc32(kind + body) & 0xFFFFFFFF)
    )


def fake_png(width: int = 320, height: int = 320, pad: int = 6000, seed: bytes = b"fixture", color=(40, 90, 160)) -> bytes:
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    row = b"\x00" + bytes(color) * width
    idat = zlib.compress(row * height, 6)
    text = b"Comment\x00" + (seed + b"-" * pad)[:pad]
    return (
        sa.PNG_SIGNATURE
        + chunk(b"IHDR", ihdr)
        + chunk(b"tEXt", text)
        + chunk(b"IDAT", idat)
        + chunk(b"IEND", b"")
    )


def fake_png_black_map_with_chrome(
    width: int = 320, height: int = 320, chrome_fraction: float = 0.2
) -> bytes:
    """Non-black right-hand chrome over a black left/central map region."""
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    chrome_start = int(width * (1 - chrome_fraction))
    row = b"\x00" + bytes(3) * chrome_start + bytes((40, 90, 160)) * (width - chrome_start)
    idat = zlib.compress(row * height, 6)
    text = b"Comment\x00" + b"fixture" + b"-" * 6000
    return (
        sa.PNG_SIGNATURE
        + chunk(b"IHDR", ihdr)
        + chunk(b"tEXt", text[:6000 + len(b"Comment")])
        + chunk(b"IDAT", idat)
        + chunk(b"IEND", b"")
    )


def workflow_text() -> str:
    return (
        Path(__file__).resolve().parent.parent
        / ".github" / "workflows" / "showcase.yml"
    ).read_text(encoding="utf-8")


def plan_file(root: Path, names: list[str]) -> Path:
    steps = [
        {"screenshot": name, "name": f"Focus {name}", "alt": f"Alt text for {name}"}
        for name in names
    ]
    path = root / "plan.json"
    path.write_text(json.dumps({"steps": steps}), encoding="utf-8")
    return path


class ShowcaseAssetsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.images = self.root / "images"
        self.publish = self.root / "publish"
        self.images.mkdir()
        self.plan = plan_file(self.root, ["showcase-a", "showcase-b"])
        self.seed = self.root / "devstack.sh"
        self.seed.write_text(
            "PANEL_REVIEW_USER ... /AuthService/Register ...\n", encoding="utf-8"
        )
        for name in ("showcase-a", "showcase-b"):
            (self.images / f"{name}.png").write_bytes(fake_png(seed=name.encode()))

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def build(self, captured_at: str | None = None, run_id: str = "123") -> None:
        sa.build(
            sa.argparse.Namespace(
                images_dir=str(self.images),
                publish_dir=str(self.publish),
                plan=str(self.plan),
                seed_script=str(self.seed),
                source_sha="a" * 40,
                source_ref="refs/heads/main",
                repository="athNdev/carbonpanel",
                run_id=run_id,
                run_url="https://github.com/athNdev/carbonpanel/actions/runs/123",
                captured_at=captured_at or datetime.now(timezone.utc).isoformat(),
                app_version="review",
                driver_version=REVIEW_DRIVER_VERSION,
                browser_version="1148",
            )
        )

    def validate(self, **overrides) -> None:
        kwargs = {
            "publish_dir": str(self.publish),
            "plan": str(self.plan),
            "expected_source_sha": "a" * 40,
            "max_age_hours": 12,
        }
        kwargs.update(overrides)
        sa.validate(sa.argparse.Namespace(**kwargs))

    def test_enabled_feature_screenshot_cannot_be_missing(self) -> None:
        plan = {
            'steps': [
                {'screenshot': 'core-a', 'name': 'core'},
                {'screenshot': 'feature-a', 'feature': 'players', 'name': 'feature'},
            ]
        }
        self.plan.write_text(json.dumps(plan), encoding='utf-8')
        (self.images / 'core-a.png').write_bytes(fake_png())
        with self.assertRaisesRegex(sa.AssetError, 'missing required screenshot'):
            sa.build(sa.argparse.Namespace(
                images_dir=str(self.images), publish_dir=str(self.publish / 'x'),
                plan=str(self.plan), seed_script=str(self.seed), source_sha='a' * 40,
                source_ref='refs/heads/main', repository='athNdev/carbonpanel',
                run_id='1', run_url='u', captured_at='2026-11-19T00:00:00Z',
                app_version='review', driver_version='1', browser_version='1',
                features='players',
            ))

    def test_black_rendered_map_canvas_is_rejected(self) -> None:
        plan = {
            'steps': [
                {'screenshot': 'showcase-world-view', 'name': 'rendered map'},
            ]
        }
        self.plan.write_text(json.dumps(plan), encoding='utf-8')
        (self.images / 'showcase-world-view.png').write_bytes(
            fake_png(color=(0, 0, 0))
        )
        sa.build(sa.argparse.Namespace(
            images_dir=str(self.images), publish_dir=str(self.publish / 'map'),
            plan=str(self.plan), seed_script=str(self.seed), source_sha='a' * 40,
            source_ref='refs/heads/main', repository='athNdev/carbonpanel',
            run_id='1', run_url='u', captured_at=datetime.now(timezone.utc).isoformat(),
            app_version='review', driver_version=REVIEW_DRIVER_VERSION,
            browser_version='1', features='worlds',
        ))
        with self.assertRaisesRegex(sa.AssetError, 'blank'):
            sa.validate(sa.argparse.Namespace(
                publish_dir=str(self.publish / 'map'), plan=str(self.plan),
                expected_source_sha='a' * 40, features='worlds', max_age_hours=12,
            ))

    def test_black_map_region_is_rejected_even_with_nonblack_chrome(self) -> None:
        # A whole-image 5% gate passes when the panel chrome is coloured even
        # though the map itself is black. The region gate must still fail
        # closed (OSS-392).
        plan = {
            'steps': [
                {'screenshot': 'showcase-world-view', 'name': 'rendered map'},
            ]
        }
        self.plan.write_text(json.dumps(plan), encoding='utf-8')
        (self.images / 'showcase-world-view.png').write_bytes(
            fake_png_black_map_with_chrome()
        )
        sa.build(sa.argparse.Namespace(
            images_dir=str(self.images), publish_dir=str(self.publish / 'black-region'),
            plan=str(self.plan), seed_script=str(self.seed), source_sha='a' * 40,
            source_ref='refs/heads/main', repository='athNdev/carbonpanel',
            run_id='1', run_url='u', captured_at=datetime.now(timezone.utc).isoformat(),
            app_version='review', driver_version=REVIEW_DRIVER_VERSION,
            browser_version='1', features='worlds',
        ))
        with self.assertRaisesRegex(sa.AssetError, 'map region is blank'):
            sa.validate(sa.argparse.Namespace(
                publish_dir=str(self.publish / 'black-region'), plan=str(self.plan),
                expected_source_sha='a' * 40, features='worlds', max_age_hours=12,
            ))

    def test_thin_strip_crop_is_rejected(self) -> None:
        # A heading-less strip is not a meaningful README crop; refuse it
        # instead of weakening the plausibility floor.
        (self.images / 'showcase-a.png').write_bytes(fake_png(width=2164, height=242))
        with self.assertRaisesRegex(sa.AssetError, 'implausible PNG dimensions'):
            self.build()

    def test_build_then_validate_happy_path(self) -> None:
        self.build()
        manifest = json.loads((self.publish / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(len(manifest["images"]), 2)
        self.assertEqual(manifest["provenance"]["mockups"], False)
        self.assertTrue(all(image["alt"] for image in manifest["images"]))
        self.validate()

    def test_missing_image_fails_closed(self) -> None:
        self.build()
        (self.publish / "showcase-b.png").unlink()
        with self.assertRaisesRegex(sa.AssetError, "missing published image"):
            self.validate()

    def test_extra_file_fails_closed(self) -> None:
        self.build()
        (self.publish / "unexpected.png").write_bytes(fake_png())
        with self.assertRaisesRegex(sa.AssetError, "unexpected files"):
            self.validate()

    def test_stale_source_fails_closed(self) -> None:
        self.build()
        with self.assertRaisesRegex(sa.AssetError, "stale source"):
            self.validate(expected_source_sha="b" * 40)

    def test_stale_capture_fails_closed(self) -> None:
        old = "2020-01-01T00:00:00+00:00"
        self.build(captured_at=old)
        with self.assertRaisesRegex(sa.AssetError, "too old"):
            self.validate()

    def test_corrupt_png_fails_closed(self) -> None:
        self.build()
        data = bytearray((self.publish / "showcase-a.png").read_bytes())
        data[-1] ^= 0xFF  # corrupt IEND CRC
        (self.publish / "showcase-a.png").write_bytes(bytes(data))
        with self.assertRaisesRegex(sa.AssetError, "CRC mismatch"):
            self.validate()

    def test_bytes_not_matching_manifest_fail_closed(self) -> None:
        self.build()
        (self.publish / "showcase-a.png").write_bytes(fake_png(seed=b"different"))
        with self.assertRaisesRegex(sa.AssetError, "SHA256 does not match"):
            self.validate()

    def test_secret_in_image_fails_closed(self) -> None:
        self.build()
        # A structurally valid PNG with literal credential text in a tEXt chunk.
        (self.publish / "showcase-a.png").write_bytes(fake_png(seed=b"Review-Panel-2026"))
        with self.assertRaisesRegex(sa.AssetError, "credential-like"):
            self.validate()

    def test_secret_in_manifest_fails_closed(self) -> None:
        self.build()
        path = self.publish / "manifest.json"
        manifest = json.loads(path.read_text(encoding="utf-8"))
        manifest["provenance"]["note"] = "gho_abcdefghijklmnopqrstuvwxyz0123456789"
        path.write_text(json.dumps(manifest), encoding="utf-8")
        with self.assertRaisesRegex(sa.AssetError, "credential-like"):
            self.validate()

    def test_noop_detects_identical_content(self) -> None:
        import contextlib
        import io

        self.build()
        current = self.publish / "manifest.json"
        first = json.loads(current.read_text(encoding="utf-8"))
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            sa.noop(sa.argparse.Namespace(
                current_manifest=str(current), new_manifest=str(current)
            ))
        self.assertIn("changed=false", out.getvalue())
        # A rerun with identical images keeps the same digest despite new run metadata.
        self.build(captured_at=datetime.now(timezone.utc).isoformat(), run_id="124")
        latest = json.loads(current.read_text(encoding="utf-8"))
        self.assertEqual(first["contentDigest"], latest["contentDigest"])
        self.assertNotEqual(first["source"]["runId"], latest["source"]["runId"])

    def test_noop_detects_changed_images(self) -> None:
        self.build()
        digest_before = json.loads((self.publish / "manifest.json").read_text())["contentDigest"]
        (self.images / "showcase-a.png").write_bytes(fake_png(seed=b"changed-image"))
        self.build()
        digest_after = json.loads((self.publish / "manifest.json").read_text())["contentDigest"]
        self.assertNotEqual(digest_before, digest_after)


    def test_narrow_world_view_image_is_rejected(self) -> None:
        plan = {
            "steps": [
                {"screenshot": "showcase-world-view", "name": "rendered map"},
            ]
        }
        self.plan.write_text(json.dumps(plan), encoding="utf-8")
        (self.images / "showcase-world-view.png").write_bytes(
            fake_png(width=1200, height=1800)
        )
        sa.build(sa.argparse.Namespace(
            images_dir=str(self.images), publish_dir=str(self.publish / "narrow"),
            plan=str(self.plan), seed_script=str(self.seed), source_sha="a" * 40,
            source_ref="refs/heads/main", repository="athNdev/carbonpanel",
            run_id="1", run_url="u", captured_at=datetime.now(timezone.utc).isoformat(),
            app_version="review", driver_version=REVIEW_DRIVER_VERSION,
            browser_version="1", features="worlds",
        ))
        with self.assertRaisesRegex(sa.AssetError, 'too narrow'):
            sa.validate(sa.argparse.Namespace(
                publish_dir=str(self.publish / "narrow"), plan=str(self.plan),
                expected_source_sha="a" * 40, features="worlds", max_age_hours=12,
            ))


class ShowcasePipelineTest(unittest.TestCase):
    """The routine main-refresh workflow must never drop the proven map image."""

    def test_every_feature_gate_is_on_for_main_push(self) -> None:
        workflow = workflow_text()
        # playerdetail is intentionally manual-only: its skin preview depends
        # on an external provider and is not part of the canonical set.
        for feature in ("players", "worlds", "worldtab", "library"):
            pattern = re.compile(
                r"FEATURES_" + feature.upper() + r": \$?\{\{([^\n]*)\}\}"
            )
            matches = pattern.findall(workflow)
            self.assertGreaterEqual(len(matches), 1, f"no gate for {feature}")
            for expression in matches:
                self.assertIn(
                    "github.event_name == 'push' || inputs." + feature,
                    expression,
                    f"{feature} can be dropped on a routine main push",
                )
        detail = re.findall(r"FEATURES_PLAYERDETAIL: \$?\{\{([^\n]*)\}\}", workflow)
        self.assertTrue(detail, "playerdetail gate is missing")
        for expression in detail:
            self.assertNotIn(
                "github.event_name == 'push'",
                expression,
                "playerdetail must stay manual-only (external skin provider)",
            )

    def test_publish_requires_the_real_world_view_asset(self) -> None:
        workflow = workflow_text()
        section = workflow.split("Require the full README-canonical image set", 1)[1]
        section = section.split("Guard provenance monotonicity", 1)[0]
        for name in (
            "showcase-world-view",
            "showcase-world-tab",
            "showcase-worlds-list",
            "showcase-dashboard-full",
            "showcase-player-allowlist",
        ):
            self.assertIn(name, section, f"canonical publish set may drop {name}")

    def test_fixture_changes_trigger_a_routine_capture(self) -> None:
        self.assertIn("scripts/review/fixtures/**", workflow_text().split("workflow_dispatch:", 1)[0])

    def test_outside_region_entries_cannot_qualify_coverage(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            def write_headers(requested: bool) -> None:
                for rx in (-1, 0):
                    for rz in (-1, 0):
                        header = bytearray(4096)
                        xs = (range(16, 32) if rx == -1 else range(0, 16))
                        zs = (range(16, 32) if rz == -1 else range(0, 16))
                        if not requested:
                            xs = range(0, 16) if rx == -1 else range(16, 32)
                            zs = range(0, 16) if rz == -1 else range(16, 32)
                        for x in xs:
                            for z in zs:
                                header[4 * (x + 32 * z) + 3] = 1
                        Path(tmp, f"r.{rx}.{rz}.mca").write_bytes(bytes(header) + b"x" * 512)

            write_headers(requested=False)
            covered, size = rc.requested_chunks(tmp)
            self.assertEqual(covered, 0, "outside entries must not qualify")
            self.assertGreater(size, 0, "region bytes must still be reported")
            write_headers(requested=True)
            covered, _ = rc.requested_chunks(tmp)
            self.assertEqual(covered, 1024, "all requested chunks must count")

    def test_fixture_terrain_batches_are_bounded_and_seed_is_fresh_only(self) -> None:
        devstack = (
            Path(__file__).resolve().parent / "review" / "devstack.sh"
        ).read_text(encoding="utf-8")
        block = devstack.split("local -a ranges=(", 1)[1].split(")", 1)[0]
        ranges = re.findall(r'"(-?\d+) (-?\d+) (-?\d+) (-?\d+)"', block)
        self.assertTrue(ranges, "no forceload ranges found")
        for x0, z0, x1, z1 in ranges:
            chunks = (int(x1) // 16 - int(x0) // 16 + 1) * (
                int(z1) // 16 - int(z0) // 16 + 1
            )
            self.assertLessEqual(chunks, 256, f"forceload range exceeds 256 chunks: {chunks}")
        # The seed must be pinned only for a fresh fixture config; an existing
        # review config (and any running container) is never modified.
        self.assertIn('if [ ! -f "${DATA}/config/config.yaml" ]; then', devstack)
        self.assertIn("JSON.stringify({minecraft:{global_config:{seed:", devstack)
        self.assertNotIn("ConfigService/UpdateServerConfig", devstack)
        # Region readiness must use the shared GetServer.dataPath resolver and
        # real .mca header coverage, never a guessed slug+ID path.
        self.assertIn("review_server_host_path", devstack)
        self.assertIn(".mca", devstack)
        self.assertNotIn('servers/${server_id}/world/region', devstack)
        # The World View camera must be zoomed out for a landscape capture.
        distance = re.search(r"world:0:0:0:(\d+):0:0:0:0:perspective", devstack)
        self.assertTrue(distance, "no BlueMap startLocation found")
        self.assertGreater(int(distance.group(1)), 100, "camera is not a wide landscape view")

    def test_narrow_player_viewport_is_restored_before_world_view(self) -> None:
        plan = json.loads((
            Path(__file__).resolve().parent / "review" / "plan-showcase.json"
        ).read_text(encoding="utf-8"))
        steps = plan["steps"]
        viewport = plan["viewport"]
        narrow = [
            i for i, step in enumerate(steps)
            if "setViewport" in step and step["setViewport"] != viewport
        ]
        self.assertTrue(narrow, "expected a narrow inline-capture viewport step")
        world_view = next(
            i for i, step in enumerate(steps)
            if step.get("screenshot") == "showcase-world-view"
        )
        restores = [
            step for i, step in enumerate(steps)
            if max(narrow) < i < world_view
            and step.get("setViewport") == viewport
        ]
        self.assertTrue(
            restores,
            "desktop viewport must be restored before the World View capture",
        )
        # The restore must always run when the narrowing step runs.
        self.assertIn(
            steps[max(narrow)].get("feature"),
            [step.get("feature") for step in restores],
        )


if __name__ == "__main__":
    unittest.main()
