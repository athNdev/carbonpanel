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
MAX_SECRET_BYTES = 64
MAX_SALT_BYTES = 64
MAX_SPKI_BYTES = 4096
MAX_CSR_PRE_BYTES = 16384
MAX_CSR_PRE_SIGNATURE_BYTES = 64
MAX_BUNDLE_BYTES = 64 * 1024
MAX_KEY_BYTES = 64 * 1024
MAX_TEXT_BYTES = 1024
MAX_NODE_LABEL_BYTES = 256
MAX_BOOTSTRAP_SCOPES = 8

# RFC 8410 fixes the DER SubjectPublicKeyInfo prefix and algorithm OID for
# Ed25519. A stored key or CSR with any other algorithm is refused before it is
# asked to verify anything.
ED25519_SPKI_PREFIX = bytes.fromhex("302a300506032b6570032100")
ED25519_SPKI_BYTES = len(ED25519_SPKI_PREFIX) + 32
ED25519_OID = bytes.fromhex("2b6570")
ED25519_SIGNATURE_BYTES = 64
IDENTITY_FILES = ("node.key", "enrollment-bundle.json", "enrollment-secret.bin")


class KeyPair:
    """Minimal interface: Ed25519 SPKI, SHA-256 fingerprint, sign()/verify()."""

    algorithm = ED25519

    def spki_der(self):
        raise NotImplementedError

    def fingerprint(self):
        return hashlib.sha256(self.spki_der()).hexdigest()

    def csr_pre(self, node_id):
        raise NotImplementedError

    def verify_csr(self, csr_pre):
        """Assert CSR_PRE is a self-signed request for this key's public key."""
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

    def verify_csr(self, csr_pre):
        # Offline fixtures do not carry DER; OpenSSLKeyPair owns the real parse,
        # self-signature and approved-key assertions.
        if not isinstance(csr_pre, bytes) or not csr_pre:
            raise ValueError("enrollment CSR_PRE is missing")

    def sign(self, context):
        return hashlib.sha256(b"SYNTHETIC-SIG::" + self._label + b"::" + context).digest()

    def verify(self, context, signature):
        return self.sign(context) == signature


def _der_tlv(data, offset, limit):
    """Read one canonical DER tag/length/value header inside data[:limit]."""
    if offset < 0 or offset + 2 > limit:
        raise ValueError("truncated DER header")
    tag = data[offset]
    first = data[offset + 1]
    pos = offset + 2
    if first & 0x80:
        count = first & 0x7f
        if count == 0 or count > 4 or pos + count > limit:
            raise ValueError("invalid DER length")
        length = int.from_bytes(data[pos:pos + count], "big")
        if length < 0x80 or data[pos] == 0:
            # DER requires the shortest long-form length: no redundant leading
            # zero and never a long form for a value below 0x80.
            raise ValueError("non-minimal DER length")
        pos += count
    else:
        length = first
    end = pos + length
    if end > limit:
        raise ValueError("truncated DER value")
    return tag, pos, length, end


def _parse_csr_pre(csr_pre):
    """Return the signed TBS bytes, signature OID and signature of a PKCS#10 DER."""
    if not isinstance(csr_pre, bytes) or not csr_pre:
        raise ValueError("enrollment CSR_PRE is missing")
    tag, content, _, end = _der_tlv(csr_pre, 0, len(csr_pre))
    if tag != 0x30:
        raise ValueError("enrollment CSR_PRE is not a PKCS#10 SEQUENCE")
    if end != len(csr_pre):
        raise ValueError("enrollment CSR_PRE is not a single valid DER PKCS#10 request")
    info_tag, _, _, info_end = _der_tlv(csr_pre, content, end)
    if info_tag != 0x30:
        raise ValueError("enrollment CSR_PRE certification request info is malformed")
    tbs = csr_pre[content:info_end]
    alg_tag, alg_content, _, alg_end = _der_tlv(csr_pre, info_end, end)
    if alg_tag != 0x30:
        raise ValueError("enrollment CSR_PRE signature algorithm is malformed")
    oid_tag, oid_content, _, oid_end = _der_tlv(csr_pre, alg_content, alg_end)
    if oid_tag != 0x06:
        raise ValueError("enrollment CSR_PRE signature algorithm is malformed")
    if oid_end != alg_end:
        # RFC 8410 requires absent AlgorithmIdentifier parameters for Ed25519;
        # the shared Go authority rejects a non-empty parameters field.
        raise ValueError(
            "enrollment CSR_PRE signature algorithm parameters are not permitted",
        )
    signature_algorithm = csr_pre[oid_content:oid_end]
    sig_tag, sig_content, sig_length, sig_end = _der_tlv(csr_pre, alg_end, end)
    if sig_tag != 0x03 or sig_end != end or sig_length < 2 or csr_pre[sig_content] != 0:
        raise ValueError("enrollment CSR_PRE signature is malformed")
    return tbs, signature_algorithm, csr_pre[sig_content + 1:sig_end]


