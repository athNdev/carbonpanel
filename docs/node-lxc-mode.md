# Unprivileged LXC node mode

Carbon Panel managed nodes have two first-class deployment modes that share the
versioned [node-control contract](adr/0019-node-control-contract.md): a
**dedicated Linux VM** and a **host-native private daemon**. This page documents
the **unprivileged LXC / Proxmox rootfs** variant of a managed node, because its
isolation boundary and host prerequisites are materially different from the VM
mode and must not be described as equivalent.

The mode's artifact lane lives in
[`packaging/node-lxc/`](../packaging/node-lxc/README.md), and the decision is
recorded in [ADR 0036](adr/0036-node-lxc-template.md). Upstream pins are real
and signed (see below), but runtime behavior is qualified by **offline fixtures
only**: no live host was provisioned, no global host configuration was changed,
no image was imported or booted, and nothing was published.

## Isolation boundary — why this is not the VM mode

| Property | Dedicated VM mode | Unprivileged LXC mode (this page) |
| --- | --- | --- |
| Kernel | Own guest kernel | **Shared host kernel** with every other LXC and the Proxmox host |
| Identity | VM-backed disk/network/node identity | Container identity on the persistent LXC volume |
| Device boundary | Virtual devices | `/dev` nesting inside an unprivileged container; `/dev/fuse` and `/dev/net/tun` required |
| Nested runtime | Rootless captive daemon inside a full OS | Rootless captive daemon inside a shared-kernel container; conditional on host capabilities |
| Failure domain | Guest crash stays in the guest | Kernel panic or host misconfiguration affects all guests |
| Preflight | VM resources/console | **Nested-runtime capability gate**, refuse with an exact reason |

A container boundary is not VM-grade isolation. No claim of proven isolation is
made here: the gate verifies the *capability to run* the nested runtime, not
that a breakout is impossible. Operators requiring a stronger boundary should
use the dedicated VM mode.

## Host prerequisites and the capability gate

Rootless nested Docker inside an unprivileged LXC depends on kernel and
container-runtime capabilities that are **never assumed**. The gate is read-only
and produces a host-provided capability snapshot; it never edits sysctl, cgroup
or LXC configuration. A supported host must provide all of:

- an unprivileged LXC container (privileged nesting is refused outright);
- kernel >= 5.11 for overlay2 / fuse-overlayfs nested storage;
- unprivileged user namespaces with `clone` allowed;
- cgroup **v2** with `cpu`, `cpuacct`, `memory`, `pids`, `io` and `cpuset`
  delegated;
- seccomp plus either AppArmor or SELinux;
- an explicit nested-runtime attestation from the host;
- `/dev/fuse` and `/dev/net/tun` device nodes present in the container;
- no visible host Docker socket or host Docker data root.

Any missing prerequisite produces a distinct refusal code rather than a silent
degradation: `PRIVILEGED_LXC_REFUSED`, `NOT_UNPRIVILEGED_LXC`, `KERNEL_TOO_OLD`,
`USERNS_DISABLED`, `CGROUP_V2_REQUIRED`, `CGROUP_CONTROLLERS_MISSING`,
`SECCOMP_UNAVAILABLE`, `MAC_PROFILE_MISSING`, `NESTING_NOT_ATTESTED`,
`FUSE_DEVICE_MISSING`, `TUN_DEVICE_MISSING`, `HOST_DOCKER_SOCKET_VISIBLE` or
`HOST_DOCKER_DATA_VISIBLE`. A guest that fails the gate must fall back to the
portable Linux guest installer from the sibling installer lane.

