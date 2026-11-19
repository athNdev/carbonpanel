# Host-native Linux private daemon mode (OSS-376)

Status: implementation with disposable fixture evidence only. No live host,
no production rollout, no global host configuration change and no Carbon Panel
application release. This document is the mode-specific prerequisite/isolation
contract; the shared enrollment/control contract is ADR 0019 and
`proto/carbonpanel/v1/node_control.proto`.

## Scope

A dedicated Docker daemon process is owned and supervised by a node agent on a
Linux host. The daemon has a **private socket**, **private data and exec roots**
and a **private bridge / network namespace**. It is rootless where the host is
qualified, with a hardened dedicated rootful service as the explicit
compatibility path. There is **no preinstalled/shared Docker daemon fallback**:
an unqualified host fails closed with a precise refusal code, and a host engine
that happens to be installed is never used as a substitute.

This mode consumes the common enrollment/control contract; it does not extend
it. No proto field, RPC, shared DB column or shared auth middleware is changed
by this mode.

## Isolation invariants (all enforced by named gates)

| Invariant | Enforced by | Refusal code |
| --- | --- | --- |
| Dedicated daemon process, not a host engine | host engine presence is recorded only; selecting its socket/root is refused by the collision gates and `NO_SHARED_DAEMON_FALLBACK` is always asserted | `PRIVATE_SOCKET_COLLISION`, `DEDICATED_ROOT_COLLISION` |
| Private Unix socket, never `/var/run/docker.sock` | `ApplyGates`, `DaemonOptions.Validate` | `PRIVATE_SOCKET_COLLISION`, `OPTIONS_PRIVATE_SOCKET` |
| Dedicated data-root and exec-root | `ApplyGates`, `DaemonOptions.Validate` | `DEDICATED_ROOT_COLLISION`, `OPTIONS_DEDICATED_ROOTS` |
| Dedicated containerd namespace | `ApplyGates`, `DaemonOptions.Validate` | `CONTAINERD_NAMESPACE_COLLISION`, `OPTIONS_NAMESPACE` |
| Private bridge CIDR (`bip`) and node-local port range excluding 2375/2376, no conflicting `bridge` key | `ApplyGates`, canonical renderer | `PRIVATE_NETWORK_COLLISION`, `DAEMON_PRIVATE_NETWORK` |
| No public raw Docker API listener | canonical renderer | `OPTIONS_NO_PUBLIC_API` |
| No silent privilege upgrade | rootless chosen first; rootful is an explicit, compatible path | `ROOTLESS_*`, `ROOTFUL_PREREQ_UNMET` |
| One source of daemon options | `CheckOptionParity` | `PARITY_*` |

### Rendered service shape

- **Rootless** is a per-user systemd unit (`WantedBy=default.target`, no
  `User=`), running the supported `dockerd-rootless.sh` launcher with
  `XDG_RUNTIME_DIR` in the environment. It is never a direct system `dockerd`
  unit; `DAEMON_ROOTLESS_LAUNCHER` refuses that mistake.
- **Rootful** is an explicit system unit (`WantedBy=multi-user.target`, no
  `User=` override) running `dockerd` as root with the capability set dockerd
  actually needs (`CAP_SYS_ADMIN`, `CAP_NET_ADMIN`, `CAP_NET_RAW`,
  `CAP_SYS_RESOURCE`, file-ownership caps, …), `NoNewPrivileges=no`,
  `ProtectSystem=full`, `ProtectHome=yes`, `PrivateTmp=yes`,
  `RestrictSUIDSGID=no` and cgroup delegation. An empty
  `CapabilityBoundingSet` or a non-root `User=` is refused
  (`DAEMON_ROOTFUL_NO_USER`) because it cannot bind or operate dockerd.
- Both units invoke the daemon with **only `--config-file`**. Every daemon
  option lives in the canonical `daemon.json`; `CheckOptionParity` fails on a
  duplicate JSON key, an extra CLI flag or a config key repeated on the
  command line.

### Canonical option renderer (exported for OSS-379)

`native.DaemonOptions` and `native.DaemonSpec.BuildManifest()` are the single
exported renderer for all deployment modes. The OSS-379 portable installer
consumes `Manifest.ConfigJSON`, `Manifest.Unit`, `Manifest.UnitKind` and
`Manifest.Options` rather than declaring its own flag set. `LogDriver` is a
renderer input: `journald` in production, `json-file` only for disposable
fixtures without journald.

## Prerequisites

### Rootless (preferred default)

- Linux with unprivileged user namespaces enabled.
- `/etc/subuid` and `/etc/subgid` ranges for the service user;
  `newuidmap`/`newgidmap` available.
