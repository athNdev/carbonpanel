<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0058 - Batch-bound publication record for staged next-restart updates

Status: Accepted (2026-11-27). Governs the next-restart staged-publication
recovery seam. Timed/idle `ApplyBatch` remains on the separate path described in
`knowledge/failures/staged-publication-timed-outcome.md`.

## Context

The next-restart lifecycle applied files and config, then mutated the container,
and derived what had happened from mutable state: staged `APPLIED` rows, the
server row, and an undo `manifest.json` inside the game data directory. Those are
not safe evidence:

- `APPLIED` proves the live tree was written, not that a container dispatch
  happened. Module rows are updated and module containers recreated before the
  content row is marked applied, so "no APPLIED" can still mean a runtime
  mutation started.
- The undo manifest is mutable game data. Restoring arbitrary targets named by
  it could modify files the batch does not own, and a crash before the manifest
  wedged recovery permanently.
- Discovering any container with the server label and adopting it cannot prove
  that container contains this batch. A same-ID start and an unrelated drift are
  equally ambiguous.
- An `APPLIED` row applied after a previous acknowledgement is not covered by
  the older runtime.

## Decision

1. Add one small controller-owned row per batch, `server_update_publications`,
   keyed by batch id and carrying an attempt generation. It records the
   preparation snapshot, dispatch intent, exact covered row identities, the
   expected replacement identity captured at creation, and the verified
   outcome. `server_update_batches` keeps `published_at` as the acknowledgement
   timestamp used by existing queries.
2. Phases are explicit: `preparing`, `prepared`, `applying`, `dispatched`,
   `verified`, `cleaned`, `abandoned`. `preparing` is the only phase that proves
   no live write began. `applying` is persisted before the first live write.
   `dispatched` is persisted before the Docker call. `verified` and `cleaned`
   are written only after a positive observation.
3. Recovery reads only the DB snapshot. Before mutating, it proves every entry's
   row id belongs to the batch, every target is the owned target (or a safe
   pack-derived path), and every backup reference is the exact indexed backup
   under the batch recovery root. A game-data `manifest.json` is never consulted
   for recovery.
4. A `preparing` attempt may discard an incomplete backup and retry. A
   `prepared` attempt may discard because no live write began. An `applying`
   attempt restores the recorded snapshot and restages the rows it restored. A
   module intent is recorded before module rows or containers change; if it is
   present without a dispatch, recovery seals and refuses to infer that the
   module runtime was untouched.
5. Dispatch records the exact applied config/content ids for this attempt. A
   retry may acknowledge only the replacement identity recorded at creation and
   only for the same attempt. Reconcile may repair the tracked id but never
   acknowledge. A missing identity, a rollback-to-baseline id, or an unrelated
   canonical id seals the batch without cleanup.
6. A pre-dispatch interruption with no container resumes the normal create path.
   A verified retry finishes cleanup and bookkeeping and leaves the operator's
   explicit start/restart in control.
7. An `applying` restore first proves every recorded target is either its
   recorded pre-image or exactly this attempt's staged post-image. The snapshot
   records each entry's staged source and action for that check; a snapshot that
   predates those fields is only trusted for unchanged targets. A target with
   any third identity, such as an ordinary save that landed after the
   interrupted attempt, stops recovery with an operator-inspection error
   instead of overwriting it. A failed attempt also keeps the server's exclusive
   data-write lease until the retry consumes the snapshot, so ordinary panel
   writes cannot land under it in the meantime; the retry re-adopts that
   retained lease rather than taking a second exclusive claim.

## Consequences

- A crash between snapshot, live write, dispatch, and verification has a
  defined conservative action, and an ambiguous transition never runs cleanup.
- An interrupted `applying` attempt recovers automatically only while its
  targets are still provably its own or untouched. An interleaved ordinary save
  is preserved and reported instead of silently erased, and the data path stays
  closed to ordinary writes until that recovery is resolved.
- The record is derived only from controller/DB state; no game-owned file can
  steer restoration.
- Timed/idle publication is not claimed by this contract. Its journal still
  treats publisher errors as definite and must be wired to an equivalent
  phase marker separately.
