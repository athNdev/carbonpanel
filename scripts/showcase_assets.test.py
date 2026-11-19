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

sys.path.insert(0, str(Path(__file__).resolve().parent))
import showcase_assets as sa  # noqa: E402


def chunk(kind: bytes, body: bytes) -> bytes:
    return (
        struct.pack(">I", len(body))
        + kind
        + body
        + struct.pack(">I", zlib.crc32(kind + body) & 0xFFFFFFFF)
    )


def fake_png(width: int = 320, height: int = 320, pad: int = 6000, seed: bytes = b"fixture") -> bytes:
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)
    row = b"\x00" + bytes([40, 90, 160]) * width
    idat = zlib.compress(row * height, 6)
    text = b"Comment\x00" + (seed + b"-" * pad)[:pad]
    return (
        sa.PNG_SIGNATURE
        + chunk(b"IHDR", ihdr)
        + chunk(b"tEXt", text)
        + chunk(b"IDAT", idat)
        + chunk(b"IEND", b"")
    )


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
                driver_version="1.49.1",
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


if __name__ == "__main__":
    unittest.main()
