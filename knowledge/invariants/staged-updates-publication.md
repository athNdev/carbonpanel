---
type: Invariant
title: "Staged server updates — Publication"
description: "A due staged batch publishes through the journal; recovery and retry semantics preserve live data."
status: stable
owners: [oss-runtime]
code_refs: [internal/db/staged_config.go, internal/db/staged_content.go, internal/db/staged_publication.go, internal/staging, internal/staging/publication.go, internal/rpc/services/server.go, internal/docker/client.go, internal/scheduler/scheduler.go]
sources:
  - title: Parent concept
    resource: staged-updates.md
  - title: Publication phase record decision
    resource: ../../docs/adr/0058-staged-publication-phase-record.md
verified: { by: agent:opencode, at: 2026-11-28 }
---
# Rule

Publishing a due batch is journaled. A future timestamp or unfinished idle stretch stays staged across an incidental restart. Idle fires only with the
freeze/deep-sleep player-count provider; the stretch start is stored on the batch and a player joining clears it. Deep sleep commits onto the stopped
server without starting the JVM; a running server is saved and restarted once; a frozen server is stopped, started and left running. A failed publish
restores previous files/config and keeps rows staged for retry; completed retries only finish bookkeeping. A live field changed after staging stops the
publish before any write. A reviewed mod candidate is validated against the effective Docker `ServerConfig` type and version, and a runtime config
rollback or direct runtime-target save is refused while one is still staged. The admitted row persists that normalized loader/version and publication
revalidates it against the current effective target before any live-tree mutation; a changed target is a typed refusal that preserves live bytes. The reviewed installed SHA-512 selects the copy: no match is drift, two matches are ambiguous. A fetched
release with a required or incompatible dependency, or a temporary release lookup (network, timeout, rate limit, 5xx or decode), is refused before
download; confirmed 404/410 is definitive. Per-item staging storage/IO failures keep earlier staged rows and retry, while validation refusals and batch
conflicts stay per-item/admission refusals. The publisher owns the server data path exclusively from before the undo snapshot through publication and
rollback; ordinary writes take a shared lease and are refused meanwhile. A next-restart commit keeps that ownership until `Finish` or `Undo`. A reviewed
target's live digest is rechecked immediately before its writer commits; an out-of-band process remains outside the typed guard. See ADR 0056.
A restart selects its file and config rows in one exclusive snapshot. A legacy `on_restart` or `scheduled` batch window is normalized to next restart before selection, and legacy unbatched on-restart config joins the same snapshot; timed, idle and independent cron rows stay out. A pending legacy unbatched runtime config is included in the reviewed-mod compatibility check. Admission refuses a second staged row for one normalized live target; publication refuses a legacy duplicate batch before APPLYING instead of recording a snapshot one post-image cannot satisfy, and older duplicates stay an operator boundary. Each next-restart batch owns one durable `server_update_publications` record and
attempt generation, with phases preparing, prepared, applying, dispatched, identity-unresolved, verified, cleaned and abandoned. The record, not mutable status, APPLIED rows
or container discovery, decides whether a runtime dispatch happened. Dispatch records the exact applied config/content ids inside the journaled mutation that
performs the runtime write, immediately before that write (unpause, start, create or recreate), and captures the replacement identity returned at creation.
Dispatch is idempotent, so one attempt records it once even when several journaled steps follow. A lifecycle request ID completes for the server across
every journaled step, not per step kind: before any live write or dispatch, start and restart refuse a request ID that already completed any step kind, so a
replay cannot select the unpause, create, recreate or in-place restart step that the current state would now imply. A completed replay mutates no container,
writes no dispatch or replacement identity, and leaves fresh rows and config staged in the open, unsealed batch; a new request ID is required to publish them.
A completed restart unpause with no dispatched publication records RUNNING before the restart, and an unpause refused because the container already runs is accepted only after observing a live state (running or unhealthy), so an exited, stopped or absent container stays an error. An interrupted status write in that no-publication case recovers with a new request ID; a failed status write is an error. A dispatched in-place batch with no replacement identity remains fail-closed and sealed, and a retry is refused at the identity check instead of being acknowledged, so the operator must resolve it.
A suppressed callback or a journal identity-binding refusal likewise writes no dispatch and mutates no container, and fresh rows stay retryable. The replacement identity is written
with a bounded context detached from caller cancellation and a short idempotent retry; if it still cannot be persisted the record becomes `identity-unresolved` and a retry may adopt the canonical container that dispatch created (never the baseline) before verifying it. A created container is
never removed because persisting its server fields failed: it may already contain the staged bytes and is retained under the recorded identity for an
identity-verifying retry. A retry acknowledges only that exact identity for that attempt; reconciling another canonical
container may repair the tracked id but is never publication evidence. A newly applied batch-owned row is refused, not covered by an older runtime. A
pre-dispatch interruption with no container resumes ordinary creation. An in-place restart failure that may have mutated the existing container is retained
even when a later fallback's preflight proves the replacement untouched; only when every attempt ended before mutation may the attempt compensate. An
unverified or ambiguous dispatch seals the batch and runs no cleanup; a verified publication is finalize-only and leaves the operator's explicit start or restart in control.
Generic compensation never abandons a dispatch; abandon accepts only preparing/prepared/applying, and a dispatched attempt is released only by the explicit typed-preflight transition. See [publication identity resolution](../decisions/publication-identity-unresolved.md).

Interrupted recovery uses only the controller-owned snapshot (owned row ids, targets and backup digests), validating ownership and every backup reference
before mutation. A preparing attempt may discard an incomplete backup; prepared has not written live data; applying restores the recorded snapshot and
restages its rows. Before an applying restore mutates a target it proves the current bytes are the recorded pre-image or exactly this attempt's staged
post-image; any other identity, such as an ordinary save that landed after the failure, is refused for operator inspection instead of overwritten. A failed
attempt keeps the exclusive data-write lease until the retry consumes the snapshot, so ordinary writes cannot land in between, and the retry re-adopts that
retained lease. An unreadable durable phase read retains the lease (fail closed): it cannot prove the phase is not applying, so the write exclusion stays.
Module intent is durable before module rows or containers change, so an interrupted module transition is sealed, never auto-restored.
Game-data manifests are never recovery evidence, and legacy batches without a record do not auto-restore. Applied content rows stay durable cleanup
evidence: retry cleanup instead of republishing. Mixed rows resolve by discarding newly staged rows and retrying the retained snapshot; recovery is never
overwritten to make room. Timed/idle publication still uses the operation journal and is not covered here; see [timed/idle publisher limits](../failures/staged-publication-timed-outcome.md).
A fetched modpack commit stays recorded until its stage or sync completes, so a refused stage is retried instead of reported as no update. A server settings update commits the server row and its config sync in one writer transaction that re-runs the reviewed-mod runtime check, so a concurrent admission cannot invalidate the review between check and commit.
A modpack or module change can join the batch; a module recreate still does not start a deep-sleeping server. See [storage recovery](../../docs/storage-streaming.md).
