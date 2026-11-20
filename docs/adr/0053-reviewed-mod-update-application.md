<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0053 — Reviewed mod updates reuse the staged batch

Status: Accepted (2026-11-20). Governs OSS-423, OSS-424 and OSS-425.

## Context

The installed-mod preview identifies a compatible Modrinth release by installed
SHA-512, project, loader, game version, release status, dependencies and a
candidate SHA-512. Applying that preview needs to preserve unrecognized or
modified uploads, survive provider and download failures, serialize with other
server mutations and leave an audit record. The repository already has one
journaled staged-batch publish path with per-file backups, undo, an apply
window and a durable per-server operation lock. A separate apply path would
duplicate those guarantees and could publish different bytes under the same
request.

## Decision

1. Candidate bytes are downloaded only after the installed file fingerprint and
   the provider release are re-verified. The download is egress-checked,
   size-bounded and SHA-512 verified against the reviewed candidate. A drift,
   provider, download or checksum mismatch produces a per-item refusal; the
   live file is never touched by staging.
2. Accepted candidates are written through the existing node-owned
   `.carbon-panel/staged/<id>/` path into the server's open update batch, using
   the installed file's live target path. Publishing stays with the existing
   journaled batch apply; no mod-specific apply path exists.
3. The typed RPC surface is an additive extension of `ModService.UpdateMod`,
   reusing its `mods/update[server_id]` mapping rather than introducing a new
   procedure. No MCP tool is exposed for this operator-only review flow.
4. Automatic and scheduled policies are `mod_update` `ScheduledTask` records.
   The scheduler holds a `ModUpdatePlanner` interface; the same service method
   implements manual and automatic staging. A nil planner, disabled policy,
   unknown apply window or malformed config fails closed.
5. Simultaneous runs deduplicate by the staged file name already present in the
   open batch. Retries and next/last execution remain the scheduler's
   persisted responsibility; the planner never decides retry policy.

## Consequences

- An operator must explicitly select a preview and choose an apply window
  before bytes enter a batch. Staging alone never restarts a server.
- Backup, rollback, per-server serialization and apply-window semantics are
  exactly the staged-batch semantics already used for config and content.
- An MCP catalog change must not expose the review-and-apply flow as a tool;
  the fail-closed family gates enumerate mounted RPCs, so additive fields on
  an existing procedure avoid a new catalog/matrix/tool tuple.
- A policy can only stage compatible, unexcluded, not-already-staged updates;
  it cannot force an unknown or modified jar into the live tree.
