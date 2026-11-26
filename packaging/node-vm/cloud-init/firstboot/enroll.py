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
import binascii
import hashlib
import json
import os
from pathlib import Path
import secrets
import stat
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

# Fields written by safe_bundle(); every saved identity must carry all of them.
BUNDLE_FIELDS = frozenset({
    "node_label", "enrollment_salt", "enrollment_secret_digest", "csr_pre",
    "csr_pre_signature", "approved_public_key_spki", "approved_key_fingerprint",
    "node_id", "bootstrap_scopes", "object_id_binding", "contract_ref",
    "contract_is_shared", "fork_allowed",
})
REQUIRED_BUNDLE_FIELDS = BUNDLE_FIELDS
BOOTSTRAP_SCOPES = ("BeginEnrollmentChallenge", "ClaimEnrollment")

# Bounds reject malformed or oversized records before any crypto work. The salt
# and secret bounds are overridden by the checked-in policy when it is loaded.
DEFAULT_SECRET_BYTES = 32
DEFAULT_SALT_BYTES = 16
MAX_SPKI_BYTES = 4096
MAX_CSR_PRE_BYTES = 16384
MAX_CSR_PRE_SIGNATURE_BYTES = 64


class KeyPair:
    """Minimal interface: Ed25519 SPKI, SHA-256 fingerprint, sign()/verify()."""

    algorithm = ED25519

    def spki_der(self):
        raise NotImplementedError

    def fingerprint(self):
        return hashlib.sha256(self.spki_der()).hexdigest()

    def csr_pre(self, node_id):
        raise NotImplementedError

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

    def csr_pre(self, node_id):
        return b"SYNTHETIC-CSR::" + node_id.encode("ascii")

    def sign(self, context):
        return hashlib.sha256(b"SYNTHETIC-SIG::" + self._label + b"::" + context).digest()

    def verify(self, context, signature):
        return self.sign(context) == signature


