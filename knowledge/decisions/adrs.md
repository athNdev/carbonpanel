---
type: Decision
title: Architecture decisions
description: Index of the accepted ADRs and the concept each one governs.
status: stable
owners: [oss-runtime]
code_refs:
  - docs/adr
sources:
  - title: ADR directory
    resource: ../../docs/adr
---

# Decision records

| ADR | Decision | Governs |
| --- | --- | --- |
| [0001](../../docs/adr/0001-moby-sdk-migration.md) | Use the Moby SDK instead of a shelled-out Docker CLI | [Runtime](../architecture/runtime.md), [Node resolution](../architecture/node-resolution.md) |
| [0002](../../docs/adr/0002-sveltekit-resolve-navigation.md) | Use SvelteKit resolve/navigation conventions in the console | [Frontend change](../workflows/frontend-change.md) |
| [0003](../../docs/adr/0003-sqlite-durability.md) | Superseded. Original continuous-replication proposal | [Persistence](../architecture/persistence.md) |
| [0004-docker-locator](../../docs/adr/0004-docker-locator.md) | One node-to-daemon Locator | [Runtime seams](../architecture/runtime-seams.md), [Node resolution](../architecture/node-resolution.md) |
| [0004-proxy-route-plans](../../docs/adr/0004-proxy-route-plans.md) | Proxy route policy is a pure PlanFor | [Runtime seams](../architecture/runtime-seams.md), [TCP proxy](../architecture/proxy.md) |
| [0004-world-data-seam](../../docs/adr/0004-world-data-seam.md) | World data flows through worldstore | [Runtime seams](../architecture/runtime-seams.md), [Backup and restore](../operations/backup-restore.md) |
| [0005](../../docs/adr/0005-lifecycle-transitions.md) | Lifecycle transitions are owned leases | [Runtime seams](../architecture/runtime-seams.md) |
| [0006](../../docs/adr/0006-idle-modes-are-data.md) | Idle modes are data, not strategies | [Runtime seams](../architecture/runtime-seams.md), [Deep sleep](../operations/deep-sleep.md) |
| [0007](../../docs/adr/0007-panel-sqlite-snapshots.md) | Daily SQLite snapshots and an explicit restore | [Persistence](../architecture/persistence.md) |
| [0008](../../docs/adr/0008-carbon-panel-brand-identity.md) | Use the approved lower-cut block and technical-capitals wordmark | [Brand assets](../../assets/branding/README.md) |
| [0009](../../docs/adr/0009-shared-design-system.md) | Proposed shared design-system source and Cloud snapshot | [Frontend change](../workflows/frontend-change.md) |
| [0010](../../docs/adr/0010-versioned-release-artifacts.md) | SemVer bundles, matching images and gated publication | [Release](../workflows/release.md) |
| [0011](../../docs/adr/0011-server-runtime-admission.md) | Share server lifecycle policy | [Server lifecycle policy](../architecture/server-lifecycle.md) |
| [0012](../../docs/adr/0012-product-transports.md) | Generated product control with tested HTTP compatibility | [RPC contract](../architecture/rpc-contract.md) |
| [0013](../../docs/adr/0013-bounded-storage-streaming.md) | Bound storage frames and commit staged writes explicitly | [Remote storage transport](../architecture/remote-storage-transport.md), [Backup and restore](../operations/backup-restore.md) |
| [0014](../../docs/adr/0014-in-process-admission.md) | Serialize same-node starts with an in-process gate, not a lease | [Server lifecycle policy](../architecture/server-lifecycle.md) |
| [0015](../../docs/adr/0015-world-viewing-renderer-proxy.md) | Observe worlds through an authenticated same-origin renderer proxy | [World viewing](../architecture/world-viewing.md), [RPC contract](../architecture/rpc-contract.md) |
| [0016](../../docs/adr/0016-world-library-identity-and-binding.md) | Immutable world revisions and clone-on-bind, stop-based journaled bindings | [World Library](../architecture/world-library.md) |
| [0017](../../docs/adr/0017-player-management-identity-and-capability-gates.md) | Player identity, provenance and capability gates | [Player management](../architecture/player-management.md) |
| [0018](../../docs/adr/0018-node-response-redaction.md) | Redact node TLS material from read responses; empty TLS edits mean unchanged | [RPC contract](../architecture/rpc-contract.md), [Secrets and endpoints](../invariants/secrets-and-endpoints.md) |
| [0019](../../docs/adr/0019-node-control-contract.md) | Versioned typed node-control contract with redacted responses and fail-closed routing | [Node control protocol](../architecture/node-control-protocol.md), [RPC contract](../architecture/rpc-contract.md) |
| [0023](../../docs/adr/0023-oidc-sso-boundary.md) | Standards-based OIDC with PKCE, nonce and one-time exchange | [RPC contract](../architecture/rpc-contract.md), [SSO identity](../invariants/sso-identity.md) |
| [0024](../../docs/adr/0024-sso-identity-and-privilege-origin.md) | Issuer+subject identity; no auto-link or implicit admin | [SSO identity](../invariants/sso-identity.md) |
| [0025](../../docs/adr/0025-sso-entitlement-reconciliation.md) | Source-scoped role reconciliation before session issue | [SSO identity](../invariants/sso-identity.md), [Fail-closed authorization](../invariants/authz-fail-closed.md) |
| [0026](../../docs/adr/0026-sso-logout-and-provider-limits.md) | Local session invalidation and honest IdP logout limits | [SSO identity](../invariants/sso-identity.md) |
| [0027](../../docs/adr/0027-fail-closed-http-policy.md) | Fail-closed policy for legacy HTTP adapters | [RPC contract](../architecture/rpc-contract.md) |
| [0028](../../docs/adr/0028-credential-generation-and-rotation.md) | Credential generation, detection and rotation | [Secrets and endpoints](../invariants/secrets-and-endpoints.md) |
| [0029](../../docs/adr/0029-trusted-proxy-and-origins.md) | Trusted proxy headers and WebSocket origins | [Runtime model](../architecture/runtime.md) |
| [0030](../../docs/adr/0030-bounded-content-provenance.md) | Bounded content provenance | [Data safety](../invariants/data-safety.md) |
| [0031](../../docs/adr/0031-module-sidecar-confinement.md) | Module sidecar confinement parity | [Runtime seams](../architecture/runtime-seams.md) |
| [0032](../../docs/adr/0032-security-audit-events.md) | Security audit events and redaction | [Fail-closed authorization](../invariants/authz-fail-closed.md) |
| [0033](../../docs/adr/0033-release-tag-and-publication-policy.md) | One explicit publication boundary and separate immutable version domains | [Release](../workflows/release.md) |
| [0034](../../docs/adr/0034-mcp-server-distribution.md) | Independent MCP artifact distribution with build-bound capabilities and auth-enabled installs | [Release](../workflows/release.md), [Fail-closed authorization](../invariants/authz-fail-closed.md) |
| [0039](../../docs/adr/0039-docker-readable-build-aliases.md) | Pure readable UTC aliases for immutable Docker builds with tag-push provenance | [Release](../workflows/release.md) |
| [0040](../../docs/adr/0040-bluemap-installed-module-discovery.md) | Private identity-bound BlueMap discovery, explicit consent and server-side singleton install | [World viewing](../architecture/world-viewing.md), [Runtime seams](../architecture/runtime-seams.md) |
| [0041](../../docs/adr/0041-bounded-trusted-main-nightly.md) | Native scheduled nightly publishes only a verified current-main commit, default-off | [Release](../workflows/release.md) |

# Rule

The ADR is authoritative for rationale. This index exists so an agent can find
the decision without reading every file. When an ADR is superseded, update this
table and mark the old concept `deprecated` rather than deleting it.