class OpenSSLKeyPair(KeyPair):
    """An Ed25519 key stored in the root-only guest identity directory."""

    def __init__(self, private_key_path):
        self._priv = Path(private_key_path)
        done = subprocess.run(
            ["openssl", "pkey", "-in", str(self._priv), "-pubout", "-outform", "DER"],
            capture_output=True,
        )
        if done.returncode != 0:
            raise ValueError("stored node key is not a readable private key")
        if (len(done.stdout) != ED25519_SPKI_BYTES
                or not done.stdout.startswith(ED25519_SPKI_PREFIX)):
            raise ValueError("stored node key is not an Ed25519 key")
        self._pub_der = done.stdout

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

    def verify_csr(self, csr_pre):
        """Parse PKCS#10, verify its self-signature and bind the approved key.

        This mirrors the shared authority: x509.ParseCertificateRequest,
        CheckSignature, Ed25519-only public key and MarshalPKIXPublicKey equal
        to the approved SPKI. No reserialized CSR bytes are compared.
        """
        tbs, signature_algorithm, signature = _parse_csr_pre(csr_pre)
        if signature_algorithm != ED25519_OID:
            raise ValueError("enrollment CSR_PRE signature algorithm is not Ed25519")
        if len(signature) != ED25519_SIGNATURE_BYTES:
            raise ValueError("enrollment CSR_PRE signature is not Ed25519-sized")
        with tempfile.TemporaryDirectory() as tmp:
            csr = Path(tmp) / "csr.der"
            csr.write_bytes(csr_pre)
            pub = subprocess.run(
                ["openssl", "req", "-inform", "DER", "-in", str(csr), "-noout", "-pubkey"],
                capture_output=True,
            )
        if pub.returncode != 0:
            raise ValueError("enrollment CSR_PRE public key cannot be read")
        spki = subprocess.run(
            ["openssl", "pkey", "-pubin", "-outform", "DER"],
            input=pub.stdout, capture_output=True,
        )
        if spki.returncode != 0:
            raise ValueError("enrollment CSR_PRE public key algorithm is not recognized")
        if spki.stdout != self._pub_der:
            raise ValueError("enrollment CSR_PRE public key does not match the approved key")
        # The CSR key is the approved key, so this also proves the approved key
        # signed the request. OpenSSL 3.0's `req -verify` exit status is not
        # reliable, so verify the extracted TBS bytes directly instead.
        if not self.verify(tbs, signature):
            raise ValueError(
                "enrollment CSR_PRE self-signature does not verify with the approved key",
            )

    def sign(self, context):
        with tempfile.TemporaryDirectory() as tmp:
            ctx = Path(tmp) / "context"
            sig = Path(tmp) / "signature"
            ctx.write_bytes(context)
            subprocess.run(
                ["openssl", "pkeyutl", "-sign", "-inkey", str(self._priv),
                 "-rawin", "-in", str(ctx), "-out", str(sig)],
                check=True, capture_output=True,
            )
            return sig.read_bytes()

    def verify(self, context, signature):
        with tempfile.TemporaryDirectory() as tmp:
            pub = Path(tmp) / "public.der"
            ctx = Path(tmp) / "context"
            sig = Path(tmp) / "signature"
            pub.write_bytes(self._pub_der)
            ctx.write_bytes(context)
            sig.write_bytes(signature)
            done = subprocess.run(
                ["openssl", "pkeyutl", "-verify", "-pubin", "-keyform", "DER",
                 "-inkey", str(pub), "-rawin", "-in", str(ctx), "-sigfile", str(sig)],
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
    """Verify the saved CSR_PRE and its detached proof with the stored key."""
    if getattr(keypair, "algorithm", None) != ED25519:
        raise ValueError("enrollment verification is Ed25519-only")
    if not isinstance(csr_pre, bytes) or not csr_pre:
        raise ValueError("enrollment CSR_PRE is missing")
    if csr_pre.lstrip().startswith(b"{"):
        raise ValueError(
            "enrollment CSR_PRE is legacy signed JSON, not PKCS#10 DER; "
            "operator re-provisioning is required with no automatic migration",
        )
    keypair.verify_csr(csr_pre)
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
    expected = _policy_fields(policy)
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
    bundle["bootstrap_scopes"] = list(expected["bootstrap_scopes"])
    bundle["object_id_binding"] = expected["object_id_binding"]
    bundle["contract_ref"] = expected["contract_ref"]
    bundle["contract_is_shared"] = expected["contract_is_shared"]
    bundle["fork_allowed"] = expected["fork_allowed"]
    # A newly written record must pass the same policy checks as a reused one.
    _validate_bundle_schema(bundle, expected)
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
    # Validate the same strict secret/salt bounds that saved-identity reuse
    # enforces BEFORE consuming entropy, so generation can never persist a
    # record that load_identity() rejects on the next boot.
    secret_bytes, salt_bytes = _identity_sizes(policy)
    random_bytes = generate_secret(secret_bytes * 8, rng)
    # The shared ClaimEnrollmentRequest carries the secret in a protobuf string.
    secret = base64.urlsafe_b64encode(random_bytes).rstrip(b"=")
    salt = generate_salt(salt_bytes, rng_salt or rng)
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


def _bounded_text(value, name, max_bytes, allow_empty=False):
    """Require a bounded UTF-8 string without NUL bytes."""
    if not isinstance(value, str) or (not value and not allow_empty):
        raise ValueError("%s must be a non-empty string" % name)
    if "\x00" in value:
        raise ValueError("%s must not contain NUL" % name)
    try:
        raw = value.encode("utf-8")
    except UnicodeEncodeError:
        raise ValueError("%s must be UTF-8" % name) from None
    if len(raw) > max_bytes:
        raise ValueError("%s exceeds its %d-byte bound" % (name, max_bytes))
    return value


def _read_bounded(path, max_bytes, name):
    """Read at most max_bytes from a file, refusing an oversized value."""
    with open(path, "rb") as handle:
        data = handle.read(max_bytes + 1)
    if len(data) > max_bytes:
        raise ValueError("%s exceeds its size bound" % name)
    return data


def _policy_fields(policy):
    """The contract fields every saved or newly written record must match."""
    if policy is None:
        raise ValueError("enrollment policy is required to validate saved identity")
    if not isinstance(policy, dict):
        raise ValueError("enrollment policy must be a JSON object")
    scoped = policy.get("policy", policy)
    if not isinstance(scoped, dict):
        raise ValueError("enrollment policy must be a JSON object")
    contract_ref = _bounded_text(
        policy.get("contract_ref"), "enrollment policy contract_ref", MAX_TEXT_BYTES,
    )
    binding = _bounded_text(
        policy.get("object_id_binding", scoped.get("object_id_binding")),
        "enrollment policy object_id_binding", MAX_TEXT_BYTES,
    )
    scopes = policy.get("bootstrap_scopes", scoped.get("bootstrap_scopes"))
    if not isinstance(scopes, list) or not scopes or len(scopes) > MAX_BOOTSTRAP_SCOPES:
        raise ValueError("enrollment policy bootstrap_scopes must be a bounded non-empty list")
    for scope in scopes:
        _bounded_text(scope, "enrollment policy bootstrap scope", MAX_TEXT_BYTES)
    if list(scopes) != list(BOOTSTRAP_SCOPES):
        raise ValueError("enrollment policy bootstrap_scopes are not the bounded enrollment scopes")
    if policy.get("contract_is_shared") is not True:
        raise ValueError("enrollment policy contract_is_shared must be true")
    if policy.get("fork_allowed") is not False:
        raise ValueError("enrollment policy fork_allowed must be false")
    return {
        "contract_ref": contract_ref,
        "object_id_binding": binding,
        "bootstrap_scopes": list(scopes),
        "contract_is_shared": True,
        "fork_allowed": False,
    }


def _identity_sizes(policy):
    """Expected secret and salt byte counts from the first-boot policy."""
    scoped = policy.get("policy", policy) if isinstance(policy, dict) else None
    first_boot = scoped.get("first_boot") if isinstance(scoped, dict) else None
    if not isinstance(first_boot, dict):
        raise ValueError("enrollment policy first_boot sizes are missing")
    try:
        secret_bits = int(first_boot.get("generate_secret_bits"))
        salt_bytes = int(first_boot.get("generate_salt_bytes"))
    except (TypeError, ValueError):
        raise ValueError("enrollment policy secret/salt size is invalid") from None
    if secret_bits < 256 or secret_bits > MAX_SECRET_BYTES * 8 or secret_bits % 8:
        raise ValueError("enrollment policy secret size is invalid")
    if salt_bytes < 16 or salt_bytes > MAX_SALT_BYTES:
        raise ValueError("enrollment policy salt size is invalid")
    return secret_bits // 8, salt_bytes


def _require_sha256_hex(bundle, name):
    value = bundle.get(name)
    if (not isinstance(value, str) or len(value) != 64
            or any(char not in "0123456789abcdef" for char in value)):
        raise ValueError("enrollment bundle field %s must be a lowercase SHA-256 hex digest" % name)
    return value


def _validate_bundle_schema(bundle, expected):
    """Require the complete saved preregistration record and policy metadata."""
    if not isinstance(bundle, dict):
        raise ValueError("enrollment bundle must be a JSON object")
    missing = sorted(REQUIRED_BUNDLE_FIELDS - set(bundle))
    if missing:
        raise ValueError("enrollment bundle is missing required fields: %s" % ", ".join(missing))
    _bounded_text(
        bundle.get("node_id"), "enrollment bundle field node_id", MAX_TEXT_BYTES,
    )
    _bounded_text(
        bundle.get("node_label"), "enrollment bundle field node_label",
        MAX_NODE_LABEL_BYTES, allow_empty=True,
    )
    for name in ("object_id_binding", "contract_ref"):
        _bounded_text(bundle.get(name), "enrollment bundle field %s" % name, MAX_TEXT_BYTES)
    scopes = bundle.get("bootstrap_scopes")
    if not isinstance(scopes, list) or not scopes or len(scopes) > MAX_BOOTSTRAP_SCOPES:
        raise ValueError("enrollment bundle bootstrap_scopes must be a bounded non-empty list")
    for scope in scopes:
        _bounded_text(scope, "enrollment bundle bootstrap scope", MAX_TEXT_BYTES)
    if scopes != expected["bootstrap_scopes"]:
        raise ValueError(
            "enrollment bundle bootstrap_scopes do not match the checked-in policy",
        )
    for name in ("contract_ref", "object_id_binding"):
        if bundle[name] != expected[name]:
            raise ValueError(
                "enrollment bundle field %s does not match the checked-in policy" % name,
            )
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
    expected = _policy_fields(policy)
    secret_bytes, salt_bytes = _identity_sizes(policy)
    max_sizes = {
        "node.key": MAX_KEY_BYTES,
        "enrollment-bundle.json": MAX_BUNDLE_BYTES,
        "enrollment-secret.bin": 4 * ((secret_bytes + 2) // 3),
    }
    for name in IDENTITY_FILES:
        try:
            info = (out / name).lstat()
        except FileNotFoundError:
            raise ValueError(
                "incomplete saved enrollment identity (missing %s); operator review or "
                "re-provisioning is required and no automatic migration is performed" % name,
            ) from None
        if not stat.S_ISREG(info.st_mode) or (os.name == "posix" and info.st_mode & 0o077):
            raise ValueError("enrollment identity files must be private regular files")
        if info.st_size > max_sizes[name]:
            raise ValueError("enrollment identity file %s exceeds its size bound" % name)
    data = _read_bounded(out / "enrollment-bundle.json", MAX_BUNDLE_BYTES, "enrollment bundle")
    try:
        bundle = json.loads(data.decode("utf-8"))
    except (UnicodeDecodeError, ValueError):
        raise ValueError("enrollment bundle is not valid JSON") from None
    _validate_bundle_schema(bundle, expected)
    secret = _read_bounded(
        out / "enrollment-secret.bin", max_sizes["enrollment-secret.bin"],
        "enrollment secret",
    )
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
    # Fail before key generation or any staged write when the policy sizes are
    # unusable; first_boot_identity repeats this check for direct callers.
    _identity_sizes(policy)
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
