# SPDX-License-Identifier: MPL-2.0
"""Deterministic checksum, CycloneDX SBOM and signing-plan generation.

Packaging-only: no registry push, no release publication and no signing key is
ever embedded. Signing is described as an operator action on an owned signing
host (detached signature over the checksum manifest); the private key identity
is supplied through the environment at signing time, never stored in an image,
manifest, SBOM or build config.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import uuid

ROOT = Path(__file__).resolve().parent.parent


class IntegrityError(ValueError):
    pass


def sha256_bytes(data):
    return hashlib.sha256(data).hexdigest()


def sha256_file(path):
    return sha256_bytes(Path(path).read_bytes())


def build_checksum_manifest(files):
    """files: mapping of relative-name -> bytes. Sorted, deterministic text."""
    lines = []
    for name in sorted(files):
        lines.append("%s  %s" % (sha256_bytes(files[name]), name))
    return "\n".join(lines) + "\n"


def parse_checksum_manifest(text):
    result = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        digest, name = line.split(None, 1)
        result[name.strip()] = digest
    return result


def verify_checksum_manifest(text, files):
    expected = parse_checksum_manifest(text)
    if set(expected) != set(files):
        return False
    return all(expected[name] == sha256_bytes(files[name]) for name in files)


def _deterministic_uuid(seed):
    return str(uuid.UUID(hashlib.md5(seed.encode("utf-8")).hexdigest()))


def build_sbom(config, provenance, artifacts, source_date_epoch=None):
    """CycloneDX 1.5 JSON SBOM from pinned provenance and build inputs."""
    epoch = source_date_epoch or config["source_date_epoch"]
    timestamp = datetime.fromtimestamp(int(epoch), timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    components = []

    for image in sorted(provenance["images"], key=lambda i: i["ref"]):
        components.append({
            "type": "file",
            "bom-ref": "pkg:generic/%s-cloud-image@%s?arch=%s" % (
                image["distro"], image["version"].split()[0], image["arch"]),
            "name": image["ref"],
            "version": image["version"],
            "purl": "pkg:generic/%s-cloud-image@%s?arch=%s" % (
                image["distro"], image["version"].split()[0], image["arch"]),
            "hashes": [{"alg": "SHA-256", "content": image["expected_sha256"]}],
            "properties": [
                {"name": "carbon:digest_status", "value": image["digest_status"]},
                {"name": "carbon:image_url", "value": image["image_url"]},
            ],
        })

    toolchain = config["toolchain"]
    for name, version in sorted(toolchain.items()):
        components.append({
            "type": "application",
            "bom-ref": "pkg:generic/%s@%s" % (name, version),
            "name": name,
            "version": version,
            "purl": "pkg:generic/%s@%s" % (name, version),
        })

    for name in sorted(artifacts):
        components.append({
            "type": "file",
            "bom-ref": "pkg:generic/%s@%s" % (name, config["artifact_version"]),
            "name": name,
            "version": config["artifact_version"],
            "hashes": [{"alg": "SHA-256", "content": sha256_bytes(artifacts[name])}],
        })

    components.sort(key=lambda c: (c["name"], c["bom-ref"]))
    seed = json.dumps(components, sort_keys=True)
    sbom = {
        "bomFormat": "CycloneDX",
        "specVersion": "1.5",
        "serialNumber": "urn:uuid:" + _deterministic_uuid(seed),
        "version": 1,
        "metadata": {
            "timestamp": timestamp,
            "component": {
                "type": "application",
                "bom-ref": "pkg:generic/%s@%s" % (
                    config["artifact_family"], config["artifact_version"]),
                "name": config["artifact_family"],
                "version": config["artifact_version"],
            },
        },
        "components": components,
    }
    return sbom


def check_no_signing_secret(config):
    """Refuse if a private key, passphrase or token was configured for embedding."""
    for key in ("signing_key", "private_key", "signing_key_path", "passphrase",
                "token", "password", "gpg_home"):
        if key in config:
            raise IntegrityError("signing material must not be in build config: %r" % key)
    return True


def signing_plan(artifact_names, signer_env="CARBON_VM_SIGNING_KEY_ID"):
    """Describe detached-signing commands on an owned host; execute nothing.

    The key identity is read from an environment variable by the operator. No
    key data, path to a key file, or credential is part of the artifact.
    """
    if not artifact_names:
        raise IntegrityError("no artifacts to sign")
    return {
        "schema_version": 1,
        "execute": False,
        "signing_host": "operator-owned, not part of this artifact lane",
        "key_supplied_via_environment": signer_env,
        "no_baked_key": True,
        "commands_not_executed": [
            "gpg --detach-sign --armor --local-user \"$%s\" SHA256SUMS" % signer_env,
            "sha256sum -c SHA256SUMS",
            "gpg --verify SHA256SUMS.sig SHA256SUMS",
        ],
        "artifacts": sorted(artifact_names),
    }


def generate(root=ROOT, out_dir=None):
    root = Path(root)
    config = json.loads((root / "packaging/node-vm/config/build-config.json").read_text())
    provenance = json.loads(
        (root / "packaging/node-vm" / config["provenance_file"]).read_text()
    )
    check_no_signing_secret(config)
    artifacts = {
        "build-config.json": json.dumps(config, sort_keys=True, separators=(",", ":")).encode(),
    }
    checksums = build_checksum_manifest(artifacts)
    sbom = build_sbom(config, provenance, artifacts)
    plan = signing_plan(list(artifacts))
    result = {
        "SHA256SUMS": checksums,
        "sbom.cdx.json": json.dumps(sbom, sort_keys=True, indent=2) + "\n",
        "signing-plan.json": json.dumps(plan, sort_keys=True, indent=2) + "\n",
    }
    if out_dir:
        out = Path(out_dir)
        out.mkdir(parents=True, exist_ok=True)
        for name, text in result.items():
            (out / name).write_text(text)
    return result


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--out-dir")
    parser.add_argument("--verify", action="store_true",
                        help="verify generated checksum manifest against inputs")
    args = parser.parse_args(argv)
    result = generate(args.root, args.out_dir)
    if args.verify:
        config = json.loads((Path(args.root) / "packaging/node-vm/config/build-config.json").read_text())
        artifacts = {"build-config.json": json.dumps(
            config, sort_keys=True, separators=(",", ":")).encode()}
        if not verify_checksum_manifest(result["SHA256SUMS"], artifacts):
            raise IntegrityError("checksum manifest verification failed")
    print(json.dumps({"artifacts": sorted(result), "sbom_format": "CycloneDX-1.5"},
                     sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
