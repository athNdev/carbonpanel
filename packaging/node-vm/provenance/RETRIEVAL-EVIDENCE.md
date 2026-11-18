# Base-image retrieval evidence (real pin)

Retrieved **2026-11-18** from official Canonical hosts by targeted direct HTTPS
GET/HEAD only (no web search, no third-party mirror). This is the evidence
behind `provenance/base-images.json`. The synthetic values remain exclusively
in `provenance/base-images.fixture.json`.

## Chosen pin

Ubuntu **24.04 LTS (Noble Numbat) released 20261017**. The dated directory
`https://cloud-images.ubuntu.com/releases/noble/release-20261017/` is
**immutable**; the moving `.../release/` symlink and the `/noble/current/`
daily tree are deliberately **not** used as pins.

| Arch | Immutable image URL | SHA256 (from signed SHA256SUMS) |
| --- | --- | --- |
| amd64 | `https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img` | `04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94` |
| arm64 | `https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-arm64.img` | `50e24e1fb79e89116e939d90aa7738f7af48c442fa1bc2510131e0b39c50348e` |

Both are QCOW2 UEFI/GPT bootable cloud images. The published directory listing
shows `Last modified 2026-10-17 20:31` and sizes 593M / 588M.

## Direct retrieval steps and results

1. `https://cloud-images.ubuntu.com/releases/noble/` (GET, 200) — directory
   listing includes `release-20261017/` ("Released Image") and the moving
   `release/` symlink. Chose the dated entry.
2. `https://cloud-images.ubuntu.com/releases/noble/release-20261017/SHA256SUMS`
   (GET, 200) — 7843 bytes; local SHA256 of the checksum file
   `0449a33b78dddb301a3ce2b80398984bc3e1f7880e27b86a31e58bdcbd3d3816`.
   The two image lines are the digests in the table above.
3. `https://cloud-images.ubuntu.com/releases/noble/release-20261017/SHA256SUMS.gpg`
   (GET, 200) — 833 bytes; local SHA256
   `68aae716a548e466e58afddd8b767c54c791f40f1d20d1d12e27c471edb535e5`.
   `gpg --list-packets`: signature packet algo 1, keyid `1A5D6C4C7DB87C81`,
   issuer fingerprint `D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`,
   signature created 2026-10-17.
4. Key fetched by that fingerprint from the official Ubuntu keyserver:
   `https://keyserver.ubuntu.com/pks/lookup?op=get&search=0xD2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`
   (GET, 200) — 3325 bytes, local SHA256
   `927138de292fac4d861105a052cf56e9c5921987da385c844d88f34d19b39ab0`.
   Imported into an isolated GPG home; identity reported as
   `UEC Image Automatic Signing Key <cdimage@ubuntu.com>`, primary fingerprint
   `D2EB 4462 6FDD C30B 513D 5BB7 1A5D 6C4C 7DB8 7C81`.
5. `gpg --verify SHA256SUMS.gpg SHA256SUMS` (gpg 2.4.9, isolated home) —
   `Good signature from "UEC Image Automatic Signing Key <cdimage@ubuntu.com>"`,
   exit 0. GPG warned the key is not locally certified; the fingerprint matched
   both the signature issuer and the keyserver key fetched above, so the
   signature is treated as verified for this pin.
6. HEAD on each immutable image URL returned HTTP 200 with
   `Content-Length: 621284352` (amd64) and `616522752` (arm64), last modified
   17 Oct 2026 20:31 GMT. **The image bytes were not downloaded.**

## What remains unverified

- No full image download, no SHA256 recomputation over the image bytes, and no
  real QCOW2 build. The recorded hash is the upstream published, signature-
  verified value; a real build must still fetch the image and recompute it.
- No live VM, hypervisor, host configuration, enrollment or deployment was
  touched. No registry push, release or publication.
- GPG key trust is fingerprint-based as described above, not a locally
  certified web-of-trust decision.
- Toolchain binaries (qemu-img/xorriso) remain version-pinned in
  `config/build-config.json` but are not separately signature-verified here.