class OpenSSLKeyPair(KeyPair):
    """An Ed25519 key stored in the root-only guest identity directory."""

    def __init__(self, private_key_path):
        self._priv = Path(private_key_path)
        self._pub_der = subprocess.run(
            ["openssl", "pkey", "-in", str(self._priv), "-pubout", "-outform", "DER"],
            check=True, capture_output=True,
        ).stdout

    @classmethod
    def generate(cls, private_key_path):
        subprocess.run(
            ["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private_key_path)],
            check=True, capture_output=True,
        )
        os.chmod(private_key_path, 0o600)
        return cls(private_key_path)

    def spki_der(self):
        return self._pub_der

    def csr_pre(self, node_id):
        return subprocess.run(
            ["openssl", "req", "-new", "-key", str(self._priv), "-subj", "/CN=" + node_id,
             "-outform", "DER"],
            check=True, capture_output=True,
        ).stdout

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
            pub.write(self._pub_der)
            pub.flush()
            ctx.write(context)
            ctx.flush()
            sig.write(signature)
            sig.flush()
            done = subprocess.run(
                ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER", "-inkey", pub.name,
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
    if any(b"\x00" in parts[i] for i in (1, 2, 3, 5, 6)):
        raise ValueError("NUL byte in a text claim context field")
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
    _check_claim_context(context)
    return keypair.sign(context)


def verify_claim(keypair, context, signature):
    """Verify a fresh SIG_CLAIM over the exact shared context (Ed25519 only)."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment verification is Ed25519-only")
    _check_claim_context(context)
    return bool(keypair.verify(context, signature))


def verify_csr_pre(keypair, csr_pre, signature):
    """Verify the saved CSR_PRE proof with the stored owning Ed25519 key."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment verification is Ed25519-only")
    return bool(keypair.verify(csr_pre, signature))


def _check_claim_context(context):
    # The server verifies the exact context constructed from its stored fields.
    # The opaque nonce and channel bytes may contain NUL and are not parsed here.
    prefix = SIGNING_DOMAIN + b"\x00"
    if not isinstance(context, bytes) or not context.startswith(prefix):
        raise ValueError("malformed NUL-delimited claim context")
    if context[len(prefix):].startswith(b"{"):
        raise ValueError("canonical-JSON claim context refused; use shared NUL encoding")
    if context.count(b"\x00") < 7:
        raise ValueError("malformed NUL-delimited claim context")


def build_preregistration_bundle(node_id, node_label, salt, secret, keypair, policy):
    """Build the digest-only exportable bundle. S and the private key never enter it."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment key generation is Ed25519-only")
    digest = compute_digest(salt, secret)
    csr_pre = keypair.csr_pre(node_id)
    bundle = {
        "node_id": node_id,
        "node_label": node_label,
        "enrollment_salt": _b64(salt),
        "enrollment_secret_digest": digest,
        "approved_public_key_spki": _b64(keypair.spki_der()),
        "approved_key_fingerprint": keypair.fingerprint(),
        "csr_pre": _b64(csr_pre),
        "csr_pre_signature": _b64(keypair.sign(csr_pre)),
    }
    # The policy document nests the scoped fields under "policy"; accept either
    # shape so the same module works with the checked-in file and flat test data.
    scoped = policy.get("policy", policy)
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
    return {k: v for k, v in bundle.items() if k in BUNDLE_FIELDS}


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


def first_boot_identity(instance_id, node_label, policy, keypair, rng=None,
                        rng_salt=None):
    """Pure orchestration of first-boot identity; deterministic when deps injected."""
    scoped = policy.get("policy", policy)
    random_bytes = generate_secret(scoped["first_boot"]["generate_secret_bits"], rng)
    # The shared ClaimEnrollmentRequest carries the secret in a protobuf string.
    secret = base64.urlsafe_b64encode(random_bytes).rstrip(b"=")
    salt = generate_salt(scoped["first_boot"]["generate_salt_bytes"], rng_salt or rng)
    node_id = derive_node_id(instance_id)
    bundle, digest, secret = build_preregistration_bundle(
        node_id, node_label, salt, secret, keypair, policy,
    )
    assert_bundle_has_no_secret(bundle, secret, salt)
    return {"node_id": node_id, "secret": secret, "salt": salt,
            "digest": digest, "bundle": safe_bundle(bundle)}


def _load_policy(path):
    return json.loads(Path(path).read_text())


def _bounded_b64(bundle, name, min_bytes, max_bytes):
    """Decode one canonical, bounded standard-base64 bundle field."""
    value = bundle.get(name)
    if not isinstance(value, str) or not value:
        raise ValueError("enrollment bundle field %s must be a non-empty base64 string" % name)
    if len(value) > 4 * ((max_bytes + 2) // 3):
        raise ValueError("enrollment bundle field %s exceeds its %d-byte bound" % (name, max_bytes))
    try:
        raw = base64.b64decode(value, validate=True)
    except (binascii.Error, ValueError):
        raise ValueError("enrollment bundle field %s is not valid base64" % name) from None
    if not min_bytes <= len(raw) <= max_bytes:
        raise ValueError(
            "enrollment bundle field %s must decode to %d-%d bytes" % (name, min_bytes, max_bytes),
        )
    if _b64(raw) != value:
        raise ValueError("enrollment bundle field %s is not canonical base64" % name)
    return raw


def _bounded_b64url(secret, min_bytes, max_bytes):
    """Decode the root-only secret as canonical, bounded unpadded base64url."""
    if not isinstance(secret, bytes) or not secret:
        raise ValueError("enrollment secret must be non-empty unpadded base64url")
    if len(secret) > 4 * ((max_bytes + 2) // 3):
        raise ValueError("enrollment secret exceeds its %d-byte bound" % max_bytes)
    padded = secret + b"=" * (-len(secret) % 4)
    try:
        raw = base64.b64decode(padded, altchars=b"-_", validate=True)
    except (binascii.Error, ValueError):
        raise ValueError("enrollment secret is not valid unpadded base64url") from None
    if not min_bytes <= len(raw) <= max_bytes:
        raise ValueError("enrollment secret must decode to %d-%d bytes" % (min_bytes, max_bytes))
    if base64.urlsafe_b64encode(raw).rstrip(b"=") != secret:
        raise ValueError("enrollment secret is not canonical unpadded base64url")
    return raw


def _identity_sizes(policy):
    """Expected secret and salt byte counts from the first-boot policy."""
    if policy is None:
        return DEFAULT_SECRET_BYTES, DEFAULT_SALT_BYTES
    first_boot = policy.get("policy", policy).get("first_boot", {})
    secret_bits = int(first_boot.get("generate_secret_bits", DEFAULT_SECRET_BYTES * 8))
    salt_bytes = int(first_boot.get("generate_salt_bytes", DEFAULT_SALT_BYTES))
    if secret_bits < 256 or secret_bits % 8:
        raise ValueError("enrollment policy secret size is invalid")
    if salt_bytes < 16:
        raise ValueError("enrollment policy salt size is invalid")
    return secret_bits // 8, salt_bytes


def _require_sha256_hex(bundle, name):
    value = bundle.get(name)
    if (not isinstance(value, str) or len(value) != 64
            or any(char not in "0123456789abcdef" for char in value)):
        raise ValueError("enrollment bundle field %s must be a lowercase SHA-256 hex digest" % name)
    return value


def _validate_bundle_schema(bundle):
    """Require the complete saved preregistration record and safe metadata."""
    if not isinstance(bundle, dict):
        raise ValueError("enrollment bundle must be a JSON object")
    missing = sorted(REQUIRED_BUNDLE_FIELDS - set(bundle))
    if missing:
        raise ValueError("enrollment bundle is missing required fields: %s" % ", ".join(missing))
    if not isinstance(bundle.get("node_id"), str) or not bundle["node_id"]:
        raise ValueError("enrollment bundle field node_id must be a non-empty string")
    if not isinstance(bundle.get("node_label"), str):
        raise ValueError("enrollment bundle field node_label must be a string")
    for name in ("object_id_binding", "contract_ref"):
        if not isinstance(bundle.get(name), str) or not bundle[name]:
            raise ValueError("enrollment bundle field %s must be a non-empty string" % name)
    scopes = bundle.get("bootstrap_scopes")
    if (not isinstance(scopes, list) or not scopes
            or not all(isinstance(scope, str) and scope for scope in scopes)):
        raise ValueError("enrollment bundle bootstrap_scopes must be a non-empty string list")
    if scopes != list(BOOTSTRAP_SCOPES):
        raise ValueError("enrollment bundle bootstrap_scopes are not the bounded enrollment scopes")
    if bundle.get("contract_is_shared") is not True:
        raise ValueError("enrollment bundle contract_is_shared must be true")
    if bundle.get("fork_allowed") is not False:
        raise ValueError("enrollment bundle fork_allowed must be false")
    _require_sha256_hex(bundle, "enrollment_secret_digest")
    _require_sha256_hex(bundle, "approved_key_fingerprint")


def load_identity(out, node_id, node_label, policy=None):
    """Load and validate an already-published identity; never writes or regenerates."""
    if out.is_symlink() or not stat.S_ISDIR(out.stat().st_mode):
        raise ValueError("enrollment state must be a local directory")
    if os.name == "posix" and out.stat().st_mode & 0o077:
        raise ValueError("enrollment state directory must be private")
    for name in ("node.key", "enrollment-bundle.json", "enrollment-secret.bin"):
        info = (out / name).lstat()
        if not stat.S_ISREG(info.st_mode) or (os.name == "posix" and info.st_mode & 0o077):
            raise ValueError("enrollment identity files must be private regular files")
    try:
        bundle = json.loads((out / "enrollment-bundle.json").read_text())
    except ValueError:
        raise ValueError("enrollment bundle is not valid JSON") from None
    _validate_bundle_schema(bundle)
    secret = (out / "enrollment-secret.bin").read_bytes()
    secret_bytes, salt_bytes = _identity_sizes(policy)
    _bounded_b64url(secret, secret_bytes, secret_bytes)
    salt = _bounded_b64(bundle, "enrollment_salt", salt_bytes, salt_bytes)
    spki = _bounded_b64(bundle, "approved_public_key_spki", 1, MAX_SPKI_BYTES)
    csr_pre = _bounded_b64(bundle, "csr_pre", 1, MAX_CSR_PRE_BYTES)
    csr_pre_signature = _bounded_b64(
        bundle, "csr_pre_signature", 1, MAX_CSR_PRE_SIGNATURE_BYTES,
    )
    assert_bundle_has_no_secret(bundle, secret, salt)
    if bundle["node_id"] != node_id or bundle["node_label"] != node_label:
        raise ValueError("enrollment identity does not match guest state")
    keypair = OpenSSLKeyPair(out / "node.key")
    if (bundle["approved_key_fingerprint"] != keypair.fingerprint()
            or spki != keypair.spki_der()
            or bundle["enrollment_secret_digest"] != compute_digest(salt, secret)):
        raise ValueError("enrollment identity does not match guest state")
    if not verify_csr_pre(keypair, csr_pre, csr_pre_signature):
        raise ValueError("enrollment CSR_PRE proof does not verify with the stored key")
    return bundle


def ensure_identity(out, instance_id, node_label, policy):
    node_id = derive_node_id(instance_id)
    if out.exists() or out.is_symlink():
        return load_identity(out, node_id, node_label, policy)
    out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".enrollment-", dir=out.parent) as tmp:
        # Publish the key, secret, and bundle together. A failed first boot can
        # retry without replacing any already-published identity.
        staged = Path(tmp) / "identity"
        staged.mkdir(mode=0o700)
        keypair = OpenSSLKeyPair.generate(staged / "node.key")
        identity = first_boot_identity(instance_id, node_label, policy, keypair)
        write_identity(identity["bundle"], identity["secret"], staged)
        try:
            staged.rename(out)
        except OSError:
            if not out.exists():
                raise
            return load_identity(out, node_id, node_label, policy)
        return identity["bundle"]


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
    out = Path(args.out_dir)
    bundle = ensure_identity(out, args.instance_id, args.node_label, policy)
    # stdout carries only public metadata; never the secret, key or digest-only
    # bundle contents (the operator reads the local 0600 file out of band).
    print(json.dumps({
        "node_id": bundle["node_id"],
        "approved_key_fingerprint": bundle["approved_key_fingerprint"],
        "bundle_path": str(out / "enrollment-bundle.json"),
        "contract_ref": bundle["contract_ref"],
    }, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