The supported platform/version matrix and **real upstream pins** are in
[`packaging/node-lxc/config/provenance.json`](../packaging/node-lxc/config/provenance.json):
immutable, dated Ubuntu 24.04 root tarballs from the official Canonical Cloud
Images release `20261017`, with SHA-256 values read from a GPG-signed
`SHA256SUMS` and locally verified. Exact URLs, dates, checksum-file and
signature hashes, key fingerprint and retrieval method are recorded in
[retrieval evidence](../packaging/node-lxc/qualification/RETRIEVAL-EVIDENCE.md).
The full root and LXD tarballs for both architectures have since been
downloaded, rehashed against that signed manifest and inspected for canonical
layout and version metadata in an owned disposable path; see
[signed-archive evidence](../packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md).
Byte verification of an archive is still not guest-boot evidence. Synthetic
values exist only in the explicitly marked
[`provenance.fixture.json`](../packaging/node-lxc/config/provenance.fixture.json),
which artifact builds refuse. Live import/boot evidence (milestone M5) is still
required before any format is advertised, and requires an [authorized disposable fixture](#disposable-proxmox-qualification).

## Shared control contract — no fork

Enrollment, identity, authorization and the operation envelope come entirely
from [ADR 0019](adr/0019-node-control-contract.md) as implemented by PR 516 /
517 / 518. This mode adds **no** new RPC, no new credential type and no second
TLS stack. The artifact lane only produces the node-side, digest-only
pre-registration material that ADR 0019 defines: salt, `SHA-256(salt || S)`
digest, a `CSR_PRE` signed by the node's own key, the approved SPKI and
fingerprint. The controller never emits or stores the plaintext secret, and the
node's private key never leaves the persistent volume.

On first boot the guest generates a unique Ed25519 identity and a scoped
enrollment bundle under the persistent volume:

- `secrets/node.key.pem` and `secrets/enrollment-secret.hex` are mode `0600` and
  exist **only** on the persistent volume;
- `identity/` and `enrollment/` carry public metadata, the digest and the signed
  `CSR_PRE` — no private key, no token, no plaintext secret;
- re-running first boot without an explicit force refuses rather than silently
  churning node identity;
- the content guard rejects any template or metadata file containing a
  private-key PEM marker or a secret/token/password field.

## Artifact integrity

Each artifact carries pinned provenance, a platform/version matrix, `SHA256SUMS`
over every payload file, an operator detached RSA/SHA-256 signature and a
CycloneDX 1.5 SBOM that binds the payload digest and pins every component by
purl, version and sha256. Verification fails closed on checksum mismatch,
missing or stale signature, wrong key, missing/invalid/empty/non-binding SBOM or
secret material. Signing keys are supplied by the operator and are never
committed. Versions use the reserved `runtime-vMAJOR.MINOR.PATCH` domain from
[ADR 0033](adr/0033-release-tag-and-publication-policy.md); this lane creates no
registry destination or publication workflow.

## Persistent volume, backup and recovery

Server worlds and the node identity live on the persistent LXC volume, never in
the rootfs template. Import, backup and restore verify checksums and signatures,
reject path traversal and refuse to overwrite a non-empty volume unless the
recovery action is explicit. Backups are controller-owned encrypted storage and
legitimately include the volume identity key; they are not provisioning
artifacts and must not be placed in public artifact channels. Upgrade uses two
staged slots and only activates a slot after the candidate passes full
verification; an invalid or unsigned candidate is refused before any state
change, and rollback switches only to a verified previous slot.

## Disposable Proxmox qualification

The managed-node LXC rootfs remains a shared-kernel preview. Use
`packaging/node-lxc/node_lxc_artifact.py boot-qualify` to verify the pinned
upstream archive in a caller-owned work directory and record read-only Proxmox
prerequisite checks. The manual `.github/workflows/lxc-boot-qualification.yml`
runs the same probe and uploads the JSON report. The command returns
`BOOT_QUALIFICATION_BLOCKED` after successful archive verification. Finding
Proxmox tools on the machine establishes availability only.

Fixture qualification requires a separately authorized disposable Proxmox
target with isolated guest storage and networking. The fixture must bind the
imported managed-node artifact to its verified digest, confirm an unprivileged
guest with effective kernel confinement, and collect first and second boot
evidence. The guest must retain its identity across boots, enroll through the
shared controller boundary, and run a workload through its private nested
daemon. Record guest teardown and retain the evidence with the source commit
and platform. Accept only evidence observed at each boundary.

See [qualification status](../packaging/node-lxc/qualification/QUALIFICATION.md)
for the readiness command and the available evidence.

## Verification evidence and limits

Offline fixture qualification at the recorded head covers: signed build and
verification, unsigned/tampered/wrong-key/secret refusals, provenance and
platform gates, real-pin acceptance plus synthetic/placeholder/moving-URL
refusals, supported and seven unsupported capability snapshots, unique
first-boot identity with a verified `CSR_PRE` signature and no secret in
metadata, volume import/backup/restore with tamper refusal, and staged
upgrade/rollback with state preserved on refusal, plus eight real-GPG
signed-archive scenarios. The full upstream tarballs for both architectures were
downloaded, rehashed against the GPG-signed dated `SHA256SUMS` and inspected for
Ubuntu 24.04 layout/metadata; that is byte-level artifact evidence, not boot
evidence. See
[`packaging/node-lxc/README.md`](../packaging/node-lxc/README.md) and
[signed-archive evidence](../packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md)
for exact commands and results.

These fixtures prove refusal and state-transition behavior only. They do **not**
claim live container import/boot, effective kernel enforcement, gameplay
compatibility or deployment success. No merge, registry push or release is part
of this lane.