- cgroup **v2** with `cpu`, `cpuset`, `io`, `memory` and `pids` delegated. If
  controllers are not delegated, memory/pids limits are not claimed and the
  host is not qualified for rootless.
- systemd user session with `loginctl enable-linger`; a private
  `XDG_RUNTIME_DIR` with mode 0700, not under `/tmp` (TOCTOU).
- Storage driver qualified for rootless: `overlay2` on kernel >= 5.11 with
  `ftype=1`/ext4, otherwise `fuse-overlayfs`, `btrfs` or `vfs`. NFS data-root is
  unsupported.
- Game ports >= 1024, or an explicit `CAP_NET_BIND_SERVICE` grant. No silent
  privileged-port grant is performed.

### Explicit rootful (compatibility path)

- All universal isolation invariants above.
- systemd and **explicit operator qualification** for a root-managed service
  (`RootfulServiceQualified` in `HostFacts`). The unit runs as **root** with no
  `User=` override and the capability set dockerd needs; a dedicated non-login
  UID is **not** required and is in fact refused (`DAEMON_ROOTFUL_NO_USER`).
- A hardened unit; never the host socket, never blanket `--privileged`.

## Compatibility gate matrix

| Code | Trigger | Decision | Remedy |
| --- | --- | --- | --- |
| `HOST_OS_UNSUPPORTED` | OS is not Linux | fail closed | Use a Linux host or another mode. |
| `PRIVATE_SOCKET_COLLISION` | socket empty, equals host socket, or outside private runtime dir | fail closed | Allocate a private socket under the node runtime dir. |
| `DEDICATED_ROOT_COLLISION` | data/exec root empty or overlaps `/var/lib/docker` or `/var/run/docker` | fail closed | Allocate dedicated roots. |
| `CONTAINERD_NAMESPACE_COLLISION` | namespace empty or `moby`/`default` | fail closed | Use a node-specific namespace. |
| `PRIVATE_NETWORK_COLLISION` | bridge overlaps host, empty CIDR, or port range overlaps raw API | fail closed | Choose a non-overlapping private range. |
| `NO_SHARED_DAEMON_FALLBACK` (invariant) + collision codes | a preinstalled engine is present or its socket/root is selected | presence is reported; selection fails closed via `PRIVATE_SOCKET_COLLISION` / `DEDICATED_ROOT_COLLISION` | Keep the managed daemon on its private socket. |
| `ROOTLESS_USERNAMES_DISABLED` | user namespaces off | rootless disqualified | Enable userns or use hardened rootful. |
| `ROOTLESS_SUBUID_SUBGID_MISSING` | subuid/subgid unavailable | rootless disqualified | Configure ranges or use hardened rootful. |
| `ROOTLESS_CGROUP_V2_NOT_DELEGATED` | cgroup v1 or controllers not delegated | rootless disqualified | Delegate `cpu cpuset io memory pids`. |
| `STORAGE_DRIVER_UNSUPPORTED` | no qualified rootless driver | rootless disqualified | Use qualified driver/kernel. |
| `ROOTLESS_RUNTIME_DIR_UNSAFE` | runtime dir missing, world-accessible or under `/tmp` | rootless disqualified | Use a private 0700 directory outside `/tmp`. |
| `PRIVILEGED_PORT_UNSUPPORTED` | `ip_unprivileged_port_start > 1024` | capability withheld | Use ports >=1024 or grant explicitly. |
| `ROOTFUL_PREREQ_UNMET` | no systemd or no explicit rootful qualification | fail closed | Require systemd plus explicit operator qualification for a root-managed service with the dockerd capability set; never fall back to host Docker. |
| `UNSUPPORTED_TLS_TOPOLOGY` | plain HTTP, unknown/unattested proxy | enrollment refused | Direct TLS, L4 pass-through, or attested trusted proxy only. |
| `DAEMON_ROOTLESS_LAUNCHER` | rootless unit invokes dockerd directly or uses a system unit | render refused | Use dockerd-rootless.sh in a per-user unit. |
| `DAEMON_ROOTFUL_NO_USER` | rootful unit sets a non-root `User=` | render refused | Run the dedicated unit as root with the documented capability set. |
| `PARITY_NO_DUPLICATE_KEYS` / `PARITY_SINGLE_OPTION_SOURCE` / `PARITY_NO_KEY_CONFLICT` | duplicate config key or extra CLI flag | render refused | Emit every option once from `DaemonOptions` and pass only `--config-file`. |

## Live enrollment/auth boundary (shared scheme)

The mode-specific runner composes the shared contract types from
`node_control.proto`; it never defines parallel messages. Enrollment uses:

1. node-generated >=256-bit secret `S`, salt and Ed25519 key; only `S` and the
   private key live in the node's 0600 private runtime file;
