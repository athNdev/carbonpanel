# SPDX-License-Identifier: MPL-2.0
"""Verify a downloaded node-VM base image's REAL bytes against its pin.

`packaging/node-vm/provenance/RETRIEVAL-EVIDENCE.md` records the upstream
published, signature-verified SHA256 for the immutable dated Ubuntu images, but
explicitly notes that the image bytes were not downloaded. A real build (and a
real clean-boot qualification) must still fetch the image and recompute the
digest over the actual bytes. This driver performs exactly that verification,
streaming the file so a ~600 MiB QCOW2 is never held in memory.

It is offline and read-only: it never downloads, mounts, boots, converts or
publishes anything. Callers supply already-downloaded files plus the published
SHA256SUMS text. GPG signature verification of that checksum file is a separate,
independently evidenced step recorded in RETRIEVAL-EVIDENCE.md.
"""
import argparse
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent

try:  # works for `python scripts/...` and `python -m unittest discover -s scripts`
    from scripts import node_vm_build as build
except ImportError:  # pragma: no cover - CLI path
    import node_vm_build as build


class VerificationError(ValueError):
    pass


def sha256_file(path, chunk_size=1024 * 1024):
    """Streaming SHA256 of a file; return (hexdigest, byte_count)."""
    digest = hashlib.sha256()
    count = 0
    with open(path, "rb") as handle:
        while True:
            chunk = handle.read(chunk_size)
            if not chunk:
                break
            digest.update(chunk)
            count += len(chunk)
    return digest.hexdigest(), count


def parse_sha256sums(text):
    """Parse SHA256SUMS text into {filename: digest} (GNU binary '*' prefix ok)."""
    result = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        digest, name = line.split(None, 1)
        result[name.strip().lstrip("*")] = digest.strip().lower()
    return result


def verify_image_bytes(actual_sha256, byte_count, image, checksums_text=None):
    """Check a computed digest/size against one image pin and published sums.

    Pure and dependency-free so offline fixtures can exercise it with tiny
    synthetic files. Fails closed on any digest or size disagreement.
    """
    expected = image["expected_sha256"]
    if actual_sha256 != expected:
        raise VerificationError(
            "image bytes do not match pinned SHA256: got %s expected %s"
            % (actual_sha256, expected))
    expected_size = image.get("image_content_length")
    if expected_size is not None and byte_count != int(expected_size):
        raise VerificationError(
            "image size mismatch: got %d expected %d" % (byte_count, expected_size))
    name = image["image_url"].rsplit("/", 1)[-1]
    published = None
    if checksums_text is not None:
        published = parse_sha256sums(checksums_text).get(name)
        if published is None:
            raise VerificationError("image not present in published SHA256SUMS: %s" % name)
        if published != expected or published != actual_sha256:
            raise VerificationError("published checksum does not match the image bytes")
    return {
        "ref": image["ref"],
        "arch": image["arch"],
        "filename": name,
        "bytes": byte_count,
        "sha256_actual": actual_sha256,
        "sha256_pinned": expected,
        "sha256_published": published,
        "immutable_url": image["image_url"],
        "digest_status": image["digest_status"],
        "bytes_verified": True,
    }


def verify_pinned_image(root, image_path, ref, profile="artifact", checksums_text=None):
    """Load the checked-in pin, fail closed, and verify the supplied image file."""
    root = Path(root)
    _, _, config, provenance, _, _ = build.load_inputs(root, profile)
    image = build.base_image(provenance, ref)
    if profile == "artifact":
        build.verify_artifact_provenance(provenance, image)
    actual, size = sha256_file(image_path)
    result = verify_image_bytes(actual, size, image, checksums_text)
    result["profile"] = profile
    result["image_file"] = str(image_path)
    result["gpg_signature_evidence"] = "packaging/node-vm/provenance/RETRIEVAL-EVIDENCE.md"
    return result


def verify_checksum_file(provenance, checksums_bytes, signature_bytes):
    """Confirm fetched SHA256SUMS/.gpg bytes match the pinned file hashes."""
    sums = hashlib.sha256(checksums_bytes).hexdigest()
    sig = hashlib.sha256(signature_bytes).hexdigest()
    pin = provenance["images"][0]
    for label, actual, field in (("checksum", sums, "checksum_file_sha256"),
                                 ("signature", sig, "signature_file_sha256")):
        expected = pin.get(field)
        if not expected or actual != expected:
            raise VerificationError(
                "%s file SHA256 mismatch: got %s expected %s" % (label, actual, expected))
    return {
        "checksum_file_sha256": sums,
        "signature_file_sha256": sig,
        "signing_key_fingerprint": pin["signing_key_fingerprint"],
        "file_bytes_verified": True,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--image", required=True, help="already-downloaded local image file")
    parser.add_argument("--ref", required=True, help="base image ref, e.g. ubuntu-24.04-amd64")
    parser.add_argument("--sha256sums", help="path to the published SHA256SUMS text")
    parser.add_argument("--profile", choices=("artifact", "fixture"), default="artifact")
    args = parser.parse_args(argv)
    sums = Path(args.sha256sums).read_text() if args.sha256sums else None
    result = verify_pinned_image(args.root, args.image, args.ref, args.profile, sums)
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
