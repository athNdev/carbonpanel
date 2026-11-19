# OSS-378 signed-archive end-to-end validation (2026-11-19)

This record closes the gap left by the earlier digest-only pin verification in
[RETRIEVAL-EVIDENCE.md](RETRIEVAL-EVIDENCE.md): the **actual upstream tarball
bytes** were fetched into an owned disposable path, rehashed locally and matched
to the GPG-signed `SHA256SUMS` and the checked-in pins for **both** architectures.
The canonical layout and version metadata inside both root filesystems were
inspected. This is byte-level artifact evidence. It is **not** guest-boot
evidence: the external runtime blocker below is unchanged.

## Owned disposable path and commands

All fetched bytes were staged only under the task-owned disposable directory
`%TEMP%\opencode\lxc-signed-archive-20261119\` (Windows), outside the repository.
No artifact bytes were committed. The verifier was run from this worktree at
HEAD `9e81679b` with:

```
python packaging/node-lxc/node_lxc_artifact.py verify-signed-archive --arch amd64 --work-dir <owned-path>
python packaging/node-lxc/node_lxc_artifact.py verify-signed-archive --arch arm64 --work-dir <owned-path>
```

`verify-signed-archive` refuses before any I/O for a synthetic profile, a
placeholder/unverified digest or a moving URL; in staged mode it verifies the
local control-file digests, verifies the detached GPG signature of `SHA256SUMS`
against the pinned key fingerprint in an ephemeral GNUPGHOME, matches the signed
sum to the pin, rehashes the **full downloaded tarball bytes**, checks the pinned
content length, and inspects `etc/os-release`/`usr/lib/os-release` and the LXD
`metadata.yaml` from inside the tarballs. `--fetch` performs the direct HTTPS GET
of all five files into the owned path first; it was smoke-tested separately (see
below).

## Control files (direct GET from the dated release)

Source: `https://cloud-images.ubuntu.com/releases/noble/release-20261017/`.
The moving `/release/` symlink was never used as a pin.

| File | Bytes (GET) | Local SHA-256 | Pinned SHA-256 |
| --- | --- | --- | --- |
| `SHA256SUMS` | 7843 | `0449a33b78dddb301a3ce2b80398984bc3e1f7880e27b86a31e58bdcbd3d3816` | same |
| `SHA256SUMS.gpg` | 833 | `68aae716a548e466e58afddd8b767c54c791f40f1d20d1d12e27c471edb535e5` | same |
| signing key (`keyserver.ubuntu.com`, fingerprint `0xD2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81`) | 3325 | `927138de292fac4d861105a052cf56e9c5921987da385c844d88f34d19b39ab0` | same |

GPG (2.4.9) result, both architectures:

```
gpg: Good signature from "UEC Image Automatic Signing Key <cdimage@ubuntu.com>" [unknown]
primary key fingerprint D2EB 4462 6FDD C30B 513D 5BB7 1A5D 6C4C 7DB8 7C81
```

The key is not locally certified; identity rests on the documented Canonical
image-signing key fingerprint and the matching signature issuer. The verifier
also requires the `VALIDSIG` status record to carry that exact fingerprint.

## Per-architecture byte and layout results

| Check | amd64 | arm64 |
| --- | --- | --- |
| Root tarball bytes downloaded | 227213232 | 216215656 |
| Local SHA-256 of full root tarball | `ec6972af8ea2bd05c55299d204e5ae3f91769b84cfaec7395639d236a39f9a03` | `b68ef0444ca56ce4f00ae7e8ccfb0c553ffbc92e4a1b14ed7dd8aa18c6d68652` |
| Signed `SHA256SUMS` entry | same | same |
| Checked-in `expected_sha256` pin | same | same |
| Pinned content length | 227213232 | 216215656 |
| LXD metadata bytes | 412 | 408 |
| LXD metadata SHA-256 (local = signed = pin) | `dcfdfaf5239f2e1630540c091fd38a9f1e6cdabbaff267041a11ad0a5dcb35b2` | `6fe48e77a3fa52d81f3e5a84d3e83b6c96c16f4824bd49def8efe784529963bf` |
| Rootfs members | 47271 | 47170 |
| `os-release` | `ID=ubuntu`, `VERSION_ID=24.04`, `VERSION_CODENAME=noble` | same |
| LXD `metadata.yaml` | `architecture: x86_64`, `os: ubuntu`, `release: noble`, `description: Ubuntu 24.04 LTS server (20261017)` | `architecture: aarch64` (other fields identical) |
| Top-level layout | `bin etc lib sbin usr var` plus usrmerge/boot/dev/home/lib64/media/mnt/opt/proc/root/run/snap/srv/sys/tmp and `*.usr-is-merged` markers | same |

