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
  * each VM gets a distinct node_id derived from its unique instance-id;
  * SIG_CLAIM uses the SHARED ADR0019/OSS-385 ClaimContextRaw encoding: NUL-
    delimited bytes over the SERVER-STORED nonce, enrollment expires_at and
    canonical proto capability names; Ed25519 only. Canonical JSON is refused.

The canonical byte encoder is reproduced by canonical_context() because guest
Python cannot import the Go package; byte-for-byte conformance is proven by a
golden generated from internal/noderuntime/enroll.ClaimContextRaw at PR532 head
fc998875. A production mount must delegate to the shared package, not add a
second encoder.
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
from datetime import timezone

# Re-export of the shared ADR 0019 / OSS-385 constants. The VM first boot is a
# guest-side Python generator (it cannot import the Go package inside a cloud
# image), so it reproduces the ONE byte-level encoder ClaimContextRaw exactly;
# byte-for-byte conformance is proven in fixtures against a golden produced by
# internal/noderuntime/enroll at PR532 head fc998875. Any future production
# mount must delegate the claim to the shared Go package, not a second encoder.
SIGNING_DOMAIN = b"carbon-panel-enroll-v1"
CONTEXT_PREFIX = SIGNING_DOMAIN  # backwards-compatible alias
PROTOCOL_VERSION = 1
ED25519 = "Ed25519"

# Proto enum names from node_control.proto, in declaration order. The shared
# typed ClaimContext converts enums to these names; no other spelling is valid.
CAPABILITY_NAMES = (
    "NODE_CAPABILITY_UNSPECIFIED",
    "NODE_CAPABILITY_ROOTLESS",
    "NODE_CAPABILITY_PRIVATE_SOCKET",
    "NODE_CAPABILITY_DEDICATED_ROOTS",
    "NODE_CAPABILITY_CONTAINERD_NAMESPACE",
    "NODE_CAPABILITY_CGROUP_V2",
    "NODE_CAPABILITY_PRIVILEGED_PORT",
    "NODE_CAPABILITY_STORAGE_WORKER",
    "NODE_CAPABILITY_DRAIN",
    "NODE_CAPABILITY_RESTART_REQUIRED",
)
FORBIDDEN_BUNDLE_FIELDS = frozenset({
    "enrollment_secret", "secret", "private_key", "privatekey", "token",
    "password", "passphrase", "secret_s", "s",
})


class KeyPair:
    """Minimal interface: Ed25519 SPKI, SHA-256 fingerprint, sign()/verify()."""

    algorithm = ED25519

    def spki_der(self):
        raise NotImplementedError

    def fingerprint(self):
        return hashlib.sha256(self.spki_der()).hexdigest()

    def sign(self, context):
        raise NotImplementedError

    def verify(self, context, signature):
        raise NotImplementedError


class FakeKeyPair(KeyPair):
    """Deterministic, clearly synthetic Ed25519-shaped key for offline fixtures."""

    def __init__(self, label="fixture-key", algorithm=ED25519):
        self._label = label.encode()
        self.algorithm = algorithm

    def spki_der(self):
        return b"SYNTHETIC-SPKI::" + self._label

    def sign(self, context):
        return hashlib.sha256(b"SYNTHETIC-SIG::" + self._label + b"::" + context).digest()

    def verify(self, context, signature):
        return self.sign(context) == signature


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

    def verify(self, context, signature):
        with tempfile.NamedTemporaryFile() as pub, tempfile.NamedTemporaryFile() as ctx, \
                tempfile.NamedTemporaryFile() as sig:
            subprocess.run(
                ["openssl", "pkey", "-pubin", "-inform", "DER", "-in",
                 str(self._pub_der), "-out", pub.name],
                check=True, capture_output=True,
            )
            ctx.write(context)
            ctx.flush()
            sig.write(signature)
            sig.flush()
            done = subprocess.run(
                ["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", pub.name,
                 "-rawin", "-in", ctx.name, "-sigfile", sig.name],
                capture_output=True,
            )
            return done.returncode == 0


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


def _as_bytes(value, name):
    if isinstance(value, bytes):
        return value
    if isinstance(value, str):
        return value.encode("utf-8")
    raise ValueError("%s must be bytes or UTF-8 str" % name)


def format_rfc3339_nano(expires_at):
    """expires_at.UTC().Format(time.RFC3339Nano) as UTF-8 bytes.

    Strings from the controller are already RFC3339Nano; a datetime is rendered
    in UTC with trailing fractional zeros trimmed, like Go's RFC3339Nano.
    """
    if hasattr(expires_at, "tzinfo"):
        text = expires_at.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
        if "." in text:
            head, fraction = text.split(".", 1)
            fraction = fraction.rstrip("0").rstrip("Z")
            text = head + ("." + fraction if fraction else "") + "Z"
        return text.encode("ascii")
    raw = _as_bytes(expires_at, "expires_at")
    try:
        raw.decode("ascii")
    except UnicodeDecodeError:
        raise ValueError("expires_at must be RFC3339Nano ASCII")
    if b"T" not in raw or not raw.endswith(b"Z"):
        raise ValueError("expires_at must be UTC RFC3339Nano ending in Z")
    return raw


