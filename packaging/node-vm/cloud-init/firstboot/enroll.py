#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Node VM first-boot identity and SCOPED enrollment material generator.

Self-contained and stdlib-only so it is equally usable as a cloud-init
first-boot script inside a cloud image and as an importable module in the
offline fixture tests. It implements only the NODE side of the SHARED ADR0019
enrollment contract; it does not fork, redefine or mount any node-control RPC.

Security invariants (enforced in code, not just documented):
  * the CSPRNG secret S and the private key are generated locally at first boot;
  * S and the private key are NEVER part of the exported bundle, image, seed,
    SBOM or provisioning metadata;
  * the exported bundle is digest-only and carries an operator-approved public
    key (SPKI + fingerprint) that must be registered out of band;
  * CSR_PRE is registration material only, never a claim proof;
  * each VM gets a distinct node_id derived from its unique instance-id.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import tempfile

CONTEXT_PREFIX = b"carbon-panel-enroll-v1"
FORBIDDEN_BUNDLE_FIELDS = frozenset({
    "enrollment_secret", "secret", "private_key", "privatekey", "token",
    "password", "passphrase", "secret_s", "s",
})


class KeyPair:
    """Minimal interface: DER SubjectPublicKeyInfo, SHA-256 fingerprint, sign()."""

    def spki_der(self):
        raise NotImplementedError

    def fingerprint(self):
        return hashlib.sha256(self.spki_der()).hexdigest()

    def sign(self, context):
        raise NotImplementedError


class FakeKeyPair(KeyPair):
    """Deterministic, clearly synthetic key used only by offline fixtures."""

    def __init__(self, label="fixture-key"):
        self._label = label.encode()

    def spki_der(self):
        return b"SYNTHETIC-SPKI::" + self._label

    def sign(self, context):
        return hashlib.sha256(b"SYNTHETIC-SIG::" + self._label + b"::" + context).digest()


