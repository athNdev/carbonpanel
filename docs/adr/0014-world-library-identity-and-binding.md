# ADR 0014: World Library identity, immutable revisions and clone-on-bind

Status: accepted

## Context

Carbon Panel has no world entity. A server owns one data root and a primary
level folder (`ServerConfig.Level`, default `world`); world bytes are only bytes
in that server tree. Backups, snapshots and restore are whole-tree operations
(ADR 0004). There is no cross-server world copy, no world identity, no NBT
surface and no world-level lock.

Renaming a live level folder is not a hot swap. A running JVM holds region
handles and a cached `level.dat`; `session.lock` is the running server's lock;
dimension siblings must move as a family; and container binds, the database,
backups and the operation journal all point at the old path. Two writable
servers attached to the same bytes would corrupt them.

The durable operation journal, quiescence helper, node-aware resolver and
bounded streaming/undo seam (ADR 0013) exist, but none of them defines world
ownership. Admission remains a check, not an atomic reservation (ADR 0011),
and lifecycle transition claims are process-local (ADR 0005).

## Decision

Represent library worlds as three durable records in the control-plane SQLite
database: a `WorldEntry` (logical identity `wld_*`, display data, provenance
pointer), immutable `WorldRevision` rows (content SHA-256, byte size, entry
count, manifest, provenance and license attestation) and explicit
`WorldBinding` rows keyed by `(server_id, level_name)` pointing at exactly one
revision. A `WorldImportJob` records durable one-shot import state.

A revision is immutable: no procedure writes into revision bytes, and edits
produce a new revision (copy-on-write). Binding **always materialises a private
writable clone** on the owning node; the revision stays read-only. Two writable
bindings to one revision are refused with a typed error, enforced both in the
binding store and by a keyed lock held across the operation. A shared reference
is read-only preview/template metadata only and is never mounted writable. A
future shared writable backend would need its own ADR and operator opt-in.

Changing the live world is a stop-based transaction, never a folder rename:
preflight (strict node resolution, admission, compatibility dry-run, disk
budget) → journal intent with `request_id` idempotency → node-side stage and
verify → quiesce/stop → journaled atomic level-family publish → binding update
→ restart → bounded playability verification → journal finalisation in an
independent bounded context → one activity event keyed by `request_id`.
Cancellation before mutation is honoured; after an external mutation the
outcome is persisted with a bounded independent context. An uncertain outcome
keeps the lock and is reported as `OutcomeUnknown` requiring operator
resolution; it is never auto-retried. Recovery observes real container and
filesystem state, not the database status.

Compatibility is reported as `detected`, `inferred` or `unknown` with reasons;
unknown values gate the operation rather than being guessed. Deep world
configuration is driven by a versioned capability schema keyed by
(edition, MC version, loader, data version) and each control is classified
creation-only, offline-only, live-command, restart-required or unsafe.
Live `level.dat`/region/NBT editing, arbitrary mod removal, in-place downgrade
and Java↔Bedrock conversion are unsupported and shown as disabled with reasons.

Platform integration has two separate axes. Axis (a) is bounded API-backed
artifact support (Modrinth Labrinth v2; CurseForge Core API) for
modpack/datapack/resourcepack/shader artifacts used by a world workflow.
Axis (b), automated discovery of prebuilt worlds, is **not documented**:
Modrinth publishes no `world` project category and CurseForge's worlds class
id must be confirmed from `/v1/categories`. Axis (b) stays experimental and
manual (operator URL/file import with provenance) behind investigation gate
D11; a modpack or datapack is not a prebuilt world. CurseForge's `x-api-key`
is an API-origin-only request header and is never forwarded to a CDN, a
temporary download URL or an arbitrary operator URL; credentials are stripped
on cross-origin redirects using the indexer policy.

## Consequences

- Four new control-plane tables and a `worlds` RBAC resource with fail-closed,
  server-scoped mapping plus an explicit `"*"` grant for cross-server listing.
- No hot swap, no shared writable reference and no cross-process atomicity.
  Admission and keyed locks are single-controller/process-local; a second
  controller is outside the guarantee and must be documented as such.
- Imports must verify platform hash, archive manifest and whole-archive SHA-256
  in node-local staging before publication; archive extraction rejects
  traversal, links, nested bombs, oversized entries and executable payloads.
- `world_quota_mb` remains advisory panel admission and is never reported as an
  enforced `WorldDiskEnforced` quota.
- Until the exactly-one-event gate (ADR 0011 / D7) is closed, the operation
  journal is the authoritative audit surface and the activity event stream is
  best-effort.