Ubuntu 24.04 is usrmerged: `etc/os-release` is a symlink, so the verifier reads
the regular file at `usr/lib/os-release`. Canonical directory markers
`bin.usr-is-merged`, `lib.usr-is-merged` and `sbin.usr-is-merged` are present and
accepted. LXD reports architecture in its own namespace (`x86_64`/`aarch64`),
mapped to `amd64`/`arm64` for the pin comparison.

Both invocations returned `status: ok`, `gpg_signature: GOOD`, with
`evidence_scope` explicitly stating that byte verification is **not** live
import/boot evidence.

## `--fetch` path

The self-contained fetch mode was exercised for arm64 into a second owned
disposable directory (`%TEMP%\opencode\lxc-signed-fetch-20261119\`). The tool
directly GET-ed and recorded: `SHA256SUMS` (7843 bytes), `SHA256SUMS.gpg` (833
bytes), the signing key (3325 bytes, from the keyserver URL above), the arm64
root tarball (216215656 bytes) and the arm64 LXD metadata tarball (408 bytes),
each from the exact pinned URL. The result was `status: ok`, `fetch_mode: true`,
`gpg_signature: GOOD`, with the root SHA-256
`b68ef0444ca56ce4f00ae7e8ccfb0c553ffbc92e4a1b14ed7dd8aa18c6d68652` and LXD
SHA-256 `6fe48e77a3fa52d81f3e5a84d3e83b6c96c16f4824bd49def8efe784529963bf`,
plus the same Ubuntu 24.04 `os-release` and `aarch64` LXD metadata as the staged
run. This proves the fetch code path, not a second boot claim.

## Fail-closed negative evidence (offline fixtures)

The offline suite (22/22 passing, see `../tests/test_node_lxc.py`) uses real
ephemeral GPG keys — signature success is never mocked. Signed-archive fixtures
prove the following refusals at the new command seam:

- tampered root tarball bytes -> `UPSTREAM_DIGEST_MISMATCH`;
- modified `SHA256SUMS` -> `CONTROL_FILE_DIGEST_MISMATCH`, and with the control
  digest updated -> `SIGNATURE_INVALID` (real gpg rejection);
- wrong signing key -> `SIGNATURE_INVALID` (fingerprint mismatch);
- synthetic profile, placeholder digest, unverified digest, moving `/release/`
  URL and missing signature evidence -> the corresponding provenance refusal
  before any tarball inspection;
- wrong guest version in `os-release` -> `METADATA_VERSION_MISMATCH`;
- wrong LXD architecture -> `METADATA_ARCH_MISMATCH`;
- missing staged files -> `MISSING_ARCHIVE_FILE`.

## Real external blocker — guest boot NOT qualified

No usable LXC runtime exists in a task-owned unprivileged fixture here, and none
can be installed without host configuration changes or elevation:

- Windows host: `lxc`, `lxc-create`, `lxc-copy`, `pct` and `systemd-nspawn` are
  absent; there is no `/dev/kvm`. No privileged provisioning is available.
- The task-owned WSL 2 Ubuntu distribution reports kernel
  `6.6.87.2-microsoft-standard-WSL2` with **no `/dev/kvm`** and no `lxc`/`pct`/
  `systemd-nspawn` binaries; installing them would require package installation
  (root/elevation) and WSL/host configuration changes, which are out of scope.

Therefore no clean LXC import, first-boot identity run inside a real guest,
nested daemon start, effective kernel-enforcement observation or gameplay check
was performed. **Signed-archive byte verification is not boot qualification.**
The exact blocker is preserved in [QUALIFICATION.md](QUALIFICATION.md),
[docs/node-lxc-mode.md](../../../docs/node-lxc-mode.md), ADR 0036 and the
knowledge concept. No artifact publication, registry write, release or Carbon
Panel application release is part of this lane.