class OpenSSLKeyPair(KeyPair):
    """Ed25519 key generated with the openssl CLI available in cloud images.

    Only constructed by main() on a real VM; offline fixtures never invoke it.
    """

    def __init__(self):
        self._dir = tempfile.mkdtemp(prefix="carbon-enroll-key-")
        os.chmod(self._dir, 0o700)
        self._priv = Path(self._dir) / "node.key"
        self._pub_der = Path(self._dir) / "node.spki.der"
        subprocess.run(
            ["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(self._priv)],
            check=True, capture_output=True,
        )
        os.chmod(self._priv, 0o600)
        subprocess.run(
            ["openssl", "pkey", "-in", str(self._priv), "-pubout", "-outform", "DER",
             "-out", str(self._pub_der)],
            check=True, capture_output=True,
        )

    def spki_der(self):
        return self._pub_der.read_bytes()

    def sign(self, context):
        with tempfile.NamedTemporaryFile() as ctx, tempfile.NamedTemporaryFile() as sig:
            ctx.write(context)
            ctx.flush()
            subprocess.run(
                ["openssl", "pkeyutl", "-sign", "-inkey", str(self._priv),
                 "-rawin", "-in", ctx.name, "-out", sig.name],
                check=True, capture_output=True,
            )
            return sig.read()


def _b64(raw):
    return base64.b64encode(raw).decode("ascii")


def derive_node_id(instance_id):
    """Unique, deterministic node identity from the per-VM instance-id."""
    if not instance_id:
        raise ValueError("instance-id is required for first-boot identity")
    return "node-" + hashlib.sha256(instance_id.encode("utf-8")).hexdigest()[:32]


def generate_secret(nbits, rng=None):
    if nbits < 256:
        raise ValueError("enrollment secret must be at least 256 bits")
    source = rng or secrets.token_bytes
    return source(nbits // 8)


def generate_salt(nbytes, rng=None):
    if nbytes < 16:
        raise ValueError("enrollment salt must be at least 16 bytes")
    source = rng or secrets.token_bytes
    return source(nbytes)


def compute_digest(salt, secret):
    return hashlib.sha256(salt + secret).hexdigest()


def canonical_context(node_id, enrollment_id, challenge_id, server_nonce,
                      expires_at, capabilities, channel_context):
    """Exact claim context from ADR0019; canonical JSON for stable signing."""
    payload = {
        "node_id": node_id,
        "enrollment_id": enrollment_id,
        "challenge_id": challenge_id,
        "server_nonce": server_nonce,
        "expires_at": expires_at,
        "capabilities": list(capabilities),
        "server_observed_channel_context": channel_context,
    }
    return CONTEXT_PREFIX + b"\x00" + json.dumps(
        payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True,
    ).encode("utf-8")


def sign_claim(keypair, context):
    """Fresh post-challenge signature by the operator-approved key only."""
    if not isinstance(context, bytes) or not context.startswith(CONTEXT_PREFIX + b"\x00"):
        raise ValueError("claim context is not a canonical carbon-panel-enroll-v1 context")
    return keypair.sign(context)


def build_preregistration_bundle(node_label, salt, secret, keypair, policy):
    """Build the digest-only exportable bundle. S and the private key never enter it."""
    digest = compute_digest(salt, secret)
    csr_pre = {
        "node_label": node_label,
        "enrollment_salt": _b64(salt),
        "enrollment_secret_digest": digest,
        "approved_public_key_spki": _b64(keypair.spki_der()),
        "approved_key_fingerprint": keypair.fingerprint(),
    }
    csr_pre_bytes = json.dumps(csr_pre, sort_keys=True, separators=(",", ":")).encode()
    bundle = dict(csr_pre)
    bundle["csr_pre"] = _b64(csr_pre_bytes)
    bundle["csr_pre_signature"] = _b64(keypair.sign(csr_pre_bytes))
    # The policy document nests the scoped fields under "policy"; accept either
    # shape so the same module works with the checked-in file and flat test data.
    scoped = policy.get("policy", policy)
    bundle["node_id"] = derive_node_id(node_label + "::" + str(policy.get("artifact_revision", "first-boot")))
    bundle["bootstrap_scopes"] = list(policy.get("bootstrap_scopes", scoped["bootstrap_scopes"]))
    bundle["object_id_binding"] = policy.get("object_id_binding", scoped["object_id_binding"])
    bundle["contract_ref"] = policy["contract_ref"]
    bundle["contract_is_shared"] = bool(policy["contract_is_shared"])
    bundle["fork_allowed"] = bool(policy["fork_allowed"])
    return bundle, digest, secret


def assert_bundle_has_no_secret(bundle, secret, salt=None):
    """Fail closed if any forbidden field or raw secret value leaked into the bundle."""
    lowered = {str(k).lower() for k in bundle}
    overlap = lowered & FORBIDDEN_BUNDLE_FIELDS
    if overlap:
        raise ValueError("forbidden secret field in enrollment bundle: %s" % sorted(overlap))
    blob = json.dumps(bundle, sort_keys=True, separators=(",", ":"))
    for value in (secret,):
        if value and (_b64(value) in blob or value.hex() in blob or value.decode("latin-1") in blob):
            raise ValueError("secret value leaked into enrollment bundle")
    if salt and _b64(salt) not in blob:
        # salt is public and expected in the digest-only bundle
        raise ValueError("bundle is missing the public enrollment salt")


def safe_bundle(bundle):
    """Return a copy with only allowlisted, non-secret fields."""
    allowed = {
        "node_label", "enrollment_salt", "enrollment_secret_digest", "csr_pre",
        "csr_pre_signature", "approved_public_key_spki", "approved_key_fingerprint",
        "node_id", "bootstrap_scopes", "object_id_binding", "contract_ref",
        "contract_is_shared", "fork_allowed",
    }
    return {k: v for k, v in bundle.items() if k in allowed}


def write_identity(bundle, secret, out_dir):
    """Persist the safe bundle and the root-only secret as separate files."""
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    os.chmod(out, 0o700)
    bundle_path = out / "enrollment-bundle.json"
    secret_path = out / "enrollment-secret.bin"
    bundle_path.write_text(json.dumps(safe_bundle(bundle), sort_keys=True, indent=2) + "\n")
    os.chmod(bundle_path, 0o600)
    secret_path.write_bytes(secret)
    os.chmod(secret_path, 0o600)
    return str(bundle_path), str(secret_path)


def first_boot_identity(instance_id, node_label, policy, rng=None, keypair=None,
                        rng_salt=None):
    """Pure orchestration of first-boot identity; deterministic when deps injected."""
    keypair = keypair or OpenSSLKeyPair()
    scoped = policy.get("policy", policy)
    secret = generate_secret(scoped["first_boot"]["generate_secret_bits"], rng)
    salt = generate_salt(scoped["first_boot"]["generate_salt_bytes"], rng_salt or rng)
    node_id = derive_node_id(instance_id)
    bundle, digest, secret = build_preregistration_bundle(
        node_label, salt, secret, keypair, policy,
    )
    bundle["node_id"] = node_id
    assert_bundle_has_no_secret(bundle, secret, salt)
    return {"node_id": node_id, "secret": secret, "salt": salt,
            "digest": digest, "bundle": safe_bundle(bundle)}


def _load_policy(path):
    return json.loads(Path(path).read_text())


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    boot = sub.add_parser("first-boot", help="generate local identity and digest-only bundle")
    boot.add_argument("--policy", required=True)
    boot.add_argument("--instance-id", required=True)
    boot.add_argument("--node-label", required=True)
    boot.add_argument("--out-dir", default="/var/lib/carbon-node/enrollment")
    args = parser.parse_args(argv)

    policy = _load_policy(args.policy)
    identity = first_boot_identity(args.instance_id, args.node_label, policy)
    bundle_path, _ = write_identity(identity["bundle"], identity["secret"], args.out_dir)
    # stdout carries only public metadata; never the secret, key or digest-only
    # bundle contents (the operator reads the local 0600 file out of band).
    print(json.dumps({
        "node_id": identity["node_id"],
        "approved_key_fingerprint": identity["bundle"]["approved_key_fingerprint"],
        "bundle_path": bundle_path,
        "contract_ref": identity["bundle"]["contract_ref"],
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
