# Unprivileged LXC / Proxmox rootfs artifact lane (OSS-378)

This directory builds and qualifies the **rootfs template artifact** for a
Carbon Panel managed node running inside an **unprivileged LXC** container on
Proxmox VE. It is a sibling of the host-native, dedicated-VM and portable
installer lanes and shares the common enrollment/control contract of
[ADR 0019](../../docs/adr/0019-node-control-contract.md). **Nothing here forks,
redefines or re-implements that contract.**

> **Qualification scope.** The upstream pins in `config/provenance.json` are
> **real, immutable, dated Ubuntu 24.04 rootfs artifacts** verified against a
> GPG-signed `SHA256SUMS` (see [retrieval evidence](qualification/RETRIEVAL-EVIDENCE.md)).
> Behavioral qualification, however, is exercised only by disposable offline
> fixtures using the explicitly synthetic `config/provenance.fixture.json`.
> No live host was provisioned, no global host config was changed, no image was
> imported or booted, and nothing was published. Real guest boot is
> [externally blocked](qualification/QUALIFICATION.md) on this host (no
> `/dev/kvm`, no LXC tooling); live import/boot evidence (M5) is **not** claimed.

## Why a separate lane

An unprivileged LXC guest shares the host kernel with every other container
and needs nested user namespaces, cgroup-v2 delegation and `/dev` nesting for a
rootless captive Docker daemon. That is a materially different isolation and
host-prerequisite story from the dedicated VM lane, so the mode is documented
separately in [docs/node-lxc-mode.md](../../docs/node-lxc-mode.md). The
decision is recorded in
[ADR 0036](../../docs/adr/0036-node-lxc-template.md).

## Contents

| Path | Purpose |
| --- | --- |
| `node_lxc_artifact.py` | Cross-platform CLI implementing build, verify, capability gate, first-boot identity, volume import/backup/restore and staged upgrade/rollback |
| `build-rootfs.sh`, `verify-artifact.sh`, `validate-provenance.sh` | Reproducible build, fail-closed verification and provenance-profile validation entry points |
| `qualification/RETRIEVAL-EVIDENCE.md` | Exact dated upstream URLs, digests, GPG signature evidence and retrieval method |
| `qualification/QUALIFICATION.md` | What is qualified, and the precise external guest-boot blocker |
| `capability-gate.sh` | Read-only nested-runtime capability gate; refuses unsupported hosts with exact reason codes |
| `first-boot-identity.sh` | Unique first-boot node identity and digest-only ADR0019 enrollment bundle |
| `volume.sh`, `upgrade.sh` | Persistent volume import/backup/recovery and artifact upgrade/rollback |
| `config/provenance.json` | REAL artifact profile: dated immutable upstream pins, signed-checksum evidence and platform matrix |
| `config/provenance.fixture.json` | SYNTHETIC fixture profile only; never usable for an artifact claim |
| `fixtures/rootfs-minimal/` | Synthetic, credential-free rootfs tree used by offline builds |
| `fixtures/capability-*.json` | Supported and unsupported host capability snapshots |
| `tests/test_node_lxc.py` | Offline fixture qualification suite (10 scenarios) |
| `run-fixtures.sh` | Runs the suite with no host, network, root or live daemon |

## Verification gates

- **Two provenance profiles.** `config/provenance.json` is the real artifact
  profile; `config/provenance.fixture.json` is synthetic and used only with
  `build --fixture`. `validate-provenance.sh --expect-profile artifact` accepts
  the real pins and refuses synthetic profiles, placeholder digests, unverified
  digests, missing signature evidence and moving/undated URLs. A fixture build
  refuses the artifact profile and vice versa.
- **Pinned provenance.** A fixture build accepts a source tree only when its
  canonical digest equals the pinned `fixture.manifest_sha256`. An artifact build
  requires `--base-tarball` whose canonical sha256 matches the dated image's
  `expected_sha256` from the signed upstream `SHA256SUMS`. Canonical digests and
  sizes normalize CRLF to LF, so the pin is identical on a Linux (LF) and
  Windows (CRLF) checkout; the same normalization covers `SHA256SUMS` and the
  detached signature.
- **Supported platform/version matrix.** `--platform` must be present in
  `platform_matrix`; versions use the reserved `runtime-vMAJOR.MINOR.PATCH`
  domain from ADR 0033 and are checked explicitly.
- **Checksums.** Every payload file plus `manifest.json` is listed in
  `SHA256SUMS`; verification recomputes all of them and refuses on any mismatch.
- **Signing.** An operator-supplied RSA key produces a detached SHA-256
  signature embedded in `signature.json`. Verification requires a public key
  and refuses unsigned artifacts, stale signatures, wrong keys and unsupported
  algorithms. Keys are never stored in the artifact or this repository.
- **SBOM.** A CycloneDX 1.5 document pins every component by purl, version and
  sha256 and binds the artifact payload digest. Verification refuses a missing,
  invalid, empty or non-binding SBOM.
- **No secrets in the template.** A content/field scan refuses private-key PEM
  markers and secret/token/password fields in both the source and the built
  artifact and in first-boot enrollment/identity metadata. The node's private
  key and enrollment secret are created at boot under `secrets/` on the
  persistent volume with mode `0600` and never enter the template.
- **Capability gate.** Unsupported hosts are refused with explicit reason
  codes (privileged LXC, user namespaces disabled, cgroup v1, missing
  controllers or MAC profile, missing `/dev/fuse` or `/dev/net/tun`, nested
  runtime not attested, visible host Docker socket/data root). The gate is
  read-only and never changes global host configuration.
- **Persistent data.** Import/backup restore verify checksums and signatures,
  reject path traversal and refuse to overwrite a non-empty volume unless the
  recovery action is explicit. Backups are controller-owned encrypted storage
  and legitimately contain the volume identity key; this is declared in
  `backup.json` and is distinct from a provisioning artifact.
- **Upgrade/rollback.** Artifacts are staged into two slots. An invalid or
  unsigned artifact is refused before any state change; rollback only switches
  to a verified previous slot.

## Run

```sh
sh run-fixtures.sh
```

All 14 scenarios pass at the recorded head (10 original offline qualification
scenarios, the LF/CRLF portability regression, and three real-pin profile
acceptance/refusal tests). See `qualification/QUALIFICATION.md` and the external
mode report for exact evidence and limits.
