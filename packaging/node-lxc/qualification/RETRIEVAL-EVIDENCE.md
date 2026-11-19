# Upstream pin retrieval evidence (OSS-378 LXC artifact lane)

Verified 2026-11-19. This records how the real, immutable, dated pins in
`../config/provenance.json` were obtained and checked. No web search was used;
all access was targeted direct HTTPS GET/HEAD against official Canonical hosts.

## Source

Primary release directory:
`https://cloud-images.ubuntu.com/releases/noble/release-20261017/` — the Ubuntu
24.04 LTS (Noble Numbat) immutable dated release `20261017`. The moving
`https://cloud-images.ubuntu.com/releases/noble/release/` symlink was
deliberately **not** used as a pin.

## Exact artifacts pinned

| Arch | Artifact (from the dated release) | SHA-256 | Content-Length | Last-Modified |
| --- | --- | --- | --- | --- |
| amd64 | `ubuntu-24.04-server-cloudimg-amd64-root.tar.xz` | `ec6972af8ea2bd05c55299d204e5ae3f91769b84cfaec7395639d236a39f9a03` | 227213232 | Sat, 17 Oct 2026 20:31:16 GMT |
| arm64 | `ubuntu-24.04-server-cloudimg-arm64-root.tar.xz` | `b68ef0444ca56ce4f00ae7e8ccfb0c553ffbc92e4a1b14ed7dd8aa18c6d68652` | 216215656 | Sat, 17 Oct 2026 20:31:28 GMT |

Companion LXD metadata tarballs, read from the same signed checksum file:

| Arch | Artifact | SHA-256 |
| --- | --- | --- |
| amd64 | `ubuntu-24.04-server-cloudimg-amd64-lxd.tar.xz` | `dcfdfaf5239f2e1630540c091fd38a9f1e6cdabbaff267041a11ad0a5dcb35b2` |
| arm64 | `ubuntu-24.04-server-cloudimg-arm64-lxd.tar.xz` | `6fe48e77a3fa52d81f3e5a84d3e83b6c96c16f4824bd49def8efe784529963bf` |

These digests are the exact lines of the GPG-signed `SHA256SUMS` for that dated
release. This record captures the original **pin-establishment** slice, where
HEAD responses supplied content lengths and modification times and the full
tarball bytes were not downloaded. That limitation was subsequently closed:
[the full root and LXD tarballs for both architectures were downloaded and
rehashed against the signed manifest](SIGNED-ARCHIVE-EVIDENCE.md).

## Signed checksum evidence

| File | URL | Bytes | SHA-256 |
| --- | --- | --- | --- |
| `SHA256SUMS` | `<dated-release>/SHA256SUMS` | 7843 | `0449a33b78dddb301a3ce2b80398984bc3e1f7880e27b86a31e58bdcbd3d3816` |
| `SHA256SUMS.gpg` | `<dated-release>/SHA256SUMS.gpg` | 833 | `68aae716a548e466e58afddd8b767c54c791f40f1d20d1d12e27c471edb535e5` |
| signing key | `https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xD2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81` | 3325 | `927138de292fac4d861105a052cf56e9c5921987da385c844d88f34d19b39ab0` |

Local verification (gpg 2.4.9): imported the signing key into a temporary
homedir, then `gpg --verify SHA256SUMS.gpg SHA256SUMS` returned exit 0 with
`Good signature from "UEC Image Automatic Signing Key <cdimage@ubuntu.com>"`.
Primary key fingerprint `D2EB 4462 6FDD C30B 513D 5BB7 1A5D 6C4C 7DB8 7C81`,
matching the signature issuer and the recorded pin. The key is not locally
certified; identity rests on the key fingerprint from Canonical's documented
image-signing key and the matching issuer in the signature packet.

Signature timestamp: `Sun Oct 18 07:33:10 2026 AUSEDT`. The control-file
SHA-256 values also independently match the sibling QCOW2 lane's recorded
evidence for the same dated release, corroborating the fetch.

## Retrieval method

1. Direct HTTPS GET of `SHA256SUMS`, `SHA256SUMS.gpg` and the documented signing
   key; hashed locally with SHA-256.
2. Direct HTTPS HEAD of both dated root tarballs for content length and
   Last-Modified (pin-establishment slice); the later full-byte GET and rehash
   is recorded in [SIGNED-ARCHIVE-EVIDENCE.md](SIGNED-ARCHIVE-EVIDENCE.md).
3. Local GPG verification of the detached signature against the imported key.
4. `expected_sha256` values copied from the signed checksum file, not from the
   directory listing or a third-party mirror.

No search engine, no live host provisioning, no registry write and no release
action was used.
