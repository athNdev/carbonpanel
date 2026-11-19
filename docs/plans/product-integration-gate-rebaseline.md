# OSS-365 — product integration gate re-baseline (2026-11-19)

**Acceptance baseline:** `origin/main@9e81679b346a3e1acbc50f8283b5b3c365769771`.
**Evidence commit:** `6c9f430f022a7eeeb91d0013b8e4c8050844a600` (branch
`codex/product-acceptance-gaps`, PR #555). The full external planning artefact
is `product-plans/integrated-roadmap.md` in the external audit workspace
(`$CARBONPANEL_AUDIT_DIR`, for example an out-of-tree
`carbonpanel-main-audit/` checkout); it is deliberately not tracked in this
repository. This page is the reviewable, in-repository record of the same gate.

This replaces the earlier planning pin `518b64b8` and the intermediate
re-baseline target `d8597d4e`. No live host, deployment, release or registry
write was involved.

## Decisions accepted

| Record | Status | Evidence |
| --- | --- | --- |
| [ADR 0015](../adr/0015-world-viewing-renderer-proxy.md) — renderer proxy | Accepted | PR #505 head `78103b18`, merge `e0136c4a`; D0 "BluePanel = BlueMap alongside a panel" remains a recorded assumption |
| [ADR 0016](../adr/0016-world-library-identity-and-binding.md) — world library | Accepted | PR #507 merged |
| [ADR 0017](../adr/0017-player-management-identity-and-capability-gates.md) — player identity | Accepted | PR #506 head `4e267121`, merge `15816bf5` |
| [ADR 0011](../adr/0011-server-runtime-admission.md), [0013](../adr/0013-bounded-storage-streaming.md), [0014](../adr/0014-in-process-admission.md) | Accepted | already accepted on main |
| [Integration gate decision](../../knowledge/decisions/product-integration-gate.md) | Stable | knowledge index regenerated; 77 concepts, 40/40 fixed queries |

## Cross-review findings

- **H1 (shared inventory store):** stale as a statement about current code —
  PR #467 is merged. Treated as landed; not rebuilt.
- **H2 (bounded streaming / ADR 0013):** stale — PR #471 and ADR 0013 are on
  main. Treated as landed; qualification still recorded separately.
- **H3 (landed != qualified):** still binding. W15 (OSS-334, PR #504),
  W16 (OSS-335, PR #499) and W17 (OSS-336, PR #471) are Done with recorded
  review/CI evidence. W18 (OSS-337) remains In Progress; no migration
  assumption was made.

## R1–R5 against the acceptance baseline

| Phase | Actual state at `9e81679b` |
| --- | --- |
| R1 Safe observability foundation | **Shipped** — roster/search/filter/sort, grouped detail, skin proxy, `/worlds` picker and library entry/read merged in PR #505/#506/#507. Exact-head Chromium 1440/768/390/360 smoke and stale/conflict deep-field checks passed. |
| R2 Structured reads / node-correct data | **Shipped with bounds** — authenticated same-origin proxy and fail-closed node resolution merged. Measured backend source→client p95 1.20 ms (64 KiB, n=30); browser source→UI p95 55 ms (n=15), both on owned fixtures with stated limits. W16/W17 qualified. Live renderer latency remains unmeasured. |
| R3 Governed mutation | **Partial** — typed moderation and map admin shipped through existing audited RPCs; W15 admission merged. World bind/swap and exactly-one-event remain gated; no full auditability claim. |
| R4 Deep configuration / platform breadth | **Partial** — capability profiles, static tile import and platform API slices merged; version-driven grouped world configuration and an automated prebuilt-world catalog are not claimed. |
| R5 Polish and parity | **Partial** — theme/density/reduced-motion and keyboard/focus evidence at 1440/768/390/360; W23 token migration remains incremental (OSS-342/384). No whole-panel token claim. |

## Acceptance evidence and exact blocker

- OSS-349/352: `S02` mobile rerun, `S14` stale/conflict deep fields and new
  `S23` 768px smoke all PASS on the production bundle at the acceptance
  baseline; 0 console/page errors.
- OSS-355: measured latencies in
  [proxy_measurement_test.go](../../internal/worldview/proxy_measurement_test.go)
  and the external acceptance report's `latency-evidence.json`.
- OSS-358: eight persistent world/datapack/database files byte-identical across
  managed renderer install, abort/rollback and removal in
  [world_preservation_test.go](../../internal/bluemapdiscovery/world_preservation_test.go);
  remote nodes have no controller fallback.
- OSS-357 remains **In Progress**: renderer-owned process RAM and on-disk tile
  storage are unmeasured. No renderer binary, container or real tile dataset is
  available and live host/deployment work is prohibited. This is an exact
  blocker, not a passed gate or a universal memory claim.

All browser evidence is fixture-backed: Connect RPC responses and the map iframe
are synthetic; no live controller, renderer, Minecraft server, platform API or
third-party service was contacted.
