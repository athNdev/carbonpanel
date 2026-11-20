# ADR 0049: Data-preserving compensation for multi-family world swaps

Status: accepted

## Context

ADR 0016 defines a stop Ã¢â€ â€™ stage Ã¢â€ â€™ journal Ã¢â€ â€™ publish Ã¢â€ â€™ restart world swap.
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
   every retained old family, then checks each restored path and compares any pre-existing `level.dat`
   with its streamed digest. A failed compensation rename is ambiguous. A
   failed publish rename only permits compensation if both paths confirm it
   did not take effect.
3. The prior binding row is restored only after the filesystem bytes are
   proven. The restored row is re-read and compared before the rollback is
   reported. A refused or failed binding restore leaves the new row in place
   and the outcome uncertain; a rolled-back binding is never reported before
   bytes and row are proven.
4. Temporary trees are removed after verification and confirmation that the
   journal recorded a terminal decision. Ambiguous I/O and unconfirmed journal
   finalization preserve the trees and report `unknown`. A matching binding row
   cannot satisfy a retry while the journal still holds the server lock.
5. Startup recovery preserves every tree for an active operation. For legacy
   retention trees without an active operation, recovery restores a family only
   when the live path is confirmed missing. It never deletes retained data.

## Running servers

A bind holds the server lifecycle transition from the first Docker observation
until the decision. Its active journal guards stop, promotion and restart. The
adapter requires both server power
grants for a running target and checks the active bind journal's server, node
endpoint and container identity before internal power calls. Public power
requests keep their ordinary admission checks.

Publish proceeds after the node confirms the container stopped. Success and
proven rollback restart the same container. The controller confirms the Docker
process is running before releasing recovery data. If stopping or restarting
cannot be verified, the operation stays active and the controller preserves
the recovery trees. Staged configuration and container recreation stay on the
regular start path.

Knowledge is updated in `knowledge/architecture/world-library-shape.md`.

## Consequences

- A failed swap can leave quarantine and retention directories on disk after
  an ambiguous outcome; an operator may inspect them before resolving the
  operation. This is intentional: retained bytes outrank cleanup.
- A failed or interrupted compensation reports uncertainty when it cannot
  verify the restored paths, identity files, binding row or journal.
- Acknowledged node operations and controller process interruption are within
  scope. Qualification for storage that loses acknowledged writes requires a
  separate power-loss drill.
