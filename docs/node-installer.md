# Portable Linux node installer

One guest-side installer shared by **every** managed Carbon Panel runtime mode:
the host-native private daemon, the QCOW2 VM and the unprivileged LXC. There is
no provider-specific integration in this installer; it is also the supported
fallback wherever an image format is unqualified. It is a build artifact, not
a Carbon Panel app release.

## Boundaries (what this is not)

- It does **not** provision a live host, create a VM/container, or change global
  host configuration (`/etc`, host Docker socket, host services, firewall).
- It never downloads an artifact. The caller supplies an already-built,
  integrity-checked artifact directory; signature/checksum verification happens
  locally before any file is promoted.
- It does not mount a production enrollment RPC handler. It produces the
  *material* for the SHARED ADR 0019 contract (proto messages from
  `pkg/proto/carbonpanel/v1`); the controller-side trust boundary is a separate
  track. No stub or fixture is deployment evidence.
- It never writes a plaintext secret, private key or credential into
  provisioning metadata. `S` lives only in `enrollment.secret` (mode `0600`)
  inside the state directory and appears in exactly one artifact: a
  `ClaimEnrollmentRequest`, which is sent once over the authenticated channel.

## Exported interface (for the other mode lanes)

Other lanes coordinate with this installer **only through this interface** and
never edit this package's files.

### CLI (operator/guest-facing)

Command: `scripts/install-node.sh <verb> [flags] [--apply]`, backed by the
compiled `cmd/carbon-node-installer` binary (selected with
`CARBON_NODE_INSTALLER_BIN`). Mutating verbs are dry-run until `--apply`;
`verify` and `status` are read-only.

| Verb | Purpose | Required flags |
| --- | --- | --- |
| `init` | Create/verify the unique first-boot identity (idempotent) | `--state-dir` |
| `bundle` | Build the scoped, secret-free enrollment bundle | `--state-dir`, `--label`, `--capabilities` |
| `claim` | Build the one-time, challenge-bound claim request | `--state-dir`, `--challenge` |
| `verify` | Verify artifact manifest, checksums and optional signature | `--artifact` |
| `import` | Bind an existing persistent data directory | `--state-dir`, `--data-dir` |
| `backup` | Snapshot the persistent data directory into a new backup dir | `--data-dir`, `--backup-dir` |
| `recover` | Verify a backup and copy files back (never deletes) | `--data-dir`, `--backup-dir` |
| `upgrade` | Stage and promote a verified artifact into the inactive slot | `--state-dir`, `--artifact` |
| `rollback` | Verify the undo slot and flip back to the previous version | `--state-dir` |
| `status` | Print redacted install state; finish an interrupted promotion | `--state-dir` |
| `render-daemon` | Render a disposable daemon unit/config (read-only) | `--daemon-spec` |

Every path is caller-owned. `--target-os`/`--target-arch` exist only so offline
fixtures on a non-Linux host can verify a Linux artifact; production callers
leave them unset (host values are used).

### Go package (`internal/nodeinstall`)

Mode lanes that live in the same module import these exported functions. They
are pure with respect to host state: all effects are confined to the supplied
directories.

- `InitIdentity(stateDir)` / `LoadIdentity(stateDir)` — first-boot identity.
- `BuildEnrollmentBundle(stateDir, label, capabilities, protocolVersion, ttl)` —
  secret-free registration material plus a `CreateEnrollmentRequest` built from
  the shared generated proto message.
- `ClaimContext(...)` / `BuildClaim(stateDir, challenge, now)` — canonical
  ADR 0019 signature context and single-use claim.
- `VerifyArtifact(dir, os, arch, pub)` — manifest, checksum and optional
  Ed25519 signature check.
- `ImportVolume`, `BackupVolume`, `RecoverVolume` — persistent data handling.
- `Upgrade`, `Rollback`, `Status` — two-slot artifact lifecycle.
- `RenderDaemon`, `SetDaemonRenderer`, `DaemonRenderer` — in-memory private
  daemon rendering with a seam for the native lane's canonical renderer.

Sentinel errors (`ErrIntegrity`, `ErrPlatform`, `ErrConsumed`,
`ErrUnresolvedRecovery`, `ErrSecretLeak`, ...) are stable refusal codes for
callers and fixtures; none carries secret material.

## Observed behavior

### Unique first-boot identity

`init` generates an Ed25519 key pair and a UUID v4 node ID once, stores the
private key in `node.key` (`0600`) and exports only SPKI, fingerprint and node
ID in `identity.json`. Repeating `init` in the same state directory is a no-op;
two different state directories never share an identity. A mismatched key and
identity file is an integrity failure, never a silent key regeneration.

### Scoped enrollment bundle and challenge-bound claim