def canonical_capability_names(capabilities):
    """Proto enum names in declaration order; no other spelling is accepted."""
    if not capabilities:
        raise ValueError("at least one canonical capability is required")
    names = list(capabilities)
    unknown = [name for name in names if name not in CAPABILITY_NAMES]
    if unknown:
        raise ValueError("unknown or non-canonical capability names: %s" % unknown)
    ranks = [CAPABILITY_NAMES.index(name) for name in names]
    if ranks != sorted(ranks):
        raise ValueError("capability names must be in proto declaration order")
    return names


def canonical_context(node_id, enrollment_id, challenge_id, server_nonce,
                      expires_at, capabilities, channel_context):
    """ONE byte-level encoder: shared ClaimContextRaw (NUL-delimited, no JSON).

    Exact layout (all separators a single 0x00, no trailing separator):

        "carbon-panel-enroll-v1" 0x00 node_id 0x00 enrollment_id 0x00
        challenge_id 0x00 server_nonce_raw 0x00 expires_at_RFC3339Nano 0x00
        comma_join(capability_names) 0x00 server_observed_channel_context_raw

    The nonce is ALWAYS the server-stored challenge nonce; there is no client
    nonce field. Capabilities are proto enum names in declaration order.
    """
    parts = [
        SIGNING_DOMAIN,
        _as_bytes(node_id, "node_id"),
        _as_bytes(enrollment_id, "enrollment_id"),
        _as_bytes(challenge_id, "challenge_id"),
        _as_bytes(server_nonce, "server_nonce"),
        format_rfc3339_nano(expires_at),
        ",".join(canonical_capability_names(capabilities)).encode("ascii"),
        _as_bytes(channel_context, "channel_context"),
    ]
    if any(b"\x00" in part for part in parts[1:7]):
        raise ValueError("NUL byte in a delimited claim context field")
    return b"\x00".join(parts)


def claim_from_challenge(keypair, node_id, enrollment_id, challenge,
                         capabilities, channel_context=None):
    """Build the challenge-bound fresh SIG_CLAIM for the enrolled VM.

    challenge is the server's BeginEnrollmentChallenge response: it carries the
    SERVER-STORED nonce, challenge_id and enrollment expires_at. Nothing here
    invents or accepts a client nonce.
    """
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment signing is Ed25519-only")
    channel = channel_context if channel_context is not None else challenge["channel_context"]
    context = canonical_context(
        node_id, enrollment_id, challenge["challenge_id"], challenge["server_nonce"],
        challenge["expires_at"], capabilities, channel,
    )
    return {
        "challenge_id": challenge["challenge_id"],
        "context": context,
        "fresh_claim_signature": sign_claim(keypair, context),
        "capabilities": list(capabilities),
        "channel_binding_evidence": _as_bytes(channel, "channel_context"),
        "protocol_version": PROTOCOL_VERSION,
    }


def sign_claim(keypair, context):
    """Fresh post-challenge signature by the operator-approved Ed25519 key only."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment signing is Ed25519-only")
    if not isinstance(context, bytes):
        raise ValueError("claim context must be raw bytes")
    if not context.startswith(SIGNING_DOMAIN + b"\x00"):
        raise ValueError("claim context is not a carbon-panel-enroll-v1 context")
    # The old canonical-JSON encoding is explicitly refused; its bytes cannot
    # verify against the shared ClaimContextRaw encoder.
    remainder = context[len(SIGNING_DOMAIN) + 1:]
    if remainder.startswith(b"{") or b'\x00{"' in context or b'"node_id"' in context:
        raise ValueError("canonical-JSON claim context refused; use shared NUL encoding")
    return keypair.sign(context)


def verify_claim(keypair, context, signature):
    """Verify a fresh SIG_CLAIM over the exact shared context (Ed25519 only)."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment verification is Ed25519-only")
    expected = canonical_context(*_claim_fields(context))
    if context != expected:
        raise ValueError("claim context does not match shared ClaimContextRaw encoding")
    return bool(keypair.verify(context, signature))


def _claim_fields(context):
    """Parse a NUL-delimited context into canonical_context() arguments."""
    fields = context.split(b"\x00")
    if len(fields) != 8 or fields[0] != SIGNING_DOMAIN:
        raise ValueError("malformed NUL-delimited claim context")
    return (fields[1], fields[2], fields[3], fields[4], fields[5],
            fields[6].decode("ascii").split(",") if fields[6] else [], fields[7])


def build_preregistration_bundle(node_label, salt, secret, keypair, policy):
    """Build the digest-only exportable bundle. S and the private key never enter it."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment key generation is Ed25519-only")
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
