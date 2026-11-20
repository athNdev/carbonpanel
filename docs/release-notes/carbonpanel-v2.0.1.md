## UPGRADE NOTES — read before upgrading from v1.0.40

**User-facing migration guide: https://github.com/athNdev/carbonpanel/blob/carbonpanel-v2.0.1/docs/releases.md#upgrade-notes-from-v1040** (full provenance and evidence: https://github.com/athNdev/carbonpanel/blob/carbonpanel-v2.0.1/docs/release-history.md#upgrade-notes--read-before-upgrading-from-v1040).

This MAJOR release has three prerequisites; complete all three before upgrading:

1. **OIDC raw-group role mapping and explicit admin linking.** v1.0.40's `role_mapping: {}` example granted raw IdP group names directly. Raw claims are no longer granted by default and privileged names are always refused, so add an explicit `auth.oidc.role_mapping` entry for every retained role (including admin). There is no implicit first-user admin: keep a local administrator and explicitly link each admin OIDC subject.
2. **PROXY protocol trust.** PROXY v2 headers are trusted only from `proxy.trusted_proxy_cidrs` (default `[]` trusts nobody). Add every load balancer source CIDR and confirm real client IPs are preserved after upgrade.
3. **Backup/restore drill.** Back up the database (db + wal + shm), configuration, worlds and backups, then rehearse the documented copy-back restore on disposable copies before upgrading a live installation.

## Downloads

Choose the archive for your operating system and CPU. Each includes the panel with its web UI, the storage helper, configuration example and licenses.

Verify downloads against **checksums.txt**. **release-manifest.json** records the exact source commit and Docker image digest. Back up your database and configuration before upgrading; preserve server worlds and existing data.

Container images (Linux amd64 and arm64):

- Docker Hub: `atharvnaphade/carbonpanel:2.0.1`
- GitHub Packages: `ghcr.io/athndev/carbonpanel:2.0.1`