`bundle` generates a >=256-bit CSPRNG secret `S` as a base64url string, a random
salt, and a `CSR_PRE` signed by the node key (carrying the node label, salt and
digest as private extension arcs under `2.25.42`). It writes:

- `bundle.json` — `salt`, `digest`, `CSR_PRE`, approved SPKI and fingerprint,
  plus an explicit `scope` (node ID, capabilities, protocol version).
- `create_enrollment_request.json` — a protojson `CreateEnrollmentRequest`
  using the SHARED generated message with all five ADR 0019 required fields.
- `enrollment.secret` (`0600`) — `S` only; never exported.

`claim` consumes a `BeginEnrollmentChallenge` response, checks that the nonce,
expiry, node ID and channel context are current and fail closed on any mismatch,
then signs the exact ADR context with the approved key and writes a
`ClaimEnrollmentRequest`. A second claim is refused (`ErrConsumed`), matching
the one-time rule. `S` is stored/replayed as a UTF-8 string so the proto `string`
field stays valid; the digest is computed over those same bytes.

### Private daemon rendering (one source of options)

The installer can render a **disposable** daemon unit and `daemon.json` from a
caller-supplied spec via `render-daemon` (read-only). It never writes a unit,
installs a service or starts a process. This closes the three defects found in
the merged native-daemon baseline (PR527):

- dockerd options are defined **once**, in the option table that generated
  `daemon.json`; the unit carries only `--config-file` and repeats no option
  flag, so CLI flags and config can no longer disagree.
- rootless renders as a **per-user** service (`default.target`,
  `XDG_RUNTIME_DIR`, `dockerd-rootless.sh`, per-user install hint) rather than a
  direct system unit.
- rootful renders as an **explicit root** service with a non-empty
  `CapabilityBoundingSet`/`AmbientCapabilities`, rather than a non-root service
  with an empty capability bound that cannot start dockerd.

Every spec is validated fail-closed: global config paths, host sockets, public
API ports, shared roots/namespaces, rootless without a runtime UID or a socket
outside `XDG_RUNTIME_DIR`, and unknown modes are all refused. All paths remain
caller-owned.

The native-daemon lane owns the canonical renderer. Until it lands on main,
`internal/nodeinstall` provides a local implementation behind the
`DaemonRenderer` interface and `SetDaemonRenderer` integration seam: wiring the
shared renderer later requires no change to callers, the CLI or fixtures.

### Artifact integrity and repeatable upgrade/rollback

An artifact is a directory with `manifest.json` (schema, name, version, OS,
arch, relative-path → SHA-256 map, optional detached Ed25519 signature over the
canonical manifest). `verify` refuses platform mismatches, hash mismatches,
unexpected files and a signature with no supplied public key.

The installer keeps two immutable slots (`slots/a`, `slots/b`) and a single
atomic `state.json`. `upgrade` verifies the bundle, stages it into the inactive
slot, then promotes it with one atomic state-file write. The previous version
stays in the other slot, so `rollback` verifies that undo slot and flips back
without re-downloading. Repeating an upgrade at the active version is a no-op.
An interrupted promotion is recorded in `upgrade.journal`: `status` (or the next
verb) completes a staged or promoted journal, and any other state returns
`ErrUnresolvedRecovery` instead of guessing an outcome. Worlds and databases
are not arguments to these verbs and are never touched.

### Persistent volume import/backup/recovery

`import` binds an existing data directory and records a digest of its file
manifest — it never moves or copies it. `backup` writes a verified snapshot
into a **new** directory (source is read-only), and `recover` verifies every
backup hash before copying files back. Recovery overwrites only manifest-named
files; unrelated worlds and databases are never deleted. A tampered backup is
refused before any file is touched.

## Offline fixtures and verification

From the repository root:

```sh
go test ./internal/nodeinstall/...
python scripts/test_node_installer_fixture.py
```

The Go tests cover identity idempotency/uniqueness, secret-free bundle and
digest/CSR/fingerprint correctness, signature verification and replay refusal,
artifact integrity/platform/signature checks, two-slot upgrade/rollback with
data preservation, interrupted-promotion recovery, volume backup/recovery, and
daemon rendering (no duplicated options, per-user rootless launcher, explicit
rootful capabilities and fail-closed spec validation).
The Python fixture builds the binary once and drives the whole CLI surface in
a temporary directory, and parses the bash wrapper with a real shell parser
(`tree-sitter-bash`) plus a dry-run/global-action guard. Both are offline and
use only synthetic keys and fixture files — no daemon, host or real credential.

> Evidence boundary: these are M3 disposable fixtures. They prove refusal,
> state-machine and data-preservation behavior only. No live host import/boot,
> no real hypervisor/LXC, no production enrollment handler and no artifact
> publication is claimed by this page. Those belong to later milestones.
