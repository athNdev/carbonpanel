# ADR 0017: Player identity, provenance and capability gates

Status: Proposed

## Context

OSS-345 replaces the thin Players tab with a cross-server player product. The
available sources disagree in important ways: the native Minecraft Server
Management Protocol is structured but only on Java 1.21.9+, RCON is text with
ambiguous replies, on-disk `playerdata`/`stats`/`advancements` are game-owned
files, and plugin bridges are loader-specific with different licences. Mojang
removed the public username-history API in 2022 and no official replacement
returns a name list. Several proposed capabilities (mute, groups, live
inventory, Bedrock parity) have no verified vanilla source.

## Decision

1. A panel-scoped `PlayerIdentity` uses an opaque `pl_*` key. Online UUID,
   offline UUID, XUID and Floodgate UUID stay distinct and are never merged by
   name alone. Only *observed* aliases are recorded; no name-history service is
   queried and the UI says so explicitly.
2. Every non-authoritative field carries `Observation {source, observed_at,
   confidence, applicability, conflict_note}`. Unknown and stale values are
   rendered as such; an empty field is not treated as a zero.
3. The structured management protocol is negotiated first when explicitly
   configured on Java 1.21.9+; RCON and node-scoped `exec` are allowlisted
   fallbacks. No adapter falls back to controller-local state, and no live
   Minecraft server is assumed in tests.
4. Skins and capes are fetched server-side from the signed Mojang texture
   chain, cached in a bounded same-origin cache and rendered as a 2D face in
   the panel. There is no client-side third-party request and no tracker by
   default. The cache accepts only canonical UUIDs and pinned texture hashes;
   arbitrary URLs and cross-host redirects are rejected structurally.
5. Deep character/progress data is read-only through the injected node
   resolver with caller contexts and W17 bounded reads. Live NBT is never
   edited; snapshots are labelled with their age and consistency.
6. Moderation is typed, permissioned with `players:*` scopes on `server_id`
   (so subuser grants apply), journaled with a request id, audited, and
   reversible only where a prior value was recorded. The legacy `SendCommand`
   string path is not a moderation boundary.
7. Optional Paper/Fabric/NeoForge bridges and full Bedrock parity are a
   **decision gate, not an implementation promise**. No bridge artifact is
   bundled and no unverified capability is advertised. The UI and capability
   RPC report each such capability as `DECISION_PENDING` or `UNSUPPORTED` with
   an actionable explanation and a licence/verification requirement.

## Decision register (OSS-353)

| Candidate | Capability evidence | Licence evidence | Decision |
| --- | --- | --- | --- |
| Native management protocol (Java 1.21.9+) | Structured `Player{id,name}`, typed allowlist/bans/ops, joined/left notifications — Minecraft Wiki, *Minecraft Server Management Protocol* (accessed 2026-11-18) | First-party protocol; no bundled artifact | **GO** behind explicit configuration and version gate; fixture tests only, one real 1.21.9+ server qualification still required |
| RCON / node-scoped exec fallback | Java-only, text replies with ambiguous termination — Minecraft Wiki, *RCON* and */list* (accessed 2026-11-18) | In-repo wrappers, no new dependency | **GO** with allowlisted commands, bounded timeout, redaction and node-scoped exec only |
| Paper bridge (vitals, inventory, groups, mute) | Loader API can enumerate players and listen to join/leave (PaperMC docs) but specific field parity is **UNVERIFIED**; mute/groups are plugin-only | Paper server is GPL-3.0; plugins compile against the API — legal review required | **NO-GO for bundling now**; decision-pending in the capability RPC; revisit only with a separate licence-reviewed artifact |
| Fabric bridge | Loader-side bridge feasibility documented (fabricmc.net); field parity **UNVERIFIED** | Fabric API Apache-2.0 | **NO-GO now**; separate artifact and verification required |
| NeoForge bridge | Loader docs exist (neoforged.net); field parity **UNVERIFIED** | NeoForge docs MIT | **NO-GO now**; do not assume one bridge binary covers all loaders |
| Bedrock identity parity | XUID identity and Floodgate Java-shaped UUID mapping documented (GeyserMC/Floodgate; Microsoft Learn Bedrock `/list`) | No bundled artifact | **NO-GO for full parity now**; identity presentation may be added after an explicit scope decision; never default to offline auth (CLOU-29) |
| Official name history | Removed 2022-09-13; current authenticated endpoint returns only change metadata, not a name list (Minecraft Help Center) | n/a | **NO-GO**; observed aliases only |

Licence notes are orientation, not legal advice. "UNVERIFIED" is a decision gate, not a promise that a capability is absent forever.

## Consequences

- Roster and detail work on vanilla data alone; plugin-only fields degrade to
  explicit "unsupported / requires verified bridge" states rather than blanks
  that look like missing data.
- Adding a bridge later is a separate ADR with legal review of GPL-3.0 (Paper
  server), Apache-2.0 (Fabric API) and MIT (NeoForge docs), plus a fixture and
  one real-server qualification run for the management protocol.
- Licence summaries are orientation, not legal advice. Fixture protocol tests
  do not prove interoperability with a real 1.21.9+ server.

## Evidence

`product-plans/player-management.md` §§5,7,8,9,15 and `product-plans/
integrated-roadmap.md` §§2,4,6, with primary sources cited there and accessed
2026-11-18. No live Minecraft server, RCON endpoint, skin service or container
was contacted by the implementation work.
