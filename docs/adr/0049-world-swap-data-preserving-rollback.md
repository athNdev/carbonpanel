# ADR 0049: Data-preserving compensation for multi-family world swaps

Status: accepted

## Context

ADR 0016 defines a stop → stage → journal → publish → restart world swap.
The binding row is swapped before filesystem promotion. The original rollback
closure renamed retained old families back with errors ignored, then deleted
the whole retention tree. For a normal Java world with overworld, nether and
end families, a failure after the first promotion left the first family on new
bytes, deleted the retained bytes of families not yet restored, and removed
the recovery tree before `Recover` could see it. The binding row was restored
regardless of whether bytes were proven. This is a data-loss path and a Stable
upgrade blocker.

## Decision

Compensation for a failed multi-family publish is data-preserving and staged:

1. Retained old families are kept under `.world-library/old-<stageID>`.
   Promoted new bytes are moved to a separate
   `.world-library/quarantine-<stageID>`; they are never deleted or mixed into
   the retained tree during compensation.
2. A rollback first quarantines every already-promoted family, then restores
   every retained old family, then verifies each restored `level.dat` against
   the digest captured before mutation. Any failed rename or digest mismatch
   makes the rollback ambiguous.
3. The prior binding row is restored only after the filesystem bytes are
   proven. The restored row is re-read and compared before the rollback is
   reported. A refused or failed binding restore leaves the new row in place
   and the outcome uncertain; a rolled-back binding is never reported before
   bytes and row are proven.
4. Temporary trees are removed only after every filesystem step and the
   durable binding decision are verified. On ambiguous I/O nothing is deleted:
   the outcome is `unknown`, the journal keeps its lock, and both trees remain
   for operator recovery.
5. `Recover` restores a retained family only when the live family is missing,
   preserves staged and quarantined trees for inspection, keeps ambiguity
   fail-closed, and never deletes retained old data.

Knowledge is updated in `knowledge/architecture/world-library-shape.md`.

## Consequences

- A failed swap can leave quarantine and retention directories on disk after
  an ambiguous outcome; an operator may inspect them before resolving the
  operation. This is intentional: retained bytes outrank cleanup.
- Successful rollback now proves old bytes and re-reads the binding row, so a
  failed or interrupted compensation is reported as uncertain instead of a
  completed rollback.
- Test-only seams (`SetAccessorDecorator`, `RollbackBindingHook`) inject
  promotion, restore and binding faults at the real node-accessor and binding
  seams. Production leaves them nil.