[Installation and verification](https://github.com/athNdev/carbonpanel/blob/carbonpanel-v2.0.1/docs/releases.md)

## What's Changed
* Carry cancellation through Docker client and executor locators by @athNdev in https://github.com/athNdev/carbonpanel/pull/474
* Share server inventory, lifecycle actions and status presentation by @athNdev in https://github.com/athNdev/carbonpanel/pull/467
* OSS-336: Stream bulk storage with verified recovery by @athNdev in https://github.com/athNdev/carbonpanel/pull/471
* refactor(studio): extract dependency reconciliation workflow by @athNdev in https://github.com/athNdev/carbonpanel/pull/500
* feat(config): migrate provider key validation to generated RPC by @athNdev in https://github.com/athNdev/carbonpanel/pull/498
* refactor(studio): extract version migration workflow by @athNdev in https://github.com/athNdev/carbonpanel/pull/502
* refactor(frontend): own display preferences in a single store by @athNdev in https://github.com/athNdev/carbonpanel/pull/501
* refactor(storage): own node storage resolvers per service instance by @athNdev in https://github.com/athNdev/carbonpanel/pull/499
* feat(mods): migrate product browse/install to generated RPC by @athNdev in https://github.com/athNdev/carbonpanel/pull/503
* feat(runtime): share cancellable per-node admission across lifecycle entrypoints by @athNdev in https://github.com/athNdev/carbonpanel/pull/504
* Release/tag policy: single auditable publication boundary by @athNdev in https://github.com/athNdev/carbonpanel/pull/508
* fix(frontend): preserve stored node TLS credentials on redacted edits by @athNdev in https://github.com/athNdev/carbonpanel/pull/510
* Track A: harden OIDC SSO (PKCE, issuer-bound identity, source-scoped roles) by @athNdev in https://github.com/athNdev/carbonpanel/pull/511
* fix(web): accept numeric node capacity input values by @athNdev in https://github.com/athNdev/carbonpanel/pull/514
* security: fail-closed legacy policy, trusted proxy and defensive defaults (Track B) by @athNdev in https://github.com/athNdev/carbonpanel/pull/513
* feat(world-library): OSS-347 cumulative World Library (OSS-359-OSS-364) by @athNdev in https://github.com/athNdev/carbonpanel/pull/507
* fix(ci): derive editor browser cache in a step instead of job-level env by @athNdev in https://github.com/athNdev/carbonpanel/pull/515
* P0-GATE: redact node TLS material from read responses by @athNdev in https://github.com/athNdev/carbonpanel/pull/509
* feat(worlds): first-class world viewing (OSS-346) by @athNdev in https://github.com/athNdev/carbonpanel/pull/505
* OSS-368 P0 contract: versioned node-control protocol and ownership model by @athNdev in https://github.com/athNdev/carbonpanel/pull/516
* feat(players): rich player management (OSS-345 / OSS-348-353) by @athNdev in https://github.com/athNdev/carbonpanel/pull/506
* docs(runtime): specify node enrollment proof-of-possession and auth domains by @athNdev in https://github.com/athNdev/carbonpanel/pull/517
* OSS-368 P1: enrollment contract revision and harmless fixtures by @athNdev in https://github.com/athNdev/carbonpanel/pull/518
* Bounded nightly channel policy with pre-write sha verification by @athNdev in https://github.com/athNdev/carbonpanel/pull/519
* fix(seo): exclude utility pages from sitemap by @athNdev in https://github.com/athNdev/carbonpanel/pull/512
* Harden nightly sha reuse, pinned provenance and edge promotion by @athNdev in https://github.com/athNdev/carbonpanel/pull/520
* Prepare MCP artifact distribution, docs and announcement content by @athNdev in https://github.com/athNdev/carbonpanel/pull/521
* mcp: typed stdio tool gateway (first slice, 30 tools) by @athNdev in https://github.com/athNdev/carbonpanel/pull/522
* docs(runtime): two first-class managed node deployment modes (ADR0019 addendum) by @athNdev in https://github.com/athNdev/carbonpanel/pull/523
* feat(node-runtime): host-native Linux private daemon mode (OSS-376) by @athNdev in https://github.com/athNdev/carbonpanel/pull/527
* OSS-377: reproducible QCOW2/cloud-init node VM artifact (fixtures) by @athNdev in https://github.com/athNdev/carbonpanel/pull/526
* OSS-380: isolated runtime-artifact publication boundary and format evidence by @athNdev in https://github.com/athNdev/carbonpanel/pull/525
* Add human-readable immutable Docker build aliases by @athNdev in https://github.com/athNdev/carbonpanel/pull/524
* feat(node-lxc): unprivileged LXC rootfs artifact lane (OSS-378) by @athNdev in https://github.com/athNdev/carbonpanel/pull/528
* OSS-385: shared enrollment boundary with atomic consume by @athNdev in https://github.com/athNdev/carbonpanel/pull/532
* Portable Linux node installer shared by managed runtime modes (OSS-379) by @athNdev in https://github.com/athNdev/carbonpanel/pull/529
* fix(build): ship /worlds source route in packaged images (OSS-381) by @athNdev in https://github.com/athNdev/carbonpanel/pull/530
* OSS-377: pin real dated Ubuntu 24.04 provenance for node VM artifact by @athNdev in https://github.com/athNdev/carbonpanel/pull/533
* Fix readable-alias backfill workflow command rendering by @athNdev in https://github.com/athNdev/carbonpanel/pull/534
* Tighten the terrain crop in README and social previews by @athNdev in https://github.com/athNdev/carbonpanel/pull/536
* OSS-369 B-12/B-15: request/session limits and safe Geyser bind defaults by @athNdev in https://github.com/athNdev/carbonpanel/pull/537
* fix(node-runtime): make host-native daemon rendering deployable (OSS-376) by @athNdev in https://github.com/athNdev/carbonpanel/pull/535
* OSS-383: read-only exact-identity lookup and per-server allowlist add by @athNdev in https://github.com/athNdev/carbonpanel/pull/538
* feat(node-installer): qualify daemon units on a disposable Linux systemd fixture (OSS-379) by @athNdev in https://github.com/athNdev/carbonpanel/pull/539
* feat(console): server detail World tab with tuple-fenced adoption (OSS-382) by @athNdev in https://github.com/athNdev/carbonpanel/pull/540
* mcp: OSS-370 partial integration (path B hold, 9 stream ops pending) by @athNdev in https://github.com/athNdev/carbonpanel/pull/531
* mcp: separate ordinary regression gate from strict publication gate by @athNdev in https://github.com/athNdev/carbonpanel/pull/541
* test(enroll): qualify production boundary lifecycle through shared seam by @athNdev in https://github.com/athNdev/carbonpanel/pull/543
* Consume canonical daemon renderer in installer CLI (OSS-379) by @athNdev in https://github.com/athNdev/carbonpanel/pull/544
* feat(node-lxc): real dated Ubuntu rootfs pins + fail-closed provenance (OSS-378) by @athNdev in https://github.com/athNdev/carbonpanel/pull/545
* fix(node-runtime): qualify exact shipped daemon units on real systemd (OSS-376) by @athNdev in https://github.com/athNdev/carbonpanel/pull/546
* OSS-388: discover installed BlueMap and add managed in-context install by @athNdev in https://github.com/athNdev/carbonpanel/pull/542
* fix(ui): OSS-384 light-theme tokenization, nav hit area, stale refresh by @athNdev in https://github.com/athNdev/carbonpanel/pull/548
* MCP: authenticated controller-side bounded-frame stream transport (OSS-387) by @athNdev in https://github.com/athNdev/carbonpanel/pull/547
* OSS-366: Carbon Panel product tag line, provenance catalog and release gates by @athNdev in https://github.com/athNdev/carbonpanel/pull/549
* fix(enroll): qualify node-installer integration with shared boundary (OSS-385) by @athNdev in https://github.com/athNdev/carbonpanel/pull/551
* feat(node-lxc): end-to-end signed Ubuntu rootfs archive validation (OSS-378) by @athNdev in https://github.com/athNdev/carbonpanel/pull/552
* Qualify shipped rootless daemon unit on disposable systemd fixture (OSS-376) by @athNdev in https://github.com/athNdev/carbonpanel/pull/558
* OSS-377: verify real VM base-image bytes; clean boot blocked (no qemu-system/KVM) by @athNdev in https://github.com/athNdev/carbonpanel/pull/550
* MCP upload/commit family seams and executable tools/call evidence by @athNdev in https://github.com/athNdev/carbonpanel/pull/554
* OSS-389: hydrate-safe showcase capture and persistent asset branch by @athNdev in https://github.com/athNdev/carbonpanel/pull/553
* OSS-369 hardening: bounded imports, module sidecar parity, split egress (B-08/B-10/B-07) by @athNdev in https://github.com/athNdev/carbonpanel/pull/557
* feat(qa): product acceptance gaps and integration-gate re-baseline (OSS-349/352/355/357/358/365) by @athNdev in https://github.com/athNdev/carbonpanel/pull/555
* mcp: typed bounded byte path for pending download operations (OSS-370/371-375) by @athNdev in https://github.com/athNdev/carbonpanel/pull/559
* OSS-391: bounded trusted-main nightly schedule and scoped local updater by @athNdev in https://github.com/athNdev/carbonpanel/pull/560
* refactor(studio): extract mod, resource, maintenance and loader workflows (OSS-341) by @athNdev in https://github.com/athNdev/carbonpanel/pull/556
* refactor(ui): tokenize logs and datapack features, extend ratchet (OSS-342) by @athNdev in https://github.com/athNdev/carbonpanel/pull/562
* feat(rpc): generate Packwiz and StagedConfig product contracts (OSS-337) by @athNdev in https://github.com/athNdev/carbonpanel/pull/563
* chore(deps): aggregate Dependabot PRs #485-#496 and security fixes (OSS-390) by @athNdev in https://github.com/athNdev/carbonpanel/pull/561
* OSS-389: fix automatic showcase capture path with real fixtures by @athNdev in https://github.com/athNdev/carbonpanel/pull/564
* fix(deps): migrate console to SvelteKit 3 for security advisories (OSS-393) by @athNdev in https://github.com/athNdev/carbonpanel/pull/565
* fix(runtime-artifacts): reconcile build allowlist with real entry points (OSS-380) by @athNdev in https://github.com/athNdev/carbonpanel/pull/568
* mcp: mount byte-transport lanes in production and close STRICT gate by @athNdev in https://github.com/athNdev/carbonpanel/pull/567
* OSS-401: Correct stable-release example in docs/releases.md by @athNdev in https://github.com/athNdev/carbonpanel/pull/569
* OSS-397/OSS-398: remove workstation paths and repair ADR drift by @athNdev in https://github.com/athNdev/carbonpanel/pull/571
* docs(mcp): align public docs with generated catalog truth (OSS-404) by @athNdev in https://github.com/athNdev/carbonpanel/pull/574
* fix(node-lxc): raw upstream tarball digest in build (OSS-402) by @athNdev in https://github.com/athNdev/carbonpanel/pull/570
* OSS-406: run MCP STRICT full-coverage gate and stdio conformance in normal CI by @athNdev in https://github.com/athNdev/carbonpanel/pull/576
* fix(runtime-artifacts): fail-closed publication predicate (OSS-400) by @athNdev in https://github.com/athNdev/carbonpanel/pull/572
* fix(mcp): pin content descriptor artifact to LF for Windows checkouts by @athNdev in https://github.com/athNdev/carbonpanel/pull/577
* docs(mcp): correct ADR 0034 tool counts from generator truth (OSS-408) by @athNdev in https://github.com/athNdev/carbonpanel/pull/578
* test(auth,rpc): OIDC REST boundary and two-tenant stream revocation coverage (OSS-406) by @athNdev in https://github.com/athNdev/carbonpanel/pull/580
* OSS-399: close B-07 SSRF bypass and make remote saves atomic by @athNdev in https://github.com/athNdev/carbonpanel/pull/573
* OSS-406: real DOM and Chromium accessibility coverage by @athNdev in https://github.com/athNdev/carbonpanel/pull/583
* fix(admission): fail-closed deep-sleep gate and close ungated production starts (OSS-405) by @athNdev in https://github.com/athNdev/carbonpanel/pull/579
* OSS-406: recurring disposable real-binary upgrade rehearsal in CI by @athNdev in https://github.com/athNdev/carbonpanel/pull/582
* Version contract, multi-version docs and exact OpenAPI version parity by @athNdev in https://github.com/athNdev/carbonpanel/pull/575
* OSS-389: publish full-window dashboard and honest showcase captures by @athNdev in https://github.com/athNdev/carbonpanel/pull/584
* test(worldview): pin BlueMap discovery fail-closed seams (OSS-388) by @athNdev in https://github.com/athNdev/carbonpanel/pull/585
* test(rbac): combine descriptor-driven conformance with reflector drift guard (OSS-403) by @athNdev in https://github.com/athNdev/carbonpanel/pull/597
* OSS-406: real application-route a11y across themes and viewports by @athNdev in https://github.com/athNdev/carbonpanel/pull/599
* fix(worldview): accept BlueMap v5.29 string map IDs in settings.json by @athNdev in https://github.com/athNdev/carbonpanel/pull/596
* OSS-369 closeout: close B-07 modpack egress and B-09 strict digest gaps by @athNdev in https://github.com/athNdev/carbonpanel/pull/595
* chore(deps): qualified routine dependency successor (OSS-394) by @athNdev in https://github.com/athNdev/carbonpanel/pull/593
* refactor(studio): finish metadata/content and export extraction (OSS-341) by @athNdev in https://github.com/athNdev/carbonpanel/pull/605
* feat(rpc): generate Packwiz mod-control contracts (OSS-337) by @athNdev in https://github.com/athNdev/carbonpanel/pull/602
* fix(release): set GH_TOKEN on publication gate steps by @athNdev in https://github.com/athNdev/carbonpanel/pull/601
* OSS-389/OSS-392: fail-closed World View capture and full-panel composition by @athNdev in https://github.com/athNdev/carbonpanel/pull/606
* feat(closure): native Done guard and fail-closed acceptance evidence by @athNdev in https://github.com/athNdev/carbonpanel/pull/600
* fix(ui): measured panel token consistency - OSS-384 reopened In Progress by @athNdev in https://github.com/athNdev/carbonpanel/pull/603
* test(playerskin): make TTL cache tests deterministic with injected clock by @athNdev in https://github.com/athNdev/carbonpanel/pull/612
* OSS-185/OSS-245: own console log buffer, fail closed on AUTH_FAIL, add named reconnect fixture by @athNdev in https://github.com/athNdev/carbonpanel/pull/609
* fix(worldview): exact BlueMap gzip routing and CSP-safe asset readiness by @athNdev in https://github.com/athNdev/carbonpanel/pull/610
* OSS-24: enforce concept line caps and prove MCP merge over stdio by @athNdev in https://github.com/athNdev/carbonpanel/pull/604
* OSS-414: reconcile docs and module example after inherited-tag retirement by @athNdev in https://github.com/athNdev/carbonpanel/pull/611
* OSS-392: use authoritative renderer READY signal in capture wait by @athNdev in https://github.com/athNdev/carbonpanel/pull/614
* OSS-413 docs sources: Stable/Nightly channels, accessible selector, exact promotion marker by @athNdev in https://github.com/athNdev/carbonpanel/pull/608
* feat(proxmox): first-party controller installer for existing guests by @athNdev in https://github.com/athNdev/carbonpanel/pull/631
* fix(proxmox): detect standard Compose names and refuse unsafe updates by @athNdev in https://github.com/athNdev/carbonpanel/pull/632
* fix(docs): derive maintained guide source from trusted checkout at build by @athNdev in https://github.com/athNdev/carbonpanel/pull/633
* docs(ci): scoped docs-only maintained-guide publication by @athNdev in https://github.com/athNdev/carbonpanel/pull/634
* docs(mcp): reconcile exact-main catalogue counts and add drift guard by @athNdev in https://github.com/athNdev/carbonpanel/pull/618
* test(console): prove reconnect continuity in browser journey by @athNdev in https://github.com/athNdev/carbonpanel/pull/616
* test(auth): Okta/authentik OIDC provider profiles and callback/claim docs alignment by @athNdev in https://github.com/athNdev/carbonpanel/pull/630
* feat(node-lxc): fail-closed boot qualification verification (OSS-378, OSS-402 reconciliation) by @athNdev in https://github.com/athNdev/carbonpanel/pull/591
* worldview: keep read view on scoped action denials by @athNdev in https://github.com/athNdev/carbonpanel/pull/622
* fix(settings): wire tabpanel semantics for settings tabs by @athNdev in https://github.com/athNdev/carbonpanel/pull/617
* fix(world-library): emit idempotent bind/unbind activity events (OSS-362) by @athNdev in https://github.com/athNdev/carbonpanel/pull/625
* staging: reconcile batch retries after partial publication failures by @athNdev in https://github.com/athNdev/carbonpanel/pull/626
* test(acceptance-evidence): harden offline record validation by @athNdev in https://github.com/athNdev/carbonpanel/pull/628
* db: verify pre-migration backup preserves data by @athNdev in https://github.com/athNdev/carbonpanel/pull/627
* module: stop orphan sidecars when parent server record is gone by @athNdev in https://github.com/athNdev/carbonpanel/pull/623
* test(installer): qualify OSS-379/380 acceptance in disposable systemd fixture by @athNdev in https://github.com/athNdev/carbonpanel/pull/594
* mcp: add build-only packaged stdio smoke seam by @athNdev in https://github.com/athNdev/carbonpanel/pull/590
* fix(showcase): keep proven BlueMap World View capture in routine publish by @athNdev in https://github.com/athNdev/carbonpanel/pull/635
* docs(readme): link verified World View capture by @athNdev in https://github.com/athNdev/carbonpanel/pull/636
* handlers: accept session cookies in global REST policy by @athNdev in https://github.com/athNdev/carbonpanel/pull/621
* mcp: harden controller stream handle cleanup on failure paths by @athNdev in https://github.com/athNdev/carbonpanel/pull/620
* fix(website): publish version index at /docs/version-index.json by @athNdev in https://github.com/athNdev/carbonpanel/pull/592
* diagnostics: add safe recovery guidance to evidence observations by @athNdev in https://github.com/athNdev/carbonpanel/pull/624
* feat(wizard): queue local datapack uploads for post-create install by @athNdev in https://github.com/athNdev/carbonpanel/pull/629
* OSS-415: importable Debian 13 controller CT artifact lane by @athNdev in https://github.com/athNdev/carbonpanel/pull/637
* fix(controller-ct): validate draft release via release-id API URL by @athNdev in https://github.com/athNdev/carbonpanel/pull/638
* docs(proxmox): link controller-ct-v0.1.0 release assets (preview) by @athNdev in https://github.com/athNdev/carbonpanel/pull/639
* fix(showcase): wide real BlueMap World View framing (OSS-416) by @athNdev in https://github.com/athNdev/carbonpanel/pull/640
* OSS-406: add modpacks library recurring journey plan by @athNdev in https://github.com/athNdev/carbonpanel/pull/615
* test(storage): qualify public file/archive/log primitives across fixtures by @athNdev in https://github.com/athNdev/carbonpanel/pull/588
* test(mcp): make docs catalogue count assertions CRLF-independent by @athNdev in https://github.com/athNdev/carbonpanel/pull/641
* test(mcp): make stream memory fixture deterministic with synctest by @athNdev in https://github.com/athNdev/carbonpanel/pull/642
* fix(players): sanitize transient lookup failures and preserve skin-unavailable identity context by @athNdev in https://github.com/athNdev/carbonpanel/pull/643
* Bind OSS acceptance receipts to GitHub workflow and commit ancestry by @athNdev in https://github.com/athNdev/carbonpanel/pull/653
* Retain packaged MCP conformance evidence by @athNdev in https://github.com/athNdev/carbonpanel/pull/654
* mcp: reconcile audit claims with mounted controller surfaces by @athNdev in https://github.com/athNdev/carbonpanel/pull/657
* native: qualify cgroup v2 capability delegation in captive daemon preflight by @athNdev in https://github.com/athNdev/carbonpanel/pull/619
* Keep LXC boot qualification read-only without a Proxmox fixture by @athNdev in https://github.com/athNdev/carbonpanel/pull/651
* backup: refuse restore while a world binding is active by @athNdev in https://github.com/athNdev/carbonpanel/pull/645
* Fix dashboard reflow and restore keyboard focus across the panel by @athNdev in https://github.com/athNdev/carbonpanel/pull/647
* worldlib: preserve worlds during multi-family swap rollback by @athNdev in https://github.com/athNdev/carbonpanel/pull/646
* worldlib: allow the active bind to stop and restart its container by @athNdev in https://github.com/athNdev/carbonpanel/pull/648
* OSS-406: Run Modpacks browser plans on a disposable controller by @athNdev in https://github.com/athNdev/carbonpanel/pull/650
* worldlib: reject conflicting and superseded bind replays by @athNdev in https://github.com/athNdev/carbonpanel/pull/659
* Verify cross-built MCP packages and exact Registry listings by @athNdev in https://github.com/athNdev/carbonpanel/pull/661
* Refuse Studio bake when required CurseForge mods have no download URL by @athNdev in https://github.com/athNdev/carbonpanel/pull/664
* test: isolate snapshot and restart fixtures from the host daemon by @athNdev in https://github.com/athNdev/carbonpanel/pull/669
* Require exact deployment evidence before runtime artifact publication by @athNdev in https://github.com/athNdev/carbonpanel/pull/655
* Keep server boot errors visible after container exit by @athNdev in https://github.com/athNdev/carbonpanel/pull/674
* Qualify packaged Panel installation and upgrade under systemd by @athNdev in https://github.com/athNdev/carbonpanel/pull/667
* Qualify packaged Panel database restore on a hosted runner by @athNdev in https://github.com/athNdev/carbonpanel/pull/668
* fix(worldview): verify textures for discovered BlueMap modules by @athNdev in https://github.com/athNdev/carbonpanel/pull/673
* Exercise the packaged MCP stream client under real scopes by @athNdev in https://github.com/athNdev/carbonpanel/pull/671
* Document the actual MCP onboarding and compatibility contract by @athNdev in https://github.com/athNdev/carbonpanel/pull/656
* Fix Java v1 player management requests and live roster notifications by @athNdev in https://github.com/athNdev/carbonpanel/pull/658
* Qualify staged Compose releases with real Minecraft joins by @athNdev in https://github.com/athNdev/carbonpanel/pull/670
* Qualify the packaged Panel guest installer on hosted Linux by @athNdev in https://github.com/athNdev/carbonpanel/pull/676
* Reject Modrinth packs that declare dedicated servers unsupported by @athNdev in https://github.com/athNdev/carbonpanel/pull/662
* Fix selected Modrinth release and loader resolution by @athNdev in https://github.com/athNdev/carbonpanel/pull/672
* Allow operators to collapse server runtime metrics by @athNdev in https://github.com/athNdev/carbonpanel/pull/649
* Wire managed node enrollment and health reporting over TLS by @athNdev in https://github.com/athNdev/carbonpanel/pull/677
* OSS-389: make README showcase captures readable and reliable by @athNdev in https://github.com/athNdev/carbonpanel/pull/652
* Define native server portability archives (OSS-426) by @athNdev in https://github.com/athNdev/carbonpanel/pull/665
* mcp: expose scoped staged rollout metadata by @athNdev in https://github.com/athNdev/carbonpanel/pull/675
* Expose bounded Packwiz Studio inventory reads over MCP by @athNdev in https://github.com/athNdev/carbonpanel/pull/678
* fix(deps): align console formatter and TypeScript peers (OSS-394) by @athNdev in https://github.com/athNdev/carbonpanel/pull/660
* OSS-413: synchronized Stable/Nightly docs and OCI channel promotion by @athNdev in https://github.com/athNdev/carbonpanel/pull/613
* Bind and persist managed-node enrollment over TLS by @athNdev in https://github.com/athNdev/carbonpanel/pull/663
* build(deps): bump qualify compose client to minecraft-protocol 1.68.0 (OSS-394) by @athNdev in https://github.com/athNdev/carbonpanel/pull/685
* Report per-entry Packwiz batch mutation outcomes by @athNdev in https://github.com/athNdev/carbonpanel/pull/684
* OSS-431: Docker Hub README reconciliation with public read-back by @athNdev in https://github.com/athNdev/carbonpanel/pull/683
* OSS-413: Preserve published Stable in site-only Pages builds by @athNdev in https://github.com/athNdev/carbonpanel/pull/679
* OSS-413: Recover the split Nightly channel by @athNdev in https://github.com/athNdev/carbonpanel/pull/666
* Restore release-image-context artifact for Compose qualification matrix by @athNdev in https://github.com/athNdev/carbonpanel/pull/688
* OSS-388: fix managed BlueMap install to reach a visible map by @athNdev in https://github.com/athNdev/carbonpanel/pull/689
* feat(auth): explicit local-admin OIDC linking (B2 release blocker) by @athNdev in https://github.com/athNdev/carbonpanel/pull/644
* Fix World Library bind and recovery on registered local Docker nodes by @athNdev in https://github.com/athNdev/carbonpanel/pull/691
* Fix World Library archive paths for imported worlds by @athNdev in https://github.com/athNdev/carbonpanel/pull/692
* Use the correct Java 1.21.1 world data version by @athNdev in https://github.com/athNdev/carbonpanel/pull/693


**Full Changelog**: https://github.com/athNdev/carbonpanel/compare/v1.0.40...carbonpanel-v2.0.1