# SPDX-License-Identifier: MPL-2.0
"""Reproducible node-VM build driver (offline planner).

Produces a deterministic cloud-init NoCloud seed, a content manifest and a
build plan for the reproducible QCOW2 node VM. The driver never invokes
qemu-img, virt-customize, a hypervisor or any host provisioning command. It
writes local files under an owned output directory.

Two provenance profiles exist:

  * ``fixture``  - synthetic placeholder digests, used only by offline tests;
  * ``artifact`` - REAL immutable dated Ubuntu image pins with verified hashes.

``artifact`` mode calls verify_artifact_provenance() and FAILS CLOSED on any
placeholder/synthetic digest or missing signature evidence. No enrollment
input (secret, private key, digest bundle, approved SPKI or fingerprint) is
ever read from the build config or written into the image or seed. First-boot
identity is generated inside the VM by
packaging/node-vm/cloud-init/firstboot/enroll.py.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parent.parent
PKG = ROOT / "packaging" / "node-vm"

# Public metadata placeholders only. Nothing here is a credential.
PLACEHOLDERS = ("@@NODE_LABEL@@", "@@INSTANCE_ID@@", "@@FIRSTBOOT_SCRIPT_B64@@",
                "@@ENROLLMENT_POLICY_JSON@@")
PEM_MARKER = re.compile(r"-----BEGIN (?:[A-Z ]*PRIVATE KEY|CERTIFICATE)-----")
SECRET_ASSIGNMENT = re.compile(
    r"(?im)^\s*(enrollment_secret|private_key|token|password|passphrase|api[_-]?key)\s*[:=]\s*\S+"
)


class BuildError(ValueError):
    pass


def load_json(path):
    return json.loads(Path(path).read_text())


def provenance_file_for(config, profile):
    """Select the checked-in provenance file for a named profile."""
    files = config.get("profiles", {})
    if profile not in files:
        raise BuildError("unknown provenance profile: %s" % profile)
    return files[profile]


def load_inputs(root=ROOT, profile="fixture"):
    root = Path(root)
    pkg = root / "packaging" / "node-vm"
    config = load_json(pkg / "config" / "build-config.json")
    provenance = load_json(pkg / provenance_file_for(config, profile))
    policy = load_json(pkg / config["enrollment_policy"])
    upgrade_policy = load_json(pkg / config["upgrade_policy"])
    return root, pkg, config, provenance, policy, upgrade_policy


def base_image(provenance, ref):
    for image in provenance["images"]:
        if image["ref"] == ref:
            return image
    raise BuildError("unknown base image ref: %s" % ref)


_PLACEHOLDER_DIGESTS = frozenset({
    "0" * 64, "1" * 64, "a" * 64, "b" * 64, "f" * 64,
})


def verify_artifact_provenance(provenance, image):
    """Fail closed unless the image pin is real, dated and signature-backed.

    Called by artifact-mode builds before any plan or manifest is emitted.
    Placeholder/repeated digests, moving``/release/`` or ``/current/`` URLs,
    missing signature URLs or missing retrieval evidence are all refused.
    """
    if provenance.get("profile") != "artifact" or provenance.get("synthetic") is not False:
        raise BuildError("artifact provenance profile is synthetic or missing")
    digest = image.get("expected_sha256", "")
    if image.get("digest_status") != "verified":
        raise BuildError("image digest_status is not verified: %s" % image.get("ref"))
    if not re.fullmatch(r"[0-9a-f]{64}", digest) or digest in _PLACEHOLDER_DIGESTS \
            or len(set(digest)) <= 2:
        raise BuildError("placeholder or invalid image SHA256: %s" % image.get("ref"))
    if image.get("immutable") is not True:
        raise BuildError("image URL is not an immutable dated pin: %s" % image.get("ref"))
    url = image.get("image_url", "")
    if not re.search(r"/release-\d{8}/", url) or "/current/" in url or "/release/" in url:
        raise BuildError("image URL is not an immutable dated release path: %s" % url)
    for field in ("checksum_url", "signature_url", "signing_key_fingerprint",
                  "checksum_file_sha256", "signature_file_sha256"):
        if not image.get(field):
            raise BuildError("missing artifact provenance field %s" % field)
    if not re.fullmatch(r"[0-9A-Fa-f]{40}", image["signing_key_fingerprint"]):
        raise BuildError("signing key fingerprint is not a 40-hex fingerprint")
    evidence = provenance.get("retrieval_evidence", {})
    if not evidence.get("gpg_verify") or "Good signature" not in evidence["gpg_verify"]:
        raise BuildError("no recorded good GPG signature verification")
    if "HTTP 200" not in evidence.get("checksum_get", "") \
            or "HTTP 200" not in evidence.get("signature_get", ""):
        raise BuildError("checksum/signature retrieval evidence is missing")
    return True


def verify_image_checksum(image, checksums_text):
    """Verify an image pin against the content of its published SHA256SUMS."""
    expected = {}
    for line in checksums_text.splitlines():
        if not line.strip():
            continue
        digest, name = line.split(None, 1)
        expected[name.strip().lstrip("*")] = digest.strip()
    name = image["image_url"].rsplit("/", 1)[-1]
    if expected.get(name) != image["expected_sha256"]:
        raise BuildError("published checksum does not match pin: %s" % name)
    return True


def check_no_baked_secrets(config, policy, rendered):
    """Fail closed if a secret-looking value or PEM/assignment reached the seed."""
    if policy.get("baked_secrets_allowed") is not False:
        raise BuildError("enrollment policy must forbid baked secrets")
    if policy.get("fork_allowed") is not False or policy.get("contract_is_shared") is not True:
        raise BuildError("enrollment contract must be shared and must not be forked")
    for name, text in rendered.items():
        if PEM_MARKER.search(text):
            raise BuildError("PEM private material in rendered seed: %s" % name)
        match = SECRET_ASSIGNMENT.search(text)
        if match:
            raise BuildError("secret assignment in rendered seed: %s (%s)" % (name, match.group(1)))
    # JSON build config must not carry secret-bearing keys as *values to embed*.
    for key in ("secret", "private_key", "enrollment_secret", "token", "password"):
        if key in config:
            raise BuildError("build config must not contain %r" % key)
    return True


def render_templates(root, config, policy, instance_id, node_label):
    pkg = Path(root) / "packaging" / "node-vm"
    ci = config["cloud_init"]
    firstboot = (pkg / ci["firstboot_script"]).read_bytes()
    values = {
        "@@NODE_LABEL@@": node_label,
        "@@INSTANCE_ID@@": instance_id,
        "@@FIRSTBOOT_SCRIPT_B64@@": __import__("base64").b64encode(firstboot).decode("ascii"),
        "@@ENROLLMENT_POLICY_JSON@@": json.dumps(policy, sort_keys=True, separators=(",", ":")),
    }
    rendered = {}
    for key in ("user_data_template", "meta_data_template", "network_config_template"):
        template = (pkg / ci[key]).read_text()
        for token, value in values.items():
            template = template.replace(token, value)
        if "@@" in template:
            raise BuildError("unresolved placeholder in %s" % key)
        name = {"user_data_template": "user-data", "meta_data_template": "meta-data",
                "network_config_template": "network-config"}[key]
        rendered[name] = template
    return rendered


def deterministic_manifest(rendered, source_date_epoch, digest_status):
    """Canonical, stable content manifest. No wall-clock time is used."""
    files = []
    for name in sorted(rendered):
        raw = rendered[name].encode("utf-8")
        files.append({
            "path": "nocloud/" + name,
            "size": len(raw),
            "sha256": hashlib.sha256(raw).hexdigest(),
        })
    generated_at = datetime.fromtimestamp(int(source_date_epoch), timezone.utc).strftime(
        "%Y-%m-%dT%H:%M:%SZ"
    )
    manifest = {
        "schema_version": 1,
        "generated_at": generated_at,
        "provenance_status": digest_status,
        "files": files,
    }
    canonical = json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()
    manifest["sha256"] = hashlib.sha256(canonical).hexdigest()
    return manifest


def build_plan(config, image, manifest, instance_id, node_label, mode, profile):
    """Describe the build without executing any virtualization command."""
    return {
        "schema_version": 1,
        "mode": mode,
        "provenance_profile": profile,
        "artifact_family": config["artifact_family"],
        "artifact_version": config["artifact_version"],
        "artifact_revision": config["artifact_revision"],
        "instance_id": instance_id,
        "node_label": node_label,
        "base_image": {
            "ref": image["ref"],
            "arch": image["arch"],
            "expected_sha256": image["expected_sha256"],
            "digest_status": image["digest_status"],
            "image_url": image["image_url"],
            "checksum_url": image["checksum_url"],
            "signature_url": image["signature_url"],
            "signing_key_fingerprint": image["signing_key_fingerprint"],
            "release_date": image["release_date"],
        },
        "toolchain": config["toolchain"],
        "disk": config["disk"],
        "seed_manifest_sha256": manifest["sha256"],
        "commands_not_executed": [
            "qemu-img create -f qcow2 <out> <size_gib>G",
            "xorriso -as mkisofs -o <seed.iso> -J -r -V CIDATA <nocloud-dir>",
            "virt-customize -a <out> --attach <seed.iso> (cloud-init NoCloud datasource)",
        ],
        "fixture_only": mode != "apply",
        "no_qcow2_built": True,
    }


def offline_build(root=ROOT, out_dir=None, instance_id=None, node_label=None,
                  check=False, profile="fixture"):
    root, pkg, config, provenance, policy, _ = load_inputs(root, profile)
    instance_id = instance_id or "fixture-instance-0001"
    node_label = node_label or "fixture01"
    image = base_image(provenance, config["base_image_ref"])
    if profile == "artifact":
        verify_artifact_provenance(provenance, image)
    rendered = render_templates(root, config, policy, instance_id, node_label)
    check_no_baked_secrets(config, policy, rendered)
    manifest = deterministic_manifest(rendered, config["source_date_epoch"],
                                      image["digest_status"])
    plan = build_plan(config, image, manifest, instance_id, node_label, "offline", profile)

    if out_dir:
        out = Path(out_dir)
        (out / "nocloud").mkdir(parents=True, exist_ok=True)
        for name, text in rendered.items():
            (out / "nocloud" / name).write_text(text)
        (out / "seed-manifest.json").write_text(
            json.dumps(manifest, sort_keys=True, indent=2) + "\n"
        )
        (out / "build-plan.json").write_text(
            json.dumps(plan, sort_keys=True, indent=2) + "\n"
        )

    if check:
        again = render_templates(root, config, policy, instance_id, node_label)
        manifest2 = deterministic_manifest(again, config["source_date_epoch"],
                                           image["digest_status"])
        if manifest2 != manifest:
            raise BuildError("build is not deterministic")
        plan2 = build_plan(config, image, manifest2, instance_id, node_label, "offline", profile)
        if plan2 != plan:
            raise BuildError("build plan is not deterministic")
    return {"rendered": rendered, "manifest": manifest, "plan": plan}


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=str(ROOT))
    parser.add_argument("--out-dir")
    parser.add_argument("--instance-id", default="fixture-instance-0001")
    parser.add_argument("--node-label", default="fixture01")
    parser.add_argument("--check", action="store_true", help="verify deterministic output")
    parser.add_argument("--profile", choices=("artifact", "fixture"), default="artifact",
                        help="provenance profile (artifact fails closed on placeholder digests)")
    args = parser.parse_args(argv)
    result = offline_build(args.root, args.out_dir, args.instance_id,
                           args.node_label, args.check, args.profile)
    print(json.dumps({
        "manifest_sha256": result["manifest"]["sha256"],
        "provenance_status": result["manifest"]["provenance_status"],
        "provenance_profile": result["plan"]["provenance_profile"],
        "fixture_only": result["plan"]["fixture_only"],
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