2. REQUIRED operator-approved public key (SPKI + SHA-256 fingerprint), salt,
   digest and `CSR_PRE` registered out of band; `CSR_PRE` is material, not
   proof;
3. bounded, rate-limited `BeginEnrollmentChallenge` issuing a single-use nonce;
4. fresh `SIG_CLAIM` over node ID, enrollment ID, challenge ID, server nonce,
   expiry, capabilities and controller-derived channel context;
5. atomic compare-and-consume with distinct replay, stale-nonce,
   revoked/generation-mismatch and topology refusals;
6. a signed **public** X.509 client certificate for mTLS; the private key never
   leaves the node and no read response or fixture contains a secret.

Three authorization domains never cross: operator provisioning, one-time
bootstrap (challenge + one claim only), and enrolled node (per-node scopes and
object ID). Unsupported TLS topologies are refused with
`UNSUPPORTED_TLS_TOPOLOGY`; a client header alone is never trusted.

## Verification and qualification limits

### What was actually proven

Go fixtures cover the qualified rootless path, every refusal above, the rootful
fallback, canonical option parity and mode-correct unit rendering. Enrollment is
now owned by the shared `internal/noderuntime/enroll` boundary and consumed by
this lane; its state-machine fixtures live there.

A disposable Linux boot fixture boots the **contributed daemon.json** in a
pinned `docker:29.3.0-dind` container. Precisely: it is a **PRIVILEGED nested
ROOTFUL** boot, with a `json-file` log-driver override because the disposable
container has no journald. It asserted: dockerd starts, the private socket
exists, a trivial container runs, memory/pids limits apply, and the data root
survives a daemon restart. The published-port mapping and a matching DNAT rule
were observed, and a listener on the private bridge answered from a second
container; **client TCP reachability on the published game port was not
measured**. `systemd-analyze verify` was also run on both rendered units — this
checks **syntax only**, not that they boot. Exact output is in the external
audit file `security-runtime-plans/node-native-boot-evidence.md`.

Separately, the exact shipped ROOTFUL unit was booted with real dockerd 29.1.3
under systemd 255 on a clean disposable fixture: `active/success`, private
socket/pidfile/roots, default-cap container, limits and live-restore restart were
observed. DNAT-rule presence and bridge-side listener success remain distinct
from measured client reachability on the published host port.

### Real-defect fixes from exact-unit qualification (2026-11-19)

Booting the EXACT shipped units with a real dockerd 29.1.3 on a clean disposable
Ubuntu 24.04 / systemd 255 fixture exposed and led to fixes for three real
defects:

1. **Private pidfile.** The canonical daemon.json now emits a `pidfile` under
   the owned runtime root (no-clobber validation), and the rootful unit carries
   a matching `PIDFile=` directive. A second dockerd no longer collides on
   `/var/run/docker.pid`.
2. **Default container capabilities.** The rootful capability set now includes
   `CAP_FSETID`, `CAP_KILL`, `CAP_NET_BIND_SERVICE` and `CAP_SETFCAP` alongside
   the daemon capabilities, in both `CapabilityBoundingSet` and
   `AmbientCapabilities`. Default-capability containers now start; the prior
   set failed with `unable to apply caps: operation not permitted`.
3. **Rootless notify.** The rootless per-user unit now has `NotifyAccess=all`,
   so the READY signal from the child dockerd under rootlesskit is accepted by
   `Type=notify` instead of leaving the unit stuck in `activating/start`.

`CheckOptionParity` and `CheckUnitContract` assert these properties in Go
fixtures; they are contract checks, not boot proof.

### What remains NOT verified (do not claim deployability)

- **Rootless active/success on a clean systemd host**: the `NotifyAccess=all`
  fix is present and its unit contract/syntax verified, but a real rootless boot
  to `active/success` was **not** completed in the latest fixture: nested
  user-namespace iptables cannot create the bridge NAT chain on the WSL2
  fixture kernel (`can't initialize iptables table 'nat': Permission denied`).
  This is a fixture environment limitation, not a claim that the unit works.
- **Live-host install, real-gameplay, release or deployment** evidence does not
  exist. The re-qualification was fixture-only.
- **VM guest boot smoke** may use isolated QEMU software emulation only if
  available without KVM; this host has no `/dev/kvm`, so no VM boot was done.
- **LXC guest boot** needs its own suitable disposable runtime with real pinned
  provenance; not attempted here. Both are separate next steps/blockers.

Exact rootful re-qualification output and limits are in the external audit file
`security-runtime-plans/node-runtime-requalification-20261119.md`.

All enrollment keys and secrets are synthetic/ephemeral and private files live
in temp directories. No fixture changes a host, installs a service, writes a
real credential, publishes an image or deploys anything.
