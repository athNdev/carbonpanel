# ADR 0036: Unprivileged LXC node template with a nested-runtime capability gate

Status: Accepted (offline-fixture qualified; no live import/boot evidence)

Date: 2026-11-18

## Context

OSS-368 defines two first-class managed deployment modes that share the
versioned node-control contract of ADR 0019: a host-native private daemon and a
dedicated Linux VM. It also calls for an **unprivileged LXC / Proxmox rootfs
template** to be documented separately because its isolation boundary and host
prerequisites differ from the VM mode. The QCOW2 VM, LXC and portable installer
artifacts are independent lanes with separate writers and no publish step.

A rootless captive Docker daemon inside an unprivileged LXC container shares the
host kernel with every other guest. It needs nested user namespaces, cgroup-v2
delegation, a MAC profile, `/dev/fuse` and `/dev/net/tun`, and an explicit
nested-runtime attestation. Proxmox does not grant these by default, and a
privileged container would broaden host privileges. Support therefore cannot be
assumed from a template alone.

## Decision

1. The unprivileged LXC mode is a **separately documented artifact lane** under
   `packaging/node-lxc/`, with user-facing prerequisites in
   `docs/node-lxc-mode.md`. It does not fork, redefine or mount the shared
   enrollment/control contract of ADR 0019. There is no new RPC, credential
   type, TLS stack or product release in this lane.
2. A **read-only nested-runtime capability gate** consumes a host-provided
   capability snapshot and refuses unsupported hosts with distinct reason codes
   (privileged LXC, user namespaces, cgroup version/controllers, seccomp, MAC
   profile, nesting attestation, device nodes, visible host Docker socket or
   data root). A refused guest uses the portable Linux guest installer. The gate
   never edits sysctl, cgroup or LXC configuration and never provisions a host.
3. The rootfs artifact is reproducible from **real, immutable, dated upstream
   pins** and a supported platform/version matrix. `config/provenance.json`
   pins official Ubuntu 24.04 root tarballs from release `20261017` by SHA-256
   values read from a GPG-signed `SHA256SUMS`, with exact URL, date,
   checksum/signature hashes, key fingerprint and retrieval evidence recorded.
   The moving `/release/` symlink is never a pin. Synthetic values live only in
   an explicitly marked fixture profile; artifact mode refuses synthetic
   profiles, placeholder digests, unverified pins, missing signature evidence
   and moving URLs. The artifact carries `SHA256SUMS` over every payload file,
   an operator-supplied detached RSA/SHA-256 signature and a CycloneDX 1.5 SBOM
   binding the payload digest and listing components by purl/version/license.
   Verification fails closed on any mismatch. Versions use the reserved
   `runtime-vMAJOR.MINOR.PATCH` domain of ADR 0033; this lane creates no
   registry destination or publication workflow.
4. **First-boot identity** is generated on the persistent volume, never baked
   into the template: a unique node key pair, a mode-`0600` private key and
   enrollment secret, and a digest-only ADR 0019 pre-registration bundle (salt,
   digest, `CSR_PRE`, approved SPKI/fingerprint). Re-running first boot without
   an explicit force refuses rather than churning identity. A content guard
   rejects private-key material and secret/token/password fields in the template
   and in enrollment/identity metadata.
5. Worlds and identity live on the **persistent volume**, never in the rootfs.
   Import, backup and restore verify integrity, reject path traversal and refuse
   to overwrite a non-empty volume unless the recovery action is explicit.
   Backups are controller-owned encrypted storage and legitimately carry the
   identity key; they are distinct from provisioning artifacts. Upgrade uses two
   staged slots and activates only a fully verified candidate; rollback switches
   only to a verified previous slot.
6. **No live host provisioning, global host configuration change, registry push
   or release** is part of this decision. Runtime behavior is qualified only by
   disposable offline fixtures; upstream pins are independently verified but
   that is not boot evidence. Real guest boot is externally blocked on this
   host (no `/dev/kvm`, no LXC tooling). Live clean import/boot evidence is a
   later milestone and is required before any format is advertised.

## Consequences

- Distinct isolation documentation prevents the shared-kernel container from
  being mistaken for VM-grade isolation. Operators needing a stronger boundary
  choose the dedicated VM mode.
- Unsupported hosts obtain an exact refusal reason instead of a silently
  degraded daemon, and are directed to the portable guest installer.
- The shared contract stays single-owner: no second enrollment scheme, no
  caller-declared fingerprint as an authentication factor, and no secret in any
  provisioning artifact or metadata response.
- Qualification is honest about its limits: fixtures prove refusals and state
  transitions only, never effective kernel enforcement, live import/boot or
  gameplay compatibility.

## Verification

The 14 offline fixture scenarios and exact evidence are reported in the
external mode report and `packaging/node-lxc/qualification/`. They cover signed
build and verification; unsigned, tampered, wrong-key, secret-material,
provenance-digest and platform refusals; real-pin acceptance plus
synthetic/placeholder/unverified/moving-URL refusals; supported and seven
unsupported capability snapshots; unique first-boot identity with a verified
`CSR_PRE` signature and no secret in metadata; volume import/backup/restore
with tamper refusal; and staged upgrade/rollback with state preserved on
refusal. Upstream pin verification is recorded separately in
`packaging/node-lxc/qualification/RETRIEVAL-EVIDENCE.md`; neither is live boot
evidence.
